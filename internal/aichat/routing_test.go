package aichat

import "testing"

func TestRouteSkillsKeepsFavoriteWordOnLibraryQuery(t *testing.T) {
	mounted := []string{
		"music-library-query",
		"music-favorite-status",
		"music-rediscover",
		"music-playlist-lookup",
		"music-playlist-curation",
	}
	assertSkills(t, RouteSkills("我有没有这首", mounted), "music-library-query")
	assertSkills(t, RouteSkills("收藏", mounted), "music-library-query")
	assertSkills(t, RouteSkills("收藏同步上一轮", mounted), "music-library-query", "music-favorite-status")
	assertSkills(t, RouteSkills("遗珠", mounted), "music-rediscover")
	assertSkills(t, RouteSkills("歌单说明", mounted), "music-playlist-lookup")
	assertSkills(t, RouteSkills("整理歌单", mounted), "music-playlist-curation")
}

func assertSkills(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("skills = %v, want %v", got, want)
	}
	have := map[string]bool{}
	for _, name := range got {
		have[name] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Fatalf("skills = %v, want %v", got, want)
		}
	}
}
