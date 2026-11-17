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
	Notify          func(int64, string) error
	OnError         func(error)
	Authorized      func(int64, int64) bool
	Enabled         bool
	PollInterval    time.Duration
	SummaryInterval time.Duration
	syncMu          sync.Mutex
	workMu          sync.Mutex
}

func (s *Service) Sync(ctx context.Context, user int64) error {
	if !s.Enabled {
		return fmt.Errorf("自动收藏同步未启用，请先配置 favorites.enable: true；可先预览并绑定账号")
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	bindings, err := s.Store.Bindings(ctx, user)
	if err != nil {
		return err
	}
	var failures []string
	for _, b := range bindings {
		if b.Paused || (s.Authorized != nil && !s.Authorized(b.User, b.Chat)) {
			continue
		}
		snap, e := s.Provider.Fetch(ctx, b.Platform)
		if e == nil && snap.Platform != b.Platform {
			e = fmt.Errorf("收藏来源标识不一致，未更新基线")
		}
		if e == nil {
			_, e = s.Store.Apply(ctx, b.User, b.Chat, snap, false)
		}
		if e != nil {
			message := b.Platform + "：" + e.Error()
			pause := strings.Contains(message, "登录失效") || strings.Contains(message, "账号已变化") || strings.Contains(message, "列表不可访问")
			changed, _ := s.Store.SourceError(ctx, b, message, pause)
			if changed && s.Notify != nil {
				_ = s.Notify(b.Chat, "⚠️ 收藏同步异常\n"+message)
			}
			failures = append(failures, message)
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "\n"))
	}
	return nil
}

func (s *Service) Recommend(ctx context.Context, user int64) ([]Track, error) {
	favorites, err := s.Store.Favorites(ctx, user)
	if err != nil {
		return nil, err
	}
	// Enrich a bounded number of old YouTube favourites per request and cache
	// the result. Unstructured video titles never become invented song names.
	enriched := 0
	for i, t := range favorites {
		if t.Platform == "youtube" && !t.Verified && !t.NonMusic && enriched < 10 {
			updated, skip, e := s.Provider.Enrich(ctx, t)
			enriched++
			if e != nil {
				continue
			}
			updated.NonMusic = skip
			favorites[i] = updated
			if e = s.Store.RememberFavorite(ctx, user, updated); e != nil {
				return nil, e
			}
		}
	}
	history, historyErr := s.History(ctx)
	if historyErr != nil && len(favorites) == 0 {
		return nil, historyErr
	}
	merged := map[string]Track{}
	for _, t := range append(history, favorites...) {
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
		return nil, err
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
	var out []Track
	artists := map[string]int{}
	for _, t := range ranked {
		if err := ctx.Err(); err != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		if artists[Normalize(t.Artist)] >= 2 {
			continue
		}
		owned, e := s.Library.Contains(ctx, t)
		if e != nil {
			return nil, e
		}
		if owned {
			continue
		}
		if s.Pending != nil {
			pending, e := s.Pending(ctx, t)
			if e != nil {
				return nil, e
			}
			if pending {
				continue
			}
		}
		resolved, e := s.Provider.Resolve(ctx, t, "")
		if e != nil {
			continue
		}
		resolved.Plays = t.Plays
		resolved.LastPlayed = t.LastPlayed
		resolved.Favorite = t.Favorite
		out = append(out, resolved)
		artists[Normalize(t.Artist)]++
		if len(out) == 10 {
			break
		}
	}
	// Rendering, not selection, commits shown timestamps. A Telegram delivery
	// failure must not hide this batch for a month.
	return out, nil
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
					_ = s.Sync(ctx, 0)
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
