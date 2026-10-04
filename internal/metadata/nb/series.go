package nb

import (
	"context"
	"log/slog"
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

const (
	seriesIDPrefix = "nb-series:"
	// seriesConcurrency bounds MODS requests in flight for one catalogue.
	seriesConcurrency = 4
	// maxSeriesProbes caps MODS requests per work. Not every edition records
	// the author's series (a reprint may list only the publisher's imprint),
	// so a few are tried before giving up.
	maxSeriesProbes = 3
)

// modsRecord is the part of a MODS record that carries series membership.
type modsRecord struct {
	RelatedItems []struct {
		Type  string `xml:"type,attr"`
		Href  string `xml:"http://www.w3.org/1999/xlink href,attr"`
		Title string `xml:"titleInfo>title"`
		Part  string `xml:"titleInfo>partNumber"`
	} `xml:"relatedItem"`
}

// seriesRecords returns the IDs of the records whose search hit names any
// series. Only those are worth a MODS request; the search JSON has the name
// but not the number.
func seriesRecords(items []item) map[string]bool {
	ids := make(map[string]bool)
	for _, it := range items {
		if len(it.Metadata.Series) > 0 {
			ids[firstNonEmpty(it.Metadata.Identifiers.SesamID, it.ID)] = true
		}
	}
	return ids
}

// fillSeries sets each book's series and position from the MODS record of
// one of its editions. Best-effort: series is enrichment, so a failed lookup
// leaves the book without one rather than failing the catalogue.
//
// Two passes. The author's own series are the entries linked to their
// authority record. A cataloguer occasionally records one without the link;
// such an entry is accepted only when its series is linked elsewhere in the
// same books, because an unlinked series nobody links is a publisher imprint.
func (c *Client) fillSeries(ctx context.Context, books []models.Book, withSeries map[string]bool, authorID string) {
	var todo []int
	for i := range books {
		if len(seriesCandidates(books[i], withSeries)) > 0 {
			todo = append(todo, i)
		}
	}
	unlinked := make([][]models.SeriesRef, len(books))
	// Each goroutine writes only its own books[i] and unlinked[i].
	concurrency.RunBounded(ctx, todo, seriesConcurrency, func(ctx context.Context, i int) {
		for _, id := range seriesCandidates(books[i], withSeries) {
			linked, other, err := c.recordSeries(ctx, id, authorID)
			if err != nil {
				slog.Debug("nb: series lookup failed", "record", id, "error", err)
				return
			}
			if linked != nil {
				books[i].SeriesRefs = []models.SeriesRef{*linked}
				return
			}
			unlinked[i] = append(unlinked[i], other...)
		}
	})

	known := make(map[string]bool)
	for _, b := range books {
		for _, ref := range b.SeriesRefs {
			known[ref.ForeignID] = true
		}
	}
	for i := range books {
		if len(books[i].SeriesRefs) > 0 {
			continue
		}
		for _, ref := range unlinked[i] {
			if known[ref.ForeignID] {
				books[i].SeriesRefs = []models.SeriesRef{ref}
				break
			}
		}
	}
}

// seriesCandidates lists the book's record IDs that name a series, the
// representative first, at most maxSeriesProbes.
func seriesCandidates(b models.Book, withSeries map[string]bool) []string {
	var ids []string
	seen := make(map[string]bool)
	add := func(foreignID string) {
		id := strings.TrimPrefix(foreignID, idPrefix)
		if withSeries[id] && !seen[id] && len(ids) < maxSeriesProbes {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	add(b.ForeignID)
	for _, ed := range b.Editions {
		add(ed.ForeignID)
	}
	return ids
}

// recordSeries reads a record's MODS. linked is the author's own series: the
// entry linked to their authority record. Publisher imprint series ("<publisher>
// krim") are recorded as series too but carry no such link; those, and any
// author series recorded without the link, come back in unlinked when they
// have a number, for fillSeries to decide on.
func (c *Client) recordSeries(ctx context.Context, sesamID, authorID string) (linked *models.SeriesRef, unlinked []models.SeriesRef, err error) {
	var rec modsRecord
	found, err := c.getXML(ctx, metadataBase+sesamID+"/mods", &rec)
	if err != nil || !found {
		return nil, nil, err
	}
	for _, ri := range rec.RelatedItems {
		if ri.Type != "series" {
			continue
		}
		title := stripLanguageQualifier(strings.Join(strings.Fields(ri.Title), " "))
		slug := seriesSlug(title)
		if slug == "" {
			continue
		}
		ref := models.SeriesRef{
			// Scoped to the author: the series is the author's authority-
			// linked one, and two authors can name a series alike.
			ForeignID: seriesIDPrefix + authorID + ":" + slug,
			Title:     title,
			Position:  strings.TrimRight(strings.TrimSpace(ri.Part), "."),
			Primary:   true,
		}
		if strings.TrimSpace(ri.Href) == "(NO-TrBIB)"+authorID {
			return &ref, nil, nil
		}
		if ref.Position != "" {
			unlinked = append(unlinked, ref)
		}
	}
	return nil, unlinked, nil
}

// seriesSlug is the stable part of a series ID: folded, script-preserving
// (textutil.FoldForSlug), with each run of other characters collapsed to "-".
func seriesSlug(title string) string {
	words := strings.FieldsFunc(textutil.FoldForSlug(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Mc, r)
	})
	return strings.Join(words, "-")
}
