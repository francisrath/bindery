package api

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// What makes a merge stick (#2554): a refresh that still reports a merged-away
// series id files the book under the series it was merged into, and does not
// recreate the old series.
func TestExistingBookSeriesLinker_FollowsMergeAliases(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	ctx := context.Background()
	kept := f.addImportedBook(t, "OL1W", "Fjellvinden")
	later := f.addImportedBook(t, "OL2W", "Havets stemme")

	target := &models.Series{ForeignID: "hc-series:900", Title: "Fjellserien"}
	source := &models.Series{ForeignID: "ol-series:fjellserien-trilogien", Title: "Fjellserien-trilogien"}
	for _, s := range []*models.Series{target, source} {
		if err := f.series.CreateOrGet(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.series.LinkBook(ctx, source.ID, kept.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.Merge(ctx, target.ID, []int64{source.ID}, ""); err != nil {
		t.Fatal(err)
	}

	linker := newExistingBookSeriesLinker(f.series, f.author.ID, map[int64]struct{}{kept.ID: {}, later.ID: {}})
	for _, b := range []*models.Book{kept, later} {
		pos := map[int64]string{kept.ID: "1", later.ID: "2"}[b.ID]
		linker.link(ctx, b, []models.SeriesRef{{ForeignID: source.ForeignID, Title: source.Title, Position: pos, Primary: true}})
	}

	for b, want := range map[*models.Book]string{kept: "1", later: "2"} {
		links := linksForBook(t, f, b.ID)
		if len(links) != 1 || links[0].seriesTitle != "Fjellserien" || links[0].position != want {
			t.Errorf("%s links = %+v, want only Fjellserien at %s", b.Title, links, want)
		}
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM series`).Scan(&n); err != nil || n != 1 {
		t.Errorf("series rows = %d (%v), want 1: the refresh recreated the merged series", n, err)
	}
}
