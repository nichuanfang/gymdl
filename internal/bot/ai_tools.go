package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/aichat"
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
			Name:        "get_lyrics",
			Description: "按歌名和艺人查询歌词。用户问歌词、根据歌词找歌时使用；不要凭记忆补全歌词。",
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
	}
	return tools
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
