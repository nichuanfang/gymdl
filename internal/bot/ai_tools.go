package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/aichat"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/gin/task"
	"github.com/nichuanfang/gymdl/internal/musicdedupe"
	"github.com/nichuanfang/gymdl/utils"
)

// aiTools builds the whitelist exposed to the model. Write actions are
// deliberately absent: downloads still require an explicit button press.
func (app *BotApp) aiTools() []aichat.Tool {
	tools := []aichat.Tool{
		{
			Name: "search_music",
			Description: "按具体歌名搜索单曲，返回真实链接。推荐流程必须先自己决定歌名，再逐首调用这里；" +
				"可以用 titles 一次查多个歌名，比反复调用更快。不要用“雨天通勤”这类场景描述直接搜，否则只会拿到歌单合集。合集和杂音结果会单独返回，不能当作单曲推荐。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"keyword": map[string]any{"type": "string", "description": "单个具体歌名，或 歌名 艺人；不要传场景词"},
					"titles": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "一次查询多个具体歌名（最多 5 个），与 keyword 二选一",
					},
					"platform": map[string]any{
						"type": "string", "description": "netease/qq/youtube/bilibili，逗号分隔，留空为自动",
					},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
				},
			},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				const (
					maxQueries    = 5
					maxSingles    = 8
					maxOthers     = 3
					maxToolResult = 6000
				)
				queries := make([]string, 0, maxQueries)
				if keyword, _ := args["keyword"].(string); strings.TrimSpace(keyword) != "" {
					queries = append(queries, keyword)
				}
				if list, ok := args["titles"].([]any); ok {
					for _, entry := range list {
						if title, ok := entry.(string); ok && strings.TrimSpace(title) != "" {
							queries = append(queries, title)
						}
					}
				}
				if len(queries) == 0 {
					return `{"error":"需要 keyword 或 titles"}`, nil
				}
				if len(queries) > maxQueries {
					queries = queries[:maxQueries]
				}
				platforms := splitPlatforms(args["platform"])
				limit := intArg(args["limit"], 6)
				if limit < 1 {
					limit = 6
				}
				if limit > maxSingles {
					limit = maxSingles
				}

				var b strings.Builder
				collected := 0
				rejected := make([]string, 0, len(queries))
				for _, query := range queries {
					if web.IsSceneKeyword(query) {
						rejected = append(rejected, query)
						continue
					}
					if len(queries) > 1 {
						b.WriteString("\n## " + query + "\n")
					}
					singles, others, err := web.SearchSinglesForAgent(ctx, query, platforms, limit)
					if err != nil {
						// One failing lookup must not discard the others; the
						// model can still answer with what succeeded.
						b.WriteString("[检索失败] " + err.Error() + "\n")
						continue
					}
					remaining := maxSingles - collected
					if remaining <= 0 {
						b.WriteString("[预算] 已达到本轮单曲结果上限，停止继续检索。\n")
						break
					}
					if len(singles) > remaining {
						singles = singles[:remaining]
					}
					for _, hit := range singles {
						item := hit.SearchResultItem
						collected++
						// The URL stays in the process for the download buttons;
						// the model does not need to echo it, which saves tokens
						// and removes an easy way to invent a link.
						b.WriteString(fmt.Sprintf("[单曲] %s — %s | %s | %ds | vip=%t\n",
							item.Name, item.Artists, aichat.PlatformLabel(item.Platform), item.DurationSec, item.IsVIP))
						if collector := aichat.CollectorFrom(ctx); collector != nil {
							collector.Add([]aichat.SearchResult{{
								Platform: item.Platform, SongID: item.SongID, SongMID: item.SongMID,
								Name: item.Name, Artists: item.Artists, Album: item.Album,
								DurationSec: item.DurationSec, URL: item.URL, IsVIP: item.IsVIP,
							}})
						}
					}
					if len(others) > maxOthers {
						others = others[:maxOthers]
					}
					for _, hit := range others {
						item := hit.SearchResultItem
						b.WriteString(fmt.Sprintf("[%s，不可作为单曲下载] %s — %s | %s\n",
							hit.Kind, item.Name, item.Artists, aichat.PlatformLabel(item.Platform)))
					}
				}
				if len(rejected) > 0 {
					b.WriteString("[已拒绝] " + strings.Join(rejected, "、") +
						" 是场景描述而不是歌名。请先自己决定具体歌名再查。\n")
				}
				if collected == 0 {
					if len(rejected) > 0 {
						return fmt.Sprintf(`{"results":[],"rejected":%q,"hint":"这些是场景描述而不是歌名；先自己决定具体歌名（可用收听画像里的艺人）再查"}`,
							strings.Join(rejected, "、")), nil
					}
					return `{"results":[],"hint":"没有搜到单曲；先自己给出具体歌名再查，不要用场景词反复搜"}`, nil
				}
				return truncateToolResult(strings.TrimSpace(b.String()), maxToolResult), nil
			},
		},
		{
			Name:        "list_active_downloads",
			Description: "查看当前正在进行的下载任务（WebUI 队列和本机器人正在处理的任务）。",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			Run: func(_ context.Context, _ map[string]any) (string, error) {
				lines := make([]string, 0, 4)
				if tm := web.GetTaskManager(); tm != nil {
					for _, t := range tm.GetActiveTasks() {
						lines = append(lines, fmt.Sprintf("%s | %s | %s | %s", t.ID, t.Platform, t.Status, t.Progress))
					}
				}
				// Telegram downloads never enter the WebUI task manager, so
				// report the bot's own in-flight reservations as well. Without
				// this the tool always answered "没有任务" while a download was
				// visibly running in the chat.
				app.pendingMu.Lock()
				app.purgeExpiredPendingLocked(time.Now())
				active := len(app.activeMusicKeys)
				pending := len(app.pendingMusic)
				app.pendingMu.Unlock()
				if pending > 0 {
					lines = append(lines, fmt.Sprintf("telegram | 等待确认 | 待确认重复下载 %d 个", pending))
				}
				if active > 0 {
					lines = append(lines, fmt.Sprintf("telegram | 下载中 | 进行中任务 %d 个", active))
				}
				if len(lines) == 0 {
					return `{"tasks":[]}`, nil
				}
				return strings.Join(lines, "\n"), nil
			},
		},
		{
			Name:        "library_stats",
			Description: "统计本地曲库：已登记下载的曲目数量、艺人数量、平台分布。回答“我有多少歌曲”时使用。",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			Run: func(ctx context.Context, _ map[string]any) (string, error) {
				if app.musicDedupe == nil {
					return `{"available":false,"reason":"查重数据库不可用"}`, nil
				}
				stats, err := app.musicDedupe.Stats(ctx)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf(`{"available":true,"tracks":%d,"artists":%d,"platforms":%s}`,
					stats.Tracks, stats.Artists, stats.Platforms), nil
			},
		},
		{
			Name:        "library_lookup",
			Description: "按歌名和艺人查询本地曲库是否已有这首歌。回答“我有没有这首歌”时使用，不能用统计代替。",
			Parameters: map[string]any{
				"type":     "object",
				"required": []string{"title"},
				"properties": map[string]any{
					"title":  map[string]any{"type": "string", "description": "歌名"},
					"artist": map[string]any{"type": "string", "description": "艺人，可选"},
				},
			},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				if app.musicDedupe == nil {
					return `{"available":false}`, nil
				}
				title, _ := args["title"].(string)
				artist, _ := args["artist"].(string)
				if strings.TrimSpace(title) == "" {
					return "", fmt.Errorf("歌名不能为空")
				}
				match, err := app.musicDedupe.FindByTitle(ctx, title, artist)
				if err != nil {
					return "", err
				}
				if match == nil {
					return `{"owned":false}`, nil
				}
				return fmt.Sprintf(`{"owned":true,"title":%q,"artist":%q,"platform":%q}`,
					match.Title, match.Artist, match.Platform), nil
			},
		},
		{
			Name:        "library_search",
			Description: "按艺人或歌名片段列出本地曲库里的歌。回答“某艺人我有哪些”“库里有没有名字里带某词的歌”时使用。语言、年代、曲风筛选不了。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"artist": map[string]any{"type": "string", "description": "艺人，可只给名字的一部分"},
					"title":  map[string]any{"type": "string", "description": "歌名片段，可选"},
					"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
				},
			},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				if app.musicDedupe == nil {
					return `{"available":false,"reason":"查重数据库不可用"}`, nil
				}
				artist, _ := args["artist"].(string)
				title, _ := args["title"].(string)
				if strings.TrimSpace(artist) == "" && strings.TrimSpace(title) == "" {
					return `{"error":"需要艺人或歌名"}`, nil
				}
				limit := intArg(args["limit"], 8)
				matches, err := app.musicDedupe.Search(ctx, musicdedupe.LibraryQuery{Artist: artist, Title: title, Limit: limit})
				if err != nil {
					return "", err
				}
				if len(matches) == 0 {
					return `{"available":true,"count":0,"items":[]}`, nil
				}
				items := make([]string, 0, len(matches))
				for _, match := range matches {
					items = append(items, fmt.Sprintf(`{"title":%q,"artist":%q,"album":%q,"platform":%q}`,
						match.Title, match.Artist, match.Album, match.Platform))
				}
				return fmt.Sprintf(`{"available":true,"count":%d,"items":[%s]}`, len(matches), strings.Join(items, ",")), nil
			},
		},
		{
			Name:        "get_lyrics",
			Description: "按歌名查询歌词，可附带艺人和专辑。用户只给了一句词、没有歌名时不要调用，先问歌名。不要凭记忆补全歌词。",
			Parameters: map[string]any{
				"type":     "object",
				"required": []string{"title"},
				"properties": map[string]any{
					"title":  map[string]any{"type": "string", "description": "歌名"},
					"artist": map[string]any{"type": "string", "description": "艺人，可选"},
					"album":  map[string]any{"type": "string", "description": "专辑，可选"},
				},
			},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				title, _ := args["title"].(string)
				artist, _ := args["artist"].(string)
				album, _ := args["album"].(string)
				if strings.TrimSpace(title) == "" {
					return `{"error":"歌名不能为空"}`, nil
				}
				if core.GlobalLrcAPI == nil {
					return `{"available":false,"reason":"歌词服务未配置"}`, nil
				}
				lyrics, err := core.GlobalLrcAPI.GetLyrics(strings.TrimSpace(title), strings.TrimSpace(artist), strings.TrimSpace(album))
				if err != nil {
					return fmt.Sprintf(`{"available":true,"found":false,"reason":%q}`, utils.TruncateString(err.Error(), 200)), nil
				}
				return fmt.Sprintf(`{"available":true,"found":true,"title":%q,"artist":%q,"lyrics":%q}`,
					title, artist, lyrics), nil
			},
		},
		{
			Name:        "recent_downloads",
			Description: "查看最近的下载任务记录，包括失败任务和错误原因。用户问某次下载为什么失败、最近下了什么时使用。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status":   map[string]any{"type": "string", "description": "可选：completed/failed/cancelled"},
					"platform": map[string]any{"type": "string", "description": "可选：netease/qq/youtube/bilibili 等"},
					"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
				},
			},
			Run: func(_ context.Context, args map[string]any) (string, error) {
				tm := web.GetTaskManager()
				if tm == nil {
					return `{"available":false,"reason":"下载历史不可用"}`, nil
				}
				limit := intArg(args["limit"], 5)
				if limit < 1 {
					limit = 5
				}
				if limit > 20 {
					limit = 20
				}
				status, _ := args["status"].(string)
				platform, _ := args["platform"].(string)
				items, total := tm.GetFilteredHistory(0, limit, task.HistoryFilter{
					Status: strings.TrimSpace(status), Platform: strings.TrimSpace(platform),
				})
				lines := make([]string, 0, len(items))
				for _, item := range items {
					line := fmt.Sprintf("%s | %s | %s | %s", item.UpdatedAt.Format("2006-01-02 15:04"), item.Platform, item.Status, item.Progress)
					if item.Error != "" {
						line += " | " + utils.TruncateString(item.Error, 160)
					}
					if len(item.SongInfo) > 0 && item.SongInfo[0] != nil {
						line += " | " + item.SongInfo[0].SongName + " — " + item.SongInfo[0].SongArtists
					}
					lines = append(lines, line)
				}
				if len(lines) == 0 {
					return fmt.Sprintf(`{"available":true,"total":%d,"items":[]}`, total), nil
				}
				return fmt.Sprintf(`{"available":true,"total":%d,"items":%q}`, total, lines), nil
			},
		},
		{
			Name:        "pending_uploads",
			Description: "查看 WebDAV 待上传队列。用户问为什么还没入库、NAS 恢复没有时使用。",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			Run: func(_ context.Context, _ map[string]any) (string, error) {
				if core.GlobalWebDAV == nil || core.GlobalWebDAV.Outbox == nil {
					return `{"available":false,"reason":"WebDAV 未启用"}`, nil
				}
				items, err := core.GlobalWebDAV.Outbox.List()
				if err != nil {
					return `{"available":false,"reason":"读取待上传队列失败"}`, nil
				}
				lines := make([]string, 0, len(items))
				for _, item := range items {
					line := fmt.Sprintf("%s | %s/%s | 尝试 %d 次", item.CreatedAt.Format("01-02 15:04"), item.RemoteDir, item.FileName, item.Attempts)
					if item.LastError != "" {
						line += " | " + utils.TruncateString(item.LastError, 120)
					}
					lines = append(lines, line)
				}
				return fmt.Sprintf(`{"available":true,"count":%d,"items":%q}`, len(lines), lines), nil
			},
		},
		{
			Name:        "favorite_status",
			Description: "查看当前用户的收藏同步：各平台是否绑定、脱敏账号、上次同步、同步异常，以及入库队列和入库异常数量。只读，不会触发同步、暂停或重试。",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			Run: func(ctx context.Context, _ map[string]any) (string, error) {
				return app.favoriteStatusTool(ctx), nil
			},
		},
		{
			Name:        "rediscover_preview",
			Description: "预览曲库里缺少的听过或收藏歌曲。平台只能是 netease、qq、youtube。只返回歌名和艺人，不会入库，也不会记成已展示。",
			Parameters: map[string]any{
				"type":     "object",
				"required": []string{"platform"},
				"properties": map[string]any{
					"platform": map[string]any{"type": "string", "description": "netease、qq 或 youtube"},
					"limit":    map[string]any{"type": "integer", "description": "兼容旧参数；每次仍只预览一首"},
				},
			},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				return app.rediscoverPreviewTool(ctx, args), nil
			},
		},
		{
			Name:        "playlist_catalog",
			Description: "查看已同步歌单的名称，以及说明是否已写、说明摘录。最多 20 条。只读，不能改说明，也不能列出歌单曲目。",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			Run: func(ctx context.Context, _ map[string]any) (string, error) {
				return app.playlistCatalogTool(ctx), nil
			},
		},
	}
	return tools
}

func (app *BotApp) favoriteStatusTool(ctx context.Context) string {
	scope, ok := aichat.TurnScopeFrom(ctx)
	if !ok {
		return toolJSON(map[string]any{"available": false, "reason": "无法确认当前用户"})
	}
	if app == nil || app.discovery == nil || app.discovery.Store == nil {
		return toolJSON(map[string]any{"available": false, "reason": "收藏同步不可用"})
	}
	bindings, err := app.discovery.Store.Bindings(ctx, scope.UserID)
	if err != nil {
		return toolJSON(map[string]any{"available": false, "reason": "读取收藏状态失败"})
	}
	counts, err := app.discovery.Store.JobCounts(ctx, scope.UserID)
	if err != nil {
		return toolJSON(map[string]any{"available": false, "reason": "读取入库队列失败"})
	}
	byPlatform := map[string]discovery.Binding{}
	for _, binding := range bindings {
		if binding.User != scope.UserID {
			continue
		}
		byPlatform[binding.Platform] = binding
	}
	rows := make([]favoriteStatusBinding, 0, 3)
	for _, platform := range []string{"netease", "qq", "youtube"} {
		row := favoriteStatusBinding{Platform: aichat.PlatformLabel(platform), State: "未绑定"}
		binding, bound := byPlatform[platform]
		if bound {
			row.State = favoriteCardState(app.discovery.Enabled, binding.PausedByUser)
			if account := maskedDiscoveryAccount(binding.Account); account != "" {
				row.Account = account
			}
			if binding.LastSync > 0 {
				row.LastSync = time.Unix(binding.LastSync, 0).Format("01-02 15:04")
			}
			if reason := favoritePlatformError(platform, binding.Error); reason != "" {
				row.LastError = utils.TruncateString(reason, 80)
			}
		}
		rows = append(rows, row)
	}
	queue := make([]favoriteStatusCount, 0, 5)
	for _, state := range []string{discovery.Queued, discovery.Running, discovery.PendingUpload, discovery.NeedsConfirmation, discovery.Failed} {
		if counts[state] == 0 {
			continue
		}
		label := discovery.StateLabel(state)
		if state == discovery.PendingUpload {
			label = "待上传"
		}
		if state == discovery.Failed {
			label = "入库异常"
		}
		queue = append(queue, favoriteStatusCount{State: state, Label: label, Count: counts[state]})
	}
	payload := favoriteStatusPayload{Available: true, AutoSync: app.discovery.Enabled, Bindings: rows, Queue: queue}
	if len(queue) == 0 {
		payload.QueueNote = "暂无待处理任务"
	}
	return toolJSON(payload)
}

func (app *BotApp) rediscoverPreviewTool(ctx context.Context, args map[string]any) string {
	scope, ok := aichat.TurnScopeFrom(ctx)
	if !ok {
		return toolJSON(map[string]any{"available": false, "reason": "无法确认当前用户"})
	}
	platform, platformOK := rediscoverPlatformArg(args["platform"])
	if !platformOK {
		return toolJSON(map[string]any{"available": false, "reason": "平台只能是 netease、qq 或 youtube"})
	}
	limit, limitOK := rediscoverLimitArg(args)
	if !limitOK {
		return toolJSON(map[string]any{"available": false, "reason": "请重新发起找回"})
	}
	if app == nil || app.discovery == nil {
		return toolJSON(map[string]any{"available": false, "reason": "遗珠预览不可用"})
	}
	batch, err := app.discovery.Rediscover(ctx, scope.UserID, platform, limit)
	if err != nil {
		return toolJSON(map[string]any{"available": false, "reason": utils.TruncateString(err.Error(), 120)})
	}
	songs := make([]rediscoverPreviewSong, 0, len(batch.Tracks))
	for _, track := range batch.Tracks {
		title := strings.TrimSpace(track.Title)
		artist := strings.TrimSpace(track.Artist)
		if title == "" && artist == "" {
			continue
		}
		songs = append(songs, rediscoverPreviewSong{Title: title, Artist: artist})
	}
	payload := rediscoverPreviewPayload{
		Available: true,
		Platform:  aichat.PlatformLabel(platform),
		Requested: limit,
		Count:     len(songs),
		Tracks:    songs,
	}
	payload.Note = strings.TrimSpace(rediscoverShortfallLine(discovery.Card{
		SkippedOwned: batch.SkippedOwned, Unconfirmed: batch.Unconfirmed,
		SkippedShown: batch.SkippedShown, SkippedPending: batch.SkippedPending,
		SkippedOther: batch.SkippedOther, Unavailable: batch.Unavailable,
		Requested: limit, Tracks: batch.Tracks,
	}))
	if payload.Note == "" && len(songs) == 0 {
		payload.Note = rediscoverEmptyCopy(platform, batch)
	}
	return toolJSON(payload)
}

func (app *BotApp) playlistCatalogTool(ctx context.Context) string {
	if app == nil || app.playlistCatalog == nil {
		return toolJSON(map[string]any{"available": false, "reason": "歌单目录不可用"})
	}
	synced, err := app.playlistCatalog.Synced(ctx)
	if err != nil {
		return toolJSON(map[string]any{"available": false, "reason": "读取歌单目录失败"})
	}
	if !synced {
		return toolJSON(playlistCatalogPayload{Available: true, Synced: false, Reason: "歌单目录还没同步"})
	}
	rows, err := app.playlistCatalog.Active(ctx)
	if err != nil {
		return toolJSON(map[string]any{"available": false, "reason": "读取歌单目录失败"})
	}
	const maxPlaylists = 20
	truncated := len(rows) > maxPlaylists
	if truncated {
		rows = rows[:maxPlaylists]
	}
	items := make([]playlistCatalogEntry, 0, len(rows))
	for _, row := range rows {
		item := playlistCatalogEntry{Name: row.Name, HasDescription: strings.TrimSpace(row.Desc) != ""}
		if item.HasDescription {
			item.Excerpt = utils.TruncateString(strings.TrimSpace(row.Desc), 80)
		}
		items = append(items, item)
	}
	payload := playlistCatalogPayload{Available: true, Synced: true, Count: len(items), Truncated: truncated, Playlists: items}
	if truncated {
		payload.Note = "只返回了前 20 张，不是全部。"
	}
	return toolJSON(payload)
}

type favoriteStatusPayload struct {
	Available bool                    `json:"available"`
	AutoSync  bool                    `json:"auto_sync"`
	Bindings  []favoriteStatusBinding `json:"bindings"`
	Queue     []favoriteStatusCount   `json:"queue,omitempty"`
	QueueNote string                  `json:"queue_note,omitempty"`
}

type favoriteStatusBinding struct {
	Platform  string `json:"platform"`
	State     string `json:"state"`
	Account   string `json:"account,omitempty"`
	LastSync  string `json:"last_sync,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

type favoriteStatusCount struct {
	State string `json:"state"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type rediscoverPreviewPayload struct {
	Available bool                    `json:"available"`
	Platform  string                  `json:"platform,omitempty"`
	Requested int                     `json:"requested,omitempty"`
	Count     int                     `json:"count"`
	Tracks    []rediscoverPreviewSong `json:"tracks"`
	Note      string                  `json:"note,omitempty"`
}

type rediscoverPreviewSong struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

type playlistCatalogPayload struct {
	Available bool                   `json:"available"`
	Synced    bool                   `json:"synced"`
	Reason    string                 `json:"reason,omitempty"`
	Count     int                    `json:"count,omitempty"`
	Truncated bool                   `json:"truncated,omitempty"`
	Note      string                 `json:"note,omitempty"`
	Playlists []playlistCatalogEntry `json:"playlists,omitempty"`
}

type playlistCatalogEntry struct {
	Name           string `json:"name"`
	HasDescription bool   `json:"has_description"`
	Excerpt        string `json:"excerpt,omitempty"`
}

func rediscoverPlatformArg(value any) (string, bool) {
	platform, _ := value.(string)
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "netease", "qq", "youtube":
		return strings.ToLower(strings.TrimSpace(platform)), true
	default:
		return "", false
	}
}

func rediscoverLimitArg(args map[string]any) (int, bool) {
	if args == nil || args["limit"] == nil {
		return 1, true
	}
	limit := intArg(args["limit"], 0)
	switch limit {
	case 1:
		return limit, true
	default:
		return 0, false
	}
}

func toolJSON(payload any) string {
	data, err := json.Marshal(payload)
	if err != nil {
		return `{"available":false,"reason":"结果无法编码"}`
	}
	return string(data)
}

// truncateToolResult bounds what a tool returns to the model. The marker tells
// the model the data is incomplete so it narrows the next query instead of
// treating a truncated list as exhaustive.
func truncateToolResult(result string, maxBytes int) string {
	if maxBytes <= 0 || len(result) <= maxBytes {
		return result
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(result[cut]) {
		cut--
	}
	return result[:cut] + "\n[结果已截断，请减少一次查询的歌名数量]"
}

func (app *BotApp) ownedChecker(ctx context.Context, title, artist, url string) (bool, error) {
	if app.musicDedupe == nil {
		return false, nil
	}
	identity := musicdedupe.Track{Title: title, Artist: artist, SourceURL: url}
	match, err := app.musicDedupe.FindDuplicate(ctx, identity)
	if err != nil {
		return false, err
	}
	return match != nil, nil
}

func splitPlatforms(value any) []string {
	text, _ := value.(string)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	parts := strings.Split(text, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		switch p {
		case "netease", "qq", "youtube", "bilibili":
			out = append(out, p)
		}
	}
	return out
}

func intArg(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case string:
		if n, err := strconv.Atoi(typed); err == nil {
			return n
		}
	}
	return fallback
}
