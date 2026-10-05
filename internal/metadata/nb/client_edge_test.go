package nb

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// The constructor's client is usable and names itself the way the prefix
// routing (models.BookProviderFromForeignID) expects.
func TestNewClient(t *testing.T) {
	c := New()
	if c.Name() != "nb" || c.ready() != nil {
		t.Errorf("New() = name %q, ready %v", c.Name(), c.ready())
	}
}

// Lookups that find nothing return (nil, nil), never an error: an error
// would count against the provider in a fan-out, and "no such record" is an
// answer, not a failure.
func TestNotFoundIsNotAnError(t *testing.T) {
	ctx := context.Background()
	gone := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "", 404 }}
	if b, err := gone.client().GetBook(ctx, "nb:a0000000000000000000000000000001"); b != nil || err != nil {
		t.Errorf("GetBook on 404: book=%v err=%v", b, err)
	}
	if eds, err := gone.client().GetEditions(ctx, "nb:a0000000000000000000000000000001"); eds != nil || err != nil {
		t.Errorf("GetEditions on 404: editions=%v err=%v", eds, err)
	}
	empty := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "empty_search.json", 200 }}
	if b, err := empty.client().GetBookByISBN(ctx, "9788200000028"); b != nil || err != nil {
		t.Errorf("GetBookByISBN with no hit: book=%v err=%v", b, err)
	}
	if books, err := empty.client().SearchBooks(ctx, "Fjellvinden"); len(books) != 0 || err != nil {
		t.Errorf("SearchBooks with no hit: books=%v err=%v", books, err)
	}
}

// Input that cannot name a record is answered without a request.
func TestUnusableInputMakesNoRequest(t *testing.T) {
	ctx := context.Background()
	f := &fakeNB{t: t, route: func(*http.Request) (string, int) { return "author_works.json", 200 }}
	c := f.client()
	if b, err := c.GetBookByISBN(ctx, "not an isbn"); b != nil || err != nil {
		t.Errorf("GetBookByISBN(junk): book=%v err=%v", b, err)
	}
	if books, err := c.SearchBooks(ctx, "   "); books != nil || err != nil {
		t.Errorf("SearchBooks(blank): books=%v err=%v", books, err)
	}
	if authors, err := c.SearchAuthors(ctx, ""); authors != nil || err != nil {
		t.Errorf("SearchAuthors(blank): authors=%v err=%v", authors, err)
	}
	if _, err := c.GetAuthorWorks(ctx, "OL1A"); err == nil {
		t.Error("GetAuthorWorks accepted a foreign ID of another provider")
	}
	if len(f.reqs) != 0 {
		t.Errorf("made %d requests for unusable input", len(f.reqs))
	}
}

// A network failure is an error, not an empty answer: an empty answer from
// the primary is taken as fact (#2332).
func TestTransportErrorIsAnError(t *testing.T) {
	boom := errors.New("connection refused")
	c := &Client{http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, boom })}}
	if _, err := c.GetBookByISBN(context.Background(), "9788200000028"); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the transport error", err)
	}
	if _, _, err := c.GetAuthorWorksSnapshot(context.Background(), "nb:author:10000001"); !errors.Is(err, boom) {
		t.Errorf("snapshot err = %v, want the transport error", err)
	}
}

// A person credit without a usable authority link yields no author ID, so it
// is never turned into an "nb:author:" record that GetAuthor cannot resolve.
func TestAuthorityID(t *testing.T) {
	for identifier, want := range map[string]string{
		"bibsys.no:authority:90000001": "90000001",
		"bibsys.no:authority:":         "",
		"bibsys.no:authority:12ab":     "",
		"viaf:12345":                   "",
		"":                             "",
	} {
		if got := (person{Identifier: identifier}).authorityID(); got != want {
			t.Errorf("authorityID(%q) = %q, want %q", identifier, got, want)
		}
	}
}

func TestInvertName(t *testing.T) {
	for in, want := range map[string]string{
		"Nordmann, Kari": "Kari Nordmann",
		"Nordmann":       "Nordmann", // a single name has nothing to invert
		"Nordmann, ":     "Nordmann, ",
	} {
		if got := invertName(in); got != want {
			t.Errorf("invertName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseYear(t *testing.T) {
	for in, want := range map[string]int{"2019": 2019, "[2015]": 2015, "cop. 2012": 2012, "s.a.": 0, "1066": 0, "12": 0} {
		got := 0
		if y := parseYear(in); y != nil {
			got = y.Year()
		}
		if got != want {
			t.Errorf("parseYear(%q) = %d, want %d", in, got, want)
		}
	}
}

// A record with no language and no subject genres still builds a book: the
// fields stay empty rather than failing the work.
func TestBuildWork_SparseRecord(t *testing.T) {
	var m itemMetadata
	m.Title = "Fjellvinden"
	m.Identifiers.SesamID = "a0000000000000000000000000000099"
	books := groupWorks([]item{{ID: m.Identifiers.SesamID, Metadata: m}}, "")
	if len(books) != 1 || books[0].Language != "" || books[0].Genres == nil || len(books[0].Genres) != 0 {
		t.Errorf("books = %+v, want one book with no language and empty, non-nil genres", books)
	}
}

// NB credits an author as "aut" or, about as often, as the generic "cre";
// other creator-index credits (translator, narrator) are not authorship.
func TestIsAuthor(t *testing.T) {
	for role, want := range map[string]bool{"aut": true, "cre": true, "creator": true, "trl": false, "nrt": false, "aui": false, "": false} {
		p := person{Roles: []struct {
			Name string `json:"name"`
		}{{Name: role}}}
		if got := p.isAuthor(); got != want {
			t.Errorf("isAuthor(%q) = %v, want %v", role, got, want)
		}
	}
}
