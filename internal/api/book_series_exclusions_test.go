package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// The book page lists the series a book was taken out of, and Unlock all
// fields forgets them; any other edit leaves them alone (#2554).
func TestBookSeriesExclusionsEndpointAndUnlockAll(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	books, series := db.NewBookRepo(database), db.NewSeriesRepo(database)
	author := &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "openlibrary"}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	h := NewBookHandler(books, nil, db.NewHistoryRepo(database), nil).WithSeries(series)
	book := &models.Book{ForeignID: "B1", AuthorID: author.ID, Title: "Fjellvinden", SortTitle: "fjellvinden",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	s := &models.Series{ForeignID: "s:fjell", Title: "Fjellserien"}
	if err := series.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := series.LinkBook(ctx, s.ID, book.ID, "2", true); err != nil {
		t.Fatal(err)
	}
	if _, err := series.RemoveBookFromSeries(ctx, s.ID, book.ID); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(book.ID, 10)

	list := func() []db.BookSeriesExclusion {
		t.Helper()
		rec := httptest.NewRecorder()
		h.SeriesExclusions(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/book/"+id+"/series-exclusions", nil), "id", id))
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		var out []db.BookSeriesExclusion
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	update := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+id, bytes.NewBufferString(body)), "id", id))
		if rec.Code != http.StatusOK {
			t.Fatalf("update %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}

	if got := list(); len(got) != 1 || got[0].SeriesID != s.ID || got[0].SeriesTitle != "Fjellserien" || got[0].Position != "2" {
		t.Fatalf("exclusions = %+v, want Fjellserien at 2", got)
	}
	update(`{"title":"Fjellvinden (ny)"}`)
	if got := list(); len(got) != 1 {
		t.Fatalf("an ordinary edit cleared the exclusions: %+v", got)
	}
	update(`{"lockedFields":[]}`)
	if got := list(); len(got) != 0 {
		t.Errorf("after Unlock all = %+v, want none", got)
	}
}
