package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/nichuanfang/gymdl/internal/aichat"
)

func TestRunPlaylistWebSearchRejectsEmptyAndErrorEnvelopes(t *testing.T) {
	for _, result := range []string{
		"",
		`{"is_error":true,"content":"search failed"}`,
		`{"error":"empty result"}`,
		`{"content":""}`,
	} {
		t.Run(result, func(t *testing.T) {
			tool := aichat.Tool{
				Name: "mcp_webtool_websearch",
				Parameters: map[string]any{"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				}},
				Run: func(context.Context, map[string]any) (string, error) { return result, nil },
			}
			if _, err := runPlaylistWebSearch(context.Background(), []aichat.Tool{tool}, unplaylistSong{Title: "Song", Artist: "Artist"}); err == nil {
				t.Fatal("WebSearch without usable evidence should fail")
			}
		})
	}
	tool := aichat.Tool{
		Name:       "mcp_webtool_websearch",
		Parameters: map[string]any{"properties": map[string]any{"query": map[string]any{"type": "string"}}},
		Run:        func(context.Context, map[string]any) (string, error) { return "search result text", nil },
	}
	if result, err := runPlaylistWebSearch(context.Background(), []aichat.Tool{tool}, unplaylistSong{Title: "Song", Artist: "Artist"}); err != nil || result != "search result text" {
		t.Fatalf("valid WebSearch evidence result=%q err=%v", result, err)
	}
	tool.Run = func(context.Context, map[string]any) (string, error) { return "", errors.New("failed") }
	if _, err := runPlaylistWebSearch(context.Background(), []aichat.Tool{tool}, unplaylistSong{Title: "Song", Artist: "Artist"}); err == nil {
		t.Fatal("WebSearch tool errors should be surfaced")
	}
}
