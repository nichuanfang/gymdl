package audiodupe

import (
	"sort"

	"github.com/nichuanfang/gymdl/internal/lyricmeta"
)

// Track is one library file that already has a fingerprint.
// The fingerprint stays in SQLite and is not sent to the console.
type Track struct {
	Path          string             `json:"path"`
	Name          string             `json:"name"`
	Ext           string             `json:"ext"`
	Size          int64              `json:"size"`
	ModTimeUnix   int64              `json:"mod_unix"`
	Fingerprint   Fingerprint        `json:"-"`
	LyricEvidence lyricmeta.Evidence `json:"lyric_evidence,omitempty"`
}

// Group is either exact audio copies or one similar pair.
// Similar means the same recording edited somewhere, such as a clean version or a cut.
type Group struct {
	Kind   Kind    `json:"kind"`
	Tracks []Track `json:"tracks"`
}

// HasLyricConflict reports a group that mixes a vocal and an explicit
// instrumental. A report saved before lyric evidence existed can still carry
// such a group; deletion must refuse it.
func (g Group) HasLyricConflict() bool {
	var set lyricEvidenceSet
	for _, track := range g.Tracks {
		set.add(track.LyricEvidence)
	}
	return set.vocal && set.instrumental
}

// lyricEvidenceSet tracks whether a group already contains a vocal or an
// explicit instrumental. Unknown contributes nothing.
type lyricEvidenceSet struct{ vocal, instrumental bool }

func (s *lyricEvidenceSet) add(evidence lyricmeta.Evidence) {
	switch lyricmeta.Normalize(evidence) {
	case lyricmeta.Present:
		s.vocal = true
	case lyricmeta.ExplicitInstrumental:
		s.instrumental = true
	}
}

func (s lyricEvidenceSet) conflictsWith(other lyricEvidenceSet) bool {
	return (s.vocal && other.instrumental) || (s.instrumental && other.vocal)
}

// Cluster groups copies of the same audio. Similar pairs stay outside those groups.
// Low-information fingerprints are ignored so silence does not collapse the library.
//
// The second slice holds lyric conflicts: files whose audio is identical but
// whose lyric evidence proves one is a vocal and the other an instrumental.
// Files involved in a conflict never appear in a deletable group. Grouping
// also checks the group-level evidence set so one unknown file cannot bridge a
// vocal file and an instrumental file into the same group.
func Cluster(tracks []Track) ([]Group, []Group) {
	usable := make([]int, 0, len(tracks))
	for i, track := range tracks {
		if !LowInformation(track.Fingerprint) {
			usable = append(usable, i)
		}
	}

	// Pass one: find direct audio-equal pairs with contradictory lyrics.
	conflictPairs := make([][2]int, 0)
	inConflict := make(map[int]struct{})
	for i := 0; i < len(usable); i++ {
		for j := i + 1; j < len(usable); j++ {
			left, right := tracks[usable[i]], tracks[usable[j]]
			if !DurationClose(left.Fingerprint.Duration, right.Fingerprint.Duration) {
				continue
			}
			if Compare(left.Fingerprint, right.Fingerprint).Kind != KindSame {
				continue
			}
			if lyricmeta.Conflict(left.LyricEvidence, right.LyricEvidence) {
				conflictPairs = append(conflictPairs, [2]int{usable[i], usable[j]})
				inConflict[usable[i]] = struct{}{}
				inConflict[usable[j]] = struct{}{}
			}
		}
	}
	conflicts := conflictGroups(tracks, conflictPairs)

	// Pass two: cluster the remaining files, checking the merged evidence set
	// so an unknown bridge cannot join a vocal and an instrumental.
	parent := make([]int, len(tracks))
	evidence := make([]lyricEvidenceSet, len(tracks))
	for i := range parent {
		parent[i] = i
		if _, skip := inConflict[i]; !skip {
			evidence[i].add(tracks[i].LyricEvidence)
		}
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) bool {
		ra, rb := find(a), find(b)
		if ra == rb {
			return true
		}
		if evidence[ra].conflictsWith(evidence[rb]) {
			return false
		}
		root, other := ra, rb
		if rb < ra {
			root, other = rb, ra
		}
		parent[other] = root
		evidence[root].vocal = evidence[root].vocal || evidence[other].vocal
		evidence[root].instrumental = evidence[root].instrumental || evidence[other].instrumental
		return true
	}
	var similar [][2]int
	for i := 0; i < len(usable); i++ {
		if _, skip := inConflict[usable[i]]; skip {
			continue
		}
		for j := i + 1; j < len(usable); j++ {
			if _, skip := inConflict[usable[j]]; skip {
				continue
			}
			left, right := tracks[usable[i]], tracks[usable[j]]
			if !DurationClose(left.Fingerprint.Duration, right.Fingerprint.Duration) {
				continue
			}
			switch Compare(left.Fingerprint, right.Fingerprint).Kind {
			case KindSame:
				union(usable[i], usable[j])
			case KindSimilar:
				similar = append(similar, [2]int{usable[i], usable[j]})
			}
		}
	}

	buckets := map[int][]Track{}
	order := make([]int, 0)
	for _, index := range usable {
		if _, skip := inConflict[index]; skip {
			continue
		}
		root := find(index)
		if _, ok := buckets[root]; !ok {
			order = append(order, root)
		}
		buckets[root] = append(buckets[root], tracks[index])
	}
	groups := make([]Group, 0)
	for _, root := range order {
		items := buckets[root]
		if len(items) < 2 {
			continue
		}
		sortTracks(items)
		groups = append(groups, Group{Kind: KindSame, Tracks: items})
	}
	for _, item := range similar {
		if find(item[0]) == find(item[1]) {
			continue
		}
		pairTracks := []Track{tracks[item[0]], tracks[item[1]]}
		sortTracks(pairTracks)
		groups = append(groups, Group{Kind: KindSimilar, Tracks: pairTracks})
	}
	sortGroups(groups)
	return groups, conflicts
}

// conflictGroups merges conflicting pairs so one review item lists every file
// sharing the same audio. Union is transitive here because the audio is
// identical across the whole component; only the lyric labels disagree.
func conflictGroups(tracks []Track, pairs [][2]int) []Group {
	if len(pairs) == 0 {
		return []Group{}
	}
	parent := make([]int, len(tracks))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	for _, pair := range pairs {
		ra, rb := find(pair[0]), find(pair[1])
		if ra == rb {
			continue
		}
		if ra < rb {
			parent[rb] = ra
		} else {
			parent[ra] = rb
		}
	}
	buckets := map[int][]Track{}
	order := make([]int, 0)
	for _, pair := range pairs {
		for _, index := range pair {
			root := find(index)
			if _, ok := buckets[root]; !ok {
				order = append(order, root)
			}
			buckets[root] = appendUnique(buckets[root], tracks[index])
		}
	}
	groups := make([]Group, 0, len(order))
	for _, root := range order {
		items := buckets[root]
		if len(items) < 2 {
			continue
		}
		sortTracks(items)
		groups = append(groups, Group{Kind: KindSame, Tracks: items})
	}
	sortGroups(groups)
	return groups
}

func appendUnique(tracks []Track, track Track) []Track {
	for _, existing := range tracks {
		if existing.Path == track.Path {
			return tracks
		}
	}
	return append(tracks, track)
}

func sortTracks(tracks []Track) {
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Path < tracks[j].Path })
}

func sortGroups(groups []Group) {
	sort.SliceStable(groups, func(i, j int) bool {
		left, right := groups[i], groups[j]
		if len(left.Tracks) == 0 || len(right.Tracks) == 0 {
			return len(left.Tracks) < len(right.Tracks)
		}
		return left.Tracks[0].Path < right.Tracks[0].Path
	})
}
