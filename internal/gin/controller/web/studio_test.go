package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/playlistcatalog"
)

func TestDiscoveryNoticeSkipsTheOwnedCountWhenSongsRemain(t *testing.T) {
	notice := discoveryNotice("netease", 3, DiscoveryBatch{Tracks: []DiscoveryTrack{{Title: "晴天"}}, SkippedOwned: 4, Unconfirmed: 2})
	if strings.Contains(notice, "已避开") || !strings.Contains(notice, "有 2 首对不上唯一版本，所以没有找满 3 首。") {
		t.Fatalf("notice = %q", notice)
	}
	empty := discoveryNotice("qq", 3, DiscoveryBatch{SkippedOwned: 2})
	if !strings.Contains(empty, "曲库已经有了") {
		t.Fatalf("empty notice = %q", empty)
	}
}

func TestPlaylistDescriptionsHideFunctionalPlaylists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "gymdl.sqlite3")
	store, err := playlistcatalog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Apply(context.Background(), []playlistcatalog.Remote{
		{ID: "trash", Name: "回收站"},
		{ID: "holder", Name: "歌单"},
		{ID: "ai", Name: "[AI] 夜间"},
		{ID: "zh", Name: "中文"},
	}, nil, time.Unix(1_700_000_000, 0))
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	SetPlaylistDBPath(path)
	t.Cleanup(func() { SetPlaylistDBPath("") })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/playlists", nil)
	HandlePlaylistDescriptions(ctx)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "中文") || strings.Contains(recorder.Body.String(), "回收站") || strings.Contains(recorder.Body.String(), "[AI]") {
		t.Fatalf("playlist list = %d %s", recorder.Code, recorder.Body.String())
	}

	body := bytes.NewBufferString(`{"desc":"` + strings.Repeat("字", 201) + `"}`)
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "zh"}}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/playlists/zh/desc", body)
	ctx.Request.Header.Set("Content-Type", "application/json")
	HandleUpdatePlaylistDescription(ctx)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "200") {
		t.Fatalf("long description = %d %s", recorder.Code, recorder.Body.String())
	}

	previous := GetWebConfig()
	SetWebConfig(&config.Config{})
	t.Cleanup(func() { SetWebConfig(previous) })
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/playlists/sync", bytes.NewBufferString(`{}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	HandleSyncPlaylists(ctx)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "尚未配置 Navidrome") {
		t.Fatalf("sync without organizer = %d %s", recorder.Code, recorder.Body.String())
	}
}
