package importer

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A release packed as a RAR set waits for an unpacker instead of spending its
// import retries, and once it is extracted the archive stays out of the
// library (#3203).

// rarRelease lays out a torrent folder holding a RAR set and an .nfo, as a
// scene audiobook release ships, and returns the folder and its file names.
func rarRelease(t *testing.T) (string, []string) {
	t.Helper()
	folder := filepath.Join(t.TempDir(), "Fjellvinden")
	if err := os.MkdirAll(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	names := []string{"Fjellvinden.part1.rar", "Fjellvinden.part2.rar", "Fjellvinden.nfo"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return folder, names
}

func TestIsArchivePart(t *testing.T) {
	for name, want := range map[string]bool{
		"Fjellvinden.rar": true, "Fjellvinden.part1.rar": true, "Fjellvinden.part12.RAR": true,
		"Fjellvinden.r00": true, "Fjellvinden.r99": true, "Fjellvinden.s00": true,
		// Book files and sidecars, including the RAR-based comic format.
		"Fjellvinden.cbr": false, "Fjellvinden.mp3": false, "Fjellvinden.epub": false, "Fjellvinden.nfo": false,
		"Fjellvinden.r": false, "Fjellvinden.r100": false, "Fjellvinden.rtf": false, "rar": false,
	} {
		if got := isArchivePart(name); got != want {
			t.Errorf("isArchivePart(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestArchiveAwaitingExtraction(t *testing.T) {
	folder, _ := rarRelease(t)
	if got := archiveAwaitingExtraction(folder); filepath.Base(got) != "Fjellvinden.part1.rar" {
		t.Errorf("archive-only folder: got %q, want its first volume", got)
	}

	// Extracted: a book file next to the archive means there is something to
	// import.
	if err := os.WriteFile(filepath.Join(folder, "01 Spor 1.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := archiveAwaitingExtraction(folder); got != "" {
		t.Errorf("extracted folder: got %q, want none", got)
	}

	// The RAR 4 naming: the first volume is x.rar, although x.r00 sorts first.
	old := filepath.Join(t.TempDir(), "Havgapet")
	if err := os.MkdirAll(old, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Havgapet.r00", "Havgapet.r01", "Havgapet.rar"} {
		if err := os.WriteFile(filepath.Join(old, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := archiveAwaitingExtraction(old); filepath.Base(got) != "Havgapet.rar" {
		t.Errorf("RAR 4 set: got %q, want Havgapet.rar", got)
	}

	// No archive at all, and a folder that does not exist.
	if got := archiveAwaitingExtraction(t.TempDir()); got != "" {
		t.Errorf("empty folder: got %q, want none", got)
	}
	if got := archiveAwaitingExtraction(filepath.Join(t.TempDir(), "gone")); got != "" {
		t.Errorf("missing folder: got %q, want none", got)
	}
}

// Counted as present, an archive-only folder spent a retry on every poll.
func TestImportSourcePresent_WaitsForArchiveExtraction(t *testing.T) {
	folder, _ := rarRelease(t)
	if importSourcePresent(folder, nil) {
		t.Error("an archive-only folder counts as present, so each poll spends a retry")
	}
	if err := os.WriteFile(filepath.Join(folder, "01 Spor 1.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !importSourcePresent(folder, nil) {
		t.Error("once extracted, the folder must count as present")
	}
}

// After the wait the download is blocked with a reason that names the archive
// and says what to do, not the "files moved or PathRemap" one.
func TestImportSourceGoneReason_NamesTheArchive(t *testing.T) {
	folder, _ := rarRelease(t)
	reason := importSourceGoneReason(folder)
	for _, want := range []string{"Fjellvinden.part1.rar", "does not unpack archives", "unpackerr"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not mention %q", reason, want)
		}
	}
	if gone := importSourceGoneReason(t.TempDir()); strings.Contains(gone, "archive") {
		t.Errorf("a folder without an archive got the archive reason: %q", gone)
	}
}

// End to end through the qBittorrent poller, as the issue reproduced it: the
// first attempt says it is waiting for the archive, the polls after it spend
// no retry, and once the tracks are extracted into the torrent's folder they
// import, without the archive volumes.
func TestQbittorrentRARRelease_WaitsThenImportsWithoutTheArchive(t *testing.T) {
	s, book, dlRepo, _, ctx := multiDiscFixture(t, "hardlink")
	folder, names := rarRelease(t)

	const hash = "0ddba11e0ddba11e0ddba11e0ddba11e0ddba11e"
	var files []map[string]any
	for _, name := range names {
		files = append(files, map[string]any{"name": "Fjellvinden/" + name, "size": len(name)})
	}
	srv := httptest.NewServer(qbittorrentMatrixHandler(t, map[string]any{
		"hash": hash, "name": "Fjellvinden", "progress": 1.0, "state": "uploading",
		"category": "audiobooks", "amount_left": 0, "save_path": filepath.Dir(folder), "content_path": folder,
	}, files))
	t.Cleanup(srv.Close)
	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", Host: host, Port: port,
		Username: "admin", Password: "secret", Category: "audiobooks", Enabled: true}
	if err := s.clients.Create(ctx, client); err != nil {
		t.Fatal(err)
	}
	torrentHash := hash
	dl := &models.Download{GUID: "guid-fjellvinden", Title: "Fjellvinden", NZBURL: "magnet:?xt=urn:btih:" + hash,
		Status: models.StateDownloading, Protocol: "torrent", TorrentID: &torrentHash,
		DownloadClientID: &client.ID, BookID: book.BookID}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	poll := func() *models.Download {
		t.Helper()
		s.checkQbittorrentDownloads(ctx, client)
		got, err := dlRepo.GetByID(ctx, dl.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	got := poll()
	if got.Status != models.StateImportFailed || !strings.Contains(got.ErrorMessage, "waiting for Fjellvinden.part1.rar to be extracted") {
		t.Fatalf("first attempt: status %q, message %q; want importFailed waiting for the archive", got.Status, got.ErrorMessage)
	}
	// Before #3203 these spent the five retries and blocked the download.
	for i := range importRetryLimit + 1 {
		if got = poll(); got.Status != models.StateImportFailed || got.ImportRetryCount != 0 {
			t.Fatalf("poll %d while waiting: status %q, retries %d; want importFailed with no retry spent", i+1, got.Status, got.ImportRetryCount)
		}
	}

	// unpackerr with move_back = true: the tracks land in the torrent's own
	// folder, and the archive stays for seeding.
	for _, track := range []string{"01 Spor 1.mp3", "02 Spor 2.mp3"} {
		if err := os.WriteFile(filepath.Join(folder, track), []byte(track), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got = poll(); got.Status != models.StateImported {
		t.Fatalf("after extraction: status %q (%s), want imported", got.Status, got.ErrorMessage)
	}
	placed := relFiles(t, importedAudiobookDir(t, s))
	for _, want := range []string{"01 Spor 1.mp3", "02 Spor 2.mp3", "Fjellvinden.nfo"} {
		if _, ok := placed[want]; !ok {
			t.Errorf("%s not placed (placed: %v)", want, placed)
		}
	}
	for name := range placed {
		if isArchivePart(name) {
			t.Errorf("archive volume %s placed in the library", name)
		}
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(folder, name)); err != nil {
			t.Errorf("%s gone from the torrent folder: %v", name, err)
		}
	}
}

// In move mode on one filesystem the torrent folder is renamed into the
// library wholesale, and the sweep that follows is there for the receipt and
// repair clutter. It must leave archive volumes alone: the move already took
// the folder from the client, and deleting the volumes would destroy the
// archive for good.
func TestMoveMode_SweepKeepsArchiveVolumes(t *testing.T) {
	s, dl, dlRepo, _, ctx := multiDiscFixture(t, "move")
	folder := filepath.Join(t.TempDir(), "Fjellvinden")
	if err := os.MkdirAll(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	var audio []string
	for _, name := range []string{
		"01 Spor 01.mp3", "02 Spor 02.mp3", "Fjellvinden.part1.rar", "Fjellvinden.r00", "Fjellvinden.nzb", "Fjellvinden.vol01+02.par2",
	} {
		p := filepath.Join(folder, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		if filepath.Ext(name) == ".mp3" {
			audio = append(audio, p)
		}
	}
	sort.Strings(audio)

	s.tryImportInternal(withTorrentOwnFolder(ctx, true), dl, folder, "", "", "", nil, audio)

	if got, _ := dlRepo.GetByID(ctx, dl.ID); got.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", got.Status, got.ErrorMessage)
	}
	placed := relFiles(t, importedAudiobookDir(t, s))
	for _, want := range []string{"01 Spor 01.mp3", "Fjellvinden.part1.rar", "Fjellvinden.r00"} {
		if _, ok := placed[want]; !ok {
			t.Errorf("%s missing after the move (placed: %v)", want, placed)
		}
	}
	for _, clutter := range []string{"Fjellvinden.nzb", "Fjellvinden.vol01+02.par2"} {
		if _, ok := placed[clutter]; ok {
			t.Errorf("%s was not swept from the moved folder", clutter)
		}
	}
}
