package aichat

import (
	"context"
	"strings"
	"testing"
)

func TestMergeToolsKeepsFirstDefinitionAndSkipsInvalid(t *testing.T) {
	first := []Tool{{Name: "search_music", Run: func(context.Context, map[string]any) (string, error) { return "builtin", nil }}}
	second := []Tool{
		{Name: "search_music", Run: func(context.Context, map[string]any) (string, error) { return "remote", nil }},
		{Name: ""},
		{Name: "remote_only", Run: func(context.Context, map[string]any) (string, error) { return "ok", nil }},
	}
	merged := MergeTools(first, second)
	if len(merged) != 2 {
		t.Fatalf("merged = %d tools, want 2", len(merged))
	}
	result, _ := merged[0].Run(context.Background(), nil)
	if !strings.Contains(result, "builtin") {
		t.Fatalf("built-in tool must win name conflicts, got %q", result)
	}
}
