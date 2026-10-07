package bot

import (
	"testing"

	"github.com/nichuanfang/gymdl/processor/music"
	tb "gopkg.in/telebot.v4"
)

func TestMusicActionMarkupRegistersPlaybackAndDeleteActions(t *testing.T) {
	app := &BotApp{musicActions: make(map[string]pendingMusicAction)}
	song := &music.SongInfo{
		SongName:    "Test Song",
		SongArtists: "Test Artist",
		Tidy:        "WEBDAV",
		MusicPath:   "Test Artist/Test Album/Test Song.m4a",
	}

	token, markup := app.musicActionMarkup(song)
	if token == "" || markup == nil {
		t.Fatal("expected an action token and inline keyboard")
	}
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("expected one row with play and delete buttons, got %#v", markup.InlineKeyboard)
	}
	for _, button := range markup.InlineKeyboard[0] {
		if callbackLength := len(button.Unique) + 1 + len(button.Data); callbackLength > 64 {
			t.Errorf("callback payload is %d bytes; Telegram allows at most 64", callbackLength)
		}
	}
	for _, action := range []string{"confirm_delete", "cancel_delete"} {
		if callbackLength := len(musicActionCallbackUnique) + 1 + len(action) + 1 + len(token); callbackLength > 64 {
			t.Errorf("%s callback payload is %d bytes; Telegram allows at most 64", action, callbackLength)
		}
	}

	action, ok := app.getMusicAction(token)
	if !ok {
		t.Fatal("expected the callback token to resolve to a pending action")
	}
	if action.path != song.MusicPath || action.storage != "WEBDAV" {
		t.Fatalf("stored action does not match the song: %#v", action)
	}
}

func TestMusicActionMarkupRequiresStoredPathAndSupportedStorage(t *testing.T) {
	app := &BotApp{}
	for _, song := range []*music.SongInfo{
		{Tidy: "LOCAL"},
		{Tidy: "UNKNOWN", MusicPath: "song.m4a"},
	} {
		token, markup := app.musicActionMarkup(song)
		if token != "" || markup != nil {
			t.Fatalf("expected no actions for incomplete storage data, got token=%q markup=%#v", token, markup)
		}
	}
}

func TestRemoveMusicActionRowKeepsOtherSongs(t *testing.T) {
	markup := &tb.ReplyMarkup{}
	first := markup.Data("▶️ 播放", musicActionCallbackUnique, "play", "first-token")
	firstDelete := markup.Data("🗑 删除", musicActionCallbackUnique, "delete", "first-token")
	second := markup.Data("▶️ 播放", musicActionCallbackUnique, "play", "second-token")
	secondDelete := markup.Data("🗑 删除", musicActionCallbackUnique, "delete", "second-token")
	markup.Inline(markup.Row(first, firstDelete), markup.Row(second, secondDelete))

	updated := removeMusicActionRow(markup, "first-token")
	if len(updated.InlineKeyboard) != 1 || len(updated.InlineKeyboard[0]) != 2 {
		t.Fatalf("expected the second song's action row to remain, got %#v", updated.InlineKeyboard)
	}
	if got := updated.InlineKeyboard[0][0].Data; got != "play|second-token" {
		t.Fatalf("remaining play callback = %q, want %q", got, "play|second-token")
	}
}
