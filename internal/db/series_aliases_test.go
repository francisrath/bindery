package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func addSeriesAlias(t *testing.T, repo *SeriesRepo, foreignID string, seriesID int64) {
	t.Helper()
	if _, err := repo.db.Exec(`INSERT INTO series_aliases (foreign_id, series_id) VALUES (?, ?)`, foreignID, seriesID); err != nil {
		t.Fatal(err)
	}
}

// A provider id that was merged away resolves to the series it was merged
// into, through both lookups, and is never created again (#2554).
func TestSeriesAliasResolution(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewSeriesRepo(database)

	target := &models.Series{ForeignID: "hc-series:1", Title: "Fjellserien"}
	if err := repo.CreateOrGet(ctx, target); err != nil {
		t.Fatal(err)
	}
	addSeriesAlias(t, repo, "nb-series:1:serien-om-fjellet", target.ID)

	got, err := repo.GetByForeignID(ctx, "nb-series:1:serien-om-fjellet")
	if err != nil || got == nil || got.ID != target.ID {
		t.Fatalf("GetByForeignID(alias) = %+v, %v; want series %d", got, err, target.ID)
	}
	if got, err := repo.GetByForeignID(ctx, "hc-series:1"); err != nil || got == nil || got.ID != target.ID {
		t.Fatalf("GetByForeignID(own id) = %+v, %v", got, err)
	}
	if got, err := repo.GetByForeignID(ctx, "nb-series:1:unknown"); err != nil || got != nil {
		t.Fatalf("GetByForeignID(unknown) = %+v, %v; want nil, nil", got, err)
	}

	again := &models.Series{ForeignID: "nb-series:1:serien-om-fjellet", Title: "Serien om fjellet"}
	if err := repo.CreateOrGet(ctx, again); err != nil {
		t.Fatal(err)
	}
	if again.ID != target.ID {
		t.Errorf("CreateOrGet(alias) = series %d, want the merge target %d", again.ID, target.ID)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM series`).Scan(&n); err != nil || n != 1 {
		t.Errorf("series rows = %d (%v), want 1: an aliased id must not be recreated", n, err)
	}
}

// An alias id cannot become another series' own id, by creation or by a
// foreign-id change, or the two would name different series.
func TestSeriesAliasRefusedAsForeignID(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewSeriesRepo(database)

	target := &models.Series{ForeignID: "hc-series:1", Title: "Fjellserien"}
	other := &models.Series{ForeignID: "hc-series:2", Title: "Havserien"}
	for _, s := range []*models.Series{target, other} {
		if err := repo.CreateOrGet(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	addSeriesAlias(t, repo, "nb-series:1:old", target.ID)

	if err := repo.Create(ctx, &models.Series{ForeignID: "nb-series:1:old", Title: "x"}); !errors.Is(err, ErrSeriesAlias) {
		t.Errorf("Create(alias) err = %v, want ErrSeriesAlias", err)
	}
	if err := repo.UpdateForeignID(ctx, other.ID, "nb-series:1:old"); !errors.Is(err, ErrSeriesAlias) {
		t.Errorf("UpdateForeignID(alias) err = %v, want ErrSeriesAlias", err)
	}
}

// Deleting the target drops its aliases: the user deleted the series, so a
// provider that still reports the old id creates it anew.
func TestSeriesAliasDroppedWithTarget(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewSeriesRepo(database)

	target := &models.Series{ForeignID: "hc-series:1", Title: "Fjellserien"}
	if err := repo.CreateOrGet(ctx, target); err != nil {
		t.Fatal(err)
	}
	addSeriesAlias(t, repo, "nb-series:1:old", target.ID)
	if err := repo.Delete(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	s := &models.Series{ForeignID: "nb-series:1:old", Title: "Old"}
	if err := repo.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if s.ID == 0 || s.ID == target.ID {
		t.Errorf("CreateOrGet after the target's delete = %d, want a new series", s.ID)
	}
}

// The maintainer's condition for #2554: the alias-aware resolver is the only
// place a series is looked up by provider foreign id. Any other query of that
// shape could find nothing for a merged-away id and let the caller create the
// series again.
func TestSeriesForeignIDLookupsGoThroughResolver(t *testing.T) {
	lookup := regexp.MustCompile(`(?is)FROM\s+series\s+(?:AS\s+\w+\s+|\w+\s+)?WHERE[^;]*?\bforeign_id\s*=`)
	const resolver = "const seriesByForeignIDSQL = `SELECT id FROM series WHERE foreign_id = ?"
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, loc := range lookup.FindAllStringIndex(src, -1) {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			if strings.HasPrefix(src[max(0, loc[0]-len("const seriesByForeignIDSQL = `SELECT id ")):], resolver) {
				continue
			}
			hits = append(hits, fmt.Sprintf("%s:%d", f, line))
		}
		if f == "series.go" && !strings.Contains(src, resolver) {
			t.Fatal("seriesByForeignIDSQL is gone or reshaped; update this test with it")
		}
	}
	if len(hits) > 0 {
		t.Errorf("series looked up by foreign_id outside seriesByForeignIDSQL at %v", hits)
	}
}
