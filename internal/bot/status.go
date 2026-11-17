package bot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/gin/task"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

type serviceStatus struct {
	Active         []string
	LastFailure    string
	PendingUploads int
	PendingKnown   bool
	CookieKnown    bool
	CookieExists   bool
	CookieModTime  time.Time
}

// StatusCommand reports download, upload and cookie state without calling the model.
func (app *BotApp) StatusCommand(c tb.Context) error {
	return c.Send(formatServiceStatus(app.collectServiceStatus(time.Now())))
}

func (app *BotApp) collectServiceStatus(now time.Time) serviceStatus {
	status := serviceStatus{}
	if tm := web.GetTaskManager(); tm != nil {
		for _, item := range tm.GetActiveTasks() {
			if item == nil {
				continue
			}
			line := strings.TrimSpace(item.Platform + " " + item.Progress)
			if line == "" {
				line = string(item.Status)
			}
			status.Active = append(status.Active, line)
		}
		failed, _ := tm.GetFilteredHistory(0, 1, task.HistoryFilter{Status: string(task.TaskStatusFailed)})
		if len(failed) > 0 && failed[0] != nil {
			status.LastFailure = formatFailedTask(failed[0])
		}
	}
	app.pendingMu.Lock()
	app.purgeExpiredPendingLocked(now)
	active := len(app.activeMusicKeys)
	pending := len(app.pendingMusic)
	app.pendingMu.Unlock()
	if active > 0 {
		status.Active = append(status.Active, fmt.Sprintf("机器人正在下载 %d 个", active))
	}
	if pending > 0 {
		status.Active = append(status.Active, fmt.Sprintf("等待确认重复 %d 个", pending))
	}
	if core.GlobalWebDAV != nil && core.GlobalWebDAV.Outbox != nil {
		if items, err := core.GlobalWebDAV.Outbox.List(); err == nil {
			status.PendingKnown = true
			status.PendingUploads = len(items)
		}
	}
	if app.cfg != nil && app.cfg.CookieCloud != nil && app.cfg.CookieCloud.CookieFilePath != "" && app.cfg.CookieCloud.CookieFile != "" {
		status.CookieKnown = true
		info, err := os.Stat(filepath.Join(app.cfg.CookieCloud.CookieFilePath, app.cfg.CookieCloud.CookieFile))
		if err == nil {
			status.CookieExists = true
			status.CookieModTime = info.ModTime()
		}
	}
	return status
}

func formatFailedTask(item *task.Task) string {
	parts := make([]string, 0, 3)
	if platform := strings.TrimSpace(item.Platform); platform != "" {
		parts = append(parts, platform)
	}
	if len(item.SongInfo) > 0 && item.SongInfo[0] != nil {
		name := strings.TrimSpace(item.SongInfo[0].SongName)
		artist := strings.TrimSpace(item.SongInfo[0].SongArtists)
		switch {
		case name != "" && artist != "":
			parts = append(parts, name+" — "+artist)
		case name != "":
			parts = append(parts, name)
		}
	}
	if item.Error != "" {
		parts = append(parts, utils.TruncateString(item.Error, 80))
	} else if progress := strings.TrimSpace(item.Progress); progress != "" {
		parts = append(parts, progress)
	}
	if !item.UpdatedAt.IsZero() {
		parts = append(parts, item.UpdatedAt.Format("01-02 15:04"))
	}
	if len(parts) == 0 {
		return "有一次失败，没有更多细节"
	}
	return strings.Join(parts, " · ")
}

func formatServiceStatus(status serviceStatus) string {
	var text strings.Builder
	text.WriteString("📡 服务状态\n──────────────────\n")
	if len(status.Active) == 0 {
		text.WriteString("下载：空闲\n")
	} else {
		text.WriteString("下载：\n")
		shown := status.Active
		extra := 0
		if len(shown) > 5 {
			extra = len(shown) - 5
			shown = shown[:5]
		}
		for _, line := range shown {
			text.WriteString("· ")
			text.WriteString(strings.TrimSpace(line))
			text.WriteString("\n")
		}
		if extra > 0 {
			fmt.Fprintf(&text, "· 还有 %d 个\n", extra)
		}
	}
	if status.LastFailure == "" {
		text.WriteString("失败：最近没有失败记录\n")
	} else {
		text.WriteString("失败：")
		text.WriteString(status.LastFailure)
		text.WriteString("\n")
	}
	if !status.PendingKnown {
		text.WriteString("入库：WebDAV 未启用\n")
	} else if status.PendingUploads == 0 {
		text.WriteString("入库：没有待上传\n")
	} else {
		fmt.Fprintf(&text, "入库：待上传 %d 个\n", status.PendingUploads)
	}
	switch {
	case !status.CookieKnown:
		text.WriteString("Cookie：未配置")
	case !status.CookieExists:
		text.WriteString("Cookie：还没有文件，网易云、B 站、YouTube 下载会失败")
	default:
		text.WriteString("Cookie：更新于 ")
		text.WriteString(status.CookieModTime.Format("01-02 15:04"))
	}
	return text.String()
}
