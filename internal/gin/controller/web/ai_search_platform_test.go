package web

import (
	"context"
	"testing"
)

func TestAgentPlatformsDefaultsToRandomNetEaseOrQQOnly(t *testing.T) {
	previous := agentPlatformPicker
	t.Cleanup(func() { agentPlatformPicker = previous })

	for _, picked := range []string{"netease", "qq"} {
		agentPlatformPicker = func() string { return picked }
		got := agentPlatforms(nil)
		if len(got) != 1 || got[0] != picked {
			t.Fatalf("agentPlatforms(nil) with picker %q = %#v", picked, got)
		}
	}

	agentPlatformPicker = func() string { t.Fatal("explicit platform must skip the random picker"); return "" }
	if got := agentPlatforms([]string{"youtube"}); len(got) != 1 || got[0] != "youtube" {
		t.Fatalf("explicit platform was replaced: %#v", got)
	}
}

func TestSearchForAgentWithoutPlatformQueriesOnlyThePickedSource(t *testing.T) {
	previousSearch := searchProviderSearch
	previousPicker := agentPlatformPicker
	t.Cleanup(func() {
		searchProviderSearch = previousSearch
		agentPlatformPicker = previousPicker
	})

	agentPlatformPicker = func() string { return "qq" }
	var calls []string
	searchProviderSearch = func(_ context.Context, platform, _ string, _, _ int) ([]SearchResultItem, error) {
		calls = append(calls, platform)
		return []SearchResultItem{{SongID: "1", Name: "Song - Artist", Platform: platform}}, nil
	}
	if _, err := SearchForAgent(context.Background(), "Song", nil, 3); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "qq" {
		t.Fatalf("platform-free search fans out: %#v", calls)
	}
}
