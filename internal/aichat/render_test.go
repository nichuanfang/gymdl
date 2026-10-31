package aichat

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderEscapesReplyAndAddsNoModeFooter(t *testing.T) {
	out := &TurnOutput{
		Reply: "推荐 <歌>",
		Candidates: []Candidate{{
			Name: "Song <A>", Artist: "Artist", Platform: "bilibili", URL: "https://example.test/x", Grounded: true,
		}},
	}
	text := Render(out, false, true)
	if !strings.Contains(text, "&lt;歌&gt;") || !strings.Contains(text, "&lt;A&gt;") {
		t.Fatalf("reply must be HTML escaped: %q", text)
	}
	if strings.Contains(text, "<blockquote>") || strings.Contains(text, "/mode") {
		t.Fatalf("reply must not carry a mode footer: %q", text)
	}
}

func TestRenderKeepsPlainReplyUnchanged(t *testing.T) {
	text := Render(&TurnOutput{Reply: "ok"}, false, false)
	if text != "ok" {
		t.Fatalf("plain reply changed: %q", text)
	}
}

func TestCollectorDeduplicatesProviderHits(t *testing.T) {
	c := NewCollector()
	c.Add([]SearchResult{{Platform: "qq", SongID: "1", Name: "A"}})
	c.Add([]SearchResult{{Platform: "qq", SongID: "1", Name: "A"}, {Platform: "qq", SongID: "2", Name: "B"}})
	if got := len(c.Results()); got != 2 {
		t.Fatalf("collector size = %d, want 2", got)
	}
}

// TestRenderCandidatesHonoursEngineLimit guards the mismatch where the text
// showed 10 tracks but the keyboard had every candidate. The engine already
// caps the list by ai.max_suggestions, so Render must not re-cap it.
func TestRenderCandidatesHonoursEngineLimit(t *testing.T) {
	candidates := make([]Candidate, 0, 14)
	for i := 0; i < 14; i++ {
		candidates = append(candidates, Candidate{Name: fmt.Sprintf("Song %d", i+1), Platform: "qq", URL: "u"})
	}
	text := Render(&TurnOutput{Reply: "ok", Candidates: candidates}, false, false)
	if !strings.Contains(text, "Song 14") {
		t.Fatal("render cut candidates the engine had already limited")
	}
}

// TestSplitMessageKeepsChunksWithinTelegramLimit pins the 4096-rune bound that
// used to leave the placeholder message stuck on "正在理解你的需求…".
func TestSplitMessageKeepsChunksWithinTelegramLimit(t *testing.T) {
	body := strings.Repeat("一首歌的说明文字，换行继续\n", 500)
	chunks := SplitMessage(body)
	if len(chunks) < 2 {
		t.Fatalf("long body was not split: %d chunk(s)", len(chunks))
	}
	for index, chunk := range chunks {
		if utf8.RuneCountInString(chunk) > telegramMessageLimit {
			t.Errorf("chunk %d has %d runes", index, utf8.RuneCountInString(chunk))
		}
	}
	if strings.Join(chunks, "\n") != strings.TrimRight(body, "\n") {
		t.Error("split lost content")
	}
	if got := SplitMessage("短消息"); len(got) != 1 || got[0] != "短消息" {
		t.Fatalf("short message changed: %#v", got)
	}
}
