package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/gin/task"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

type serviceStatus struct {
	Active             []string
	LastFailure        string
	PendingUploads     int
	PendingKnown       bool
	PendingUnavailable bool
	CookieKnown        bool
	CookieExists       bool
	CookieModTime      time.Time
	// Favorites owns a separate durable import queue. Its downloading/to-upload
	// rows still belong in /status instead of being hidden behind "空闲".
	DiscoveryRunning        int
	DiscoveryQueued         int
	DiscoveryPendingUpload  int
	DiscoveryPendingConfirm int
	DiscoveryFailed         int
}

// StatusCommand reports download, upload and cookie state without calling the model.
func (app *BotApp) StatusCommand(c tb.Context) error {
	var user int64
	if c.Sender() != nil {
		user = c.Sender().ID
	}
	return c.Send(formatServiceStatus(app.collectServiceStatus(time.Now(), user)))
}

func (app *BotApp) collectServiceStatus(now time.Time, user int64) serviceStatus {
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
	if app.discovery != nil && app.discovery.Store != nil {
		counts, err := app.discovery.Store.JobCounts(context.Background(), user)
		if err != nil {
			utils.WarnWithFormat("[Status] 读取收藏入库队列失败: %v", err)
		} else {
			status.DiscoveryRunning = counts[discovery.Running]
			status.DiscoveryQueued = counts[discovery.Queued]
			status.DiscoveryPendingUpload = counts[discovery.PendingUpload]
			status.DiscoveryFailed = counts[discovery.Failed]
		}
	}
	app.pendingMu.Lock()
	app.purgeExpiredPendingLocked(now)
	active := len(app.activeMusicKeys)
	pending := len(app.pendingMusic)
	pendingKeys := make(map[string]struct{}, pending)
	for _, item := range app.pendingMusic {
		if _, reserved := app.activeMusicKeys[item.reservationKey]; reserved && item.reservationKey != "" {
			pendingKeys[item.reservationKey] = struct{}{}
		}
	}
	active -= len(pendingKeys)
	app.pendingMu.Unlock()
	if active > 0 {
		status.Active = append(status.Active, fmt.Sprintf("机器人正在下载 %d 个", active))
	}
	if pending > 0 {
		status.Active = append(status.Active, fmt.Sprintf("等待确认重复 %d 个", pending))
	}
	var outbox []*core.PendingUpload
	if core.GlobalWebDAV != nil && core.GlobalWebDAV.Outbox != nil {
		if items, err := core.GlobalWebDAV.Outbox.List(); err == nil {
			status.PendingKnown = true
			status.PendingUploads = len(items)
			outbox = items
		} else {
			status.PendingUnavailable = true
		}
	}
	if status.DiscoveryPendingUpload > 0 && !status.PendingUnavailable && app.discovery != nil && app.discovery.Store != nil {
		jobs, err := app.discovery.Store.PendingJobs(context.Background(), user)
		if err != nil {
			utils.WarnWithFormat("[Status] 读取收藏待上传任务失败: %v", err)
			status.DiscoveryPendingConfirm = status.DiscoveryPendingUpload
		} else {
			status.DiscoveryPendingConfirm = pendingNeedsConfirmation(jobs, outbox)
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

// pendingNeedsConfirmation counts favorite jobs waiting on a library check that
// are not already represented by a WebDAV outbox file. Both queues legitimately
// contain the same song after a NAS outage; status should not count it twice.
func pendingNeedsConfirmation(jobs []discovery.Job, outbox []*core.PendingUpload) int {
	if len(jobs) == 0 {
		return 0
	}
	uploaded := map[string]bool{}
	for _, item := range outbox {
		if item == nil {
			continue
		}
		if key := statusTrackKey(item.Title, item.Artist); key != "" {
			uploaded[key] = true
		}
	}
	pending := 0
	for _, job := range jobs {
		if !uploaded[statusTrackKey(job.Track.Title, job.Track.Artist)] {
			pending++
		}
	}
	return pending
}

func statusTrackKey(title, artist string) string {
	title = discovery.Normalize(title)
	if title == "" {
		return ""
	}
	return title + "\x00" + discovery.Normalize(artist)
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
	if len(status.Active) == 0 && status.DiscoveryRunning == 0 && status.DiscoveryQueued == 0 {
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
		if status.DiscoveryRunning > 0 || status.DiscoveryQueued > 0 {
			text.WriteString("· 收藏入库：")
			if status.DiscoveryRunning > 0 {
				fmt.Fprintf(&text, "%d 个下载中", status.DiscoveryRunning)
			}
			if status.DiscoveryQueued > 0 {
				if status.DiscoveryRunning > 0 {
					text.WriteString("，")
				}
				fmt.Fprintf(&text, "%d 个排队", status.DiscoveryQueued)
			}
			text.WriteByte('\n')
		}
	}
	if status.LastFailure == "" && status.DiscoveryFailed == 0 {
		text.WriteString("失败：最近没有失败记录\n")
	} else if status.LastFailure != "" {
		text.WriteString("失败：")
		text.WriteString(status.LastFailure)
		text.WriteString("\n")
		if status.DiscoveryFailed > 0 {
			fmt.Fprintf(&text, "收藏失败：%d 个，发送 /favorites 查看\n", status.DiscoveryFailed)
		}
	} else {
		fmt.Fprintf(&text, "失败：收藏入库异常 %d 个，发送 /favorites 查看\n", status.DiscoveryFailed)
	}

	pending := make([]string, 0, 2)
	if status.PendingUnavailable {
		pending = append(pending, "WebDAV 待上传队列暂不可读")
		if status.DiscoveryPendingUpload > 0 {
			pending = append(pending, fmt.Sprintf("收藏待上传或曲库确认 %d 个", status.DiscoveryPendingUpload))
		}
	}
	if status.PendingKnown {
		if status.PendingUploads > 0 {
			pending = append(pending, fmt.Sprintf("WebDAV 待上传 %d 个", status.PendingUploads))
		}
	}
	if status.DiscoveryPendingConfirm > 0 {
		pending = append(pending, fmt.Sprintf("待曲库确认 %d 个", status.DiscoveryPendingConfirm))
	}
	if len(pending) > 0 {
		text.WriteString("入库：")
		text.WriteString(strings.Join(pending, "；"))
		text.WriteByte('\n')
	} else if !status.PendingKnown {
		text.WriteString("入库：WebDAV 未启用\n")
	} else {
		text.WriteString("入库：没有待上传\n")
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
