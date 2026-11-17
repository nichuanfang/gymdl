package audiodupe

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFingerprintCacheRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	fp := Fingerprint{Duration: 12.5, Items: []uint32{1, 2, 3, 4}}
	if err = store.SaveFingerprint(ctx, "a.flac", 10, 20, fp); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.LoadFingerprint(ctx, "a.flac", 10, 20)
	if err != nil || !ok || len(got.Items) != 4 || got.Items[3] != 4 || got.Duration != 12.5 {
		t.Fatalf("cached fingerprint ok=%v err=%v got=%#v", ok, err, got)
	}
	if _, ok, err = store.LoadFingerprint(ctx, "a.flac", 11, 20); err != nil || ok {
		t.Fatalf("size change must miss the cache ok=%v err=%v", ok, err)
	}
	report := Report{Status: "done", Scanned: 2, Total: 2, UpdatedAt: time.Unix(10, 0).UTC(), Groups: []Group{{Kind: KindSame, Tracks: []Track{{Path: "a.flac"}, {Path: "b.mp3"}}}}}
	if err = store.SaveReport(ctx, report); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadReport(ctx)
	if err != nil || loaded.Status != "done" || len(loaded.Groups) != 1 || len(loaded.Groups[0].Tracks) != 2 {
		t.Fatalf("report=%#v err=%v", loaded, err)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil || !strings.Contains(string(encoded), `"kind":"same"`) || strings.Contains(string(encoded), "Fingerprint") {
		t.Fatalf("console report must name groups and omit fingerprints: %s err=%v", encoded, err)
	}
}
