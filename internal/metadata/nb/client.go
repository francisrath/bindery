// Package nb provides a read-only client for Nasjonalbiblioteket, the
// National Library of Norway, via its public catalogue search API. No API key
// is required.
//
// Role: opt-in primary. NB holds every Norwegian publication by legal deposit
// under its original title, where OpenLibrary tends to catalogue Norwegian
// books under their English translation. It is wired only when selected as the
// primary provider, so installs that did not choose it never call NB.
//
// Endpoints:
//   - https://api.nb.no/catalog/v1/items — bibliographic search (CC0 metadata)
//   - https://authority.bibsys.no — the Norwegian authority file, used only to
//     turn an author's authority ID back into a name, because NB's search has
//     no field that accepts the ID.
//
// NB catalogues editions, not works: every printing, translation and
// audiobook is its own record. groupWorks folds them into works.
package nb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	itemsBase     = "https://api.nb.no/catalog/v1/items"
	authorityBase = "https://authority.bibsys.no/authority/rest/authorities/v2/"
	idPrefix      = "nb:"
	authorPrefix  = "nb:author:"
	// authorityIDPrefix is how NB records name a person's authority record.
	authorityIDPrefix = "bibsys.no:authority:"
	// pageSize is the API's largest accepted page; 101 is rejected with 400.
	pageSize = 100
	// maxWorksPages caps an author catalogue at 500 edition records. A larger
	// author is reported as a partial snapshot rather than paged without end.
	maxWorksPages = 5
	// maxResponseBytes bounds what a misbehaving host can make Bindery decode
	// (#2357). A full 100-record page with expanded metadata is ~1 MiB.
	maxResponseBytes = 32 << 20
)

// sesamIDRe matches NB's record identifier, a 32-char hex string. Validated
// before it is put in a request path.
var sesamIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// authorityIDRe matches a Norwegian authority file system control number.
var authorityIDRe = regexp.MustCompile(`^[0-9]+$`)

// Client implements metadata.Provider for Nasjonalbiblioteket.
type Client struct {
	http *http.Client
}

// New creates a new NB client.
func New() *Client {
	return &Client{
		http: &http.Client{Timeout: 15 * time.Second, Transport: httpsec.DefaultProxyTransport()},
	}
}

func (c *Client) Name() string { return "nb" }

// ready reports ErrProviderNotConfigured for a client that cannot make
// requests. NB needs no credentials, so New always returns a usable client;
// this guards the zero value so it is skipped rather than panicking.
func (c *Client) ready() error {
	if c == nil || c.http == nil {
		return metadata.ErrProviderNotConfigured
	}
	return nil
}

// SearchAuthors finds authors whose name matches every word of query, from the
// author credits of matching records. Each person is keyed by authority ID, so
// two people sharing a name stay apart.
func (c *Client) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	words := strings.Fields(query)
	if len(words) == 0 {
		return nil, nil
	}
	params := url.Values{"q": {"*"}}
	for _, w := range words {
		params.Add("filter", "api_nameauthor:"+escapeQuery(w))
	}
	params.Add("filter", "mediatype:bøker")
	page, err := c.search(ctx, params, 0)
	if err != nil {
		return nil, fmt.Errorf("nb search authors: %w", err)
	}

	counts := make(map[string]int)
	byID := make(map[string]models.Author)
	var order []string
	for _, it := range page.Embedded.Items {
		for _, p := range it.Metadata.People {
			id := p.authorityID()
			if id == "" || !p.isAuthor() || !nameMatches(p.Name, words) {
				continue
			}
			if _, ok := byID[id]; !ok {
				byID[id] = personToAuthor(p)
				order = append(order, id)
			}
			counts[id]++
		}
	}
	authors := make([]models.Author, 0, len(order))
	for _, id := range order {
		a := byID[id]
		// A count of matching records, not works: editions are not grouped
		// here. It only ranks same-name candidates against each other.
		a.Statistics = &models.AuthorStats{BookCount: counts[id]}
		authors = append(authors, a)
	}
	return authors, nil
}

// SearchBooks searches catalogue metadata (not digitised full text) and folds
// the matching editions into works. An ISBN query is answered by
// GetBookByISBN.
func (c *Client) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	// Only a query that is nothing but an ISBN; a title containing digits
	// stays a text search.
	if isbn13, isbn10 := isbnutil.Extract(query); isbnutil.Normalize(query) == firstNonEmpty(isbn13, isbn10) {
		b, err := c.GetBookByISBN(ctx, query)
		if err != nil || b == nil {
			return nil, err
		}
		return []models.Book{*b}, nil
	}
	params := url.Values{
		"q":          {escapeQuery(query)},
		"searchType": {"FIELD_RESTRICTED_SEARCH"},
		"filter":     {"mediatype:bøker"},
	}
	page, err := c.search(ctx, params, 0)
	if err != nil {
		return nil, fmt.Errorf("nb search books: %w", err)
	}
	return groupWorks(page.Embedded.Items, ""), nil
}

// GetAuthor resolves an "nb:author:<authority id>" through the Norwegian
// authority file. Returns (nil, nil) when the authority record does not exist.
func (c *Client) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	id, err := authorityIDFromForeignID(foreignID)
	if err != nil {
		return nil, err
	}
	name, found, err := c.authorityName(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("nb get author %s: %w", foreignID, err)
	}
	if !found {
		return nil, nil
	}
	a := personToAuthor(person{Name: name, Identifier: authorityIDPrefix + id})
	return &a, nil
}

// GetAuthorWorks returns the author's catalogue, folded into works.
func (c *Client) GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	books, _, err := c.GetAuthorWorksSnapshot(ctx, authorForeignID)
	return books, err
}

// GetAuthorWorksSnapshot is GetAuthorWorks plus whether the catalogue is
// complete. It is not when the author has more records than maxWorksPages
// covers, so catalogue reconciliation must not read a missing work as removed.
//
// The records are fetched by the authority file's name heading and then kept
// only when they credit this authority ID as author, which drops homonyms and
// records where the person is translator or narrator. A failed name lookup is
// an error, never an empty catalogue: an empty result from a primary is taken
// as fact.
func (c *Client) GetAuthorWorksSnapshot(ctx context.Context, authorForeignID string) ([]models.Book, bool, error) {
	if err := c.ready(); err != nil {
		return nil, false, err
	}
	id, err := authorityIDFromForeignID(authorForeignID)
	if err != nil {
		return nil, false, err
	}
	name, found, err := c.authorityName(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("nb get author works %s: %w", authorForeignID, err)
	}
	if !found {
		return nil, true, nil
	}

	params := url.Values{
		"q": {"*"},
		// Audiobooks are included so their ISBNs land on the work: a
		// library file is as likely to be the audiobook edition.
		"filter": {`nameauthor:"` + escapeQuery(name) + `"`, "mediatype:(bøker OR lydopptak)"},
	}
	var items []item
	complete := false
	for p := 0; p < maxWorksPages; p++ {
		page, err := c.search(ctx, params, p)
		if err != nil {
			return nil, false, fmt.Errorf("nb get author works %s: %w", authorForeignID, err)
		}
		items = append(items, page.Embedded.Items...)
		if p+1 >= page.Page.TotalPages {
			complete = true
			break
		}
	}
	return groupWorks(items, id), complete, nil
}

// GetBook fetches a single edition record by "nb:<sesam id>". The book's
// editions are that one record; NB has no work record to list siblings from.
func (c *Client) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	id := strings.TrimPrefix(foreignID, idPrefix)
	if id == foreignID || !sesamIDRe.MatchString(id) {
		return nil, fmt.Errorf("nb: not a book id: %q", foreignID)
	}
	var it item
	found, err := c.getJSON(ctx, itemsBase+"/"+id, &it)
	if err != nil {
		return nil, fmt.Errorf("nb get book %s: %w", foreignID, err)
	}
	if !found {
		return nil, nil
	}
	books := groupWorks([]item{it}, "")
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

// GetEditions returns the record behind bookForeignID as its only edition, so
// profile checks on ISBN and page count have evidence instead of nothing.
func (c *Client) GetEditions(ctx context.Context, bookForeignID string) ([]models.Edition, error) {
	b, err := c.GetBook(ctx, bookForeignID)
	if err != nil || b == nil {
		return nil, err
	}
	return b.Editions, nil
}

// GetBookByISBN looks up an edition by ISBN-13 or ISBN-10, any media type, so
// an audiobook ISBN resolves as well as a print one.
func (c *Client) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	isbn13, isbn10 := isbnutil.Extract(isbn)
	want := firstNonEmpty(isbn13, isbn10)
	if want == "" {
		return nil, nil
	}
	page, err := c.search(ctx, url.Values{"q": {"isbn:" + want}}, 0)
	if err != nil {
		return nil, fmt.Errorf("nb get book by ISBN: %w", err)
	}
	books := groupWorks(page.Embedded.Items, "")
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

// search runs one page of an items query with expanded metadata, which is
// what carries the author credits and uniform titles.
func (c *Client) search(ctx context.Context, params url.Values, page int) (*searchResponse, error) {
	params.Set("size", strconv.Itoa(pageSize))
	params.Set("page", strconv.Itoa(page))
	params.Set("expand", "metadata")
	var out searchResponse
	if _, err := c.getJSON(ctx, itemsBase+"?"+params.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// authorityName returns the authorised name heading ("Last, First") for an
// authority record. found is false on 404.
func (c *Client) authorityName(ctx context.Context, id string) (string, bool, error) {
	var rec authorityRecord
	found, err := c.getJSON(ctx, authorityBase+id+"?format=json", &rec)
	if err != nil || !found {
		return "", found, err
	}
	name := rec.heading()
	if name == "" || rec.Deleted {
		return "", false, nil
	}
	return name, true, nil
}

// getJSON GETs endpoint and decodes the body into out. found is false on 404.
func (c *Client) getJSON(ctx context.Context, endpoint string, out any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", useragent.Get())
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
		return false, fmt.Errorf("decode response: %w", err)
	}
	return true, nil
}

func authorityIDFromForeignID(foreignID string) (string, error) {
	id := strings.TrimPrefix(foreignID, authorPrefix)
	if id == foreignID || !authorityIDRe.MatchString(id) {
		return "", errors.New("nb: not an author id: " + strconv.Quote(foreignID))
	}
	return id, nil
}

// escapeQuery backslash-escapes the query_string syntax characters so user
// input is searched as text instead of being parsed as query operators.
func escapeQuery(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`+-=&|><!(){}[]^"~*?:\/`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
