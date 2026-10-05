package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/models"
)

// pngBytes is enough of a PNG for content sniffing.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func coverFixture(t *testing.T) (*BookHandler, *models.Book, func(body []byte) *httptest.ResponseRecorder) {
	t.Helper()
	h, books, _, author, ctx := bookFixture(t)
	h.WithCoverStore(covers.NewStore(t.TempDir()))
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t", Status: "wanted",
		ImageURL: "https://example.invalid/old.jpg", Genres: []string{}, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	post := func(body []byte) *httptest.ResponseRecorder {
		id := strconv.FormatInt(book.ID, 10)
		req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/book/"+id+"/cover", bytes.NewReader(body)), "id", id)
		rec := httptest.NewRecorder()
		h.UploadCover(rec, req)
		return rec
	}
	return h, book, post
}

func TestUploadCover_StoresAndSetsCover(t *testing.T) {
	h, book, post := coverFixture(t)
	if rec := post(pngBytes); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got, err := h.books.GetByID(t.Context(), book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !covers.IsRef(got.ImageURL) {
		t.Fatalf("image_url = %q, want a stored-cover reference", got.ImageURL)
	}
	if _, ct, ok := h.coverStore.Resolve(got.ImageURL); !ok || ct != "image/png" {
		t.Errorf("stored cover resolves = %v, content type %q", ok, ct)
	}
}

// Anything that is not an image, or too large, is refused and the book keeps
// its cover.
func TestUploadCover_RejectsBadUploads(t *testing.T) {
	for name, tc := range map[string]struct {
		body []byte
		want int
	}{
		"not an image": {[]byte("<html>hello</html>"), http.StatusUnsupportedMediaType},
		"empty":        {nil, http.StatusUnsupportedMediaType},
		"too large":    {append(append([]byte{}, pngBytes...), make([]byte, covers.MaxBytes)...), http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			h, book, post := coverFixture(t)
			if rec := post(tc.body); rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			got, _ := h.books.GetByID(t.Context(), book.ID)
			if got.ImageURL != book.ImageURL {
				t.Errorf("image_url changed to %q", got.ImageURL)
			}
		})
	}
}

func TestUploadCover_NoStoreIsUnavailable(t *testing.T) {
	h, _, post := coverFixture(t)
	h.coverStore = nil
	if rec := post(pngBytes); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}

// Another user's book is not found, as for every per-book route.
func TestUploadCover_OtherUsersBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserAuthors(t)
	ctx := t.Context()
	book := &models.Book{ForeignID: "B2", AuthorID: f.a1.ID, Title: "T", SortTitle: "t", Status: "wanted", Genres: []string{}, OwnerUserID: f.u1}
	if err := f.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	h := NewBookHandler(f.books, nil, nil, nil).WithCoverStore(covers.NewStore(t.TempDir()))
	id := strconv.FormatInt(book.ID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/book/"+id+"/cover", bytes.NewReader(pngBytes)), "id", id)
	req = req.WithContext(withAuthCtx(req.Context(), f.u2, "user"))
	rec := httptest.NewRecorder()
	h.UploadCover(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if got, _ := f.books.GetByID(ctx, book.ID); got.ImageURL != "" {
		t.Errorf("another user's book got a cover: %q", got.ImageURL)
	}
}
