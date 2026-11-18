package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/internal/audiodupe"
)

type DownloadResult struct {
	Pending bool
	Detail  string
	Track   *Track
}
type Service struct {
	Store    *Store
	Provider Provider
	Library  Library
	History  func(context.Context) ([]Track, error)
	Download func(context.Context, Track) (DownloadResult, error)
	Pending  func(context.Context, Track) (bool, error)
	// Notify returns an error on delivery failure so digests remain unacknowledged.
	Notify               func(int64, string) error
	OnError              func(error)
	Authorized           func(int64, int64) bool
	Enabled              bool
	PollInterval         time.Duration
	SummaryInterval      time.Duration
	syncMu               sync.Mutex
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

func (s *Service) Sync(ctx context.Context, user int64) ([]FreshFavorite, error) {
	return s.sync(ctx, user, true)
}

// SyncQuietly runs a user-requested sync without sending background notices.
// The caller owns the interaction and should report the result in its message.
func (s *Service) SyncQuietly(ctx context.Context, user int64) ([]FreshFavorite, error) {
	return s.sync(ctx, user, false)
}

func (s *Service) sync(ctx context.Context, user int64, notifyErrors bool) ([]FreshFavorite, error) {
	if !s.Enabled {
		return nil, fmt.Errorf("自动收藏同步未启用，请先配置 favorites.enable: true；可先预览并绑定账号")
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	bindings, err := s.Store.Bindings(ctx, user)
	if err != nil {
		return nil, err
	}
	var fresh []FreshFavorite
	var failures []string
	for _, b := range bindings {
		if b.Paused || (s.Authorized != nil && !s.Authorized(b.User, b.Chat)) {
			continue
		}
		snap, e := s.fetchSnapshot(ctx, b)
		if e == nil && snap.Platform != b.Platform {
			e = fmt.Errorf("收藏来源标识不一致，未更新基线")
		}
		var added []Track
		if e == nil {
			_, added, e = s.Store.Apply(ctx, b.User, b.Chat, snap, false)
		}
		if e != nil {
			message := b.Platform + "：" + e.Error()
			pause := strings.Contains(message, "登录失效") || strings.Contains(message, "账号已变化") || strings.Contains(message, "列表不可访问")
			changed, _ := s.Store.SourceError(ctx, b, message, pause)
			if changed && notifyErrors && s.Notify != nil {
				_ = s.Notify(b.Chat, "⚠️ 收藏同步异常\n"+message)
			}
			failures = append(failures, message)
			continue
		}
		for _, track := range added {
			fresh = append(fresh, FreshFavorite{User: b.User, Chat: b.Chat, Platform: b.Platform, Track: track})
		}
	}
	if len(failures) > 0 {
		return fresh, errors.New(strings.Join(failures, "\n"))
	}
	return fresh, nil
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
		if refreshErr == nil {
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
	tracks, _, _, err := s.recommend(ctx, user, "", 10)
	return tracks, err
}

// RediscoverBatch is one platform search. SkippedOwned counts distinct works
// already in the library, including different artist credits of the same title.
// Unconfirmed counts distinct works whose platform search had no single version.
type RediscoverBatch struct {
	Tracks       []Track
	SkippedOwned int
	Unconfirmed  int
}

// RecommendLimited searches one download platform and stops after limit songs.
// limit is 3, 6, or 9. History, library identity, and search results are cached.
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
	case 3, 6, 9:
	default:
		return RediscoverBatch{}, fmt.Errorf("数量只能是 3、6 或 9")
	}
	tracks, skipped, unconfirmed, err := s.recommend(ctx, user, platform, limit)
	if err != nil {
		return RediscoverBatch{}, err
	}
	return RediscoverBatch{Tracks: tracks, SkippedOwned: skipped, Unconfirmed: unconfirmed}, nil
}

func (s *Service) recommend(ctx context.Context, user int64, platform string, limit int) ([]Track, int, int, error) {
	favorites, err := s.Store.Favorites(ctx, user)
	if err != nil {
		return nil, 0, 0, err
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
				return nil, 0, 0, e
			}
		}
	}
	history, historyErr := s.cachedHistory(ctx)
	if historyErr != nil && len(favorites) == 0 {
		return nil, 0, 0, historyErr
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
	excluded, err := s.Store.Excluded(ctx, user)
	if err != nil {
		return nil, 0, 0, err
	}
	var ranked []Track
	for key, t := range merged {
		if !excluded[key] && !t.NonMusic && t.Title != "" && t.Artist != "" {
			ranked = append(ranked, t)
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.Favorite != b.Favorite {
			return a.Favorite
		}
		if a.Plays != b.Plays {
			return a.Plays > b.Plays
		}
		if !a.LastPlayed.Equal(b.LastPlayed) {
			return a.LastPlayed.Before(b.LastPlayed)
		}
		return Identity(a) < Identity(b)
	})
	state := &recommendPick{
		artists:     map[string]int{},
		skipped:     map[string]struct{}{},
		unconfirmed: map[string]struct{}{},
	}
	finish := func(err error) ([]Track, int, int, error) {
		if err != nil {
			return nil, 0, 0, err
		}
		// Rendering, not selection, commits shown timestamps. A Telegram delivery
		// failure must not hide this batch for a month.
		return state.out, len(state.skipped), len(state.unconfirmed), nil
	}
	if platform == "" {
		_, stop, err := s.pickTracks(ctx, state, ranked, limit, platform, 0)
		if stop {
			return finish(nil)
		}
		return finish(err)
	}
	var historyRows, favoriteRows []Track
	for _, track := range ranked {
		if track.Favorite {
			favoriteRows = append(favoriteRows, track)
			continue
		}
		historyRows = append(historyRows, track)
	}
	historyQuota, favoriteQuota := discoveryQuotas(limit)
	// Each side has its own resolve budget. Burning the history budget must
	// still leave the favorite pass, and a cancelled request still stops both.
	restHistory, stop, err := s.pickTracks(ctx, state, historyRows, historyQuota, platform, resolveAttemptBudget(historyQuota))
	if err != nil || stop {
		if stop {
			err = nil
		}
		return finish(err)
	}
	restFavorites, stop, err := s.pickTracks(ctx, state, favoriteRows, favoriteQuota, platform, resolveAttemptBudget(favoriteQuota))
	if err != nil || stop {
		if stop {
			err = nil
		}
		return finish(err)
	}
	if len(state.out) < limit {
		_, stop, err = s.pickTracks(ctx, state, append(restHistory, restFavorites...), limit-len(state.out), platform, resolveAttemptBudget(limit-len(state.out)))
		if stop {
			err = nil
		}
	}
	return finish(err)
}

func resolveAttemptBudget(want int) int {
	if want <= 0 {
		return 0
	}
	return want * 8
}

// discoveryQuotas keeps listening history on the card when favorites are much
// larger. The extra song on an odd limit goes to history.
func discoveryQuotas(limit int) (history int, favorite int) {
	if limit <= 0 {
		return 0, 0
	}
	history = limit/2 + limit%2
	return history, limit - history
}

type recommendPick struct {
	out         []Track
	artists     map[string]int
	skipped     map[string]struct{}
	unconfirmed map[string]struct{}
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
	nextPoll := time.Now()
	nextSummary := time.Now().Add(summary)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if time.Now().After(nextPoll) {
				if s.Enabled {
					fresh, _ := s.Sync(ctx, 0)
					s.notifyFresh(fresh)
				}
				nextPoll = time.Now().Add(poll)
			}
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
				_ = s.Store.Recover(ctx)
			}
			if time.Now().After(nextSummary) {
				_ = s.Digest(ctx)
				nextSummary = time.Now().Add(summary)
			}
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
	defer func() {
		if recover() != nil {
			resultErr = s.Store.Finish(context.Background(), j, Failed, "下载处理异常，请检查处理器日志后重试", "", 0)
		}
	}()
	finish := func(state, message, result string, next int64) error {
		return s.Store.Finish(ctx, j, state, message, result, next)
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
	owned, err := s.Library.Contains(ctx, t)
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
		owned, e = s.Library.Contains(ctx, alternate)
		if e != nil {
			return retry(e)
		}
		if owned {
			return finish(Owned, "曲库已有该版本", "", 0)
		}
		result, err = s.Download(ctx, alternate)
		if err != nil {
			return retry(err)
		}
	}
	if result.Track != nil {
		j.Track = *result.Track
	}
	state := Completed
	if result.Pending {
		state = PendingUpload
	}
	return finish(state, "", result.Detail, 0)
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
			if e = s.Store.Finish(ctx, j, Completed, "", j.Result, 0); e != nil {
				return e
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
	if s.Notify == nil {
		return nil
	}
	_ = s.Reconcile(ctx, 0)
	jobs, err := s.Store.Jobs(ctx, 0, true)
	if err != nil {
		return err
	}
	groups := map[int64][]Job{}
	for _, j := range jobs {
		if j.State != Queued && j.State != Running {
			groups[j.Chat] = append(groups[j.Chat], j)
		}
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
		b.WriteString("🎵 收藏入库汇总\n")
		for _, key := range keys {
			fmt.Fprintf(&b, "%s：%d 首\n", key, counts[key])
		}
		b.WriteString("发送 /favorites 查看详情、待确认和失败任务。")
		if err = s.Notify(chat, b.String()); err != nil {
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
