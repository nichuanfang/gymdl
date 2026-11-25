package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/processor/music"
	tb "gopkg.in/telebot.v4"
)

func TestDiscoveryCompletionSendsTheStandardLibraryButtons(t *testing.T) {
	var sent map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode Telegram payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42,"chat":{"id":10,"type":"private"}}}`)
	}))
	defer server.Close()
	bot, err := tb.NewBot(tb.Settings{Offline: true, Token: "1:test", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	app := &BotApp{bot: bot}
	err = app.sendDiscoveryIngested(discovery.Job{Chat: 10, User: 20,
		Track: discovery.Track{Title: "Song", Artist: "Artist", URL: "https://music.163.com/song?id=1"},
		File:  &discovery.IngestedFile{Path: "music/song.flac", Storage: "LOCAL", Extension: "flac", Bitrate: "900"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var markup tb.ReplyMarkup
	if err := json.Unmarshal([]byte(sent["reply_markup"]), &markup); err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, button := range markup.InlineKeyboard[0] {
		labels = append(labels, button.Text)
		parts := strings.Split(button.Data, "|")
		if len(parts) != 3 || parts[0] != "\f"+musicActionCallbackUnique {
			t.Fatalf("invalid Telegram callback: %q", button.Data)
		}
		action, ok := app.getMusicAction(parts[2])
		if !ok || action.chatID != 10 || action.userID != 20 || action.origin.ID != 42 || action.path != "music/song.flac" {
			t.Fatalf("button not bound to queue owner and stored file: %#v", action)
		}
	}
	if !reflect.DeepEqual(labels, []string{"▶️ 播放", "🗑 删除", "整理", "其他音源"}) || !strings.Contains(sent["text"], "900 kbps") {
		t.Fatalf("completion card buttons = %v, text = %q", labels, sent["text"])
	}
}

func TestTasteCompletionWithoutOriginalCardDoesNotSendASecondSongCard(t *testing.T) {
	requests := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42,"chat":{"id":10,"type":"private"}}}`)
	}))
	defer server.Close()
	bot, err := tb.NewBot(tb.Settings{Offline: true, Token: "1:test", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	app := &BotApp{bot: bot}
	err = app.sendDiscoveryIngested(discovery.Job{Chat: 10, User: 20,
		Track: discovery.Track{Title: "Song", Artist: "Artist", TasteRecommended: true, SourceCard: "expired-card"},
		File:  &discovery.IngestedFile{Path: "music/song.flac", Storage: "LOCAL", Extension: "flac"},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-requests:
		t.Fatalf("expired recommendation sent a standalone card: %s", path)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestIngestedMusicButtonsThroughTelebot(t *testing.T) {
	requests := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- payload.Text
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			fmt.Fprint(w, `{"ok":true,"result":true}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42,"chat":{"id":10,"type":"private"}}}`)
	}))
	defer server.Close()
	bot, err := tb.NewBot(tb.Settings{Offline: true, Synchronous: true, Token: "1:test", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	app := &BotApp{bot: bot}
	bot.Handle(&musicActionCallbackEndpoint, app.HandleMusicActionCallback)
	token, markup := app.musicActionMarkup(&music.SongInfo{MusicPath: "/music/song.flac", Tidy: "LOCAL"}, "")
	chat := &tb.Chat{ID: 10, Type: tb.ChatPrivate}
	message := &tb.Message{ID: 42, Chat: chat}
	app.setMusicActionMessages([]string{token}, message, markup, 10, chat.ID)

	click := func(button tb.InlineButton, user, messageID int64) {
		bot.ProcessUpdate(tb.Update{Callback: &tb.Callback{
			ID: "callback", Sender: &tb.User{ID: user}, Data: "\f" + button.Unique + "|" + button.Data,
			Message: &tb.Message{ID: int(messageID), Chat: chat},
		}})
	}
	click(markup.InlineKeyboard[0][2], 20, 42)
	click(markup.InlineKeyboard[0][2], 10, 41)
	click(markup.InlineKeyboard[0][2], 10, 42)
	click(markup.InlineKeyboard[0][3], 10, 42)
	close(requests)
	var texts []string
	for text := range requests {
		texts = append(texts, text)
	}
	want := []string{"只有发起入库的人可以操作这首歌", "请使用入库成功消息中的按钮",
		"正在打开歌单整理…", "歌单整理配置不可用。", "歌曲缺少标题，无法搜索音源。"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("Telegram feedback = %q, want %q", texts, want)
	}
}

func TestOtherMusicSourcesIncludeDifferentEditionsAndExcludeCurrentSource(t *testing.T) {
	action := pendingMusicAction{title: "东方之珠（Live）", artist: "罗大佑 / 合唱", sourceURL: "https://music.163.com/song?id=1"}
	items := []web.SearchResultItem{
		{Platform: "netease", Name: "东方之珠（Live）", Artists: "罗大佑", URL: "https://music.163.com/song?id=1"},
		{Platform: "netease", Name: "东方之珠", Artists: "罗大佑", URL: "https://music.163.com/song?id=2"},
		{Platform: "qq", Name: "東方之珠", Artists: "羅大佑", URL: "https://y.qq.com/n/ryqq/songDetail/abc"},
		{Platform: "qq", Name: "东方之珠", Artists: "未知歌手", URL: "https://y.qq.com/n/ryqq/songDetail/def"},
		{Platform: "bilibili", Name: "罗大佑 东方之珠 精选歌单", URL: "https://www.bilibili.com/video/BV1111"},
	}
	if got := otherMusicSourceMatches(action, items); !reflect.DeepEqual(got, items[1:3]) {
		t.Fatalf("alternate sources = %#v", got)
	}
}

func TestMusicActionMarkupRegistersLibraryActions(t *testing.T) {
	app := &BotApp{musicActions: make(map[string]pendingMusicAction)}
	song := &music.SongInfo{
		SongName:    "Test Song",
		SongArtists: "Test Artist",
		Tidy:        "WEBDAV",
		MusicPath:   "Test Artist/Test Album/Test Song.m4a",
	}

	token, markup := app.musicActionMarkup(song, "https://music.163.com/song?id=1")
	if token == "" || markup == nil {
		t.Fatal("expected an action token and inline keyboard")
	}
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 4 {
		t.Fatalf("expected all four library actions, got %#v", markup.InlineKeyboard)
	}
	for i, button := range markup.InlineKeyboard[0] {
		want := []string{"▶️ 播放", "🗑 删除", "整理", "其他音源"}[i]
		if button.Text != want {
			t.Errorf("button %d = %q, want %q", i, button.Text, want)
		}
		if callbackLength := 1 + len(button.Unique) + 1 + len(button.Data); callbackLength > 64 {
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
		token, markup := app.musicActionMarkup(song, "")
		if token != "" || markup != nil {
			t.Fatalf("expected no actions for incomplete storage data, got token=%q markup=%#v", token, markup)
		}
	}
}

func TestMusicActionButtonsAreBoundToOwnerAndOriginalChat(t *testing.T) {
	app := &BotApp{musicActions: make(map[string]pendingMusicAction)}
	token, _ := app.musicActionMarkup(&music.SongInfo{SongName: "Song", Tidy: "LOCAL", MusicPath: "/music/song.mp3"}, "")
	message := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 10}}
	app.setMusicActionMessages([]string{token}, message, nil, 20, 10)

	for name, test := range map[string]struct {
		userID int64
		chatID int64
	}{
		"different user": {userID: 99, chatID: 10},
		"different chat": {userID: 20, chatID: 11},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := &aiPlaylistInteractionTestContext{
				args: []string{"play", token}, sender: &tb.User{ID: test.userID},
				callback: &tb.Callback{Message: message}, chat: &tb.Chat{ID: test.chatID},
			}
			if err := app.HandleMusicActionCallback(ctx); err != nil {
				t.Fatalf("unauthorized callback handling returned an error: %v", err)
			}
			if ctx.response != 1 || ctx.edited != nil {
				t.Fatalf("unauthorized callback was not rejected before action: response=%d edited=%v", ctx.response, ctx.edited)
			}
		})
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
