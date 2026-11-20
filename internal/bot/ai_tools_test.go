package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/aichat"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/gin/task"
	"github.com/nichuanfang/gymdl/internal/playlistcatalog"
	_ "modernc.org/sqlite"
)

func toolByName(t *testing.T, tools []aichat.Tool, name string) aichat.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return aichat.Tool{}
}

func TestAgentToolsExposeReadOnlyCapabilities(t *testing.T) {
	app := &BotApp{}
	tools := app.aiTools()
	for _, name := range []string{"search_music", "get_lyrics", "recent_downloads", "pending_uploads", "favorite_status", "rediscover_preview", "playlist_catalog"} {
		toolByName(t, tools, name)
	}
}

func TestGetLyricsReportsMissingServiceWithoutPanicking(t *testing.T) {
	previous := core.GlobalLrcAPI
	core.GlobalLrcAPI = nil
	t.Cleanup(func() { core.GlobalLrcAPI = previous })

	tool := toolByName(t, (&BotApp{}).aiTools(), "get_lyrics")
	result, err := tool.Run(context.Background(), map[string]any{"title": "晴天", "artist": "周杰伦"})
	if err != nil {
		t.Fatalf("missing lyrics service must be a tool result, not an error: %v", err)
	}
	if !strings.Contains(result, `"available":false`) {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestGetLyricsReturnsLyricsFromService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"title": "晴天", "artist": "周杰伦", "lyrics": "[00:00]故事的小黄花"}})
	}))
	defer server.Close()
	previous := core.GlobalLrcAPI
	core.GlobalLrcAPI = &core.LrcAPI{Config: &config.LrcAPIConfig{Enable: true, LrcApiUrl: server.URL}}
	t.Cleanup(func() { core.GlobalLrcAPI = previous })

	tool := toolByName(t, (&BotApp{}).aiTools(), "get_lyrics")
	result, err := tool.Run(context.Background(), map[string]any{"title": "晴天", "artist": "周杰伦"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "故事的小黄花") || !strings.Contains(result, `"found":true`) {
		t.Fatalf("lyrics not returned: %s", result)
	}
}

func TestPendingUploadsReportsEmptyQueue(t *testing.T) {
	previous := core.GlobalWebDAV
	core.GlobalWebDAV = &core.WebDAV{Outbox: core.NewWebDAVOutbox(t.TempDir())}
	t.Cleanup(func() { core.GlobalWebDAV = previous })

	tool := toolByName(t, (&BotApp{}).aiTools(), "pending_uploads")
	result, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, `"count":0`) || !strings.Contains(result, `"items":[]`) {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestRecentDownloadsReportsHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.sqlite3")
	tm := task.NewTaskManagerWithDBPath(nil, dbPath, "")
	t.Cleanup(func() { _ = tm.Close() })
	web.SetTaskManager(tm)
	t.Cleanup(func() { web.SetTaskManager(nil) })

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixNano()
	_, err = db.Exec(`INSERT INTO download_tasks
		(id, url, platform, status, progress, error, song_info_json, created_at_ns, updated_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"t1", "https://example.test/song", "qq", "failed", "", "code=-462", "[]", now, now)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	tool := toolByName(t, (&BotApp{}).aiTools(), "recent_downloads")
	result, err := tool.Run(context.Background(), map[string]any{"status": "failed", "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "code=-462") || !strings.Contains(result, `"total":1`) {
		t.Fatalf("history not returned: %s", result)
	}
}

func TestFavoriteStatusMasksAccountAndStaysOnOneUser(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "discovery.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const rawAccount = "11234567890170"
	if _, _, err = store.Apply(ctx, 9, 9, discovery.Snapshot{
		Platform: "netease", Account: rawAccount, Complete: true,
		Tracks: []discovery.Track{{Platform: "netease", ID: "1", Title: "晴天", Artist: "周杰伦", Verified: true}},
	}, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Apply(ctx, 2, 2, discovery.Snapshot{
		Platform: "qq", Account: "998877665544", Complete: true,
		Tracks: []discovery.Track{{Platform: "qq", ID: "2", Title: "别人的歌", Artist: "乙", Verified: true}},
	}, true); err != nil {
		t.Fatal(err)
	}
	bindings, err := store.Bindings(ctx, 9)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings = %+v err=%v", bindings, err)
	}
	if err = store.Pause(ctx, 9, "netease", true); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SourceError(ctx, bindings[0], "netease：登录已过期", false); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Enqueue(ctx, 9, 9, discovery.Track{Platform: "netease", ID: "q1", Title: "排队歌", Artist: "甲", Verified: true}); err != nil {
		t.Fatal(err)
	}
	failedID, err := store.Enqueue(ctx, 9, 9, discovery.Track{Platform: "netease", ID: "q2", Title: "失败歌", Artist: "甲", Verified: true})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.Job(ctx, failedID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Finish(ctx, failed, discovery.Failed, "secret-job-error-token", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Enqueue(ctx, 2, 2, discovery.Track{Platform: "qq", ID: "q3", Title: "别人的队列", Artist: "乙", Verified: true}); err != nil {
		t.Fatal(err)
	}

	app := &BotApp{discovery: &discovery.Service{Store: store, Enabled: true}}
	tool := toolByName(t, app.aiTools(), "favorite_status")
	unowned, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unowned, "无法确认当前用户") || strings.Contains(unowned, rawAccount) || strings.Contains(unowned, "112…170") {
		t.Fatalf("missing scope leaked status: %s", unowned)
	}

	result, err := tool.Run(aichat.WithTurnScope(ctx, 9, 9), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{rawAccount, "998877665544", "998…544", "netease", "secret-job-error-token", "别人的歌"} {
		if strings.Contains(result, secret) {
			t.Fatalf("result contains %q: %s", secret, result)
		}
	}
	var payload favoriteStatusPayload
	if err = json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode %s: %v", result, err)
	}
	if !payload.Available || !payload.AutoSync || len(payload.Bindings) != 3 {
		t.Fatalf("payload = %+v", payload)
	}
	var netease favoriteStatusBinding
	for _, row := range payload.Bindings {
		if row.Platform == "网易云" {
			netease = row
		}
		if row.Platform == "QQ 音乐" && row.State != "未绑定" {
			t.Fatalf("other user's QQ binding leaked: %+v", row)
		}
	}
	if netease.Account != "112…170" || netease.State != "已暂停" || netease.LastError != "登录已过期" || netease.LastSync == "" {
		t.Fatalf("netease row = %+v", netease)
	}
	counts := map[string]int{}
	for _, item := range payload.Queue {
		counts[item.State] = item.Count
	}
	if counts[discovery.Queued] != 1 || counts[discovery.Failed] != 1 || len(payload.Queue) != 2 {
		t.Fatalf("queue = %+v", payload.Queue)
	}
}

func TestRediscoverPreviewRejectsBadInputAndDoesNotRecordShown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discovery.sqlite3")
	store, err := discovery.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := &BotApp{discovery: &discovery.Service{
		Store:    store,
		Provider: previewProvider{},
		Library:  previewLibrary{},
		History: func(context.Context) ([]discovery.Track, error) {
			return []discovery.Track{{
				Title: "晴天", Artist: "周杰伦", Verified: true,
				ID: "history-secret", URL: "https://music.163.com/song?id=999",
			}}, nil
		},
	}}
	tool := toolByName(t, app.aiTools(), "rediscover_preview")
	ctx := aichat.WithTurnScope(context.Background(), 9, 9)

	badPlatform, err := tool.Run(ctx, map[string]any{"platform": "bilibili", "limit": 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(badPlatform, "平台只能是 netease、qq 或 youtube") || strings.Contains(badPlatform, "晴天") {
		t.Fatalf("bad platform = %s", badPlatform)
	}
	badLimit, err := tool.Run(ctx, map[string]any{"platform": "netease", "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(badLimit, "每次只能找回 1 首") || strings.Contains(badLimit, "晴天") {
		t.Fatalf("bad limit = %s", badLimit)
	}
	if _, ok := rediscoverLimitArg(map[string]any{"limit": float64(4)}); ok {
		t.Fatal("limit 4 was accepted")
	}

	result, err := tool.Run(ctx, map[string]any{"platform": "Netease"})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"history-secret", "music.163.com", "http", "resolved-secret"} {
		if strings.Contains(result, secret) {
			t.Fatalf("preview leaked %q: %s", secret, result)
		}
	}
	var payload rediscoverPreviewPayload
	if err = json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode %s: %v", result, err)
	}
	if !payload.Available || payload.Requested != 1 || payload.Count != 1 || len(payload.Tracks) != 1 {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Tracks[0].Title != "晴天" || payload.Tracks[0].Artist != "周杰伦" {
		t.Fatalf("track = %+v", payload.Tracks[0])
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var cards, prefs, jobs int
	if err = db.QueryRow(`SELECT COUNT(*) FROM discovery_cards`).Scan(&cards); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM discovery_preferences`).Scan(&prefs); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM discovery_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if cards != 0 || prefs != 0 || jobs != 0 {
		t.Fatalf("preview wrote cards=%d prefs=%d jobs=%d", cards, prefs, jobs)
	}
}

func TestPlaylistCatalogTruncatesDescriptions(t *testing.T) {
	store, err := playlistcatalog.Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	long := strings.Repeat("夜", 100)
	if _, err = store.Apply(context.Background(), []playlistcatalog.Remote{
		{ID: "secretnight01", Name: "夜路", Comment: "内部备注不要出现"},
		{ID: "secretdawn02", Name: "清晨"},
	}, []config.PlaylistInfo{{Name: "夜路", Desc: long}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	tool := toolByName(t, (&BotApp{playlistCatalog: store}).aiTools(), "playlist_catalog")
	result, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{long, "secretnight01", "secretdawn02", "内部备注不要出现"} {
		if strings.Contains(result, secret) {
			t.Fatalf("catalog leaked %q: %s", secret, result)
		}
	}
	var payload playlistCatalogPayload
	if err = json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode %s: %v", result, err)
	}
	if !payload.Available || !payload.Synced || payload.Count != 2 {
		t.Fatalf("payload = %+v", payload)
	}
	found := false
	for _, item := range payload.Playlists {
		if item.Name != "夜路" {
			if item.Name == "清晨" && item.HasDescription {
				t.Fatalf("empty description marked written: %+v", item)
			}
			continue
		}
		found = true
		if !item.HasDescription || !strings.HasSuffix(item.Excerpt, "...") || strings.Contains(item.Excerpt, long) {
			t.Fatalf("excerpt = %q", item.Excerpt)
		}
	}
	if !found {
		t.Fatalf("夜路 missing: %+v", payload.Playlists)
	}
}

func TestPlaylistCatalogReportsAnUnsyncedStore(t *testing.T) {
	store, err := playlistcatalog.Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tool := toolByName(t, (&BotApp{playlistCatalog: store}).aiTools(), "playlist_catalog")
	result, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "歌单目录还没同步") || strings.Contains(result, `"playlists"`) {
		t.Fatalf("unsynced catalog = %s", result)
	}
}

func TestPlaylistCatalogCapsAtTwenty(t *testing.T) {
	store, err := playlistcatalog.Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	remotes := make([]playlistcatalog.Remote, 21)
	for i := range remotes {
		remotes[i] = playlistcatalog.Remote{ID: fmt.Sprintf("idlist%02d", i+1), Name: fmt.Sprintf("列表%02d", i+1)}
	}
	if _, err = store.Apply(context.Background(), remotes, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	tool := toolByName(t, (&BotApp{playlistCatalog: store}).aiTools(), "playlist_catalog")
	result, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, "idlist") {
		t.Fatalf("catalog leaked an id: %s", result)
	}
	var payload playlistCatalogPayload
	if err = json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Count != 20 || !payload.Truncated || len(payload.Playlists) != 20 {
		t.Fatalf("payload count=%d truncated=%v items=%d", payload.Count, payload.Truncated, len(payload.Playlists))
	}
}

type previewProvider struct{}

func (previewProvider) Fetch(context.Context, string) (discovery.Snapshot, error) {
	return discovery.Snapshot{}, nil
}
func (previewProvider) Resolve(context.Context, discovery.Track, string) (discovery.Track, error) {
	return discovery.Track{}, nil
}
func (previewProvider) ResolveOn(_ context.Context, track discovery.Track, platform string) (discovery.Track, error) {
	track.Platform = platform
	track.ID = "resolved-secret"
	track.URL = "https://music.163.com/song?id=999"
	track.Verified = true
	return track, nil
}
func (previewProvider) Enrich(context.Context, discovery.Track) (discovery.Track, bool, error) {
	return discovery.Track{}, false, nil
}

type previewLibrary struct{}

func (previewLibrary) Contains(context.Context, discovery.Track) (bool, error) {
	return false, nil
}

func TestTruncateToolResultKeepsUTF8Boundary(t *testing.T) {
	result := truncateToolResult(strings.Repeat("歌", 100), 31)
	if !strings.Contains(result, "[结果已截断") {
		t.Fatalf("truncation marker missing: %q", result)
	}
	if !utf8.ValidString(result) {
		t.Fatalf("truncation split a UTF-8 sequence: %q", result)
	}
}
