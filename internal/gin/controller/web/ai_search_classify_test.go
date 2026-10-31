package web

import "testing"

func TestClassifyAgentResultSeparatesSinglesFromCompilations(t *testing.T) {
	cases := []struct {
		name string
		item SearchResultItem
		want AgentResultKind
	}{
		{"single", SearchResultItem{Platform: "qq", Name: "Luv(sic) Part 3", DurationSec: 220}, AgentKindSingle},
		{"playlist", SearchResultItem{Platform: "bilibili", Name: "雨天歌单丨治愈", DurationSec: 300}, AgentKindPlaylist},
		{"mix", SearchResultItem{Platform: "bilibili", Name: "Nujabes 合集", DurationSec: 2400}, AgentKindPlaylist},
		{"noise", SearchResultItem{Platform: "bilibili", Name: "雨声白噪音", DurationSec: 3600}, AgentKindNoise},
		{"long-single", SearchResultItem{Platform: "bilibili", Name: "Luv(sic) Hexalogy 整轨", DurationSec: 1800}, AgentKindPlaylist},
		{"short-clip", SearchResultItem{Platform: "bilibili", Name: "片段", DurationSec: 20}, AgentKindUnknown},
		{"long-mix", SearchResultItem{Platform: "youtube", Name: "Late night jazz", DurationSec: 3600}, AgentKindMix},
		{"livestream", SearchResultItem{Platform: "bilibili", Name: "抖音直播录屏存档", DurationSec: 600}, AgentKindNoise},
		{"drama", SearchResultItem{Platform: "bilibili", Name: "影子小剧社：雨天的故事", DurationSec: 900}, AgentKindNoise},
		{"rain-frequency", SearchResultItem{Platform: "bilibili", Name: "432HZ的雨声助眠", DurationSec: 3600}, AgentKindNoise},
		{"driving-tips", SearchResultItem{Platform: "bilibili", Name: "雨天开车驾驶技巧", DurationSec: 600}, AgentKindNoise},
		{"stuffed-title", SearchResultItem{Platform: "bilibili", Name: "A丨B｜C|D", DurationSec: 200}, AgentKindPlaylist},
		{"uppercase-playlist", SearchResultItem{Platform: "bilibili", Name: "PLAYLIST || 雨天氛围歌单", DurationSec: 300}, AgentKindPlaylist},
	}
	for _, tc := range cases {
		if got := ClassifyAgentResult(tc.item); got != tc.want {
			t.Errorf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}

func TestClassifyAgentResultKeepsNormalBilibiliSong(t *testing.T) {
	item := SearchResultItem{Platform: "bilibili", Name: "Nujabes - Luv(sic) Part 3", DurationSec: 220}
	if got := ClassifyAgentResult(item); got != AgentKindSingle {
		t.Fatalf("regular bilibili song classified as %s", got)
	}
}

// TestIsSceneKeywordAcceptsRealTitles pins the fix for the tool refusing to
// search real songs whose title contains a scene word ("雨天", "下雨天").
func TestIsSceneKeywordAcceptsRealTitles(t *testing.T) {
	for _, keyword := range []string{
		"孙燕姿 雨天", "雨天", "南拳妈妈 下雨天", "下雨天", "雨下一整晚",
		"周杰伦 晴天", "放松", "深夜", "来点爵士", "Algorithm",
	} {
		if IsSceneKeyword(keyword) {
			t.Errorf("%q was rejected as a scene description", keyword)
		}
	}
}

// TestIsSceneKeywordRejectsPureScenes keeps the guard that stops scenario words
// from being sent to providers, where they only return compilations.
func TestIsSceneKeywordRejectsPureScenes(t *testing.T) {
	for _, keyword := range []string{
		"", "推荐通勤听的歌", "适合雨天通勤听的歌", "深夜放松",
		"想听点放松的", "下班路上来点歌", "工作时听的", "健身时听点什么",
		"睡前放松一下",
	} {
		if !IsSceneKeyword(keyword) {
			t.Errorf("%q should be treated as a scene description", keyword)
		}
	}
}
