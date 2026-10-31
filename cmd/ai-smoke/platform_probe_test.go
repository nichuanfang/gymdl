package main

import (
	"context"
	"os"
	"testing"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
)

// TestPlatformProbe is a manual helper; run with GYMDL_SMOKE_CONFIG set to a
// real config path. It is skipped in normal test runs.
func TestPlatformProbe(t *testing.T) {
	path := os.Getenv("GYMDL_SMOKE_CONFIG")
	if path == "" {
		t.Skip("set GYMDL_SMOKE_CONFIG to probe providers")
	}
	cfg := config.LoadConfig(path)
	web.EnsureAgentRuntime(cfg)
	for _, platform := range []string{"qq", "netease", "bilibili"} {
		items, err := web.SearchForAgent(context.Background(), "孙燕姿 雨天", []string{platform}, 3)
		t.Logf("%s: n=%d err=%v", platform, len(items), err)
	}
}
