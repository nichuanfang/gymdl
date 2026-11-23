package discovery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/utils"
	"golang.org/x/sync/errgroup"
)

type TasteResult struct {
	Track   *Track
	Explore bool
	Reason  string
	Notice  string
}

type tasteArtist struct {
	name string
	seed string // Familiar artist that led to this new artist.
}

type tasteCandidate struct {
	track  Track
	artist tasteArtist
	tags   map[string]struct{}
	score  float64
	usable bool
}

func (s *Service) RecommendTaste(ctx context.Context, user int64, req TasteRequest) (TasteResult, error) {
	if !ValidTastePlatform(req.Platform) || s.Taste == nil || (s.Taste.Search == nil && s.Taste.SearchPage == nil) {
		return TasteResult{}, errors.New("个性化推荐暂不可用")
	}
	if s.Library == nil {
		return TasteResult{}, errors.New("暂时无法核对曲库，请稍后重试")
	}
	favorites, err := s.Store.Favorites(ctx, user)
	if err != nil {
		return TasteResult{}, errors.New("暂时无法读取收藏，请稍后重试")
	}
	var history []Track
	var notices []string
	if s.Taste.HistoryOwner != 0 && user == s.Taste.HistoryOwner {
		historyCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		history, err = s.cachedHistory(historyCtx)
		cancel()
	}
	if len(history) == 0 {
		notices = append(notices, "本次按收藏推断口味，未核对已听记录")
	}
	artists, familiar := rankTasteArtists(history, favorites, time.Now())
	if len(artists) == 0 {
		return TasteResult{}, errors.New("还没有可用的口味数据。可先同步收藏或积累收听记录")
	}
	exclusions, err := s.Store.exclusions(ctx, user, true)
	if err != nil {
		return TasteResult{}, errors.New("暂时无法核对入库队列，请稍后重试")
	}
	if s.SlowNetease && req.Platform == "netease" {
		return s.recommendPooledTaste(ctx, user, req)
	}
	explore := s.similarTasteArtists(ctx, artists, familiar)
	platforms := []string{req.Platform}
	if req.Platform == TasteBoth {
		platforms = []string{"netease", "qq"}
		if req.ExploreFirst {
			platforms[0], platforms[1] = platforms[1], platforms[0]
		}
	}
	if s.SlowNetease {
		filtered := platforms[:0]
		for _, platform := range platforms {
			if platform != "netease" {
				filtered = append(filtered, platform)
			}
		}
		platforms = filtered
	}
	known := tasteKnownTracks(history, favorites)
	if req.Page < 0 {
		req.Page = 0
	}
	if req.Page > 5 {
		return TasteResult{Notice: "再往下也没有更多单曲了。可以换个场景或平台。"}, nil
	}
	candidates, unavailable, dropped := s.tasteCandidates(ctx, artists, explore, platforms, known, exclusions, req.Page*tasteSearchPage)
	notices = append(notices, unavailable...)
	if ctx.Err() != nil {
		return TasteResult{}, errors.New("这次推荐超时，请再试一次")
	}
	index := s.tasteLocalTags(ctx, known)
	preference := tasteTagPreferences(index, history, favorites)
	lanes, owned, err := s.shortlistTaste(ctx, candidates)
	if err != nil {
		return TasteResult{}, err
	}
	dropped.owned += owned
	s.scoreTaste(ctx, lanes, req.Filter, preference, index)
	if ctx.Err() != nil {
		return TasteResult{}, errors.New("这次推荐超时，请再试一次")
	}
	laneOrder := []int{0, 1}
	if req.ExploreFirst {
		laneOrder = []int{1, 0}
	}
	for _, lane := range laneOrder {
		sort.SliceStable(lanes[lane], func(i, j int) bool { return lanes[lane][i].score > lanes[lane][j].score })
		for _, candidate := range lanes[lane] {
			if !candidate.usable {
				continue
			}
			reason := "来自你常听或收藏的艺人"
			if candidate.artist.seed != "" {
				reason = "与「" + candidate.artist.seed + "」风格相近"
			}
			if label := req.Filter.Label(); label != "" {
				reason += "，歌曲标签符合" + label
			} else if tag := tasteReasonTag(candidate.tags, preference); tag != "" {
				reason += "，也符合你的「" + tag + "」偏好"
			}
			return TasteResult{Track: &candidate.track, Explore: lane == 1, Reason: reason, Notice: strings.Join(notices, " · ")}, nil
		}
	}
	if reason := tasteEmptyReason(dropped); reason != "" {
		notices = append(notices, reason)
	}
	if s.SlowNetease && req.Platform == TasteBoth {
		pooled, err := s.recommendPooledTaste(ctx, user, req)
		if err != nil {
			return TasteResult{}, err
		}
		if pooled.Track != nil {
			if note := strings.Join(notices, " · "); note != "" {
				if pooled.Notice != "" {
					pooled.Notice = note + " · " + pooled.Notice
				} else {
					pooled.Notice = note
				}
			}
			return pooled, nil
		}
		if pooled.Notice != "" {
			notices = append(notices, pooled.Notice)
		}
	}
	return TasteResult{Notice: strings.Join(notices, " · ")}, nil
}

const tasteSearchPage = 20

type tasteDrops struct {
	hits, known, unverified, owned int
}

func tasteEmptyReason(dropped tasteDrops) string {
	switch {
	case dropped.known+dropped.owned > 0:
		return "靠前的歌已经听过或在曲库里。再点换一首会往下找。"
	case dropped.hits > 0 && dropped.unverified == dropped.hits:
		return "搜到的结果对不上这些艺人，没有拿来推荐。"
	default:
		return ""
	}
}

func rankTasteArtists(history, favorites []Track, now time.Time) ([]tasteArtist, map[string]bool) {
	type artistScore struct {
		name  string
		score float64
	}
	scores := map[string]artistScore{}
	add := func(artist string, score float64) {
		for _, credit := range artistSplit.Split(artist, -1) {
			credit = strings.TrimSpace(credit)
			key := Normalize(credit)
			switch key {
			case "", "unknown", "unknownartist", "各种艺人", "未知艺人", "群星", "variousartists":
				continue
			}
			entry := scores[key]
			if entry.name == "" {
				entry.name = credit
			}
			entry.score += score
			scores[key] = entry
		}
	}
	for _, track := range history {
		score := math.Log1p(float64(max(track.Plays, 1)))
		if !track.LastPlayed.IsZero() && now.Sub(track.LastPlayed) <= 90*24*time.Hour {
			score *= 1.5
		}
		add(track.Artist, score)
	}
	liked := map[string]bool{}
	for _, track := range favorites {
		key := Identity(track)
		if track.Favorite && !track.NonMusic && !liked[key] {
			add(track.Artist, 2)
			liked[key] = true
		}
	}
	known := make(map[string]bool, len(scores))
	ranked := make([]artistScore, 0, len(scores))
	for key, score := range scores {
		known[key] = true
		ranked = append(ranked, score)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].name < ranked[j].name
		}
		return ranked[i].score > ranked[j].score
	})
	artists := make([]tasteArtist, 0, min(4, len(ranked)))
	for _, score := range ranked[:min(4, len(ranked))] {
		artists = append(artists, tasteArtist{name: score.name})
	}
	return artists, known
}

func (s *Service) similarTasteArtists(ctx context.Context, artists []tasteArtist, familiar map[string]bool) []tasteArtist {
	if s.Taste.Similar == nil {
		return nil
	}
	hits := make([][]string, len(artists))
	var group errgroup.Group
	group.SetLimit(3)
	for i, artist := range artists {
		group.Go(func() error {
			query, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			hits[i], _ = s.Taste.Similar(query, artist.name)
			return nil
		})
	}
	_ = group.Wait()
	seen := map[string]bool{}
	var related []tasteArtist
	for rank := 0; rank < 8 && len(related) < 4; rank++ {
		for i, names := range hits {
			if rank >= len(names) {
				continue
			}
			name := names[rank]
			key := Normalize(name)
			if key == "" || familiar[key] || seen[key] {
				continue
			}
			seen[key] = true
			related = append(related, tasteArtist{name: name, seed: artists[i].name})
			if len(related) == 4 {
				break
			}
		}
	}
	return related
}

func tasteKnownTracks(history, favorites []Track) map[string][]Track {
	known := map[string][]Track{}
	for _, tracks := range [][]Track{history, favorites} {
		for _, track := range tracks {
			if key := ownershipTitle(track.Title); key != "" && !track.NonMusic {
				known[key] = append(known[key], track)
			}
		}
	}
	return known
}

func tasteSameSong(a, b Track) bool {
	return ownershipTitle(a.Title) == ownershipTitle(b.Title) &&
		(Normalize(a.Artist) == Normalize(b.Artist) || artistsOverlap(a.Artist, b.Artist))
}

func tasteHasSong(track Track, songs []Track) bool {
	for _, other := range songs {
		if tasteSameSong(track, other) {
			return true
		}
	}
	return false
}

func verifiedTasteTrack(track Track, platform, artist string) bool {
	if track.Platform != platform || !track.Verified || track.NonMusic || track.ID == "" ||
		Normalize(track.Title) == "" || !(Normalize(track.Artist) == Normalize(artist) || artistsOverlap(track.Artist, artist)) {
		return false
	}
	link, err := url.Parse(track.URL)
	if err != nil || link.Scheme != "https" || link.User != nil {
		return false
	}
	switch platform {
	case "netease":
		return link.Host == "music.163.com" && link.Path == "/song" && link.Query().Get("id") == track.ID
	case "qq":
		return link.Host == "y.qq.com" && link.Path == "/n/ryqq/songDetail/"+track.ID
	}
	return false
}

func (s *Service) tasteCandidates(ctx context.Context, familiar, explore []tasteArtist, platforms []string,
	known map[string][]Track, exclusions map[string]string, offset int) ([]tasteCandidate, []string, tasteDrops) {
	type lookup struct {
		artist   tasteArtist
		platform string
		tracks   []Track
		err      error
	}
	var searches []lookup
	for _, lane := range [][]tasteArtist{familiar, explore} {
		for _, artist := range lane {
			for _, platform := range platforms {
				searches = append(searches, lookup{artist: artist, platform: platform})
			}
		}
	}
	var group errgroup.Group
	group.SetLimit(4)
	// NetEase rejects bursts with code 405. Keep its in-flight searches small.
	neteaseSlots := make(chan struct{}, 2)
	for i := range searches {
		group.Go(func() error {
			search := &searches[i]
			if search.platform == "netease" {
				select {
				case neteaseSlots <- struct{}{}:
					defer func() { <-neteaseSlots }()
				case <-ctx.Done():
					search.err = ctx.Err()
					return nil
				}
			}
			query, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			search.tracks, search.err = s.searchTaste(query, search.platform, search.artist.name, offset)
			return nil
		})
	}
	_ = group.Wait()
	succeeded := map[string]bool{}
	empty := map[string]bool{}
	failed := map[string]bool{}
	rejected := map[string]int{}
	for _, search := range searches {
		if search.err == nil {
			continue
		}
		failed[search.platform] = true
		if tasteSearchRejected(search.err) {
			rejected[search.platform]++
		}
	}
	seen := map[string][]Track{}
	var dropped tasteDrops
	var out []tasteCandidate
	// Interleave each artist/platform's search hits before taking the shortlist.
	for rank := 0; rank < tasteSearchPage; rank++ {
		for _, search := range searches {
			if search.err != nil {
				continue
			}
			if len(search.tracks) == 0 {
				empty[search.platform] = true
				continue
			}
			succeeded[search.platform] = true
			if rank >= len(search.tracks) {
				continue
			}
			track := search.tracks[rank]
			dropped.hits++
			key := ownershipTitle(track.Title)
			switch {
			case !verifiedTasteTrack(track, search.platform, search.artist.name):
				dropped.unverified++
				continue
			case exclusions[Identity(track)] != "" || tasteHasSong(track, known[key]) || tasteHasSong(track, seen[key]):
				dropped.known++
				continue
			}
			seen[key] = append(seen[key], track)
			out = append(out, tasteCandidate{track: track, artist: search.artist})
		}
	}
	var unavailable []string
	for _, platform := range platforms {
		if succeeded[platform] {
			continue
		}
		name := "网易云"
		if platform == "qq" {
			name = "QQ 音乐"
		}
		if failed[platform] || !empty[platform] {
			if rejected[platform] > 0 {
				utils.WarnWithFormat("[Discovery] 个性化推荐搜索异常 platform=%s failed=%t rejected=%d", platform, failed[platform], rejected[platform])
				unavailable = append(unavailable, name+"搜索暂时受限，请稍后重试")
				continue
			}
			if failed[platform] {
				utils.WarnWithFormat("[Discovery] 个性化推荐搜索异常 platform=%s failed=%t rejected=0", platform, failed[platform])
			}
			unavailable = append(unavailable, name+"暂未完成搜索")
			continue
		}
		unavailable = append(unavailable, name+"没有返回单曲")
	}
	return out, unavailable, dropped
}

func tasteSearchRejected(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "暂时受限") || strings.Contains(text, "上游响应 code=405")
}

func (s *Service) searchTaste(ctx context.Context, platform, artist string, offset int) ([]Track, error) {
	if s.Taste != nil && s.Taste.SearchPage != nil {
		return s.Taste.SearchPage(ctx, platform, artist, offset)
	}
	if s.Taste == nil || s.Taste.Search == nil {
		return nil, errors.New("个性化推荐暂不可用")
	}
	tracks, err := s.Taste.Search(ctx, platform, artist)
	if err != nil || offset <= 0 {
		return tracks, err
	}
	if offset >= len(tracks) {
		return nil, nil
	}
	return tracks[offset:], nil
}

func (s *Service) shortlistTaste(ctx context.Context, candidates []tasteCandidate) ([2][]tasteCandidate, int, error) {
	var lanes [2][]tasteCandidate
	ownedCount := 0
	for _, candidate := range candidates {
		lane := 0
		if candidate.artist.seed != "" {
			lane = 1
		}
		if len(lanes[lane]) >= 8 {
			continue
		}
		owned, err := s.Library.Contains(ctx, candidate.track)
		if err != nil {
			if _, ok := s.Library.(*NavidromeLibrary); ok {
				return lanes, ownedCount, fmt.Errorf("暂时无法核对曲库：%w", err)
			}
			return lanes, ownedCount, errors.New("暂时无法核对曲库，请稍后重试")
		}
		if owned {
			ownedCount++
			continue
		}
		if s.Pending != nil {
			pending, err := s.Pending(ctx, candidate.track)
			if err != nil {
				return lanes, ownedCount, errors.New("暂时无法核对待上传歌曲，请稍后重试")
			}
			if pending {
				continue
			}
		}
		lanes[lane] = append(lanes[lane], candidate)
		if len(lanes[0]) == 8 && len(lanes[1]) == 8 {
			break
		}
	}
	return lanes, ownedCount, nil
}

func tasteTagKey(title, artist string) string {
	return Normalize(title) + "\n" + Normalize(artist)
}

func (s *Service) tasteLocalTags(ctx context.Context, known map[string][]Track) map[string][]string {
	index := map[string][]string{}
	if s.Taste.LibraryTags == nil {
		return index
	}
	query, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	songs, err := s.Taste.LibraryTags(query)
	if err != nil {
		return index
	}
	for _, song := range songs {
		track := Track{Title: song.Title, Artist: song.Artist}
		key := tasteTagKey(song.Title, song.Artist)
		localTags := strings.FieldsFunc(strings.Join([]string{song.Genre, song.Mood, song.Language}, ","), func(r rune) bool {
			return strings.ContainsRune(",，、;；/", r)
		})
		if len(localTags) == 0 {
			continue
		}
		index[key] = append(index[key], localTags...)
		for _, prior := range known[ownershipTitle(song.Title)] {
			if tasteSameSong(track, prior) {
				priorKey := tasteTagKey(prior.Title, prior.Artist)
				if priorKey != key {
					index[priorKey] = append(index[priorKey], localTags...)
				}
			}
		}
	}
	return index
}

func tasteTagPreferences(index map[string][]string, history, favorites []Track) map[string]float64 {
	out := map[string]float64{}
	for _, track := range history {
		for tag := range tasteTagSet(index[tasteTagKey(track.Title, track.Artist)]) {
			out[tag] += math.Log1p(float64(max(1, track.Plays)))
		}
	}
	for _, track := range favorites {
		if !track.Favorite {
			continue
		}
		for tag := range tasteTagSet(index[tasteTagKey(track.Title, track.Artist)]) {
			out[tag] += 2
		}
	}
	return out
}

func (s *Service) scoreTaste(ctx context.Context, lanes [2][]tasteCandidate, filter TasteFilter, preference map[string]float64, index map[string][]string) {
	var group errgroup.Group
	group.SetLimit(4)
	normalizer := float64(1)
	for _, weight := range preference {
		normalizer = max(normalizer, weight)
	}
	for _, lane := range lanes {
		for i := range lane {
			candidate := &lane[i]
			group.Go(func() error {
				local := index[tasteTagKey(candidate.track.Title, candidate.track.Artist)]
				var remote []string
				if s.Taste.TrackTags != nil && (len(preference) != 0 || filter != (TasteFilter{})) {
					query, cancel := context.WithTimeout(ctx, 2*time.Second)
					remote, _ = s.Taste.TrackTags(query, candidate.track)
					cancel()
				}
				tags := append(append([]string(nil), local...), remote...)
				candidate.tags = tasteTagSet(tags)
				candidate.usable = filter.matches(candidate.tags)
				// Normalize overlap so a rarely used tag cannot overwhelm
				// the strongest tags for this listener.
				matches := make([]string, 0, len(candidate.tags))
				for tag := range candidate.tags {
					matches = append(matches, tag)
				}
				sort.Strings(matches)
				for _, tag := range matches {
					candidate.score += preference[tag] / normalizer
				}
				return nil
			})
		}
	}
	_ = group.Wait()
}

func tasteReasonTag(tags map[string]struct{}, preference map[string]float64) string {
	best := ""
	score := 0.0
	for _, terms := range [][]tasteTerm{tasteGenres, tasteLanguages} {
		for _, term := range terms {
			key := Normalize(term.name)
			if _, found := tags[key]; found && preference[key] > score {
				best, score = term.name, preference[key]
			}
		}
	}
	return best
}
