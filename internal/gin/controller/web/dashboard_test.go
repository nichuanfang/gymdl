package web

import (
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

func TestDashboardSnapshotDoesNotWaitForExternalHealthChecks(t *testing.T) {
	previous := GetWebConfig()
	SetWebConfig(&config.Config{
		WebConfig: &config.WebConfig{AppPort: 8080},
		Tidy:      &config.TidyConfig{Mode: 1, DistDir: "data/dist"},
		QQMusicApiConfig: &config.QQMusicApiConfig{
			Enable: true, Endpoint: "http://127.0.0.1:1",
		},
	})
	defer SetWebConfig(previous)
	statusCacheMu.Lock()
	statusCache = nil
	statusCheckedAt = time.Time{}
	statusRefreshing = false
	statusCacheMu.Unlock()

	start := time.Now()
	result := getSystemStatus(true)
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("dashboard response blocked on health checks: %s", elapsed)
	}
	if len(result.Services) == 0 || result.Metrics.TotalHistory != 0 {
		t.Fatalf("unexpected initial dashboard snapshot: %#v", result)
	}
}

func TestUncheckedDashboardHasNoCheckedAtTimestamp(t *testing.T) {
	if got := checkedAtOrNil(time.Time{}); got != nil {
		t.Fatalf("unchecked status should not expose a checked_at timestamp: %v", got)
	}
	now := time.Now()
	if got := checkedAtOrNil(now); got == nil || !got.Equal(now) {
		t.Fatalf("checked status should preserve its timestamp: got %v want %v", got, now)
	}
}
