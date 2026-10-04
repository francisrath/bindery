package db

import "testing"

// OpenMemory hands out copies of one migrated template; a write to one copy
// must not show up in the next, and per-connection pragmas must survive the
// copy.
func TestOpenMemory_CopiesAreIndependent(t *testing.T) {
	a, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Exec(`INSERT INTO settings (key, value) VALUES ('openmemory_probe', 'x')`); err != nil {
		t.Fatal(err)
	}

	b, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var n int
	if err := b.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'openmemory_probe'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("row written to one OpenMemory database leaked into another")
	}
	var fk int
	if err := b.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d (err %v), want 1", fk, err)
	}
	if got, want := len(versionSetForTest(t, b)), countMigrationFiles(t); got != want {
		t.Fatalf("schema_migrations has %d versions, want %d", got, want)
	}
}

func countMigrationFiles(t *testing.T) int {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
