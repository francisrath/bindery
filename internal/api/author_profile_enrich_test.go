package api

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// The manual Refresh Metadata always asks the enrichers, even right after the
// staggered refresh stamped the author, and stamps a full answer (#3287).
func TestRefreshAuthorProfile_EnrichesWhateverTheStamp(t *testing.T) {
	provider := &stubMetaProvider{author: &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann"}}
	f := newCoreFixture(t, provider)
	e := &countingEnricher{}
	f.h.meta.SetAuthorEnrichers(e)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "openlibrary"}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	earlier := time.Now().Add(-time.Hour)
	if err := f.authors.StampEnrichment(ctx, author.ID, earlier); err != nil {
		t.Fatal(err)
	}

	f.h.refreshAuthorProfile(ctx, author)

	got, err := f.authors.GetByID(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || got.ImageURL != "https://commons.example/k.jpg" || got.ImageSource == nil {
		t.Errorf("%d enricher calls, photo %q from %+v; want the enriched photo", e.calls, got.ImageURL, got.ImageSource)
	}
	if ts, err := f.authors.LastEnrichmentAt(ctx, author.ID); err != nil || ts == nil || !ts.After(earlier.Add(time.Minute)) {
		t.Errorf("enrichment stamp %v, %v; want it moved to now", ts, err)
	}

	// Nothing new on a second click: the profile is unchanged, the answer is
	// still stamped.
	if err := f.authors.StampEnrichment(ctx, author.ID, earlier); err != nil {
		t.Fatal(err)
	}
	f.h.refreshAuthorProfile(ctx, got)
	if ts, _ := f.authors.LastEnrichmentAt(ctx, author.ID); ts == nil || !ts.After(earlier.Add(time.Minute)) {
		t.Errorf("an unchanged refresh did not stamp the enrichment: %v", ts)
	}
}
