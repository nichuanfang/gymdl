package cron

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
)

// Job is one scheduled task shown in the Web console. It describes a job the
// process already registered. It does not create a new schedule.
type Job struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Schedule string `json:"schedule"`
	Running  bool   `json:"running"`
	LastRun  string `json:"last_run,omitempty"`
	NextRun  string `json:"next_run,omitempty"`
	LastErr  string `json:"last_error,omitempty"`
}

var (
	statusMu sync.Mutex
	active   gocron.Scheduler
	running  = map[string]bool{}
	lastErr  = map[string]string{}
)

func setActive(s gocron.Scheduler) {
	statusMu.Lock()
	active = s
	statusMu.Unlock()
}

func noteJobEvent(event, name, message string) {
	statusMu.Lock()
	defer statusMu.Unlock()
	switch event {
	case "started":
		running[name] = true
	case "finished":
		running[name] = false
		lastErr[name] = ""
	case "failed":
		running[name] = false
		lastErr[name] = message
	}
}

// Jobs lists the tasks registered with the running scheduler.
// Cron disabled means an empty list and enabled=false, not an error.
func Jobs() (enabled bool, jobs []Job) {
	statusMu.Lock()
	s := active
	statusMu.Unlock()
	if s == nil {
		return false, []Job{}
	}
	listed := s.Jobs()
	jobs = make([]Job, 0, len(listed))
	for _, item := range listed {
		name := item.Name()
		if name == "" {
			name = item.ID().String()
		}
		job := Job{Name: name, Label: jobLabel(name), Schedule: scheduleLabel(item.Tags())}
		statusMu.Lock()
		job.Running = running[name]
		job.LastErr = lastErr[name]
		statusMu.Unlock()
		if last, err := item.LastRun(); err == nil && !last.IsZero() {
			job.LastRun = last.Format(time.RFC3339)
		}
		if next, err := item.NextRun(); err == nil && !next.IsZero() {
			job.NextRun = next.Format(time.RFC3339)
		}
		jobs = append(jobs, job)
	}
	return true, jobs
}

// RunNow asks an already registered job to run once. The schedule is unchanged.
func RunNow(name string) error {
	statusMu.Lock()
	s := active
	statusMu.Unlock()
	if s == nil {
		return fmt.Errorf("定时任务没有启动")
	}
	for _, item := range s.Jobs() {
		if item.Name() == name {
			if err := item.RunNow(); err != nil {
				return fmt.Errorf("无法立即执行 %s", jobLabel(name))
			}
			return nil
		}
	}
	return fmt.Errorf("没有这个定时任务")
}

func jobLabel(name string) string {
	switch name {
	case "installDependency":
		return "安装依赖"
	case "updateDependency":
		return "检查依赖更新"
	case "syncCookieCloud":
		return "同步 CookieCloud"
	case "syncPlaylists":
		return "同步歌单"
	case "importMusicRAG":
		return "导入曲库状态"
	case "indexMusicLibrary":
		return "索引曲库"
	case "syncScrobbles":
		return "同步收听记录"
	default:
		return name
	}
}

func scheduleLabel(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return tags[0]
}
