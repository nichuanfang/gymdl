package aichat

import "testing"

func TestNormalizeTrackKeyCollapsesSameSong(t *testing.T) {
	got := normalizeTrackKey("Can't Hold Us")
	for _, name := range []string{
		"Can't Hold Us",
		"Can’t Hold Us",
		"Can't Hold Us (feat. Ray Dalton)",
		"《Can't Hold Us》",
		"Cant Hold Us - Remastered",
	} {
		if normalizeTrackKey(name) != got {
			t.Errorf("%q -> %q, want %q", name, normalizeTrackKey(name), got)
		}
	}
}

func TestNormalizeTrackKeyKeepsDifferentSongsApart(t *testing.T) {
	if normalizeTrackKey("雨天") == normalizeTrackKey("下雨天") {
		t.Fatal("different titles collapsed into one")
	}
}

// TestNormalizeTrackKeyKeepsWordsInsideTitles guards the production bug where
// the "live" marker matched inside "Alive" and truncated the key to "a", which
// also collapsed "Deliver" into "de" and made filterMentioned match almost any
// answer. Decorations must only be stripped as whole words or bracketed groups.
func TestNormalizeTrackKeyKeepsWordsInsideTitles(t *testing.T) {
	cases := map[string]string{
		"Alive":                    "alive",
		"Tonight Alive":            "tonight alive",
		"Deliver":                  "deliver",
		"Best Version":             "best",
		"Love Story (Live)":        "love story",
		"雨下一整晚 - 现场版":              "雨下一整晚",
		"Deliver- Remastered 2011": "deliver",
		"Luv(sic) Part 3":          "luv sic part 3",
	}
	for name, want := range cases {
		if got := normalizeTrackKey(name); got != want {
			t.Errorf("%q -> %q, want %q", name, got, want)
		}
	}
}

// TestFilterMentionedKeepsEveryNamedTrack reproduces the "recommended three
// songs but got one button" report.
func TestFilterMentionedKeepsEveryNamedTrack(t *testing.T) {
	results := []SearchResult{
		{Name: "Alive", Platform: "qq", SongID: "1"},
		{Name: "晴天", Platform: "netease", SongID: "2"},
		{Name: "Deliver", Platform: "qq", SongID: "3"},
	}
	answer := "给你三首：\n1. Alive — 某乐队\n2. 晴天 — 周杰伦\n3. Deliver — 某歌手"
	if got := filterMentioned(results, answer); len(got) != len(results) {
		t.Fatalf("kept %d of %d mentioned tracks: %#v", len(got), len(results), got)
	}
}

// TestFilterMentionedRequiresWholeWordForShortLatinTitles stops a one-letter
// title from matching inside an unrelated word.
func TestFilterMentionedRequiresWholeWordForShortLatinTitles(t *testing.T) {
	if mentionsTrack("推荐 Alive 和 Best", "A") {
		t.Fatal("one-letter title matched inside another word")
	}
	if !mentionsTrack("推荐 A 和 Best", "A") {
		t.Fatal("one-letter title must match a standalone word")
	}
}

// TestFilterMentionedIgnoresSceneWordsInProse reproduces a live smoke-test bug:
// the answer "适合傍晚通勤时听的歌" made the unrelated songs 《傍晚》 and 《通勤》
// into download buttons even though the model never presented them.
func TestFilterMentionedIgnoresSceneWordsInProse(t *testing.T) {
	results := []SearchResult{
		{Name: "傍晚", Platform: "qq", SongID: "1"},
		{Name: "通勤", Platform: "qq", SongID: "2"},
		{Name: "城市傍晚", Platform: "qq", SongID: "3"},
	}
	answer := "推荐两首适合傍晚通勤时听的歌：\n1. 城市傍晚 — 毛不易\n2. 遇见 — 孙燕姿"
	got := filterMentioned(results, answer)
	if len(got) != 1 || got[0].Name != "城市傍晚" {
		t.Fatalf("expected only the listed track, got %#v", got)
	}
}

// TestMentionsTrackAcceptsModelListStyles covers the shapes the model actually
// writes, including an inline "推荐 晴天（周杰伦）".
func TestMentionsTrackAcceptsModelListStyles(t *testing.T) {
	cases := []struct {
		answer string
		title  string
	}{
		{"推荐两首：\n1. 城市傍晚 — 毛不易\n2. 遇见 — 孙燕姿", "城市傍晚"},
		{"推荐两首适合傍晚通勤时听的歌：\n1. 城市傍晚 — 毛不易\n2. 遇见 — 孙燕姿", "遇见"},
		{"1. 晴天 — 周杰伦", "晴天"},
		{"- 《晴天》 周杰伦", "晴天"},
		{"《晴天》听起来不错", "晴天"},
		{"2) 晴天 - 周杰伦", "晴天"},
		{"推荐 晴天（周杰伦）", "晴天"},
		{"1. Alive — 某乐队", "Alive"},
		{"给你三首：\n1. Alive — 某乐队\n2. 晴天 — 周杰伦\n3. Deliver — 某歌手", "Deliver"},
		{"我理解你想要三首适合傍晚通勤时听的歌\n\n1. 傍晚 — 刘若英，温暖舒缓。\n2. 遇见 — 孙燕姿", "傍晚"},
	}
	for _, tc := range cases {
		if !mentionsTrack(tc.answer, tc.title) {
			t.Errorf("answer %q should mention %q", tc.answer, tc.title)
		}
	}
}

// TestMentionsTrackRejectsProseWordReuse guards the counterpart: a scene word
// inside the question must not promote a same-named song.
func TestMentionsTrackRejectsProseWordReuse(t *testing.T) {
	answer := "推荐两首适合傍晚通勤时听的歌：\n1. 城市傍晚 — 毛不易\n2. 遇见 — 孙燕姿"
	for _, title := range []string{"傍晚", "通勤"} {
		if mentionsTrack(answer, title) {
			t.Errorf("scene word %q was treated as a recommended track", title)
		}
	}
	if mentionsTrack("通勤路上听什么？推荐这几首。", "通勤") {
		t.Error("prose re-use of a scene word became a track mention")
	}
}

// TestFilterMentionedDropsNearMisses pins the counterpart of the fallback: when
// the model listed tracks but none of the collected hits match, the hits are
// failed-lookup noise and must not become download buttons.
func TestFilterMentionedDropsNearMisses(t *testing.T) {
	results := []SearchResult{{Name: "〔女性向音声〕驾校车内", Platform: "bilibili", SongID: "1"}}
	answer := "没有找到明确标注为“深夜放松”的歌曲。\n\n1. 《夜曲》 — 周杰伦\n2. 《匆匆那年》 — 王菲"
	if got := filterMentioned(results, answer); len(got) != 0 {
		t.Fatalf("near-miss hits became buttons: %#v", got)
	}
}

// TestFilterMentionedKeepsButtonsForPureProse keeps the case where the model
// answered in prose without naming tracks; the user still needs a way to act.
func TestFilterMentionedKeepsButtonsForPureProse(t *testing.T) {
	results := []SearchResult{{Name: "晴天", Platform: "qq", SongID: "1"}}
	if got := filterMentioned(results, "我不确定，你要不要换个说法试试？"); len(got) != 1 {
		t.Fatalf("prose answer lost its fallback buttons: %#v", got)
	}
}
