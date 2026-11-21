package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/task"
	"github.com/nichuanfang/gymdl/processor/music"
)

func TestFormatServiceStatusKeepsTheQuietAndBusyShapes(t *testing.T) {
	quiet := formatServiceStatus(serviceStatus{PendingKnown: true})
	for _, want := range []string{"下载：空闲", "失败：最近没有失败记录", "入库：没有待上传", "Cookie：未配置"} {
		if !strings.Contains(quiet, want) {
			t.Errorf("quiet status missing %q:\n%s", want, quiet)
		}
	}
	busy := formatServiceStatus(serviceStatus{
		Active:         []string{"qq 下载中", "netease 整理中", "a", "b", "c", "d"},
		LastFailure:    formatFailedTask(&task.Task{Platform: "qq", Error: "musickey 过期", UpdatedAt: time.Date(2026, 11, 16, 7, 23, 0, 0, time.Local), SongInfo: []*music.SongInfo{{SongName: "晴天", SongArtists: "周杰伦"}}}),
		PendingKnown:   true,
		PendingUploads: 2,
		CookieKnown:    true,
		CookieExists:   true,
		CookieModTime:  time.Date(2026, 11, 16, 8, 1, 0, 0, time.Local),
	})
	for _, want := range []string{"· qq 下载中", "· 还有 1 个", "晴天 — 周杰伦", "待上传 2 个", "Cookie：更新于 11-16 08:01"} {
		if !strings.Contains(busy, want) {
			t.Errorf("busy status missing %q:\n%s", want, busy)
		}
	}
	if strings.Contains(busy, "http") || strings.Contains(busy, "/data/") {
		t.Fatalf("status leaked a url or path:\n%s", busy)
	}
}

func TestFormatServiceStatusIncludesTheFavoritesQueue(t *testing.T) {
	status := formatServiceStatus(serviceStatus{
		PendingKnown:            true,
		DiscoveryRunning:        1,
		DiscoveryQueued:         2,
		DiscoveryPendingConfirm: 1,
		DiscoveryFailed:         3,
	})
	for _, want := range []string{
		"下载：", "收藏入库：1 个下载中，2 个排队",
		"失败：收藏入库异常 3 个", "入库：待曲库确认 1 个",
	} {
		if !strings.Contains(status, want) {
			t.Errorf("favorites status missing %q:\n%s", want, status)
		}
	}
	if strings.Contains(status, "下载：空闲") || strings.Contains(status, "失败：最近没有失败记录") {
		t.Fatalf("favorites work was hidden behind an idle status:\n%s", status)
	}

	uploadOnly := formatServiceStatus(serviceStatus{
		PendingKnown:            true,
		PendingUploads:          2,
		DiscoveryPendingConfirm: 1,
	})
	if !strings.Contains(uploadOnly, "入库：WebDAV 待上传 2 个；待曲库确认 1 个") {
		t.Fatalf("both upload queues were not reported:\n%s", uploadOnly)
	}
}

func TestPendingNeedsConfirmationDoesNotDoubleCountOutbox(t *testing.T) {
	jobs := []discovery.Job{
		{Track: discovery.Track{Title: "冬天剧终（The End）", Artist: "马旭东"}},
		{Track: discovery.Track{Title: "另一首", Artist: "甲"}},
	}
	outbox := []*core.PendingUpload{{Title: "冬天剧终(The End)", Artist: "马旭东"}}
	if got := pendingNeedsConfirmation(jobs, outbox); got != 1 {
		t.Fatalf("pending without outbox = %d, want 1", got)
	}
	if got := pendingNeedsConfirmation(jobs, nil); got != 2 {
		t.Fatalf("pending without WebDAV = %d, want 2", got)
	}
}
