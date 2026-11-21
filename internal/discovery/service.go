package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/internal/audiodupe"
	"github.com/nichuanfang/gymdl/utils"
)

type DownloadResult struct {
	Pending bool
	Detail  string
	Track   *Track
	File    *IngestedFile
}
type Service struct {
	Store    *Store
	Provider Provider
	Library  Library
	History  func(context.Context) ([]Track, error)
	Download func(context.Context, Track) (DownloadResult, error)
	Pending  func(context.Context, Track) (bool, error)
	// Notify returns an error on delivery failure so digests remain unacknowledged.
	// Sync failures are logged and are not sent through Notify.
	Notify               func(int64, string) error
	NotifyIngested       func(Job) error
	OnError              func(error)
	Authorized           func(int64, int64) bool
	Enabled              bool
	PollInterval         time.Duration
	SummaryInterval      time.Duration
	syncMu               sync.Mutex
	favoriteRetries      map[favoriteSourceKey]favoriteRetry
	workMu               sync.Mutex
	cacheMu              sync.Mutex
	historyTracks        []Track
	historyAt            time.Time
	historyReady         bool
	resolveCache         map[string]resolveCacheEntry
	neteaseRefreshCursor map[string]int
}

const (
	historyCacheTTL = 15 * time.Minute
	resolveHitTTL   = 12 * time.Hour
	resolveMissTTL  = 30 * time.Minute
)

// favoriteSyncRoundTimeout bounds one automatic round so a slow source cannot
// hold the polling loop indefinitely. Tests shorten it through the variable.
var favoriteSyncRoundTimeout = 10 * time.Minute

type resolveCacheEntry struct {
	track Track
	err   error
	at    time.Time
}

// FreshFavorite is one song the latest sync queued for the first time.
type FreshFavorite struct {
	User     int64
	Chat     int64
	Platform string
	Track    Track
}

// FavoriteSyncResult is the outcome for one bound source in a user-requested
// sync. A successful source may still fail on another source in the same round.
type FavoriteSyncResult struct {
	Platform string
	Status   string // synced, paused, failed
	Added    int
	Error    string
}

type favoriteSourceKey struct {
	user     int64
	platform string
}

type favoriteRetry struct {
	account  string
	failures int
	next     time.Time
}

func (s *Service) Sync(ctx context.Context, user int64) ([]FreshFavorite, error) {
	fresh, _, err := s.syncWithResults(ctx, user, "")
	return fresh, err
}

// SyncWithResults is the user-facing sync path. It returns one result per
// bound platform so a partial success is never described as a total failure.
func (s *Service) SyncWithResults(ctx context.Context, user int64) ([]FreshFavorite, []FavoriteSyncResult, error) {
	return s.syncWithResults(ctx, user, "")
}

func (s *Service) SyncPlatformWithResults(ctx context.Context, user int64, platform string) ([]FreshFavorite, []FavoriteSyncResult, error) {
	switch platform {
	case "netease", "qq", "youtube":
		return s.syncWithResults(ctx, user, platform)
	default:
		return nil, nil, errors.New("收藏平台无效")
	}
}

func (s *Service) syncWithResults(ctx context.Context, user int64, platform string) (fresh []FreshFavorite, results []FavoriteSyncResult, err error) {
	started := time.Now()
	defer func() {
		if ctx.Err() != nil {
			return
		}
		logFavoriteSync(user, time.Since(started), fresh, err)
	}()
	if !s.Enabled {
		err = fmt.Errorf("自动收藏同步未启用，请先配置 favorites.enable: true；可先预览并绑定账号")
		return nil, nil, err
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	bindings, err := s.Store.Bindings(ctx, user)
	if err != nil {
		return nil, nil, err
	}
	var failures []string
	for _, b := range bindings {
		if platform != "" && b.Platform != platform {
			continue
		}
		if s.Authorized != nil && !s.Authorized(b.User, b.Chat) {
			continue
		}
		// Only an explicit user pause stops a source. A failed read updates the
		// error message but never turns an automatic retry into a pause.
		if b.PausedByUser {
			results = append(results, FavoriteSyncResult{Platform: b.Platform, Status: "paused"})
			continue
		}
		retryKey := favoriteSourceKey{user: b.User, platform: b.Platform}
		retry := s.favoriteRetries[retryKey]
		if retry.account != b.Account {
			retry = favoriteRetry{account: b.Account}
		}
		if user == 0 && time.Now().Before(retry.next) {
			continue
		}
		if b.Paused {
			if perr := s.Store.Pause(ctx, b.User, b.Platform, false); perr != nil {
				message := b.Platform + "：" + perr.Error()
				failures = append(failures, message)
				results = append(results, FavoriteSyncResult{Platform: b.Platform, Status: "failed", Error: perr.Error()})
				continue
			}
			b.Paused = false
			utils.InfoWithFormat("[Discovery] %s 已取消旧版的自动暂停，继续同步", freshPlatformLabel(b.Platform))
		}
		snap, e := s.readSnapshot(ctx, b)
		var added []Track
		if e == nil {
			_, added, e = s.Store.Apply(ctx, b.User, b.Chat, snap, false)
		}
		if e != nil {
			if ctx.Err() == nil {
				s.noteFavoriteFailure(retryKey, retry)
			}
			message := b.Platform + "：" + e.Error()
			if _, serr := s.Store.SourceError(ctx, b, message); serr != nil {
				utils.WarnWithFormat("[Discovery] 记录收藏同步失败原因失败: %v", serr)
			}
			failures = append(failures, message)
			results = append(results, FavoriteSyncResult{Platform: b.Platform, Status: "failed", Error: e.Error()})
			continue
		}
		delete(s.favoriteRetries, retryKey)
		results = append(results, FavoriteSyncResult{Platform: b.Platform, Status: "synced", Added: len(added)})
		for _, track := range added {
			fresh = append(fresh, FreshFavorite{User: b.User, Chat: b.Chat, Platform: b.Platform, Track: track})
		}
	}
	if platform != "" && len(results) == 0 {
		return nil, nil, errors.New("请先绑定该平台的收藏账号")
	}
	if len(failures) > 0 {
		err = errors.New(strings.Join(failures, "\n"))
		return fresh, results, err
	}
	return fresh, results, nil
}

func (s *Service) noteFavoriteFailure(key favoriteSourceKey, retry favoriteRetry) {
	retry.failures = min(retry.failures+1, 6)
	if retry.failures >= 2 {
		interval := s.PollInterval
		if interval <= 0 {
			interval = 10 * time.Minute
		}
		interval = min(interval, time.Hour)
		retry.next = time.Now().Add(min(interval*time.Duration(1<<(retry.failures-1)), 2*time.Hour))
	}
	if s.favoriteRetries == nil {
		s.favoriteRetries = make(map[favoriteSourceKey]favoriteRetry)
	}
	s.favoriteRetries[key] = retry
}

func (s *Service) readSnapshot(ctx context.Context, b Binding) (Snapshot, error) {
	snap, err := s.fetchSnapshot(ctx, b)
	if err == nil && snap.Platform != b.Platform {
		err = fmt.Errorf("收藏来源标识不一致，未更新基线")
	}
	return snap, err
}

func logFavoriteSync(user int64, elapsed time.Duration, fresh []FreshFavorite, err error) {
	elapsed = elapsed.Round(time.Millisecond)
	who := "全部绑定"
	if user != 0 {
		who = fmt.Sprintf("user=%d", user)
	}
	if err != nil {
		utils.WarnWithFormat("[Discovery] 收藏同步未完成 %s 耗时 %s 新歌 %d: %s", who, elapsed, len(fresh), clipDiscoveryLog(err.Error()))
		return
	}
	if len(fresh) == 0 && user == 0 {
		utils.DebugWithFormat("[Discovery] 收藏同步完成 %s 耗时 %s，没有新歌", who, elapsed)
		return
	}
	utils.InfoWithFormat("[Discovery] 收藏同步完成 %s 耗时 %s 新歌 %d", who, elapsed, len(fresh))
}

// fetchSnapshot keeps a routine NetEase sync to the liked-song id list.
// Song details already stored for that account are reused, so a quiet poll
// does not re-download the whole library.
func (s *Service) fetchSnapshot(ctx context.Context, b Binding) (Snapshot, error) {
	if b.Platform != "netease" {
		return s.Provider.Fetch(ctx, b.Platform)
	}
	provider, ok := s.Provider.(*Providers)
	if !ok {
		return s.Provider.Fetch(ctx, b.Platform)
	}
	account, ids, err := provider.NeteaseLikedIDs(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if account != b.Account {
		return Snapshot{Platform: "netease", Account: account, Complete: true}, nil
	}
	known, err := s.Store.ActiveFavoriteTracks(ctx, b.User, "netease", account)
	if err != nil {
		return Snapshot{}, err
	}
	missing := make([]string, 0)
	missingSet := map[string]bool{}
	for _, id := range ids {
		old, ok := known[id]
		if !ok || old.Platform != "netease" || old.ID != id || !old.Verified || strings.TrimSpace(old.Title) == "" {
			missing = append(missing, id)
			missingSet[id] = true
		}
	}
	fresh, err := provider.NeteaseTracks(ctx, missing)
	if err != nil {
		return Snapshot{}, err
	}
	byID := make(map[string]Track, len(fresh))
	for _, track := range fresh {
		byID[track.ID] = track
	}
	refreshIDs := make([]string, 0)
	for _, id := range ids {
		if !missingSet[id] {
			refreshIDs = append(refreshIDs, id)
		}
	}
	sort.Strings(refreshIDs)
	if batch := s.neteaseRefreshIDs(account, refreshIDs); len(batch) > 0 {
		refreshed, refreshErr := provider.NeteaseTracks(ctx, batch)
		if refreshErr != nil {
			utils.WarnWithFormat("[Discovery] 网易云收藏资料刷新失败，本轮保留已存歌曲: %s", clipDiscoveryLog(refreshErr.Error()))
		} else {
			for _, track := range refreshed {
				if track.Verified && strings.TrimSpace(track.Title) != "" {
					byID[track.ID] = track
				}
			}
		}
	}
	tracks := make([]Track, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return Snapshot{}, fmt.Errorf("网易云收藏 ID 数量不完整，未建立基线")
		}
		seen[id] = true
		if track, ok := byID[id]; ok {
			tracks = append(tracks, track)
			continue
		}
		tracks = append(tracks, known[id])
	}
	snap := Snapshot{Platform: "netease", Account: account, Tracks: tracks, Complete: true}
	return snap, validSnapshot(snap)
}

const neteaseMetadataRefreshBatch = 20

// neteaseRefreshIDs returns the next slice of already stored songs. A failed
// refresh keeps the stored JSON; this cursor only moves when a batch is chosen.
func (s *Service) neteaseRefreshIDs(account string, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.neteaseRefreshCursor == nil {
		s.neteaseRefreshCursor = map[string]int{}
	}
	start := s.neteaseRefreshCursor[account]
	if start < 0 || start >= len(ids) {
		start = 0
	}
	n := neteaseMetadataRefreshBatch
	if n > len(ids) {
		n = len(ids)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ids[(start+i)%len(ids)])
	}
	s.neteaseRefreshCursor[account] = (start + n) % len(ids)
	return out
}

func (s *Service) Recommend(ctx context.Context, user int64) ([]Track, error) {
	batch, err := s.recommend(ctx, user, "", 10)
	return batch.Tracks, err
}

// RediscoverBatch is one platform search. SkippedOwned counts distinct works
// already in the library, including different artist credits of the same title.
// Unconfirmed counts distinct works whose platform search had no single version.
type RediscoverBatch struct {
	Tracks           []Track
	SkippedOwned     int
	Unconfirmed      int
	SkippedShown     int
	SkippedPending   int
	SkippedDismissed int
	SkippedQueued    int
	SkippedArtistCap int
	SkippedDuplicate int
	SkippedOther     int
	Unavailable      int
}

// RecommendLimited searches one download platform and stops after limit songs.
// Discovery currently requests one song per batch. History, library identity,
// and search results are cached.
func (s *Service) RecommendLimited(ctx context.Context, user int64, platform string, limit int) ([]Track, error) {
	batch, err := s.Rediscover(ctx, user, platform, limit)
	if err != nil {
		return nil, err
	}
	return batch.Tracks, nil
}

func (s *Service) Rediscover(ctx context.Context, user int64, platform string, limit int) (RediscoverBatch, error) {
	switch platform {
	case "netease", "qq", "youtube":
	default:
		return RediscoverBatch{}, fmt.Errorf("请选择网易云、QQ 音乐或 YouTube")
	}
	switch limit {
	case 1:
	default:
		return RediscoverBatch{}, fmt.Errorf("请重新发起找回")
	}
	return s.recommend(ctx, user, platform, limit)
}

func (s *Service) recommend(ctx context.Context, user int64, platform string, limit int) (RediscoverBatch, error) {
	favorites, err := s.Store.Favorites(ctx, user)
	if err != nil {
		return RediscoverBatch{}, err
	}
	// Enrich a bounded number of old YouTube favourites per request and cache
	// the result. Unstructured video titles never become invented song names.
	enriched := 0
	for i, t := range favorites {
		if (platform == "" || platform == "youtube") && t.Platform == "youtube" && !t.Verified && !t.NonMusic && enriched < 10 {
			updated, skip, e := s.Provider.Enrich(ctx, t)
			enriched++
			if e != nil {
				continue
			}
			updated.NonMusic = skip
			favorites[i] = updated
			if e = s.Store.RememberFavorite(ctx, user, updated); e != nil {
				return RediscoverBatch{}, e
			}
		}
	}
	history, historyErr := s.cachedHistory(ctx)
	if historyErr != nil && len(favorites) == 0 {
		return RediscoverBatch{}, historyErr
	}
	activeFavorites := make([]Track, 0, len(favorites))
	for _, track := range favorites {
		if track.Favorite {
			activeFavorites = append(activeFavorites, track)
		}
	}
	merged := map[string]Track{}
	for _, t := range append(history, activeFavorites...) {
		key := Identity(t)
		old, ok := merged[key]
		if !ok {
			merged[key] = t
			continue
		}
		if t.Favorite {
			old.Favorite = true
		}
		if t.Plays > old.Plays {
			old.Plays = t.Plays
		}
		if t.LastPlayed.After(old.LastPlayed) {
			old.LastPlayed = t.LastPlayed
		}
		if old.URL == "" && t.URL != "" {
			old.Platform = t.Platform
			old.ID = t.ID
			old.URL = t.URL
			old.Verified = t.Verified
			old.Duration = t.Duration
		}
		merged[key] = old
	}
	exclusions, err := s.Store.Exclusions(ctx, user)
	if err != nil {
		return RediscoverBatch{}, err
	}
	state := &recommendPick{
		artists:           map[string]int{},
		skipped:           map[string]struct{}{},
		unconfirmed:       map[string]struct{}{},
		excludedShown:     map[string]struct{}{},
		excludedPending:   map[string]struct{}{},
		excludedDismissed: map[string]struct{}{},
		excludedQueued:    map[string]struct{}{},
		excludedArtist:    map[string]struct{}{},
		excludedDuplicate: map[string]struct{}{},
		excludedOther:     map[string]struct{}{},
		unavailable:       map[string]struct{}{},
	}
	var ranked []Track
	for key, t := range merged {
		if t.NonMusic || t.Title == "" || t.Artist == "" {
			continue
		}
		if reason := exclusions[key]; reason != "" {
			state.markExclusion(reason, t)
			continue
		}
		ranked = append(ranked, t)
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.Favorite != b.Favorite {
			return a.Favorite
		}
		// A rediscovery candidate should feel like something the user liked
		// enough to keep but has not heard for a long time. Compare a
		// log-shaped play score so one heavily repeated song does not crowd out
		// every other favorite.
		aScore, bScore := rediscoverScore(a), rediscoverScore(b)
		if aScore != bScore {
			return aScore > bScore
		}
		if !a.LastPlayed.Equal(b.LastPlayed) {
			return a.LastPlayed.Before(b.LastPlayed)
		}
		return Identity(a) < Identity(b)
	})
	finish := func(err error) (RediscoverBatch, error) {
		if err != nil {
			return RediscoverBatch{}, err
		}
		return state.batch(), nil
	}
	if platform == "" {
		_, stop, err := s.pickTracks(ctx, state, ranked, limit, platform, 0)
		if stop {
			return finish(nil)
		}
		return finish(err)
	}
	// One ranked pool lets a long-missed favorite compete with a frequently
	// played history row. Separate history/favorite quotas made the first item
	// of a one-song batch almost always come from history.
	_, stop, err := s.pickTracks(ctx, state, ranked, limit, platform, resolveAttemptBudget(limit))
	if stop {
		err = nil
	}
	return finish(err)
}

// rediscoverScore favours repeatedly played songs that have been away for a
// while. The logarithm keeps 100 plays from dwarfing a deeply missed song with
// 20 plays, while the time component lets old favorites rise again.
func rediscoverScore(t Track) float64 {
	plays := float64(t.Plays)
	if plays < 1 {
		plays = 1
	}
	score := math.Log1p(plays)
	if !t.LastPlayed.IsZero() {
		days := time.Since(t.LastPlayed).Hours() / 24
		if days > 0 {
			score += math.Log1p(days) * 0.75
		}
	}
	if t.Favorite {
		score += 1
	}
	return score
}

func resolveAttemptBudget(want int) int {
	if want <= 0 {
		return 0
	}
	return want * 8
}

type recommendPick struct {
	out               []Track
	artists           map[string]int
	skipped           map[string]struct{}
	unconfirmed       map[string]struct{}
	excludedShown     map[string]struct{}
	excludedPending   map[string]struct{}
	excludedDismissed map[string]struct{}
	excludedQueued    map[string]struct{}
	excludedArtist    map[string]struct{}
	excludedDuplicate map[string]struct{}
	excludedOther     map[string]struct{}
	unavailable       map[string]struct{}
}

func (s *Service) pickTracks(ctx context.Context, state *recommendPick, ranked []Track, want int, platform string, budget int) ([]Track, bool, error) {
	if want <= 0 {
		return ranked, false, nil
	}
	attempts := 0
	got := 0
	for i, t := range ranked {
		if got >= want {
			return ranked[i:], false, nil
		}
		if err := ctx.Err(); err != nil {
			if len(state.out) > 0 {
				return nil, true, nil
			}
			return nil, false, err
		}
		if state.artists[Normalize(t.Artist)] >= 2 {
			state.markDistinct(state.excludedArtist, t)
			continue
		}
		owned, err := s.Library.Contains(ctx, t)
		if err != nil {
			return nil, false, err
		}
		if owned {
			state.markOwned(t)
			continue
		}
		if s.Pending != nil {
			pending, err := s.Pending(ctx, t)
			if err != nil {
				return nil, false, err
			}
			if pending {
				state.markDistinct(state.excludedPending, t)
				continue
			}
		}
		attempts++
		if budget > 0 && attempts > budget {
			return ranked[i:], false, nil
		}
		resolved, err := s.resolveCandidate(ctx, t, platform)
		if err != nil {
			if errors.Is(err, ErrAmbiguousRecording) || errors.Is(err, ErrNoUniqueRecording) {
				state.markDistinct(state.unconfirmed, t)
			} else {
				state.markDistinct(state.unavailable, t)
			}
			continue
		}
		owned, err = s.Library.Contains(ctx, resolved)
		if err != nil {
			return nil, false, err
		}
		if owned || batchHasWork(state.out, resolved) {
			if owned {
				state.markOwned(resolved)
			} else {
				state.markDistinct(state.excludedDuplicate, resolved)
			}
			continue
		}
		resolved.Plays = t.Plays
		resolved.LastPlayed = t.LastPlayed
		resolved.Favorite = t.Favorite
		state.out = append(state.out, resolved)
		state.artists[Normalize(t.Artist)]++
		got++
	}
	return nil, false, nil
}

func (state *recommendPick) markOwned(t Track) {
	state.markDistinct(state.skipped, t)
}

func (state *recommendPick) markExclusion(reason string, t Track) {
	switch reason {
	case ExcludedShown:
		state.markDistinct(state.excludedShown, t)
	case ExcludedPending:
		state.markDistinct(state.excludedPending, t)
	case ExcludedDismissed:
		state.markDistinct(state.excludedDismissed, t)
	case ExcludedQueued:
		state.markDistinct(state.excludedQueued, t)
	default:
		state.markDistinct(state.excludedOther, t)
	}
}

func (state *recommendPick) batch() RediscoverBatch {
	unavailable := len(state.unavailable)
	other := len(state.excludedOther)
	return RediscoverBatch{
		Tracks:           state.out,
		SkippedOwned:     len(state.skipped),
		Unconfirmed:      len(state.unconfirmed),
		SkippedShown:     len(state.excludedShown),
		SkippedPending:   len(state.excludedPending),
		SkippedDismissed: len(state.excludedDismissed),
		SkippedQueued:    len(state.excludedQueued),
		SkippedArtistCap: len(state.excludedArtist),
		SkippedDuplicate: len(state.excludedDuplicate),
		SkippedOther:     other,
		Unavailable:      unavailable,
	}
}

func (state *recommendPick) markDistinct(dst map[string]struct{}, t Track) {
	key := ownershipTitle(t.Title)
	if key == "" {
		key = Identity(t)
	}
	dst[key] = struct{}{}
}

func batchHasWork(out []Track, candidate Track) bool {
	for _, have := range out {
		if sameOwnedWork(have, candidate) {
			return true
		}
	}
	return false
}

func (s *Service) cachedHistory(ctx context.Context) ([]Track, error) {
	if s.History == nil {
		return nil, fmt.Errorf("尚未配置收听历史")
	}
	s.cacheMu.Lock()
	if s.historyReady && time.Since(s.historyAt) < historyCacheTTL {
		tracks := append([]Track(nil), s.historyTracks...)
		s.cacheMu.Unlock()
		return tracks, nil
	}
	s.cacheMu.Unlock()
	tracks, err := s.History(ctx)
	if err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	s.historyTracks = append([]Track(nil), tracks...)
	s.historyAt = time.Now()
	s.historyReady = true
	s.cacheMu.Unlock()
	return tracks, nil
}

func (s *Service) resolveCandidate(ctx context.Context, track Track, platform string) (Track, error) {
	key := platform + "\n" + Normalize(track.Title) + "\n" + Normalize(track.Artist) + "\n" + Normalize(track.Album)
	s.cacheMu.Lock()
	if entry, ok := s.resolveCache[key]; ok {
		ttl := resolveMissTTL
		if entry.err == nil {
			ttl = resolveHitTTL
		}
		if time.Since(entry.at) < ttl {
			cached, err := entry.track, entry.err
			s.cacheMu.Unlock()
			return cached, err
		}
	}
	s.cacheMu.Unlock()
	var resolved Track
	var err error
	if platform == "" {
		resolved, err = s.Provider.Resolve(ctx, track, "")
	} else {
		resolved, err = s.Provider.ResolveOn(ctx, track, platform)
	}
	if ctx.Err() == nil && (err == nil || errors.Is(err, ErrAmbiguousRecording) || errors.Is(err, ErrNoUniqueRecording)) {
		s.cacheMu.Lock()
		if s.resolveCache == nil {
			s.resolveCache = map[string]resolveCacheEntry{}
		}
		if len(s.resolveCache) > 4000 {
			s.resolveCache = map[string]resolveCacheEntry{}
		}
		s.resolveCache[key] = resolveCacheEntry{track: resolved, err: err, at: time.Now()}
		s.cacheMu.Unlock()
	}
	return resolved, err
}

// ResolveFromAnotherPlatform forces a fresh cross-platform lookup for a failed
// job, bypassing the normal resolve cache. It never reuses the original source
// URL because that source has already failed.
func (s *Service) ResolveFromAnotherPlatform(ctx context.Context, track Track) (Track, error) {
	if s.Provider == nil {
		return Track{}, errors.New("收藏来源服务尚未就绪")
	}
	source := strings.TrimSpace(track.Platform)
	if err := s.ClearResolveCache(track, ""); err != nil {
		return Track{}, err
	}
	if source != "" {
		track.URL = ""
	}
	resolved, err := s.Provider.Resolve(ctx, track, source)
	if err != nil {
		return Track{}, err
	}
	if !SameRecording(track, resolved) || resolved.Platform == source {
		return Track{}, ErrNoUniqueRecording
	}
	resolved.Plays = track.Plays
	resolved.LastPlayed = track.LastPlayed
	resolved.Favorite = track.Favorite
	resolved.CardAction = ""
	resolved.IngestID = ""
	resolved.IngestNote = ""
	// Mark the chosen alternate as an explicit user decision. ProcessOne then
	// retries that exact source instead of trying to resolve back to the
	// original failed platform.
	resolved.Approved = true
	return resolved, nil
}

// ClearResolveCache removes cached lookup results for one recording. It is
// used for explicit user retries, where a prior miss must not suppress a new
// search.
func (s *Service) ClearResolveCache(track Track, platform string) error {
	key := platform + "\n" + Normalize(track.Title) + "\n" + Normalize(track.Artist) + "\n" + Normalize(track.Album)
	s.cacheMu.Lock()
	delete(s.resolveCache, key)
	s.cacheMu.Unlock()
	return nil
}

func (s *Service) Run(ctx context.Context) error {
	if err := s.Store.Recover(ctx); err != nil {
		return err
	}
	poll := s.PollInterval
	if poll <= 0 {
		poll = 10 * time.Minute
	}
	summary := s.SummaryInterval
	if summary <= 0 {
		summary = 30 * time.Minute
	}
	// Favorite reads stay off this loop. A slow list fetch must not stall the
	// download queue, and a long download must not delay the next read.
	utils.InfoWithFormat("[Discovery] 收藏读取已单独运行，间隔 %s", poll.Round(time.Second))
	var polls sync.WaitGroup
	polls.Add(1)
	go func() {
		defer polls.Done()
		s.runFavoritePoll(ctx, poll)
	}()
	defer polls.Wait()
	nextSummary := time.Now().Add(summary)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.ProcessOne(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
				if ctx.Err() != nil {
					return nil
				}
				if s.OnError != nil {
					s.OnError(err)
				}
				// A transient persistence failure after claiming must not leave a
				// permanent running task. Library/recent-download checks protect
				// recovery after media was already committed.
				if recoverErr := s.Store.Recover(ctx); recoverErr != nil {
					utils.WarnWithFormat("[Discovery] 恢复未完成的入库任务失败: %v", recoverErr)
				}
			}
			if time.Now().After(nextSummary) {
				if err := s.Digest(ctx); err != nil {
					utils.WarnWithFormat("[Discovery] 收藏入库汇总失败: %v", err)
				}
				nextSummary = time.Now().Add(summary)
			}
		}
	}
}

// runFavoritePoll reads favorites, then waits. The wait starts after the read
// returns, so two reads never overlap. The first read still happens as soon
// as the service starts.
func (s *Service) runFavoritePoll(ctx context.Context, poll time.Duration) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			utils.InfoWithFormat("[Discovery] 收藏读取已停止")
			return
		case <-timer.C:
			if s.Enabled {
				round, cancel := context.WithTimeout(ctx, favoriteSyncRoundTimeout)
				fresh, syncErr := s.Sync(round, 0)
				cancel()
				if ctx.Err() != nil {
					utils.InfoWithFormat("[Discovery] 收藏读取已停止")
					return
				}
				if errors.Is(syncErr, context.DeadlineExceeded) {
					utils.WarnWithFormat("[Discovery] 收藏读取本轮超时，已在 %s 停止并等待下一轮", favoriteSyncRoundTimeout)
				}
				s.notifyFresh(fresh)
			}
			timer.Reset(poll)
		}
	}
}

func (s *Service) ProcessOne(ctx context.Context) (resultErr error) {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	j, err := s.Store.Claim(ctx, s.Enabled)
	if err != nil {
		return err
	}
	j.File = nil
	utils.InfoWithFormat("[Discovery] 开始入库 %s", discoveryTrackLabel(j.Track))
	defer func() {
		if recovered := recover(); recovered != nil {
			utils.WarnWithFormat("[Discovery] 入库处理异常 %s: %v", discoveryTrackLabel(j.Track), recovered)
			resultErr = s.Store.Finish(context.Background(), j, Failed, "下载处理异常，请检查处理器日志后重试", "", 0)
		}
	}()
	finish := func(state, message, result string, next int64) error {
		err := s.Store.Finish(ctx, j, state, message, result, next)
		logDiscoveryJob(j, state, message, err)
		return err
	}
	if s.Authorized != nil && !s.Authorized(j.User, j.Chat) {
		return finish(Failed, "用户授权已变更，任务未执行", "", 0)
	}
	retry := func(err error) error {
		state := Queued
		next := time.Now().Add(time.Duration(j.Attempts) * time.Minute).Unix()
		if j.Attempts >= 4 {
			state = Failed
		}
		return finish(state, err.Error(), "", next)
	}
	t, skip, err := s.Provider.Enrich(ctx, j.Track)
	if err != nil {
		return retry(err)
	}
	j.Track = t
	if t.Verified {
		unique, e := s.Store.AdoptIdentity(ctx, j)
		if e != nil {
			return retry(e)
		}
		if !unique {
			return finish(Ignored, "已合并到同一首歌曲的任务", "", 0)
		}
	}
	if skip {
		return finish(Ignored, "不是可归档的音乐单曲", "", 0)
	}
	if !t.Verified && !t.Approved {
		return finish(NeedsConfirmation, "无法明确识别音乐单曲，需确认原链接音频", "", 0)
	}
	owned, sameTitle, err := s.downloadOwnership(ctx, t)
	if err != nil {
		return retry(err)
	}
	if owned {
		return finish(Owned, "曲库已有该版本", "", 0)
	}
	if s.Pending != nil {
		pending, e := s.Pending(ctx, t)
		if e != nil {
			return retry(e)
		}
		if pending {
			return finish(PendingUpload, "等待近期下载或上传完成", "", 0)
		}
	}
	if sameTitle && !t.Approved {
		return finish(NeedsConfirmation, "曲库里有同名歌曲，请确认是否下载这个版本", "", 0)
	}
	result, err := s.Download(ctx, t)
	if err != nil {
		var heard *audiodupe.LibraryMatch
		if errors.As(err, &heard) {
			switch heard.Decision {
			case audiodupe.DecisionSkip:
				return finish(Owned, heard.Error(), "", 0)
			case audiodupe.DecisionConfirm:
				return finish(NeedsConfirmation, heard.Error(), "", 0)
			}
		}
		if t.Approved {
			return retry(err)
		}
		if errors.Is(err, errMetadataAmbiguous) {
			return finish(NeedsConfirmation, err.Error(), "", 0)
		}
		alternate, e := s.Provider.Resolve(ctx, t, t.Platform)
		if e != nil {
			if errors.Is(e, ErrAmbiguousRecording) {
				return finish(NeedsConfirmation, "原平台不可用，其他平台版本存疑；可确认重试原链接或跳过", "", 0)
			}
			return retry(fmt.Errorf("原平台下载失败，未找到明确同版替代；%w", err))
		}
		// Recheck against the actual library after resolving a cross-platform ID.
		owned, sameTitle, e = s.downloadOwnership(ctx, alternate)
		if e != nil {
			return retry(e)
		}
		if owned {
			return finish(Owned, "曲库已有该版本", "", 0)
		}
		if sameTitle {
			j.Track = alternate
			return finish(NeedsConfirmation, "替代音源与曲库中的歌曲同名，请确认是否下载这个版本", "", 0)
		}
		result, err = s.Download(ctx, alternate)
		if err != nil {
			return retry(err)
		}
	}
	if result.Track != nil {
		j.Track = *result.Track
	}
	if j.SourceAccount == "" {
		j.File = result.File
	}
	state := Completed
	if result.Pending {
		state = PendingUpload
	}
	if err := finish(state, "", result.Detail, 0); err != nil {
		return err
	}
	if state == Completed && j.File != nil && s.NotifyIngested != nil {
		stored, err := s.Store.Job(ctx, j.ID)
		if err != nil {
			return err
		}
		return s.notifyIngestedJob(ctx, stored)
	}
	return nil
}

func (s *Service) downloadOwnership(ctx context.Context, t Track) (owned, sameTitle bool, err error) {
	if library, ok := s.Library.(interface {
		matchOwnership(context.Context, Track) (bool, bool, error)
	}); ok {
		return library.matchOwnership(ctx, t)
	}
	owned, err = s.Library.Contains(ctx, t)
	return owned, false, err
}

func (s *Service) Reconcile(ctx context.Context, user int64) error {
	jobs, err := s.Store.PendingJobs(ctx, user)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.State != PendingUpload {
			continue
		}
		owned, e := s.Library.Contains(ctx, j.Track)
		if e != nil {
			return e
		}
		if owned {
			if j.File != nil {
				ready, verifyErr := uploadedDiscoveryFile(j.File)
				if verifyErr != nil {
					utils.WarnWithFormat("[Discovery] 无法核对待上传歌曲 %s: %v", discoveryTrackLabel(j.Track), verifyErr)
				}
				if !ready {
					continue
				}
			}
			completed, e := s.Store.completePending(ctx, j)
			if e != nil {
				utils.WarnWithFormat("[Discovery] 待上传歌曲没能标成已入库 %s: %v", discoveryTrackLabel(j.Track), e)
				return e
			}
			if completed {
				utils.InfoWithFormat("[Discovery] 待上传歌曲已入库 %s", discoveryTrackLabel(j.Track))
			}
		}
	}
	return nil
}

func StateLabel(state string) string {
	switch state {
	case Queued:
		return "排队"
	case Running:
		return "下载中"
	case PendingUpload:
		return "待上传／待曲库确认"
	case Completed:
		return "已入库"
	case Owned:
		return "曲库已有"
	case NeedsConfirmation:
		return "待确认"
	case Failed:
		return "失败"
	case Ignored:
		return "已跳过"
	}
	return state
}

func (s *Service) Digest(ctx context.Context) error {
	if s.Notify == nil && s.NotifyIngested == nil {
		return nil
	}
	_ = s.Reconcile(ctx, 0)
	jobs, err := s.Store.Jobs(ctx, 0, true)
	if err != nil {
		return err
	}
	groups := map[int64][]Job{}
	for _, j := range jobs {
		if j.State == Completed && j.File != nil && s.NotifyIngested != nil {
			if err := s.notifyIngestedJob(ctx, j); err != nil {
				return err
			}
			continue
		}
		if j.State != Queued && j.State != Running {
			groups[j.Chat] = append(groups[j.Chat], j)
		}
	}
	if s.Notify == nil {
		return nil
	}
	for chat, list := range groups {
		if s.Authorized != nil && !s.Authorized(list[0].User, chat) {
			continue
		}
		counts := map[string]int{}
		for _, j := range list {
			counts[StateLabel(j.State)]++
		}
		keys := []string{}
		for key := range counts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("🎵 <b>收藏入库汇总</b>\n")
		b.WriteString("──────────────────\n")
		for _, key := range keys {
			fmt.Fprintf(&b, "%s：%d 首\n", utils.EscapeHTML(key), counts[key])
		}
		b.WriteString("发送 /favorites 查看详情、待确认和失败任务。")
		if err = s.Notify(chat, b.String()); err != nil {
			utils.WarnWithFormat("[Discovery] 收藏入库汇总发送失败: %v", err)
			continue
		}
		for _, j := range list {
			if err = s.Store.NotifyDone(ctx, j.ID, j.Updated, j.State); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) notifyIngestedJob(ctx context.Context, j Job) error {
	if j.State != Completed || (s.Authorized != nil && !s.Authorized(j.User, j.Chat)) {
		return nil
	}
	if err := s.NotifyIngested(j); err != nil {
		utils.WarnWithFormat("[Discovery] 入库操作卡片发送失败，将在汇总时重试: %v", err)
		return nil
	}
	return s.Store.NotifyDone(ctx, j.ID, j.Updated, j.State)
}

func discoveryTrackLabel(track Track) string {
	title := strings.TrimSpace(track.Title)
	if title == "" {
		title = strings.TrimSpace(track.ID)
	}
	if title == "" {
		title = "未命名"
	}
	label := freshPlatformLabel(track.Platform) + "《" + truncateRunes(title, 40) + "》"
	if artist := strings.TrimSpace(track.Artist); artist != "" {
		label += truncateRunes(artist, 24)
	}
	return label
}

func logDiscoveryJob(job Job, state, message string, err error) {
	label := discoveryTrackLabel(job.Track)
	if err != nil {
		utils.WarnWithFormat("[Discovery] 入库状态没能写入 %s → %s: %v", label, state, err)
		return
	}
	note := clipDiscoveryLog(message)
	switch state {
	case Failed, NeedsConfirmation:
		utils.WarnWithFormat("[Discovery] 入库结束 %s → %s：%s", label, StateLabel(state), note)
	case Queued:
		utils.WarnWithFormat("[Discovery] 入库稍后重试 %s：%s", label, note)
	default:
		if note != "" {
			utils.InfoWithFormat("[Discovery] 入库结束 %s → %s：%s", label, StateLabel(state), note)
			return
		}
		utils.InfoWithFormat("[Discovery] 入库结束 %s → %s", label, StateLabel(state))
	}
}

func clipDiscoveryLog(value string) string {
	return truncateRunes(strings.Join(strings.Fields(value), " "), 180)
}
