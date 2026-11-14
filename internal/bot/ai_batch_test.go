package bot

import (
	"path/filepath"
	"testing"

	"github.com/nichuanfang/gymdl/internal/aichat"
	"github.com/nichuanfang/gymdl/internal/bot/dispatch"
	"github.com/nichuanfang/gymdl/internal/musicdedupe"
	"github.com/nichuanfang/gymdl/processor/music"
	tb "gopkg.in/telebot.v4"
)

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
