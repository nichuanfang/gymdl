package cron

import (
	"context"
	"testing"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/nichuanfang/gymdl/config"
)

func TestJobsReportsDisabledWhenSchedulerIsNotRunning(t *testing.T) {
	setActive(nil)
	enabled, jobs := Jobs()
	if enabled || len(jobs) != 0 {
		t.Fatalf("disabled scheduler = enabled %v jobs %v", enabled, jobs)
	}
	if err := RunNow("syncCookieCloud"); err == nil {
		t.Fatal("run now succeeded while cron is off")
	}
}

func TestTrashCleanupAppearsAsScheduledJobOnly(t *testing.T) {
	for _, configured := range []bool{false, true} {
		var cleanup func(context.Context) error
		if configured {
			cleanup = func(context.Context) error {
				t.Error("cleanup should never run during this check")
				return nil
			}
		}
		scheduler := InitScheduler(&config.Config{}, cleanup)
		_, jobs := Jobs()
		var found bool
		for _, job := range jobs {
			if job.Name == emptyTrashJobName {
				found = true
				if job.Schedule != "每 30 分钟" || job.Label != "清空回收站" {
					t.Errorf("unexpected cleanup job: %#v", job)
				}
			}
		}
		if found != configured {
			t.Errorf("cleanup registration = %t, configured = %t", found, configured)
		}
		if err := RunNow(emptyTrashJobName); err == nil {
			t.Error("Web job endpoint accepted an immediate destructive run")
		}
		setActive(nil)
		if err := scheduler.Shutdown(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJobsListsNamedTaskWithoutChangingItsFunction(t *testing.T) {
	ran := make(chan struct{}, 1)
	scheduler, err := gocron.NewScheduler()
	if err != nil {
		t.Fatal(err)
	}
	_, err = scheduler.NewJob(
		gocron.DurationJob(time.Hour),
		gocron.NewTask(func() { ran <- struct{}{} }),
		gocron.WithName("syncCookieCloud"),
		gocron.WithTags("每 10 分钟"),
	)
	if err != nil {
		t.Fatal(err)
	}
	setActive(scheduler)
	t.Cleanup(func() {
		setActive(nil)
		_ = scheduler.Shutdown()
	})

	enabled, jobs := Jobs()
	if !enabled || len(jobs) != 1 || jobs[0].Name != "syncCookieCloud" || jobs[0].Label != "同步 CookieCloud" || jobs[0].Schedule != "每 10 分钟" {
		t.Fatalf("jobs = %#v", jobs)
	}
	if err := RunNow("missing"); err == nil {
		t.Fatal("unknown job was accepted")
	}
	scheduler.Start()
	if err := RunNow("syncCookieCloud"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("registered task did not run")
	}
}
