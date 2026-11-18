package audiodupe

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestDecideUsesFloatBands(t *testing.T) {
	cases := []struct {
		ber  float64
		want Decision
	}{
		{0, DecisionSkip},
		{0.04, DecisionSkip},
		{0.05, DecisionConfirm},
		{0.25, DecisionConfirm},
		{0.26, DecisionProceed},
		{1, DecisionProceed},
	}
	for _, tc := range cases {
		got, sim := Decide(Comparison{BER: tc.ber})
		if got != tc.want {
			t.Fatalf("BER %v decision %s similarity %v, want %s", tc.ber, got, sim, tc.want)
		}
	}
	if sim := Similarity(Comparison{BER: 1, Kind: KindDifferent}); sim != 0 {
		t.Fatal("unaligned pair scored", sim)
	}
}

func TestClosestKeepsTheHigherScore(t *testing.T) {
	same := pattern(180, nil)
	near := pattern(180, func(i int, value uint32) uint32 { return value ^ 0x3 })
	other := pattern(180, func(i int, value uint32) uint32 { return value ^ 0xffffffff })
	got, cmp, ok := Closest(same, []Track{
		{Path: "other.flac", Fingerprint: other},
		{Path: "near.flac", Fingerprint: near},
		{Path: "same.flac", Fingerprint: same},
	})
	if !ok || got.Path != "same.flac" {
		t.Fatal(got, ok)
	}
	decision, _ := Decide(cmp)
	if decision != DecisionSkip {
		t.Fatal(decision, cmp)
	}
}

func TestReviewFilesBandsAndFastPaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fp.sqlite")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	same := pattern(180, nil)
	if err = store.SaveFingerprint(ctx, "lib/same.flac", 10, 10, same); err != nil {
		t.Fatal(err)
	}
	confirmFP := pattern(180, func(i int, value uint32) uint32 { return value ^ 0x3 })
	far := pattern(180, nil)
	far.Duration = 30
	if err = store.SaveFingerprint(ctx, "lib/short.flac", 11, 11, far); err != nil {
		t.Fatal(err)
	}

	origFP := fingerprintFile
	origOpen := openStore
	t.Cleanup(func() {
		fingerprintFile = origFP
		openStore = origOpen
	})
	openStore = func(string) (*Store, error) { return Open(dbPath) }
	fingerprinted := 0
	fingerprintFile = func(context.Context, string) (Fingerprint, error) {
		fingerprinted++
		return same, nil
	}

	var match *LibraryMatch
	err = ReviewFiles(ctx, []string{"/tmp/new.flac"}, false)
	if !errors.As(err, &match) || match.Decision != DecisionSkip || match.Path != "lib/same.flac" {
		t.Fatal(err)
	}
	if fingerprinted != 1 {
		t.Fatal("skip should fingerprint once", fingerprinted)
	}

	fingerprintFile = func(context.Context, string) (Fingerprint, error) { return confirmFP, nil }
	err = ReviewFiles(ctx, []string{"/tmp/new.flac"}, false)
	match = nil
	if !errors.As(err, &match) || match.Decision != DecisionConfirm {
		t.Fatal(err)
	}
	if match.Error() == "" || match.Similarity <= 0.75 || match.Similarity > 0.95 {
		t.Fatal(match)
	}

	fingerprintFile = func(context.Context, string) (Fingerprint, error) {
		return pattern(180, func(i int, value uint32) uint32 { return value ^ 0xffffffff }), nil
	}
	if err = ReviewFiles(ctx, []string{"/tmp/new.flac"}, false); err != nil {
		t.Fatal("different audio should proceed", err)
	}

	called := false
	fingerprintFile = func(context.Context, string) (Fingerprint, error) {
		called = true
		return same, nil
	}
	if err = ReviewFiles(ctx, []string{"/tmp/new.flac"}, true); err != nil || called {
		t.Fatal("bypass fingerprinted", err, called)
	}
	if err = ReviewFiles(ctx, nil, false); err != nil || called {
		t.Fatal("empty path list fingerprinted", err, called)
	}
	if err = ReviewFiles(ctx, []string{"/tmp/a.flac", "/tmp/b.flac"}, false); err != nil || called {
		t.Fatal("album fingerprinted", err, called)
	}
	fingerprintFile = func(context.Context, string) (Fingerprint, error) {
		called = true
		return Fingerprint{}, errors.New("fpcalc missing")
	}
	if err = ReviewFiles(ctx, []string{"/tmp/new.flac"}, false); err != nil {
		t.Fatal("fingerprint failure blocked ingest", err)
	}

	openStore = func(string) (*Store, error) { return nil, errors.New("db") }
	fingerprintFile = func(context.Context, string) (Fingerprint, error) {
		t.Fatal("opened no database")
		return Fingerprint{}, nil
	}
	if err = ReviewFiles(ctx, []string{"/tmp/new.flac"}, false); err != nil {
		t.Fatal(err)
	}

	emptyPath := filepath.Join(dir, "empty.sqlite")
	if _, err = Open(emptyPath); err != nil {
		t.Fatal(err)
	}
	openStore = func(string) (*Store, error) { return Open(emptyPath) }
	fingerprintFile = func(context.Context, string) (Fingerprint, error) {
		t.Fatal("empty index fingerprinted")
		return Fingerprint{}, nil
	}
	if err = ReviewFiles(ctx, []string{"/tmp/new.flac"}, false); err != nil {
		t.Fatal(err)
	}

	openStore = func(string) (*Store, error) { return Open(dbPath) }
	fingerprintFile = func(context.Context, string) (Fingerprint, error) {
		t.Fatal("far tagged duration fingerprinted")
		return Fingerprint{}, nil
	}
	if err = ReviewFile(ctx, "/tmp/new.flac", 240, false); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = ReviewFile(cancelled, "/tmp/new.flac", 0, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
