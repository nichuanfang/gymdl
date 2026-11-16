package bot

import (
	"strings"
	"testing"
	"time"

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
