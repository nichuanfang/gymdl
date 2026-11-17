// discovery-check performs a read-only connection/inventory check. It never
// opens the discovery SQLite store, binds accounts or schedules downloads.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/utils"
)

func main() {
	file := flag.String("c", "./config.yaml", "配置路径")
	platform := flag.String("platform", "all", "netease/qq/youtube/history/library/all")
	flag.Parse()
	raw, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "无法读取配置")
		os.Exit(1)
	}
	var cfg config.Config
	if yaml.Unmarshal(raw, &cfg) != nil {
		fmt.Fprintln(os.Stderr, "配置格式无效")
		os.Exit(1)
	}
	_ = utils.InitLogger(&config.LogConfig{Mode: 1, Level: 3})
	web.EnsureAgentRuntime(&cfg)
	names := []string{*platform}
	if *platform == "all" {
		names = []string{"history", "library", "netease", "qq", "youtube"}
	}
	providers := &discovery.Providers{Config: &cfg}
	failed := false
	for _, name := range names {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		report := map[string]any{"check": name, "read_only": true}
		var checkErr error
		switch name {
		case "history":
			if cfg.AI == nil {
				checkErr = fmt.Errorf("未配置收听历史")
				break
			}
			table := cfg.AI.HistoryTable
			if table == "" {
				table = "music_scrobble_records"
			}
			var tracks []discovery.Track
			tracks, checkErr = discovery.LoadHistory(ctx, cfg.AI.HistoryDSN, table)
			if checkErr == nil {
				report["tracks"] = len(tracks)
			}
		case "library":
			l := &discovery.NavidromeLibrary{Config: cfg.PlaylistOrganizer}
			_, checkErr = l.Contains(ctx, discovery.Track{Title: "__gymdl_connection_probe__", Artist: "__gymdl_connection_probe__"})
		case "netease", "qq", "youtube":
			var snap discovery.Snapshot
			snap, checkErr = providers.Fetch(ctx, name)
			if checkErr == nil {
				report["tracks"] = len(snap.Tracks)
				report["complete"] = snap.Complete
				report["account_identified"] = strings.TrimSpace(snap.Account) != ""
			}
		default:
			checkErr = fmt.Errorf("未知检查来源")
		}
		cancel()
		report["ok"] = checkErr == nil
		if checkErr != nil {
			failed = true
			report["error"] = checkErr.Error()
		}
		_ = json.NewEncoder(os.Stdout).Encode(report)
	}
	if failed {
		os.Exit(1)
	}
}
