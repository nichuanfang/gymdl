package web

import (
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func TestInitialCronStatusSaysWhetherItIsScheduled(t *testing.T) {
	enabled := serviceByName(initialServiceStatuses(&config.Config{
		AdditionalConfig: &config.AdditionalConfig{EnableCron: true},
	}), "定时任务")
	if enabled.Name == "" || !enabled.Enabled || !enabled.Online || enabled.Detail != "按计划运行" {
		t.Fatalf("enabled cron status = %#v", enabled)
	}
	disabled := serviceByName(initialServiceStatuses(&config.Config{
		AdditionalConfig: &config.AdditionalConfig{EnableCron: false},
	}), "定时任务")
	if disabled.Enabled || disabled.Online || disabled.Detail != "未开启" {
		t.Fatalf("disabled cron status = %#v", disabled)
	}
}

func serviceByName(services []ServiceStatus, name string) ServiceStatus {
	for _, service := range services {
		if service.Name == name {
			return service
		}
	}
	return ServiceStatus{}
}
