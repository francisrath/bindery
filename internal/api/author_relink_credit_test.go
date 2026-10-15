package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type creditingEnricher struct{}

func (creditingEnricher) Name() string { return "crediting" }
func (creditingEnricher) EnrichAuthor(context.Context, *models.Author) (*metadata.AuthorProfile, error) {
	return &metadata.AuthorProfile{
		Description:       "Kari Nordmann is a writer.",
		DescriptionSource: &models.ProfileSource{Provider: "wikipedia", URL: "https://no.wikipedia.example/k", License: "CC BY-SA 4.0"},
		ImageURL:          "https://commons.example/k.jpg",
		ImageSource:       &models.ProfileSource{Provider: "wikimedia-commons", URL: "https://commons.example/File:K.jpg", License: "CC BY 2.5"},
	}, nil
}

// Both relink paths take the photo and bio from an enriched upstream, so the
// credit has to come along, and an enriched value must not displace one the
// author already has (#3287).
func TestRelinkKeepsEnrichedCredits(t *testing.T) {
	const name = "Kari Nordmann"
	newFixture := func(t *testing.T) *relinkUpstreamFixture {
		primary := &searchableAuthorProvider{
			stubMetaProvider:     stubMetaProvider{name: "openlibrary"},
			searchAuthorsByQuery: map[string][]models.Author{name: {{Name: name, ForeignID: "OL1A"}}},
			authors:              map[string]*models.Author{"OL1A": {Name: name, ForeignID: "OL1A"}},
		}
		f := newRelinkUpstreamFixture(t, primary)
		f.handler.meta.SetAuthorEnrichers(creditingEnricher{})
		return f
	}
	check := func(t *testing.T, f *relinkUpstreamFixture, id int64, wantDesc string, wantCredited bool) {
		t.Helper()
		got, err := f.authors.GetByID(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Description != wantDesc || (got.DescriptionSource != nil) != wantCredited {
			t.Errorf("bio %q credited=%v, want %q credited=%v", got.Description, got.DescriptionSource != nil, wantDesc, wantCredited)
		}
		if got.ImageURL != "https://commons.example/k.jpg" || got.ImageSource == nil || got.ImageSource.License != "CC BY 2.5" {
			t.Errorf("photo %q from %+v, want the Commons photo with its credit", got.ImageURL, got.ImageSource)
		}
	}

	t.Run("relink upstream", func(t *testing.T) {
		f := newFixture(t)
		author := f.createAuthor(t, &models.Author{Name: name, ForeignID: "abs:kari"})
		if rec := f.relink(t, author.ID); rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		check(t, f, author.ID, "Kari Nordmann is a writer.", true)
	})
	t.Run("relink upstream keeps the author's own bio", func(t *testing.T) {
		f := newFixture(t)
		author := f.createAuthor(t, &models.Author{Name: name, ForeignID: "abs:kari", Description: "From the library."})
		if rec := f.relink(t, author.ID); rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		check(t, f, author.ID, "From the library.", false)
	})
	t.Run("calibre relink", func(t *testing.T) {
		f := newFixture(t)
		author := f.createAuthor(t, &models.Author{Name: name, ForeignID: "calibre:author:1", MetadataProvider: "calibre"})
		if err := f.handler.relinkCalibreAuthor(f.ctx, author); err != nil {
			t.Fatal(err)
		}
		check(t, f, author.ID, "Kari Nordmann is a writer.", true)
	})
}
