package importer

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type exthRecord struct {
	typ uint32
	val []byte
}

// mobiFile describes a minimal Kindle file for buildMobi. Real Kindle books
// can't live in the repository, so the tests build the bytes from the layout
// documented on ReadKindleMetadata.
type mobiFile struct {
	dbName   string // Palm database name, the last-resort title
	fullName []byte // MOBI full name, raw bytes in the file's encoding
	encoding uint32 // 65001 (UTF-8) or 1252
	exth     []exthRecord
	noFlag   bool // EXTH present but the header flag not set
}

func buildMobi(m mobiFile) []byte {
	const mobiHeaderLen = 232
	var exth []byte
	if len(m.exth) > 0 {
		var recs []byte
		for _, r := range m.exth {
			recs = binary.BigEndian.AppendUint32(recs, r.typ)
			recs = binary.BigEndian.AppendUint32(recs, uint32(8+len(r.val)))
			recs = append(recs, r.val...)
		}
		exth = append([]byte("EXTH"), binary.BigEndian.AppendUint32(nil, uint32(12+len(recs)))...)
		exth = binary.BigEndian.AppendUint32(exth, uint32(len(m.exth)))
		exth = append(exth, recs...)
	}
	rec0 := make([]byte, 16+mobiHeaderLen)
	copy(rec0[16:20], "MOBI")
	binary.BigEndian.PutUint32(rec0[20:24], mobiHeaderLen)
	binary.BigEndian.PutUint32(rec0[24:28], 2)
	binary.BigEndian.PutUint32(rec0[28:32], m.encoding)
	if len(exth) > 0 && !m.noFlag {
		binary.BigEndian.PutUint32(rec0[128:132], 0x40)
	}
	rec0 = append(rec0, exth...)
	if len(m.fullName) > 0 {
		binary.BigEndian.PutUint32(rec0[84:88], uint32(len(rec0)))
		binary.BigEndian.PutUint32(rec0[88:92], uint32(len(m.fullName)))
		rec0 = append(rec0, m.fullName...)
	}
	rec0 = append(rec0, 0, 0)

	header := make([]byte, 78)
	copy(header[0:32], m.dbName)
	copy(header[60:68], "BOOKMOBI")
	binary.BigEndian.PutUint16(header[76:78], 2)
	const rec0At = 78 + 16 + 2
	list := binary.BigEndian.AppendUint32(nil, rec0At)
	list = append(list, 0, 0, 0, 0)
	list = binary.BigEndian.AppendUint32(list, uint32(rec0At+len(rec0)))
	list = append(list, 0, 0, 0, 1, 0, 0)

	out := append(header, list...)
	out = append(out, rec0...)
	return append(out, []byte("text record")...)
}

func writeMobi(t *testing.T, name string, m mobiFile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, buildMobi(m), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadKindleMetadata(t *testing.T) {
	path := writeMobi(t, "fjellvinden.azw3", mobiFile{
		dbName:   "Fjellvinden_db",
		fullName: []byte("Fjellvinden (full name)"),
		encoding: 65001,
		exth: []exthRecord{
			{exthAuthor, []byte("Kari Nordmann")},
			{exthAuthor, []byte("Ola Nordmann")},
			{exthISBN, []byte("978-82-05-12345-6")},
			{exthTitle, []byte("Fjellvinden")},
			{exthLanguage, []byte("nb-NO")},
			{exthLanguage, []byte("en")},
			{exthLanguage, []byte("nb")},
			{999, []byte("an EXTH type the reader ignores")},
			{exthAuthor, nil}, // an empty value is skipped
		},
	})
	meta, err := ReadEbookMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Fjellvinden" {
		t.Errorf("title %q: EXTH 503 should win over the full name", meta.Title)
	}
	if meta.Author != "Kari Nordmann" {
		t.Errorf("author %q: the first EXTH 100 should win", meta.Author)
	}
	if meta.ISBN != "9788205123456" {
		t.Errorf("isbn %q, want the normalised ISBN-13", meta.ISBN)
	}
	if meta.Language != "nor" || !slices.Equal(meta.Languages, []string{"nor", "eng"}) {
		t.Errorf("languages %q %v, want nor first and each language once", meta.Language, meta.Languages)
	}
}

// Older files are Windows-1252; ø is 0xF8 there and must come back as UTF-8.
func TestReadKindleMetadataWindows1252(t *testing.T) {
	path := writeMobi(t, "fjellvinden.mobi", mobiFile{
		encoding: 1252,
		exth:     []exthRecord{{exthAuthor, []byte("Kari Bj\xf8rnstad")}, {exthTitle, []byte("S\xf8lvbekken")}},
	})
	meta, err := ReadKindleMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Sølvbekken" || meta.Author != "Kari Bjørnstad" {
		t.Errorf("got %q by %q", meta.Title, meta.Author)
	}
}

func TestReadKindleMetadataTitleFallbacks(t *testing.T) {
	// No EXTH at all: the MOBI full name.
	full := writeMobi(t, "a.mobi", mobiFile{dbName: "db_name", fullName: []byte("Havgapet"), encoding: 65001})
	if meta, err := ReadKindleMetadata(full); err != nil || meta.Title != "Havgapet" {
		t.Errorf("full name fallback: %q, %v", meta.Title, err)
	}
	// No EXTH and no full name: the Palm database name.
	db := writeMobi(t, "b.azw", mobiFile{dbName: "Nordlysnatt", encoding: 65001})
	if meta, err := ReadKindleMetadata(db); err != nil || meta.Title != "Nordlysnatt" {
		t.Errorf("database name fallback: %q, %v", meta.Title, err)
	}
	// An EXTH block the header doesn't flag is not read.
	unflagged := writeMobi(t, "c.mobi", mobiFile{fullName: []byte("Steinbrua"), encoding: 65001, noFlag: true,
		exth: []exthRecord{{exthTitle, []byte("Ignored")}, {exthAuthor, []byte("Ignored")}}})
	if meta, err := ReadKindleMetadata(unflagged); err != nil || meta.Title != "Steinbrua" || meta.Author != "" {
		t.Errorf("unflagged EXTH: %+v, %v", meta, err)
	}
}

func TestReadKindleMetadataISBN10Fallback(t *testing.T) {
	path := writeMobi(t, "a.mobi", mobiFile{encoding: 65001, exth: []exthRecord{{exthISBN, []byte("82-05-12345-X")}, {exthISBN, []byte("not an isbn")}}})
	meta, err := ReadKindleMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ISBN != "820512345X" {
		t.Errorf("isbn %q, want the ISBN-10 when no ISBN-13 is given", meta.ISBN)
	}
}

// A downloaded file is untrusted: anything malformed is an error or an empty
// field, never a panic or a huge allocation.
func TestReadKindleMetadataRejectsMalformedFiles(t *testing.T) {
	good := buildMobi(mobiFile{encoding: 65001, exth: []exthRecord{{exthTitle, []byte("Fjellvinden")}}})
	write := func(t *testing.T, b []byte) string {
		p := filepath.Join(t.TempDir(), "x.mobi")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mutate := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good...)
		f(b)
		return b
	}
	for name, b := range map[string][]byte{
		"too short":            []byte("BOOKMOBI"),
		"not a MOBI":           mutate(func(b []byte) { copy(b[60:68], "TEXtREAd") }),
		"no records":           mutate(func(b []byte) { binary.BigEndian.PutUint16(b[76:78], 0) }),
		"record 0 past EOF":    mutate(func(b []byte) { binary.BigEndian.PutUint32(b[78:82], 1<<30) }),
		"no MOBI header":       mutate(func(b []byte) { copy(b[96+16:96+20], "XXXX") }),
		"EXTH length past end": mutate(func(b []byte) { binary.BigEndian.PutUint32(b[96+248+12+4:], 1<<20) }),
	} {
		t.Run(name, func(t *testing.T) {
			meta, err := ReadKindleMetadata(write(t, b))
			if name == "EXTH length past end" {
				if err != nil || meta.Title != "" {
					t.Errorf("a broken EXTH record should stop the walk, not fail: %+v, %v", meta, err)
				}
				return
			}
			if err == nil {
				t.Errorf("got %+v, want an error", meta)
			}
		})
	}
	if _, err := ReadKindleMetadata(filepath.Join(t.TempDir(), "missing.mobi")); err == nil {
		t.Error("a missing file gave no error")
	}
}

func TestHasEmbeddedMetadata(t *testing.T) {
	for path, want := range map[string]bool{
		"a.epub": true, "a.MOBI": true, "a.azw": true, "a.azw3": true, "a.pdf": false, "a.mp3": false, "a.kfx": false,
	} {
		if got := HasEmbeddedMetadata(path); got != want {
			t.Errorf("HasEmbeddedMetadata(%q) = %v, want %v", path, got, want)
		}
	}
}

// The bulk import and the could-not-match message read Kindle files too.
func TestKindleFilesReachBulkLookupAndUnmatchedReason(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "Kari Nordmann", "Fjellvinden")
	if err := os.MkdirAll(book, 0o755); err != nil {
		t.Fatal(err)
	}
	azw3 := filepath.Join(book, "fjellvinden.azw3")
	if err := os.WriteFile(azw3, buildMobi(mobiFile{encoding: 65001, exth: []exthRecord{
		{exthTitle, []byte("Fjellvinden")}, {exthAuthor, []byte("Kari Nordmann")}, {exthISBN, []byte("9788205123456")},
	}}), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := firstEpubIn(book); got != azw3 {
		t.Errorf("firstEpubIn(folder) = %q, want the AZW3 inside it", got)
	}
	reason := unmatchedReason([]string{azw3})
	for _, want := range []string{`embedded Kindle file: title="Fjellvinden" author="Kari Nordmann"`, "isbn=9788205123456"} {
		if !strings.Contains(reason, want) {
			t.Errorf("unmatched reason %q does not contain %q", reason, want)
		}
	}
}
