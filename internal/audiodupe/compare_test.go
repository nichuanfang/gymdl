package audiodupe

import "testing"

func pattern(n int, flip func(i int, value uint32) uint32) Fingerprint {
	items := make([]uint32, n)
	for i := range items {
		items[i] = uint32(i*97 + 11)
	}
	if flip != nil {
		for i := range items {
			items[i] = flip(i, items[i])
		}
	}
	return Fingerprint{Duration: float64(n) * itemSeconds, Items: items}
}

func TestCompareSameRecordingAcrossTinyBitErrors(t *testing.T) {
	base := pattern(200, nil)
	copy := pattern(200, func(i int, value uint32) uint32 {
		if i%20 == 0 {
			return value ^ 1
		}
		return value
	})
	got := Compare(base, copy)
	if got.Kind != KindSame {
		t.Fatalf("re-encode sized bit errors should stay same, got %#v", got)
	}
}

func TestCompareEditedWindowStaysSimilar(t *testing.T) {
	base := pattern(160, nil)
	edited := pattern(160, func(i int, value uint32) uint32 {
		if i < blockItems {
			return value ^ 0x03ff
		}
		return value
	})
	got := Compare(base, edited)
	if got.Kind != KindSimilar {
		t.Fatalf("a local edit should be similar, not a duplicate, got %#v", got)
	}
}

func TestCompareDifferentAudio(t *testing.T) {
	base := pattern(180, nil)
	other := pattern(180, func(i int, value uint32) uint32 { return value ^ 0xffffffff })
	if got := Compare(base, other); got.Kind != KindDifferent {
		t.Fatalf("inverted audio should differ, got %#v", got)
	}
}

func TestClusterKeepsSimilarOutOfDuplicateGroups(t *testing.T) {
	sameA := Track{Path: "a.flac", Fingerprint: pattern(200, nil)}
	sameB := Track{Path: "b.mp3", Fingerprint: pattern(200, func(i int, value uint32) uint32 {
		if i%25 == 0 {
			return value ^ 1
		}
		return value
	})}
	edited := Track{Path: "clean.flac", Fingerprint: pattern(200, func(i int, value uint32) uint32 {
		if i < blockItems {
			return value ^ 0x03ff
		}
		return value
	})}
	other := Track{Path: "other.flac", Fingerprint: pattern(200, func(i int, value uint32) uint32 { return value ^ 0xaaaaaaaa })}
	groups, conflicts := Cluster([]Track{sameA, sameB, edited, other})
	if len(conflicts) != 0 {
		t.Fatalf("unexpected lyric conflicts: %#v", conflicts)
	}
	var duplicates, similar int
	for _, group := range groups {
		switch group.Kind {
		case KindSame:
			duplicates++
			if len(group.Tracks) != 2 {
				t.Fatalf("duplicate group = %#v", group.Tracks)
			}
		case KindSimilar:
			similar++
		default:
			t.Fatalf("unexpected group %s", group.Kind)
		}
	}
	if duplicates != 1 || similar == 0 {
		t.Fatalf("groups duplicates=%d similar=%d all=%d", duplicates, similar, len(groups))
	}
}

func TestDurationGateRejectsAMuchShorterCopy(t *testing.T) {
	if DurationClose(200, 120) {
		t.Fatal("a 80 second gap is not encoder padding")
	}
	if !DurationClose(200, 200.4) {
		t.Fatal("sub-second padding should pass")
	}
}
