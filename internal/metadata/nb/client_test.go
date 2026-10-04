package nb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
)

// The fixtures in testdata/ have the exact shape of api.nb.no and
// authority.bibsys.no responses, with an invented author, titles and ISBNs.

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeNB answers each request from route, which maps it to (fixture file,
// status), and records every request it saw.
type fakeNB struct {
	t     *testing.T
	mu    sync.Mutex
	reqs  []*http.Request
	route func(*http.Request) (string, int)
}

func (f *fakeNB) client() *Client {
	return &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r)
		f.mu.Unlock()
		file, status := f.route(r)
		body := ""
		if file != "" {
			b, err := os.ReadFile("testdata/" + file)
			if err != nil {
				f.t.Fatalf("fixture: %v", err)
			}
			body = string(b)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
}

func isAuthority(r *http.Request) bool { return r.URL.Host == "authority.bibsys.no" }

func TestSearchAuthors(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "author_works.json", 200 }}
	authors, err := f.client().SearchAuthors(context.Background(), "kari nordmann")
	if err != nil {
		t.Fatal(err)
	}
	// Two people share the name; the authority ID keeps them apart. The
	// co-credited author and translators are dropped by the name match.
	if len(authors) != 2 {
		t.Fatalf("got %d authors, want 2: %+v", len(authors), authors)
	}
	a := authors[0]
	if a.ForeignID != "nb:author:10000001" || a.Name != "Kari Nordmann" || a.SortName != "Nordmann, Kari" || a.MetadataProvider != "nb" {
		t.Errorf("first author = %+v", a)
	}
	if a.Statistics == nil || a.Statistics.BookCount != 5 {
		t.Errorf("record count = %+v, want 5 (author credits only, not the translator credit)", a.Statistics)
	}
	if authors[1].ForeignID != "nb:author:10000002" {
		t.Errorf("second author = %q", authors[1].ForeignID)
	}

	q := f.reqs[0].URL.Query()
	if got := q["filter"]; strings.Join(got, ",") != "api_nameauthor:kari,api_nameauthor:nordmann,mediatype:bøker" {
		t.Errorf("filters = %v", got)
	}
	if q.Get("expand") != "metadata" {
		t.Errorf("expand = %q; without it search hits carry no author credits", q.Get("expand"))
	}
	if ua := f.reqs[0].Header.Get("User-Agent"); !strings.HasPrefix(ua, "bindery/") {
		t.Errorf("User-Agent = %q", ua)
	}
}

func TestGetBookByISBN_Audiobook(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "isbn_audiobook.json", 200 }}
	b, err := f.client().GetBookByISBN(context.Background(), "978-82-00-00002-8")
	if err != nil || b == nil {
		t.Fatalf("book=%v err=%v", b, err)
	}
	if got := f.reqs[0].URL.Query().Get("q"); got != "isbn:9788200000028" {
		t.Errorf("q = %q", got)
	}
	if got := f.reqs[0].URL.Query()["filter"]; len(got) != 0 {
		t.Errorf("ISBN lookup must not filter by media type, got %v", got)
	}
	if b.ForeignID != "nb:a0000000000000000000000000000002" || b.Title != "Fjellvinden" || b.Language != "nob" {
		t.Errorf("book = %q %q %q", b.ForeignID, b.Title, b.Language)
	}
	if b.Author == nil || b.Author.ForeignID != "nb:author:10000001" {
		t.Errorf("author = %+v; the narrator must not be taken for the author", b.Author)
	}
	if len(b.Editions) != 1 || b.Editions[0].Format != "audiobook" || *b.Editions[0].ISBN13 != "9788200000028" {
		t.Errorf("editions = %+v", b.Editions)
	}
}

func TestSearchBooks_ISBNQueryUsesISBNLookup(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "isbn_audiobook.json", 200 }}
	books, err := f.client().SearchBooks(context.Background(), "9788200000028")
	if err != nil || len(books) != 1 {
		t.Fatalf("books=%v err=%v", books, err)
	}
	if got := f.reqs[0].URL.Query().Get("q"); got != "isbn:9788200000028" {
		t.Errorf("q = %q", got)
	}
}

// The aggregator's canonical lookup searches the primary with "isbn:<n>".
func TestSearchBooks_PrefixedISBNQueryUsesISBNLookup(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "isbn_audiobook.json", 200 }}
	books, err := f.client().SearchBooks(context.Background(), "isbn:9788200000028")
	if err != nil || len(books) != 1 {
		t.Fatalf("books=%v err=%v", books, err)
	}
	if got := f.reqs[0].URL.Query().Get("q"); got != "isbn:9788200000028" {
		t.Errorf("q = %q", got)
	}
}

func TestSearchBooks_TextSearchIsMetadataOnly(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "author_works.json", 200 }}
	if _, err := f.client().SearchBooks(context.Background(), "fjellvinden: roman"); err != nil {
		t.Fatal(err)
	}
	q := f.reqs[0].URL.Query()
	// The default searchType includes OCR'd full text of digitised books.
	if q.Get("searchType") != "FIELD_RESTRICTED_SEARCH" || q.Get("q") != `fjellvinden\: roman` {
		t.Errorf("searchType=%q q=%q", q.Get("searchType"), q.Get("q"))
	}
	// Same media types as the author catalogue, so a work found by search
	// carries the same editions (audiobook ISBNs included) as in the catalogue.
	if got := q.Get("filter"); got != "mediatype:(bøker OR lydopptak)" {
		t.Errorf("filter = %q", got)
	}
}

// The translation case this provider exists for: the author's catalogue
// carries the Norwegian original title, with the English and French
// translations folded into the same work instead of listed beside it.
func TestGetAuthorWorks_TranslationJoinsOriginal(t *testing.T) {
	f := &fakeNB{t: t, route: func(r *http.Request) (string, int) {
		if isAuthority(r) {
			return "authority.json", 200
		}
		return "author_works.json", 200
	}}
	books, complete, err := f.client().GetAuthorWorksSnapshot(context.Background(), "nb:author:10000001")
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Error("a single-page catalogue must be complete")
	}
	// Homonym's book (10000002) and the book she only translated are excluded.
	if len(books) != 2 {
		titles := make([]string, len(books))
		for i, b := range books {
			titles[i] = b.Title
		}
		t.Fatalf("got %d works %v, want 2", len(books), titles)
	}
	w := books[0]
	if w.Title != "Fjellvinden" || w.ForeignID != "nb:a0000000000000000000000000000001" || w.Language != "nob" {
		t.Errorf("work = %q %q %q, want the Norwegian print edition as representative", w.Title, w.ForeignID, w.Language)
	}
	if len(w.Editions) != 4 {
		t.Errorf("editions = %d, want 4 (print, audio, English, French)", len(w.Editions))
	}
	if w.ReleaseDate == nil || w.ReleaseDate.Year() != 2019 {
		t.Errorf("release = %v, want the earliest edition's year", w.ReleaseDate)
	}
	if w.Description == "" {
		t.Error("description missing")
	}
	if strings.Join(w.ProviderISBNs, ",") != "9788200000011,9788200000028,9781000000012,9782000000013" {
		t.Errorf("ISBNs = %v", w.ProviderISBNs)
	}
	// An original's noisy uniform title ("Noveller Utvalg") must not rename it.
	if books[1].Title != "Havets stemme" || books[1].ReleaseDate.Year() != 2015 {
		t.Errorf("second work = %q %v", books[1].Title, books[1].ReleaseDate)
	}

	q := f.reqs[1].URL.Query()
	if got := strings.Join(q["filter"], ","); got != `nameauthor:"Nordmann, Kari",mediatype:(bøker OR lydopptak)` {
		t.Errorf("filters = %s", got)
	}
}

func TestGetAuthorWorks_PartialWhenCapped(t *testing.T) {
	b, err := os.ReadFile("testdata/author_works.json")
	if err != nil {
		t.Fatal(err)
	}
	many := strings.Replace(string(b), `"totalPages": 1`, `"totalPages": 9`, 1)
	calls := 0
	c := &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := many
		if isAuthority(r) {
			a, _ := os.ReadFile("testdata/authority.json")
			body = string(a)
		} else {
			calls++
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	_, complete, err := c.GetAuthorWorksSnapshot(context.Background(), "nb:author:10000001")
	if err != nil {
		t.Fatal(err)
	}
	if complete || calls != maxWorksPages {
		t.Errorf("complete=%v pages=%d; a capped catalogue must report partial", complete, calls)
	}
}

// An upstream failure must surface as an error, never as an empty catalogue:
// the aggregator treats an empty answer from the primary as fact (#2332).
func TestGetAuthorWorks_ErrorsAreNotEmpty(t *testing.T) {
	for name, route := range map[string]func(*http.Request) (string, int){
		"authority down": func(r *http.Request) (string, int) {
			if isAuthority(r) {
				return "", 503
			}
			return "author_works.json", 200
		},
		"search down": func(r *http.Request) (string, int) {
			if isAuthority(r) {
				return "authority.json", 200
			}
			return "", 500
		},
	} {
		f := &fakeNB{t: t, route: route}
		if books, err := f.client().GetAuthorWorks(context.Background(), "nb:author:10000001"); err == nil {
			t.Errorf("%s: got %d books and no error", name, len(books))
		}
	}
}

func TestGetAuthor(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "authority.json", 200 }}
	a, err := f.client().GetAuthor(context.Background(), "nb:author:10000001")
	if err != nil || a == nil {
		t.Fatalf("author=%v err=%v", a, err)
	}
	if a.Name != "Kari Nordmann" || a.ForeignID != "nb:author:10000001" {
		t.Errorf("author = %+v", a)
	}
	if got := f.reqs[0].URL.String(); got != authorityBase+"10000001?format=json" {
		t.Errorf("url = %s", got)
	}

	missing := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "", 404 }}
	if a, err := missing.client().GetAuthor(context.Background(), "nb:author:99999999"); a != nil || err != nil {
		t.Errorf("unknown authority: author=%v err=%v, want nil, nil", a, err)
	}
	if _, err := missing.client().GetAuthor(context.Background(), "nb:author:../x"); err == nil {
		t.Error("malformed id must be rejected before any request")
	}
}

// GetBook must return the whole work, not one record: the aggregator refreshes
// an ISBN match through it, and a print record alone would drop the
// audiobook's ISBN from the book.
func TestGetBook_ReturnsWorkEditions(t *testing.T) {
	f := &fakeNB{t: t, route: func(r *http.Request) (string, int) {
		if strings.HasSuffix(r.URL.Path, "/items/a0000000000000000000000000000001") {
			return "item_print.json", 200
		}
		return "author_works.json", 200
	}}
	b, err := f.client().GetBook(context.Background(), "nb:a0000000000000000000000000000001")
	if err != nil || b == nil {
		t.Fatalf("book=%v err=%v", b, err)
	}
	if b.ForeignID != "nb:a0000000000000000000000000000001" || len(b.Editions) != 4 {
		t.Errorf("book %s has %d editions, want the work's 4", b.ForeignID, len(b.Editions))
	}
	q := f.reqs[1].URL.Query()
	if got := strings.Join(q["filter"], ","); got != `nameauthor:"Nordmann, Kari",mediatype:(bøker OR lydopptak)` || q.Get("q") != "Fjellvinden" {
		t.Errorf("sibling search q=%q filters=%s", q.Get("q"), got)
	}

	// The sibling search is best-effort: the record itself is still returned.
	down := &fakeNB{t: t, route: func(r *http.Request) (string, int) {
		if strings.Contains(r.URL.Path, "/items/") {
			return "item_print.json", 200
		}
		return "", 503
	}}
	b, err = down.client().GetBook(context.Background(), "nb:a0000000000000000000000000000001")
	if err != nil || b == nil || len(b.Editions) != 1 {
		t.Errorf("sibling search down: book=%v err=%v", b, err)
	}
}

func TestGetBook_RejectsMalformedID(t *testing.T) {
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "", 200 }}
	for _, id := range []string{"OL1W", "nb:author:10000001", "nb:../../x"} {
		if _, err := f.client().GetBook(context.Background(), id); err == nil {
			t.Errorf("GetBook(%q) succeeded", id)
		}
	}
	if len(f.reqs) != 0 {
		t.Errorf("made %d requests for malformed ids", len(f.reqs))
	}
}

func TestNotConfigured(t *testing.T) {
	ctx := context.Background()
	var c Client
	checks := map[string]error{}
	_, checks["SearchAuthors"] = c.SearchAuthors(ctx, "x")
	_, checks["SearchBooks"] = c.SearchBooks(ctx, "x")
	_, checks["GetAuthor"] = c.GetAuthor(ctx, "nb:author:1")
	_, checks["GetAuthorWorks"] = c.GetAuthorWorks(ctx, "nb:author:1")
	_, checks["GetBook"] = c.GetBook(ctx, "nb:a0000000000000000000000000000001")
	_, checks["GetEditions"] = c.GetEditions(ctx, "nb:a0000000000000000000000000000001")
	_, checks["GetBookByISBN"] = c.GetBookByISBN(ctx, "9788200000028")
	for name, err := range checks {
		if !errors.Is(err, metadata.ErrProviderNotConfigured) {
			t.Errorf("%s: err = %v, want ErrProviderNotConfigured", name, err)
		}
	}
}

func TestStripLanguageQualifier(t *testing.T) {
	for in, want := range map[string]string{
		"Fjellvinden Fransk":  "Fjellvinden",
		"The quiet one Norsk": "The quiet one",
		"Fjellvinden":         "Fjellvinden",
		"Havet og Os":         "Havet og Os",
		"Mot øst":             "Mot øst",
	} {
		if got := stripLanguageQualifier(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
