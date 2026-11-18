package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/internal/aichat"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
)

func (s webStudio) FavoriteStatus(ctx context.Context) (web.FavoritesPage, error) {
	return s.favoritePage(ctx, "")
}

func (s webStudio) PreviewFavorite(ctx context.Context, platform string) (web.FavoritePreview, error) {
	user, err := s.owner()
	if err != nil {
		return web.FavoritePreview{}, err
	}
	if s.app.discovery == nil || s.app.discovery.Provider == nil || s.app.discovery.Store == nil {
		return web.FavoritePreview{}, errors.New("收藏服务尚未就绪")
	}
	if platform != "netease" && platform != "qq" && platform != "youtube" {
		return web.FavoritePreview{}, errors.New("请选择网易云、QQ 音乐或 YouTube")
	}
	if !s.app.discoveryCommandMu.TryLock() {
		return web.FavoritePreview{}, errors.New("正在处理上一条请求")
	}
	defer s.app.discoveryCommandMu.Unlock()
	snap, err := s.app.discovery.Provider.Fetch(ctx, platform)
	if err != nil {
		return web.FavoritePreview{}, err
	}
	if snap.Platform != platform || !snap.Complete {
		return web.FavoritePreview{}, errors.New("收藏快照不完整，未建立预览")
	}
	card, err := s.app.discovery.Store.SaveCard(ctx, discovery.Card{User: user, Chat: user, Kind: "binding", Snapshot: &snap})
	if err != nil {
		return web.FavoritePreview{}, errors.New("无法保存绑定预览，可以重试")
	}
	return web.FavoritePreview{
		Token: card.Token, Platform: platform, Account: maskedDiscoveryAccount(snap.Account),
		Count: len(snap.Tracks), Enabled: s.app.discovery.Enabled,
	}, nil
}

func (s webStudio) BindFavorite(ctx context.Context, token string) (web.FavoritesPage, error) {
	user, err := s.owner()
	if err != nil {
		return web.FavoritesPage{}, err
	}
	if s.app.discovery == nil || s.app.discovery.Store == nil {
		return web.FavoritesPage{}, errors.New("收藏服务尚未就绪")
	}
	card, err := s.app.discovery.Store.TakeCard(ctx, strings.TrimSpace(token), user, user)
	if err != nil {
		return web.FavoritesPage{}, err
	}
	if card.Kind != "binding" || card.Snapshot == nil {
		return web.FavoritesPage{}, errors.New("绑定已失效，请重新读取")
	}
	if _, _, err = s.app.discovery.Store.Apply(ctx, user, user, *card.Snapshot, true); err != nil {
		return web.FavoritesPage{}, err
	}
	return s.favoritePage(ctx, "已绑定并建立收藏基线。这批已有收藏未下载。")
}

func (s webStudio) PauseFavorite(ctx context.Context, platform string, paused bool) (web.FavoritesPage, error) {
	user, err := s.owner()
	if err != nil {
		return web.FavoritesPage{}, err
	}
	if s.app.discovery == nil || s.app.discovery.Store == nil {
		return web.FavoritesPage{}, errors.New("收藏服务尚未就绪")
	}
	bindings, err := s.app.discovery.Store.Bindings(ctx, user)
	if err != nil {
		return web.FavoritesPage{}, errors.New("读取收藏状态失败")
	}
	found := false
	for _, binding := range bindings {
		if binding.Platform == platform {
			found = true
			break
		}
	}
	if !found {
		return web.FavoritesPage{}, errors.New("还没有绑定这个平台")
	}
	if err = s.app.discovery.Store.Pause(ctx, user, platform, paused); err != nil {
		return web.FavoritesPage{}, err
	}
	notice := "已暂停该平台的自动同步。"
	if !paused {
		notice = "已恢复该平台的自动同步。"
	}
	return s.favoritePage(ctx, notice)
}

func (s webStudio) SyncFavorites(ctx context.Context) (web.FavoritesPage, error) {
	user, err := s.owner()
	if err != nil {
		return web.FavoritesPage{}, err
	}
	if s.app.discovery == nil || s.app.discovery.Store == nil {
		return web.FavoritesPage{}, errors.New("收藏服务尚未就绪")
	}
	if !s.app.discovery.Enabled {
		return web.FavoritesPage{}, errors.New("自动同步未启用，新增收藏也不会自动入库")
	}
	if !s.app.discoveryCommandMu.TryLock() {
		return web.FavoritesPage{}, errors.New("正在处理上一条请求")
	}
	defer s.app.discoveryCommandMu.Unlock()
	fresh, syncErr := s.app.discovery.SyncQuietly(ctx, user)
	notice := strings.TrimPrefix(favoritesSyncNotice(fresh), "✅ ")
	if syncErr != nil {
		notice = "同步没有完成：" + discoveryPublicError(syncErr)
		if len(fresh) > 0 {
			notice += " " + strings.TrimPrefix(favoritesSyncNotice(fresh), "✅ ")
		}
	}
	return s.favoritePage(ctx, notice)
}

func (s webStudio) RetryFavorites(ctx context.Context) (web.FavoritesPage, error) {
	user, err := s.owner()
	if err != nil {
		return web.FavoritesPage{}, err
	}
	if s.app.discovery == nil || s.app.discovery.Store == nil {
		return web.FavoritesPage{}, errors.New("收藏服务尚未就绪")
	}
	jobs, err := s.app.discovery.Store.Jobs(ctx, user, false)
	if err != nil {
		return web.FavoritesPage{}, errors.New("无法读取失败记录")
	}
	failed := 0
	for _, job := range jobs {
		if job.State == discovery.Failed {
			failed++
		}
	}
	if failed == 0 {
		return s.favoritePage(ctx, "没有需要重试的失败任务。")
	}
	if err = s.app.discovery.Store.Retry(ctx, user); err != nil {
		return web.FavoritesPage{}, err
	}
	return s.favoritePage(ctx, fmt.Sprintf("%d 首失败任务已重新排队。", failed))
}

func (s webStudio) RefreshFavorites(ctx context.Context) (web.FavoritesPage, error) {
	user, err := s.owner()
	if err != nil {
		return web.FavoritesPage{}, err
	}
	if s.app.discovery == nil || s.app.discovery.Store == nil {
		return web.FavoritesPage{}, errors.New("收藏服务尚未就绪")
	}
	notice := "已刷新队列状态。"
	if s.app.discovery.Library != nil {
		if err = s.app.discovery.Reconcile(ctx, user); err != nil {
			notice = "刷新队列状态失败：" + discoveryPublicError(err)
		}
	}
	return s.favoritePage(ctx, notice)
}

func (s webStudio) ResolveFavorite(ctx context.Context, id string, accept bool) (web.FavoritesPage, error) {
	user, err := s.owner()
	if err != nil {
		return web.FavoritesPage{}, err
	}
	if s.app.discovery == nil || s.app.discovery.Store == nil {
		return web.FavoritesPage{}, errors.New("收藏服务尚未就绪")
	}
	if err = s.app.discovery.Store.ResolveJob(ctx, id, user, accept); err != nil {
		return web.FavoritesPage{}, err
	}
	notice := "已标为不再处理。"
	if accept {
		notice = "已确认，这首会进入入库队列。"
	}
	return s.favoritePage(ctx, notice)
}

func (s webStudio) DiscoveryJobs(ctx context.Context) ([]web.DiscoveryJobView, error) {
	user, err := s.owner()
	if err != nil {
		return nil, err
	}
	if s.app.discovery == nil || s.app.discovery.Store == nil {
		return nil, errors.New("遗珠服务尚未就绪")
	}
	jobs, err := s.app.discovery.Store.Jobs(ctx, user, false)
	if err != nil {
		return nil, errors.New("读取入库队列失败")
	}
	out := make([]web.DiscoveryJobView, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, web.DiscoveryJobView{
			Platform: job.Track.Platform,
			ID:       job.Track.ID,
			Title:    job.Track.Title,
			Artist:   job.Track.Artist,
			State:    job.State,
			Label:    discovery.StateLabel(job.State),
			Error:    clipStudioText(job.Error, 180),
		})
	}
	return out, nil
}

func (s webStudio) favoritePage(ctx context.Context, notice string) (web.FavoritesPage, error) {
	user, err := s.owner()
	if err != nil {
		return web.FavoritesPage{}, err
	}
	if s.app.discovery == nil || s.app.discovery.Store == nil {
		return web.FavoritesPage{}, errors.New("收藏服务尚未就绪")
	}
	bindings, err := s.app.discovery.Store.Bindings(ctx, user)
	if err != nil {
		return web.FavoritesPage{}, errors.New("读取收藏状态失败")
	}
	jobs, err := s.app.discovery.Store.Jobs(ctx, user, false)
	if err != nil {
		return web.FavoritesPage{}, errors.New("读取入库队列失败")
	}
	minutes := 1
	if s.app.discovery.PollInterval > 0 {
		if n := int(s.app.discovery.PollInterval / time.Minute); n > 1 {
			minutes = n
		}
	}
	byPlatform := map[string]discovery.Binding{}
	for _, binding := range bindings {
		byPlatform[binding.Platform] = binding
	}
	page := web.FavoritesPage{
		Enabled: s.app.discovery.Enabled, PollMinutes: minutes, Notice: notice,
		Platforms: make([]web.FavoritePlatform, 0, 3),
		Queue:     []web.FavoriteQueueCount{},
		Failed:    []web.FavoriteJob{},
		Pending:   []web.FavoriteJob{},
	}
	for _, platform := range []string{"netease", "qq", "youtube"} {
		item := web.FavoritePlatform{Platform: platform, Label: aichat.PlatformLabel(platform), State: "未绑定"}
		if binding, ok := byPlatform[platform]; ok {
			item.Bound = true
			item.Paused = binding.Paused
			item.Account = maskedDiscoveryAccount(binding.Account)
			item.Error = clipStudioText(binding.Error, 60)
			item.State = favoritePlatformState(s.app.discovery.Enabled, binding.Paused)
		}
		page.Platforms = append(page.Platforms, item)
	}
	counts := map[string]int{}
	for _, job := range jobs {
		counts[job.State]++
		view := web.FavoriteJob{
			ID: job.ID, Platform: job.Track.Platform, TrackID: job.Track.ID,
			Title: job.Track.Title, Artist: job.Track.Artist, State: job.State,
			Label: discovery.StateLabel(job.State), Error: clipStudioText(job.Error, 180),
		}
		switch job.State {
		case discovery.Failed:
			if len(page.Failed) < 20 {
				page.Failed = append(page.Failed, view)
			}
		case discovery.NeedsConfirmation:
			if len(page.Pending) < 20 {
				page.Pending = append(page.Pending, view)
			}
		}
	}
	for _, state := range []string{discovery.Queued, discovery.Running, discovery.PendingUpload, discovery.NeedsConfirmation, discovery.Failed} {
		if counts[state] == 0 {
			continue
		}
		label := discovery.StateLabel(state)
		if state == discovery.PendingUpload {
			label = "待上传"
		}
		page.Queue = append(page.Queue, web.FavoriteQueueCount{State: state, Label: label, Count: counts[state]})
	}
	return page, nil
}

func favoritePlatformState(enabled, paused bool) string {
	switch {
	case !enabled && paused:
		return "已暂停 · 自动同步未启用"
	case !enabled:
		return "已绑定 · 自动同步未启用"
	case paused:
		return "自动同步已暂停"
	default:
		return "自动同步已开启"
	}
}

func clipStudioText(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if limit > 0 && len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return value
}
