package cron

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/utils"
	"go.uber.org/zap"
)

var logger *zap.Logger

// registerTasks 注册定时任务
func registerTasks(c *config.Config, scheduler gocron.Scheduler) {
	//请求池
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: false,
		},
	}
	// 执行一次依赖安装/更新
	newTask("installDependency", "启动时一次", scheduler, gocron.OneTimeJob(gocron.OneTimeJobStartImmediately()),
		gocron.NewTask(installDependency, c, client))

	// 注册依赖更新检测任务(6小时)
	newTask("updateDependency", "每 6 小时", scheduler, gocron.DurationJob(time.Hour*6), gocron.NewTask(updateDependency, c, client))

	if c.PlaylistOrganizer != nil && strings.TrimSpace(c.PlaylistOrganizer.NavidromeURL) != "" && strings.TrimSpace(c.PlaylistOrganizer.NavidromeUsername) != "" && c.PlaylistOrganizer.NavidromePassword != "" {
		newTask("syncPlaylists", "每天 04:10", scheduler, gocron.DailyJob(1, gocron.NewAtTimes(gocron.NewAtTime(4, 10, 0))), gocron.NewTask(syncPlaylists, c))
	}
	if musicRAGImportConfigured(c) {
		newTask("importMusicRAG", "启动时一次", scheduler, gocron.OneTimeJob(gocron.OneTimeJobStartImmediately()), gocron.NewTask(importMusicRAG, c))
	}
	if musicLibraryIndexConfigured(c) {
		newTask("indexMusicLibrary", "每天 04:20", scheduler, gocron.DailyJob(1, gocron.NewAtTimes(gocron.NewAtTime(4, 20, 0))), gocron.NewTask(indexMusicLibrary, c))
	}
	if scrobbleSyncConfigured(c) {
		newTask("syncScrobbles", "每天 04:05", scheduler, gocron.DailyJob(1, gocron.NewAtTimes(gocron.NewAtTime(4, 5, 0))), gocron.NewTask(syncScrobbles, c))
	}

	if c.CookieCloud != nil && c.CookieCloud.Mode == 1 {
		// 定时刷新

		// 注册cookiecloud同步任务(根据配置的时间)
		every := fmt.Sprintf("每 %d 分钟", c.CookieCloud.ExpireTime)
		newTask("syncCookieCloud", every, scheduler, gocron.DurationJob(time.Minute*time.Duration(c.CookieCloud.ExpireTime)),
			gocron.NewTask(syncCookieCloud, client))
	}
}

// InitScheduler 日志初始化
func InitScheduler(c *config.Config) gocron.Scheduler {
	logger = utils.Logger()
	var startTimes sync.Map

	newScheduler, err := gocron.NewScheduler(
		gocron.WithLocation(time.Local),
		gocron.WithGlobalJobOptions(
			gocron.WithEventListeners(
				gocron.BeforeJobRuns(func(jobID uuid.UUID, jobName string) {
					startTimes.Store(jobID, time.Now())
					logJobEvent("started", jobName, jobID, nil, nil)
				}),
				gocron.AfterJobRuns(func(jobID uuid.UUID, jobName string) {
					if v, ok := startTimes.LoadAndDelete(jobID); ok {
						start := v.(time.Time)
						logJobEvent("finished", jobName, jobID, &start, nil)
					} else {
						logJobEvent("finished", jobName, jobID, nil, nil)
					}
				}),
				gocron.AfterJobRunsWithError(func(jobID uuid.UUID, jobName string, err error) {
					if v, ok := startTimes.LoadAndDelete(jobID); ok {
						start := v.(time.Time)
						logJobEvent("failed", jobName, jobID, &start, err)
					} else {
						logJobEvent("failed", jobName, jobID, nil, err)
					}
				}),
			),
		),
	)
	if err != nil {
		logger.Fatal("Failed to initialize scheduler", zap.Error(err))
		return nil
	}

	registerTasks(c, newScheduler)
	setActive(newScheduler)
	return newScheduler
}

// newTask 日志任务创建信息
func newTask(jobName, schedule string, scheduler gocron.Scheduler, jobDefinition gocron.JobDefinition, task gocron.Task) {
	logger.Info("[Job Added]", zap.String("jobName", jobName), zap.String("schedule", schedule))
	_, _ = scheduler.NewJob(jobDefinition, task, gocron.WithName(jobName), gocron.WithTags(schedule))
}

// logJobEvent 日志任务执行信息
func logJobEvent(event, jobName string, jobID uuid.UUID, start *time.Time, err error) {
	name := jobName
	if idx := strings.LastIndex(jobName, "."); idx != -1 {
		name = jobName[idx+1:]
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	noteJobEvent(event, name, message)

	fields := []zap.Field{
		zap.String("job_name", name),
		zap.String("job_id", jobID.String()),
	}

	if start != nil {
		fields = append(fields, zap.String("duration", fmt.Sprintf("%vms", time.Since(*start).Milliseconds())))
	}
	if err != nil {
		fields = append(fields, zap.Error(err))
		logger.Error(fmt.Sprintf("[Job %s]", event), fields...)
	} else {
		logger.Info(fmt.Sprintf("[Job %s]", event), fields...)
	}
}
