package bot

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/gin/task"
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
	for _, name := range []string{"search_music", "get_lyrics", "recent_downloads", "pending_uploads"} {
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

func TestTruncateToolResultKeepsUTF8Boundary(t *testing.T) {
	result := truncateToolResult(strings.Repeat("歌", 100), 31)
	if !strings.Contains(result, "[结果已截断") {
		t.Fatalf("truncation marker missing: %q", result)
	}
	if !utf8.ValidString(result) {
		t.Fatalf("truncation split a UTF-8 sequence: %q", result)
	}
}
