// Command ai-smoke is a manual, opt-in check for the Telegram agent. It is not
// wired into production and prints only the grounded results it receives.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/aichat"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
)

func main() {
	cfgPath := flag.String("config", "", "path to a gymdl config.yaml")
	text := flag.String("text", "推荐两首适合傍晚通勤的歌", "user message")
	dbPath := flag.String("db", "data/ai-smoke.sqlite3", "smoke-test SQLite path")
	flag.Parse()
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "-config is required")
		os.Exit(2)
	}
	cfg := config.LoadConfig(*cfgPath)
	web.EnsureAgentRuntime(cfg)
	store, err := aichat.OpenStore(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		os.Exit(1)
	}
	defer store.Close()
	collector := aichat.NewCollector()
	tools := []aichat.Tool{{
		Name:        "search_music",
		Description: "搜索真实歌曲，返回可下载链接。推荐歌曲前必须调用。",
		Parameters: map[string]any{
			"type":     "object",
			"required": []string{"keyword"},
			"properties": map[string]any{
				"keyword":  map[string]any{"type": "string"},
				"platform": map[string]any{"type": "string"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			keyword, _ := args["keyword"].(string)
			rawPlatform, _ := args["platform"].(string)
			items, err := web.SearchForAgent(ctx, keyword, strings.Split(rawPlatform, ","), 5)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[smoke] search %q failed: %v\n", keyword, err)
				return "", err
			}
			fmt.Fprintf(os.Stderr, "[smoke] search %q -> %d items\n", keyword, len(items))
			lines := make([]string, 0, len(items))
			for _, item := range items {
				lines = append(lines, fmt.Sprintf("%s — %s | %s | %s", item.Name, item.Artists, item.Platform, item.URL))
				collector.Add([]aichat.SearchResult{{
					Platform: item.Platform, SongID: item.SongID, SongMID: item.SongMID,
					Name: item.Name, Artists: item.Artists, Album: item.Album,
					DurationSec: item.DurationSec, URL: item.URL, IsVIP: item.IsVIP,
				}})
			}
			return strings.Join(lines, "\n"), nil
		},
	}}
	engine, err := aichat.NewEngine(cfg.AI, store, aichat.HistorySource{}, nil, tools, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "engine:", err)
		os.Exit(1)
	}
	ctx := aichat.WithCollector(context.Background(), collector)
	out, err := engine.Respond(ctx, aichat.TurnInput{UserID: 1, ChatID: 1, Text: *text})
	if err != nil {
		fmt.Fprintln(os.Stderr, "respond:", err)
		os.Exit(1)
	}
	fmt.Println(out.Reply)
	fmt.Printf("\ntools=%s candidates=%d\n", out.ToolNote, len(out.Candidates))
	for _, c := range out.Candidates {
		fmt.Printf("- %s — %s | %s | %s\n", c.Name, c.Artist, c.Platform, c.URL)
	}
}
