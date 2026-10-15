package importer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/encoding/charmap"

	"github.com/vavallee/bindery/internal/models"
)

// Kindle files (MOBI, AZW, AZW3) keep their metadata in the EXTH block of the
// first record of a Palm database (#3016). The layout read here:
//
//	PalmDB header   78 bytes: name[32] ... type+creator "BOOKMOBI" at 60,
//	                record count (uint16) at 76, then 8 bytes per record
//	                whose first 4 are the record's file offset.
//	record 0        PalmDOC header (16 bytes), then the MOBI header:
//	                "MOBI" at +16, its length at +20, text encoding at +28
//	                (1252 or 65001), full name offset/length at +84/+88
//	                (relative to record 0), EXTH flag 0x40 at +128.
//	EXTH            right after the MOBI header: "EXTH", block length,
//	                record count, then type/length/value records whose
//	                length includes their 8 byte header.
//
// AZW3 (KF8) files carry the same record 0 and EXTH block, so one reader
// covers all three. Nothing here is compressed.

const (
	exthAuthor   = 100
	exthISBN     = 104
	exthTitle    = 503
	exthLanguage = 524

	// maxKindleRecord0 caps the bytes read for record 0: the headers and EXTH
	// block are a few kilobytes, and a cap keeps a corrupt offset from making
	// the reader allocate the whole file.
	maxKindleRecord0 = 1 << 20
)

// IsKindleFile reports whether path is a Kindle ebook we can read embedded
// metadata from.
func IsKindleFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mobi", ".azw", ".azw3":
		return true
	}
	return false
}

// HasEmbeddedMetadata reports whether ReadEbookMetadata can read path: an EPUB
// or a Kindle file.
func HasEmbeddedMetadata(path string) bool {
	return IsEpubFile(path) || IsKindleFile(path)
}

// ReadEbookMetadata reads the embedded title, author, ISBN and language of an
// EPUB or a Kindle file. Like ReadEpubMetadata it is best effort: any error
// sends the caller back to filename parsing.
func ReadEbookMetadata(path string) (EpubMetadata, error) {
	if IsKindleFile(path) {
		return ReadKindleMetadata(path)
	}
	return ReadEpubMetadata(path)
}

// ReadKindleMetadata reads a MOBI, AZW or AZW3 file's EXTH metadata into the
// same struct ReadEpubMetadata fills, so every caller works unchanged: the
// title from EXTH 503 (else the MOBI full name, else the database name), the
// first author (EXTH 100), an ISBN (EXTH 104) and the languages (EXTH 524).
func ReadKindleMetadata(path string) (EpubMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return EpubMetadata{}, fmt.Errorf("kindle: open: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return EpubMetadata{}, fmt.Errorf("kindle: stat: %w", err)
	}
	size := info.Size()

	header := make([]byte, 78)
	if _, err := f.ReadAt(header, 0); err != nil {
		return EpubMetadata{}, errors.New("kindle: file too short for a Palm database header")
	}
	if !bytes.Equal(header[60:68], []byte("BOOKMOBI")) {
		return EpubMetadata{}, errors.New("kindle: not a MOBI file")
	}
	if binary.BigEndian.Uint16(header[76:78]) == 0 {
		return EpubMetadata{}, errors.New("kindle: no records")
	}
	recordInfo := make([]byte, 16)
	n, _ := f.ReadAt(recordInfo, 78)
	start := int64(binary.BigEndian.Uint32(recordInfo[0:4]))
	end := size
	if n >= 16 && binary.BigEndian.Uint16(header[76:78]) > 1 {
		if next := int64(binary.BigEndian.Uint32(recordInfo[8:12])); next > start && next < end {
			end = next
		}
	}
	if start <= 0 || start >= size {
		return EpubMetadata{}, errors.New("kindle: record 0 outside the file")
	}
	end = min(end, start+maxKindleRecord0)
	rec := make([]byte, end-start)
	if n, err := f.ReadAt(rec, start); err != nil && int64(n) != end-start {
		return EpubMetadata{}, fmt.Errorf("kindle: read record 0: %w", err)
	}
	meta, err := parseMobiRecord0(rec)
	if err != nil {
		return EpubMetadata{}, err
	}
	if meta.Title == "" {
		meta.Title = strings.TrimSpace(string(bytes.TrimRight(header[0:32], "\x00")))
	}
	return meta, nil
}

// parseMobiRecord0 reads the MOBI header and EXTH block of record 0.
func parseMobiRecord0(rec []byte) (EpubMetadata, error) {
	var meta EpubMetadata
	if len(rec) < 32 || !bytes.Equal(rec[16:20], []byte("MOBI")) {
		return meta, errors.New("kindle: record 0 has no MOBI header")
	}
	mobiLen := int(binary.BigEndian.Uint32(rec[20:24]))
	text := decoderFor(binary.BigEndian.Uint32(rec[28:32]))

	if len(rec) >= 92 {
		off := int(binary.BigEndian.Uint32(rec[84:88]))
		n := int(binary.BigEndian.Uint32(rec[88:92]))
		if off > 0 && n > 0 && off+n <= len(rec) {
			meta.Title = text(rec[off : off+n])
		}
	}

	exthAt := 16 + mobiLen
	hasEXTH := len(rec) >= 132 && binary.BigEndian.Uint32(rec[128:132])&0x40 != 0
	if !hasEXTH || exthAt+12 > len(rec) || !bytes.Equal(rec[exthAt:exthAt+4], []byte("EXTH")) {
		return meta, nil
	}
	count := int(binary.BigEndian.Uint32(rec[exthAt+8 : exthAt+12]))
	pos := exthAt + 12
	var isbn10 string
	for i := 0; i < count && pos+8 <= len(rec); i++ {
		typ := binary.BigEndian.Uint32(rec[pos : pos+4])
		length := int(binary.BigEndian.Uint32(rec[pos+4 : pos+8]))
		if length < 8 || pos+length > len(rec) {
			break
		}
		val := text(rec[pos+8 : pos+length])
		pos += length
		if val == "" {
			continue
		}
		switch typ {
		case exthTitle:
			meta.Title = val
		case exthAuthor:
			if meta.Author == "" {
				meta.Author = val
			}
		case exthISBN:
			i13, i10 := extractISBN(val)
			if i13 != "" && meta.ISBN == "" {
				meta.ISBN = i13
			}
			if i10 != "" && isbn10 == "" {
				isbn10 = i10
			}
		case exthLanguage:
			if code := models.NormalizeLanguageCode(val); code != "" && !slices.Contains(meta.Languages, code) {
				meta.Languages = append(meta.Languages, code)
			}
		}
	}
	if meta.ISBN == "" {
		meta.ISBN = isbn10
	}
	if len(meta.Languages) > 0 {
		meta.Language = meta.Languages[0]
	}
	return meta, nil
}

// decoderFor returns the text decoder for a MOBI text encoding: 65001 is
// UTF-8, and 1252 (and anything unrecognised, which older files use) is
// Windows-1252.
func decoderFor(encoding uint32) func([]byte) string {
	if encoding == 65001 {
		return func(b []byte) string { return strings.TrimSpace(strings.ToValidUTF8(string(b), "")) }
	}
	// Windows-1252 maps every byte, so decoding cannot fail.
	dec := charmap.Windows1252.NewDecoder()
	return func(b []byte) string {
		out, _ := dec.Bytes(b)
		return strings.TrimSpace(string(out))
	}
}
