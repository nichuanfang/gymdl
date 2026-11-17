package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
)

func TestWebPreviewRejectsIncompleteRequests(t *testing.T) {
	app := &BotApp{cfg: &config.Config{}}
	studio := webStudio{app: app}
	ctx := context.Background()
	cases := []web.AIPreviewRequest{
		{},
		{Name: "夜间", PlaylistID: "abc"},
		{Name: "夜间", Preset: "instrumental", Request: "安静"},
		{Name: "夜间", Request: ""},
	}
	for _, req := range cases {
		if _, err := studio.Preview(ctx, req); err == nil {
			t.Fatalf("request %#v should be rejected before any playlist run", req)
		}
	}
	if len(app.aiPlaylistRunning) != 0 {
		t.Fatal("rejected preview must not hold the playlist maintenance lock")
	}
}

func TestWebConfirmExpiredPreview(t *testing.T) {
	app := &BotApp{cfg: &config.Config{}}
	_, err := (webStudio{app: app}).Confirm(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "预览已过期") {
		t.Fatalf("expired confirm error = %v", err)
	}
}

func TestWebEnqueueSkipsDismissedTrack(t *testing.T) {
	store, dbPath := openWebStudioDiscovery(t)
	app := &BotApp{cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"42"}}}, discovery: &discovery.Service{Store: store}}
	studio := webStudio{app: app}
	ctx := context.Background()
	track := web.DiscoveryTrack{Platform: "netease", ID: "1", Title: "Wound Up", Artist: "Example", URL: "https://example.com/a", Verified: true}
	queued, skipped, err := studio.Enqueue(ctx, []web.DiscoveryTrack{track})
	if err != nil || queued != 1 || skipped != 0 {
		t.Fatalf("first enqueue queued=%d skipped=%d err=%v", queued, skipped, err)
	}
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	var raw string
	if err = rawDB.QueryRow(`SELECT track_json FROM discovery_jobs WHERE user_id=42`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored discovery.Track
	if err = json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.CardAction != "" {
		t.Fatalf("stored card action = %q", stored.CardAction)
	}
	if _, err = studio.Dismiss(ctx, []web.DiscoveryTrack{track}); err != nil {
		t.Fatal(err)
	}
	queued, skipped, err = studio.Enqueue(ctx, []web.DiscoveryTrack{track})
	if err != nil || queued != 0 || skipped != 1 {
		t.Fatalf("second enqueue queued=%d skipped=%d err=%v", queued, skipped, err)
	}
}

func TestWebStudioRequiresOwner(t *testing.T) {
	store, _ := openWebStudioDiscovery(t)
	app := &BotApp{cfg: &config.Config{}, discovery: &discovery.Service{Store: store}}
	if _, _, err := (webStudio{app: app}).Enqueue(context.Background(), []web.DiscoveryTrack{{Platform: "netease", ID: "1", Title: "A", Artist: "B", Verified: true}}); err == nil || !strings.Contains(err.Error(), "授权用户") {
		t.Fatalf("missing owner error = %v", err)
	}
}

func openWebStudioDiscovery(t *testing.T) (*discovery.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gymdl.sqlite3")
	store, err := discovery.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}
