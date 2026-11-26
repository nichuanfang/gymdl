package audiodupe

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nichuanfang/gymdl/internal/lyricmeta"
)

type lyricCase struct {
	name       string
	candidate  lyricmeta.Evidence
	library    lyricmeta.Evidence
	want       Decision
	conflicted bool
}

func TestDecideWithLyricKeepsThresholdsUnlessConflict(t *testing.T) {
	cases := []lyricCase{
		// A clear vocal/instrumental mismatch bypasses even a skip-level score.
		{"vocal-vs-instrumental-skip", lyricmeta.Present, lyricmeta.ExplicitInstrumental, DecisionProceed, true},
		{"instrumental-vs-vocal-skip", lyricmeta.ExplicitInstrumental, lyricmeta.Present, DecisionProceed, true},
		// Everything else keeps the original audio bands.
		{"vocal-vs-vocal-skip", lyricmeta.Present, lyricmeta.Present, DecisionSkip, false},
		{"instrumental-vs-instrumental-skip", lyricmeta.ExplicitInstrumental, lyricmeta.ExplicitInstrumental, DecisionSkip, false},
		{"unknown-vs-vocal-skip", lyricmeta.Unknown, lyricmeta.Present, DecisionSkip, false},
		{"unknown-vs-unknown-skip", lyricmeta.Unknown, lyricmeta.Unknown, DecisionSkip, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, sim, conflict := DecideWithLyric(Comparison{BER: 0.02}, tc.candidate, tc.library)
			if got != tc.want || conflict != tc.conflicted {
				t.Fatalf("decision=%s conflict=%v sim=%v, want %s/%v", got, conflict, sim, tc.want, tc.conflicted)
			}
		})
	}
	// The mixed pair must still confirm in the ambiguous band.
	got, _, conflict := DecideWithLyric(Comparison{BER: 0.15}, lyricmeta.Present, lyricmeta.Present)
	if got != DecisionConfirm || conflict {
		t.Fatalf("same-evidence confirm band = %s conflict=%v", got, conflict)
	}
}

func TestClusterSeparatesLyricConflicts(t *testing.T) {
	base := pattern(200, nil)
	vocal := Track{Path: "vocal.flac", Fingerprint: base, LyricEvidence: lyricmeta.Present}
	instrumental := Track{Path: "instrumental.flac", Fingerprint: base, LyricEvidence: lyricmeta.ExplicitInstrumental}

	groups, conflicts := Cluster([]Track{vocal, instrumental})
	if len(groups) != 0 {
		t.Fatalf("a vocal/instrumental pair must not be deletable: %#v", groups)
	}
	if len(conflicts) != 1 || len(conflicts[0].Tracks) != 2 {
		t.Fatalf("conflicts = %#v", conflicts)
	}
	if !conflicts[0].HasLyricConflict() {
		t.Fatal("conflict group should report the mismatch")
	}
}

func TestClusterUnknownBridgeDoesNotMergeConflict(t *testing.T) {
	base := pattern(200, nil)
	vocal := Track{Path: "a-vocal.flac", Fingerprint: base, LyricEvidence: lyricmeta.Present}
	bridge := Track{Path: "b-unknown.flac", Fingerprint: base, LyricEvidence: lyricmeta.Unknown}
	instrumental := Track{Path: "c-instrumental.flac", Fingerprint: base, LyricEvidence: lyricmeta.ExplicitInstrumental}

	groups, conflicts := Cluster([]Track{vocal, bridge, instrumental})
	for _, group := range groups {
		if group.HasLyricConflict() {
			t.Fatalf("merged group reintroduced a lyric conflict: %#v", group)
		}
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %#v", conflicts)
	}
	if len(conflicts[0].Tracks) != 2 {
		t.Fatalf("only the directly conflicting pair belongs in the review item: %#v", conflicts[0].Tracks)
	}
}

func TestClusterKeepsCompatibleEvidenceTogether(t *testing.T) {
	base := pattern(200, nil)
	// Vocal + unknown are compatible, and so are instrumental + unknown.
	for _, tc := range []struct {
		name string
		a, b lyricmeta.Evidence
	}{
		{"vocal-unknown", lyricmeta.Present, lyricmeta.Unknown},
		{"instrumental-unknown", lyricmeta.ExplicitInstrumental, lyricmeta.Unknown},
		{"unknown-unknown", lyricmeta.Unknown, lyricmeta.Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left := Track{Path: "a.flac", Fingerprint: base, LyricEvidence: tc.a}
			right := Track{Path: "b.flac", Fingerprint: base, LyricEvidence: tc.b}
			groups, conflicts := Cluster([]Track{left, right})
			if len(conflicts) != 0 {
				t.Fatalf("unexpected conflicts: %#v", conflicts)
			}
			if len(groups) != 1 || len(groups[0].Tracks) != 2 {
				t.Fatalf("compatible evidence should cluster: %#v", groups)
			}
		})
	}
}

func TestGroupHasLyricConflictRefusesHistoricalReport(t *testing.T) {
	group := Group{Kind: KindSame, Tracks: []Track{
		{Path: "a.flac", LyricEvidence: lyricmeta.Present},
		{Path: "b.flac", LyricEvidence: lyricmeta.ExplicitInstrumental},
	}}
	if !group.HasLyricConflict() {
		t.Fatal("a mixed group must be refused even without LyricConflicts")
	}
	plain := Group{Kind: KindSame, Tracks: []Track{{Path: "a.flac"}, {Path: "b.flac"}}}
	if plain.HasLyricConflict() {
		t.Fatal("unknown-only group is not a conflict")
	}
}

func TestReviewFileSkipsWhenLibraryIsInstrumentalAndCandidateVocal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fp.sqlite")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base := pattern(180, nil)
	if err = store.SaveFingerprintWithLyric(ctx, "lib/instrumental.flac", 10, 10, base, lyricmeta.ExplicitInstrumental); err != nil {
		t.Fatal(err)
	}

	origFP, origOpen := fingerprintFile, openStore
	t.Cleanup(func() { fingerprintFile, openStore = origFP, origOpen })
	openStore = func(string) (*Store, error) { return Open(dbPath) }
	fingerprintFile = func(context.Context, string) (Fingerprint, error) { return base, nil }

	// Identical audio, but the candidate has lyrics: it must proceed.
	missingPath := filepath.Join(dir, "new.flac")
	if err = ReviewFileWithLyric(ctx, missingPath, 0, lyricmeta.Present, false); err != nil {
		t.Fatalf("vocal over instrumental library copy must proceed, got %v", err)
	}

	// Without lyric evidence on either side, the same audio is skipped.
	var match *LibraryMatch
	err = ReviewFileWithLyric(ctx, missingPath, 0, lyricmeta.Unknown, false)
	if !errors.As(err, &match) || match.Decision != DecisionSkip {
		t.Fatalf("unknown evidence should keep the audio verdict, got %v", err)
	}
}
