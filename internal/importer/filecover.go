package importer

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dhowden/tag"

	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/models"
)

// folderCoverNames are the image files audiobook releases ship beside their
// tracks, in order of preference.
var folderCoverNames = []string{"cover.jpg", "cover.jpeg", "cover.png", "folder.jpg", "folder.jpeg", "folder.png"}

// WithCoverStore enables covers taken from the user's own files: a book that
// no metadata provider gave a cover gets the art embedded in its file, or the
// cover image in its audiobook folder. Some catalogues have no covers to give
// (a national library cannot license publishers' cover art), and the file on
// disk is the one cover the user is certain to have the right to show.
func (s *Scanner) WithCoverStore(store *covers.Store) *Scanner {
	s.covers = store
	return s
}

// fillCoverFromFile sets book's cover from the file just attached to it, when
// the book has none. A provider cover is never replaced: fill empty, never
// clobber, as the author refresh does. Best-effort: a file without art, or a
// failed write, leaves the book as it was and never fails the import.
func (s *Scanner) fillCoverFromFile(ctx context.Context, book *models.Book, filePath string) {
	if s.covers == nil || book == nil || book.ImageURL != "" {
		return
	}
	art := readFileCover(filePath)
	if art == nil {
		return
	}
	ref, err := s.covers.PutBytes(art)
	if err != nil {
		slog.Debug("file cover not stored", "bookID", book.ID, "path", filePath, "error", err)
		return
	}
	if err := s.books.SetImageURL(ctx, book.ID, ref); err != nil {
		slog.Warn("failed to persist cover from file", "bookID", book.ID, "error", err)
		return
	}
	book.ImageURL = ref
	slog.Info("filled book cover from its file", "bookID", book.ID, "path", filePath)
}

// backfillFileCovers gives every book that has files but no cover the art from
// one of its files. The library scan only reaches fillCoverFromFile for files
// it has not registered yet, so books imported before covers were read from
// files would otherwise never get one.
func (s *Scanner) backfillFileCovers(ctx context.Context) {
	if s.covers == nil {
		return
	}
	books, err := s.books.ListWithFilesWithoutCover(ctx)
	if err != nil {
		slog.Warn("file cover backfill: list failed", "error", err)
		return
	}
	for i := range books {
		if ctx.Err() != nil {
			return
		}
		files, err := s.books.ListFiles(ctx, books[i].ID)
		if err != nil {
			continue
		}
		for _, f := range files {
			s.fillCoverFromFile(ctx, &books[i], f.Path)
			if books[i].ImageURL != "" {
				break
			}
		}
	}
}

// readFileCover returns the cover image for a book file, or nil. filePath is a
// single file (EPUB, M4B, MP3) or, for a multi-file audiobook, its folder.
//
// For a folder, a cover image beside the tracks wins, then the art embedded in
// the first track that has some. For a single file only its embedded art is
// used: a cover.jpg beside one file may belong to a folder of several books.
func readFileCover(filePath string) []byte {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		return embeddedCover(filePath)
	}
	for _, name := range folderCoverNames {
		if art := readCapped(filepath.Join(filePath, name)); art != nil {
			return art
		}
	}
	entries, err := os.ReadDir(filePath)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && IsAudioTagFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if art := embeddedCover(filepath.Join(filePath, name)); art != nil {
			return art
		}
	}
	return nil
}

func embeddedCover(filePath string) []byte {
	switch {
	case IsAudioTagFile(filePath):
		return audioCover(filePath)
	case IsEpubFile(filePath):
		return epubCover(filePath)
	}
	return nil
}

// audioCover returns the picture in an audio file's tags (an ID3 APIC frame,
// an MP4 covr atom, a FLAC picture block).
func audioCover(filePath string) []byte {
	f, err := os.Open(filePath) // #nosec G304 -- a book file the importer is attaching
	if err != nil {
		return nil
	}
	defer f.Close()
	// The library allocates a picture's claimed size before reading it, so a
	// tiny file can claim gigabytes; the same guard as ReadAudioTags (#2957).
	if checkAudioTagClaims(f) != nil {
		return nil
	}
	m, err := tag.ReadFrom(f)
	if err != nil {
		return nil
	}
	if p := m.Picture(); p != nil && len(p.Data) > 0 && len(p.Data) <= covers.MaxBytes {
		return p.Data
	}
	return nil
}

// epubCover returns the cover image an EPUB's package document declares:
// the manifest item with properties="cover-image" (EPUB 3), else the item
// named by <meta name="cover"> (EPUB 2).
func epubCover(filePath string) []byte {
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return nil
	}
	defer func() { _ = zr.Close() }()
	opfPath, err := epubOPFPath(zr)
	if err != nil {
		return nil
	}
	opf := findZipFile(zr, opfPath)
	if opf == nil {
		return nil
	}
	// Capped like ReadEpubMetadata's OPF read (#2957): the XML decoder
	// buffers a whole token, so an oversized document is refused.
	rc, err := openCappedEntry(opf)
	if err != nil {
		return nil
	}
	href := opfCoverHref(rc)
	_ = rc.Close()
	if href == "" {
		return nil
	}
	if unescaped, err := url.PathUnescape(href); err == nil {
		href = unescaped
	}
	img := findZipFile(zr, path.Join(path.Dir(opfPath), href))
	if img == nil {
		return nil
	}
	ir, err := img.Open()
	if err != nil {
		return nil
	}
	defer ir.Close()
	return readCappedFrom(ir)
}

// opfCoverHref returns the manifest href of the cover image, or "".
func opfCoverHref(r io.Reader) string {
	var pkg struct {
		Metas []struct {
			Name    string `xml:"name,attr"`
			Content string `xml:"content,attr"`
		} `xml:"metadata>meta"`
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"manifest>item"`
	}
	if err := xml.NewDecoder(r).Decode(&pkg); err != nil {
		return ""
	}
	for _, it := range pkg.Items {
		for _, p := range strings.Fields(it.Properties) {
			if p == "cover-image" {
				return it.Href
			}
		}
	}
	for _, m := range pkg.Metas {
		if m.Name != "cover" {
			continue
		}
		for _, it := range pkg.Items {
			if it.ID == m.Content {
				return it.Href
			}
		}
	}
	return ""
}

func readCapped(filePath string) []byte {
	f, err := os.Open(filePath) // #nosec G304 -- a fixed cover filename inside a book folder
	if err != nil {
		return nil
	}
	defer f.Close()
	return readCappedFrom(f)
}

// readCappedFrom reads at most covers.MaxBytes; anything larger is not used.
func readCappedFrom(r io.Reader) []byte {
	body, err := io.ReadAll(io.LimitReader(r, covers.MaxBytes+1))
	if err != nil || len(body) == 0 || len(body) > covers.MaxBytes {
		return nil
	}
	return body
}
