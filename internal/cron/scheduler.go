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
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// 执行一次依赖安装/更新
	newTask("installDependency", scheduler, gocron.OneTimeJob(gocron.OneTimeJobStartImmediately()),
		gocron.NewTask(installDependency, c))

	// 注册依赖更新检测任务(6小时)
	newTask("updateDependency", scheduler, gocron.DurationJob(time.Hour*6), gocron.NewTask(updateDependency, c))

	if c.CookieCloud.Mode == 1 {
		// 定时刷新

		// 注册cookiecloud同步任务(根据配置的时间)
		newTask("syncCookieCloud", scheduler, gocron.DurationJob(time.Minute*time.Duration(c.CookieCloud.ExpireTime)),
			gocron.NewTask(syncCookieCloud))
	}

	//刷新cookie
	if c.QQMusicApiConfig.Enable {
		//启动时刷新一次musickey
		newTask("refreshMusicKey", scheduler, gocron.OneTimeJob(gocron.OneTimeJobStartImmediately()),
			gocron.NewTask(refreshMusicKey, c, client))
		//定时检测musickey是否失效
		newTask("syncCookieCloud", scheduler, gocron.DurationJob(time.Minute*time.Duration(c.QQMusicApiConfig.Interval)),
			gocron.NewTask(refreshMusicKey, c, client))
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
	return newScheduler
}

// newTask 日志任务创建信息
func newTask(jobName string, scheduler gocron.Scheduler, jobDefinition gocron.JobDefinition, task gocron.Task) {
	logger.Info("[Job Added]", zap.String("jobName", jobName))
	_, _ = scheduler.NewJob(jobDefinition, task)
}

// logJobEvent 日志任务执行信息
func logJobEvent(event, jobName string, jobID uuid.UUID, start *time.Time, err error) {
	name := jobName
	if idx := strings.LastIndex(jobName, "."); idx != -1 {
		name = jobName[idx+1:]
	}

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
