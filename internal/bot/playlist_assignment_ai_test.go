package bot

import (
	"context"
	"errors"
	"strings"
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

func TestPlaylistSongSearchQueryIncludesBoundedLibrarySource(t *testing.T) {
	song := unplaylistSong{
		Title:   "One Long Moment",
		Artist:  "YON",
		Album:   "Moment",
		Comment: "https://music.163.com/#/song?id=12345",
	}
	query := playlistSongSearchQuery(song)
	for _, part := range []string{"One Long Moment", "YON", "Moment", "music.163.com", "id=12345"} {
		if !strings.Contains(query, part) {
			t.Errorf("WebSearch query %q does not include source detail %q", query, part)
		}
	}
	if got := truncatePlaylistSearchReference(strings.Repeat("x", 300)); len([]rune(got)) != 240 {
		t.Fatalf("source reference length = %d, want 240 runes", len([]rune(got)))
	}
}

func TestPlaylistWebSearchToolAcceptsToolNameAliases(t *testing.T) {
	for _, name := range []string{"mcp_webtool_websearch", "mcp_exa_web_search"} {
		tool := aichat.Tool{
			Name:        name,
			Description: "Search the web for current information",
			Parameters:  map[string]any{"properties": map[string]any{"query": map[string]any{"type": "string"}}},
			Run:         func(context.Context, map[string]any) (string, error) { return "results", nil },
		}
		if _, _, ok := playlistWebSearchTool([]aichat.Tool{tool}); !ok {
			t.Errorf("WebSearch alias %q was not discovered", name)
		}
	}
}
