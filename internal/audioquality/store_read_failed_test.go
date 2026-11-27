package audioquality

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "quality.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreRoundTripsReadFailed(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	item := CachedResult{
		FileVersion: FileVersion{Path: "a/b.flac", Size: 10, Mod: 20},
		ReadFailed:  true,
	}
	if err := store.Save(ctx, "lib", item); err != nil {
		t.Fatal(err)
	}
	got, err := store.Results(ctx, "lib", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !got["a/b.flac"].ReadFailed {
		t.Fatalf("read failure was not persisted: %#v", got["a/b.flac"])
	}
}

// Opening a database created before the column existed must migrate it, or
// every upgrade would fail to scan results.
func TestOpenAddsReadFailedColumnToOlderDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`
		CREATE TABLE audio_quality_results(
			library TEXT NOT NULL, path TEXT NOT NULL, size INTEGER NOT NULL, modified INTEGER NOT NULL,
			result TEXT NOT NULL, retry_at INTEGER NOT NULL,
			PRIMARY KEY(library,path)
		);
		INSERT INTO audio_quality_results VALUES('lib','old.flac',1,2,'{"Unreadable":true}',0);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open should migrate the old schema: %v", err)
	}
	defer store.Close()

	got, err := store.Results(context.Background(), "lib", 1)
	if err != nil {
		t.Fatalf("reading migrated rows: %v", err)
	}
	item, ok := got["old.flac"]
	if !ok {
		t.Fatal("existing row disappeared during migration")
	}
	// Pre-existing rows were real verdicts, not transfer failures.
	if item.ReadFailed {
		t.Fatal("a legacy row must not be reinterpreted as a transfer failure")
	}
	if !item.Unreadable {
		t.Fatal("legacy result payload was lost")
	}
}

func TestForgetReadFailuresKeepsRealVerdicts(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	must := func(item CachedResult) {
		t.Helper()
		if err := store.Save(ctx, "lib", item); err != nil {
			t.Fatal(err)
		}
	}
	must(CachedResult{FileVersion: FileVersion{Path: "failed.flac", Size: 1, Mod: 1}, ReadFailed: true})
	must(CachedResult{FileVersion: FileVersion{Path: "damaged.flac", Size: 1, Mod: 1}, Result: Result{Damaged: true}})
	must(CachedResult{FileVersion: FileVersion{Path: "fine.flac", Size: 1, Mod: 1}, Result: Result{Bitrate: 320}})

	dropped, err := store.ForgetReadFailures(ctx, "lib")
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Fatalf("dropped %d rows, want only the transfer failure", dropped)
	}
	got, err := store.Results(ctx, "lib", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["failed.flac"]; ok {
		t.Fatal("transfer failure should be forgotten so the next scan retries")
	}
	if !got["damaged.flac"].Damaged || got["fine.flac"].Damaged {
		t.Fatalf("real verdicts were disturbed: %#v", got)
	}
}
