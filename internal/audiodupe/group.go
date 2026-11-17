package audiodupe

// Track is one library file that already has a fingerprint.
// The fingerprint stays in SQLite and is not sent to the console.
type Track struct {
	Path        string      `json:"path"`
	Name        string      `json:"name"`
	Ext         string      `json:"ext"`
	Size        int64       `json:"size"`
	ModTimeUnix int64       `json:"mod_unix"`
	Fingerprint Fingerprint `json:"-"`
}

// Group is either exact audio copies or one similar pair.
// Similar means the same recording edited somewhere, such as a clean version or a cut.
type Group struct {
	Kind   Kind    `json:"kind"`
	Tracks []Track `json:"tracks"`
}

// Cluster groups copies of the same audio. Similar pairs stay outside those groups.
// Low-information fingerprints are ignored so silence does not collapse the library.
func Cluster(tracks []Track) []Group {
	usable := make([]int, 0, len(tracks))
	for i, track := range tracks {
		if !LowInformation(track.Fingerprint) {
			usable = append(usable, i)
		}
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
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	type pair struct{ a, b int }
	var similar []pair
	for i := 0; i < len(usable); i++ {
		for j := i + 1; j < len(usable); j++ {
			left := tracks[usable[i]]
			right := tracks[usable[j]]
			if !DurationClose(left.Fingerprint.Duration, right.Fingerprint.Duration) {
				continue
			}
			switch Compare(left.Fingerprint, right.Fingerprint).Kind {
			case KindSame:
				union(usable[i], usable[j])
			case KindSimilar:
				similar = append(similar, pair{usable[i], usable[j]})
			}
		}
	}
	buckets := map[int][]Track{}
	order := make([]int, 0)
	for _, index := range usable {
		root := find(index)
		if _, ok := buckets[root]; !ok {
			order = append(order, root)
		}
		buckets[root] = append(buckets[root], tracks[index])
	}
	groups := make([]Group, 0)
	sameRoot := map[int]int{}
	for _, root := range order {
		items := buckets[root]
		if len(items) < 2 {
			continue
		}
		sameRoot[root] = len(groups)
		groups = append(groups, Group{Kind: KindSame, Tracks: items})
	}
	for _, item := range similar {
		if find(item.a) == find(item.b) {
			continue
		}
		groups = append(groups, Group{Kind: KindSimilar, Tracks: []Track{tracks[item.a], tracks[item.b]}})
	}
	return groups
}
