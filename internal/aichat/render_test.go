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
	if !strings.Contains(text, "&lt;歌&gt;") || strings.Contains(text, "Song") {
		t.Fatalf("reply must stay escaped and leave the song list to its card: %q", text)
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

// TestRenderCandidatesHonoursEngineLimit keeps the full engine list available
// to the one-song card. The answer body itself no longer repeats that list.
func TestRenderCandidatesHonoursEngineLimit(t *testing.T) {
	candidates := make([]Candidate, 0, 14)
	for i := 0; i < 14; i++ {
		candidates = append(candidates, Candidate{Name: fmt.Sprintf("Song %d", i+1), Platform: "qq", URL: "u"})
	}
	text := RenderCandidates(candidates, 0)
	if !strings.Contains(text, "Song 14") {
		t.Fatal("candidate render cut tracks the engine had already limited")
	}
	answer := Render(&TurnOutput{Reply: "ok", Candidates: candidates}, false, false)
	if strings.Contains(answer, "Song 14") || strings.Contains(answer, "可下载曲目") {
		t.Fatalf("answer repeated the song list: %q", answer)
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

func TestSplitMessageKeepsHTMLBalancedAndEntitiesWhole(t *testing.T) {
	plain := strings.Repeat("歌曲 &amp; 艺人 ", 900)
	body := "<b>" + plain + "</b>"
	chunks := SplitMessage(body)
	if len(chunks) < 2 {
		t.Fatalf("long HTML body was not split: %d chunk(s)", len(chunks))
	}
	var reconstructed strings.Builder
	entityCount := 0
	for index, chunk := range chunks {
		if got := utf8.RuneCountInString(chunk); got > telegramMessageLimit {
			t.Errorf("chunk %d has %d runes", index, got)
		}
		if strings.Count(chunk, "<b>") != strings.Count(chunk, "</b>") || strings.Count(chunk, "<code>") != strings.Count(chunk, "</code>") {
			t.Errorf("chunk %d has unbalanced Telegram HTML: %q", index, chunk[:min(80, len(chunk))])
		}
		if strings.Count(chunk, "&") != strings.Count(chunk, "&amp;") {
			t.Errorf("chunk %d split an HTML entity", index)
		}
		entityCount += strings.Count(chunk, "&amp;")
		reconstructed.WriteString(strings.ReplaceAll(strings.ReplaceAll(chunk, "<b>", ""), "</b>", ""))
	}
	if reconstructed.String() != plain {
		t.Fatal("splitting changed the rendered text")
	}
	if entityCount != strings.Count(plain, "&amp;") {
		t.Fatalf("entity count = %d, want %d", entityCount, strings.Count(plain, "&amp;"))
	}
}
