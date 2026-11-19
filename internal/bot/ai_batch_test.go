package bot

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/internal/aichat"
	"github.com/nichuanfang/gymdl/internal/bot/dispatch"
	"github.com/nichuanfang/gymdl/internal/musicdedupe"
	"github.com/nichuanfang/gymdl/processor/music"
	tb "gopkg.in/telebot.v4"
)

func TestCandidateSongCardShowsOneTrack(t *testing.T) {
	batch := "2026-11-18T05:02:33.123456789Z"
	items := []aichat.Candidate{
		{Token: "a", BatchID: batch, Name: "第一首", Title: "第一首", Artist: "甲", Platform: "netease", URL: "https://music.163.com/song?id=1"},
		{Token: "b", BatchID: batch, Name: "A & B", Title: "A & B", Artist: "X <Y>", Platform: "youtube", URL: "https://music.youtube.com/watch?v=1&list=1"},
		{Token: "c", BatchID: batch, Name: "第三首", Title: "第三首", Artist: "丙", Platform: "qq", URL: "https://y.qq.com/n/ryqq/songDetail/3", Owned: true},
	}
	text, markup := candidateSongCard(items, 0, true)
	if strings.Contains(text, "A &amp; B") || strings.Contains(text, "第三首") || !strings.Contains(text, "1 / 3") {
		t.Fatalf("first song was not alone: %s", text)
	}
	if !strings.Contains(text, "🎵 <b>推荐 · 网易云</b>\n──────────────────\n1 / 3\n\n") || !strings.Contains(text, `<b><a href="https://music.163.com/song?id=1">第一首</a></b>`) {
		t.Fatalf("title was not the listen link: %s", text)
	}
	labels := buttonTexts(markup)
	for _, unwanted := range []string{"试听", "下载前 3 首", "全部下载", "1. 第一首", "上一首"} {
		if labels[unwanted] {
			t.Fatalf("old button still present: %s", unwanted)
		}
	}
	if !labels["下载"] || !labels["下一首"] || !labels["剩下的都下载"] || !labels["换一批"] {
		t.Fatalf("missing song actions: %#v", labels)
	}
	if got := buttonRowTexts(markup); len(got) != 4 || got[0][0] != "下载" || got[1][0] != "下一首" || got[2][0] != "剩下的都下载" || got[3][0] != "换一批" {
		t.Fatalf("button rows = %#v", got)
	}
	if markup.InlineKeyboard[0][0].Data != "download|a" || markup.InlineKeyboard[2][0].Data != "batch|"+batch+"|0" {
		t.Fatalf("callbacks = %s / %s", markup.InlineKeyboard[0][0].Data, markup.InlineKeyboard[2][0].Data)
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if len("\fai_candidate|"+button.Data) > 64 {
				t.Fatalf("callback too long: %s", button.Data)
			}
		}
	}
	second, secondMarkup := candidateSongCard(items, 1, true)
	if !strings.Contains(second, `<b><a href="https://music.youtube.com/watch?v=1&amp;list=1">A &amp; B</a></b>`) || !strings.Contains(second, "X &lt;Y&gt;") || strings.Contains(second, "第一首") {
		t.Fatalf("second song lost its link: %s", second)
	}
	if got := buttonRowTexts(secondMarkup); len(got) < 2 || len(got[1]) != 2 || got[1][0] != "上一首" || got[1][1] != "下一首" {
		t.Fatalf("middle pager = %#v", got)
	}
	owned, ownedMarkup := candidateSongCard(items, 2, true)
	if !strings.Contains(owned, "已入库") || buttonTexts(ownedMarkup)["下载"] || !buttonTexts(ownedMarkup)["上一首"] {
		t.Fatalf("owned song kept a download button: %s %#v", owned, buttonTexts(ownedMarkup))
	}
	stored := candidateModelOrder([]aichat.Candidate{items[2], items[1], items[0]})
	if stored[0].Token != "a" || stored[2].Token != "c" {
		t.Fatalf("model order = %#v", stored)
	}
}

func TestLatestCandidatesForBatchRejectsStaleButtons(t *testing.T) {
	items := []aichat.Candidate{
		{Token: "new-2", BatchID: "new"},
		{Token: "new-1", BatchID: "new"},
		{Token: "old-1", BatchID: "old"},
	}
	got, ok := latestCandidatesForBatch(items, "new")
	if !ok || len(got) != 2 || got[0].Token != "new-2" || got[1].Token != "new-1" {
		t.Fatalf("latest batch = %#v, ok=%v", got, ok)
	}
	if got, ok := latestCandidatesForBatch(items, "old"); ok || len(got) != 0 {
		t.Fatalf("stale batch was accepted: %#v", got)
	}
	if got, ok := latestCandidatesForBatch(items, "other-user-batch"); ok || len(got) != 0 {
		t.Fatalf("unknown batch was accepted: %#v", got)
	}
}

func TestCandidateMarkedOwnedOnlyAfterSuccessfulImportCallback(t *testing.T) {
	aiStore, err := aichat.OpenStore(filepath.Join(t.TempDir(), "ai.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer aiStore.Close()
	dedupeStore, err := musicdedupe.Open(filepath.Join(t.TempDir(), "dedupe.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer dedupeStore.Close()
	if err := aiStore.SaveCandidates(20, 10, []aichat.Candidate{{Token: "candidate-1", Name: "Test Song"}}); err != nil {
		t.Fatal(err)
	}

	app := &BotApp{aiStore: aiStore, musicDedupe: dedupeStore}
	ctx := &aiPlaylistInteractionTestContext{sender: &tb.User{ID: 20}, chat: &tb.Chat{ID: 10}}
	callbackCalled := false
	session := &dispatch.Session{
		Context:        ctx,
		User:           ctx.sender,
		Link:           "https://music.example/song",
		CandidateToken: "candidate-1",
		OnMusicDownloaded: func(songs []*music.SongInfo) {
			callbackCalled = len(songs) > 0
		},
	}
	app.attachDownloadRecorder(session, "test", musicdedupe.Track{Platform: "test"})

	session.OnMusicDownloaded(nil)
	candidate, err := aiStore.Candidate("candidate-1", 20, 10)
	if err != nil || candidate == nil || candidate.Owned {
		t.Fatalf("failed import must not mark candidate owned: candidate=%#v err=%v", candidate, err)
	}
	if callbackCalled {
		t.Fatal("original callback should not run for a failed empty import")
	}

	session.OnMusicDownloaded([]*music.SongInfo{{SongName: "Test Song", SongArtists: "Test Artist"}})
	candidate, err = aiStore.Candidate("candidate-1", 20, 10)
	if err != nil || candidate == nil || !candidate.Owned {
		t.Fatalf("successful import should mark candidate owned: candidate=%#v err=%v", candidate, err)
	}
	if !callbackCalled {
		t.Fatal("original import callback was not preserved")
	}
}
