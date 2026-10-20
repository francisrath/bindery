package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// savingDuringLookup runs onGet the first time the provider is asked for an
// author, the moment a user's save would land while a Refresh or relink waits
// on the provider.
type savingDuringLookup struct {
	*searchableAuthorProvider
	onGet func()
}

func (p *savingDuringLookup) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	if p.onGet != nil {
		p.onGet()
		p.onGet = nil
	}
	return p.searchableAuthorProvider.GetAuthor(ctx, foreignID)
}

// A Refresh, a relink and a Calibre relink each build the whole author row
// from a snapshot read before the provider call. A bio the user saves during
// that call must survive, with its user credit, instead of being written over
// by the older snapshot (#3287).
func TestStaleAuthorSnapshotDoesNotOverwriteUserSave(t *testing.T) {
	const name = "Kari Nordmann"
	userBio := "Written by hand."
	newFixture := func(t *testing.T) (*relinkUpstreamFixture, *savingDuringLookup) {
		p := &savingDuringLookup{searchableAuthorProvider: &searchableAuthorProvider{
			stubMetaProvider:     stubMetaProvider{name: "openlibrary"},
			searchAuthorsByQuery: map[string][]models.Author{name: {{Name: name, ForeignID: "OL1A"}}},
			authors:              map[string]*models.Author{"OL1A": {Name: name, ForeignID: "OL1A", Description: "From the provider."}},
		}}
		return newRelinkUpstreamFixture(t, p), p
	}
	// userSaves writes the bio the way the author page does, on a fresh read.
	userSaves := func(t *testing.T, f *relinkUpstreamFixture, id int64) {
		t.Helper()
		fresh, err := f.authors.GetByID(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		fresh.Description = userBio
		fresh.DescriptionSource = &models.ProfileSource{Provider: models.ProfileSourceUser}
		if ok, err := f.authors.UpdateIfUnchanged(f.ctx, fresh, fresh.UpdatedAtRaw); err != nil || !ok {
			t.Fatalf("user save: %v, %v", ok, err)
		}
	}
	keptUserBio := func(t *testing.T, f *relinkUpstreamFixture, id int64) {
		t.Helper()
		got, err := f.authors.GetByID(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Description != userBio || got.DescriptionSource == nil || got.DescriptionSource.Provider != models.ProfileSourceUser {
			t.Errorf("bio %q from %+v; the user's bio should survive with its credit", got.Description, got.DescriptionSource)
		}
	}

	t.Run("refresh", func(t *testing.T) {
		f, _ := newFixture(t)
		author := f.createAuthor(t, &models.Author{Name: name, ForeignID: "OL1A", MetadataProvider: "openlibrary"})
		stale := *author
		userSaves(t, f, author.ID)
		f.handler.refreshAuthorProfile(f.ctx, &stale, false)
		keptUserBio(t, f, author.ID)
	})
	t.Run("relink upstream", func(t *testing.T) {
		f, p := newFixture(t)
		author := f.createAuthor(t, &models.Author{Name: name, ForeignID: "abs:kari"})
		p.onGet = func() { userSaves(t, f, author.ID) }
		if rec := f.relink(t, author.ID); rec.Code != http.StatusConflict {
			t.Errorf("status %d: %s; want 409", rec.Code, rec.Body.String())
		}
		keptUserBio(t, f, author.ID)
	})
	t.Run("calibre relink", func(t *testing.T) {
		f, _ := newFixture(t)
		author := f.createAuthor(t, &models.Author{Name: name, ForeignID: "calibre:author:1", MetadataProvider: "calibre"})
		stale := *author
		userSaves(t, f, author.ID)
		if err := f.handler.relinkCalibreAuthor(f.ctx, &stale); !errors.Is(err, errAuthorRowChanged) {
			t.Errorf("err %v, want errAuthorRowChanged", err)
		}
		keptUserBio(t, f, author.ID)
	})
}
