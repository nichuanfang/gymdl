package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/playlistcatalog"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

type maintCall struct {
	method    string
	messageID string
	text      string
	markup    string
}

func maintTestBot(t *testing.T) (*tb.Bot, <-chan maintCall) {
	t.Helper()
	calls := make(chan maintCall, 64)
	var nextID atomic.Int64
	nextID.Store(200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Text      string `json:"text"`
			Markup    string `json:"reply_markup"`
			MessageID string `json:"message_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode Telegram request: %v", err)
		}
		method := filepath.Base(r.URL.Path)
		calls <- maintCall{method: method, messageID: payload.MessageID, text: payload.Text, markup: payload.Markup}
		w.Header().Set("Content-Type", "application/json")
		if method == "answerCallbackQuery" {
			fmt.Fprint(w, `{"ok":true,"result":true}`)
			return
		}
		if payload.MessageID != "" {
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%s,"chat":{"id":7,"type":"private"}}}`, payload.MessageID)
			return
		}
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":7,"type":"private"}}}`, nextID.Add(1))
	}))
	t.Cleanup(server.Close)
	api, err := tb.NewBot(tb.Settings{Offline: true, Synchronous: true, Token: "1:test", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return api, calls
}

func maintWait(t *testing.T, calls <-chan maintCall, method string, match func(maintCall) bool) maintCall {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case call := <-calls:
			if call.method == method && (match == nil || match(call)) {
				return call
			}
		case <-timer.C:
			t.Fatal("Telegram received no matching call")
			return maintCall{}
		}
	}
}

func maintTestApp(t *testing.T, api *tb.Bot) *BotApp {
	t.Helper()
	previous := logger
	logger = utils.Logger()
	t.Cleanup(func() { logger = previous })
	return &BotApp{bot: api, cfg: &config.Config{
		Tidy:     &config.TidyConfig{Mode: 1, DistDir: t.TempDir()},
		Telegram: &config.TelegramConfig{AllowedUsers: []string{"7"}},
	}}
}

func maintContext(api *tb.Bot, chatID int64) *maintTestContext {
	return &maintTestContext{api: api, sender: &tb.User{ID: chatID}, chat: &tb.Chat{ID: chatID, Type: tb.ChatPrivate}}
}

type maintTestContext struct {
	tb.Context
	api    tb.API
	sender *tb.User
	chat   *tb.Chat
}

func (c *maintTestContext) Bot() tb.API            { return c.api }
func (c *maintTestContext) Chat() *tb.Chat         { return c.chat }
func (c *maintTestContext) Sender() *tb.User       { return c.sender }
func (c *maintTestContext) Send(any, ...any) error { return nil }
func (c *maintTestContext) Respond(...*tb.CallbackResponse) error {
	return nil
}

func TestPlaylistDescReSendTakesOverTheCard(t *testing.T) {
	api, calls := maintTestBot(t)
	app := maintTestApp(t, api)
	old := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 7, Type: tb.ChatPrivate}}
	app.savePlaylistDesc(pendingPlaylistDesc{
		token: "desc-1", userID: 7, chatID: 7, message: old, synced: true, page: 1,
		playlists: []playlistcatalog.Playlist{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}},
		expiresAt: time.Now().Add(time.Hour),
	})
	live, ok := app.livePlaylistDesc(7, 7)
	if !ok {
		t.Fatal("live desc session not found")
	}
	if err := app.takeoverPlaylistDescCard(maintContext(api, 7), live); err != nil {
		t.Fatal(err)
	}
	sent := maintWait(t, calls, "sendMessage", func(c maintCall) bool { return strings.Contains(c.text, "歌单描述") })
	if !strings.Contains(sent.text, "已移到聊天底部") {
		t.Fatalf("takeover card missing the move notice: %q", sent.text)
	}
	maintWait(t, calls, "editMessageText", func(c maintCall) bool {
		return c.messageID == "42" && strings.Contains(c.text, "已移到聊天底部")
	})
	app.playlistDescMu.Lock()
	updated := app.playlistDescSessions["desc-1"]
	app.playlistDescMu.Unlock()
	if updated.page != 1 || updated.message.ID == 42 {
		t.Fatalf("takeover lost the page or the new binding: %+v", updated)
	}
}

func TestAIPlaylistReSendTakesOverTheCard(t *testing.T) {
	api, calls := maintTestBot(t)
	app := maintTestApp(t, api)
	old := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 7, Type: tb.ChatPrivate}}
	app.aiPlaylistSessions = map[string]pendingAIPlaylistSession{
		"ai-1": {token: "ai-1", userID: 7, chatID: 7, message: old, totalPlaylists: 3, expiresAt: time.Now().Add(time.Hour)},
	}
	app.aiPlaylistSessionByChat = map[int64]string{7: "ai-1"}
	if err := app.takeoverAIPlaylistCard(maintContext(api, 7), app.aiPlaylistSessions["ai-1"]); err != nil {
		t.Fatal(err)
	}
	maintWait(t, calls, "sendMessage", func(c maintCall) bool { return strings.Contains(c.text, "AI") })
	maintWait(t, calls, "editMessageText", func(c maintCall) bool {
		return c.messageID == "42" && strings.Contains(c.text, "已移到聊天底部")
	})
	app.aiPlaylistSessionMu.Lock()
	live := app.aiPlaylistSessions["ai-1"]
	app.aiPlaylistSessionMu.Unlock()
	if live.message == nil || live.message.ID == 42 {
		t.Fatalf("AI takeover did not rebind the card: %+v", live)
	}
}

func TestAIPlaylistBusySessionIsNotTakenOver(t *testing.T) {
	api, _ := maintTestBot(t)
	app := maintTestApp(t, api)
	session := pendingAIPlaylistSession{token: "ai-1", userID: 7, chatID: 7, processing: true,
		message: &tb.Message{ID: 42, Chat: &tb.Chat{ID: 7, Type: tb.ChatPrivate}}, expiresAt: time.Now().Add(time.Hour)}
	if err := app.takeoverAIPlaylistCard(maintContext(api, 7), session); err != nil {
		t.Fatalf("busy takeover should just warn, got %v", err)
	}
}

func TestEmptyTrashReSendTakesOverTheConfirmation(t *testing.T) {
	api, calls := maintTestBot(t)
	app := maintTestApp(t, api)
	old := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 7, Type: tb.ChatPrivate}}
	app.emptyTrashPending = map[string]pendingEmptyTrash{"trash-1": {
		token: "trash-1", userID: 7, chatID: 7, message: old,
		plan:      emptyTrashPlan{snapshot: emptyTrashSnapshot{TrackCount: 3}},
		expiresAt: time.Now().Add(time.Hour),
	}}
	live, ok := app.liveEmptyTrashConfirmation(7, 7)
	if !ok {
		t.Fatal("pending confirmation not found")
	}
	if err := app.takeoverEmptyTrashCard(maintContext(api, 7), live); err != nil {
		t.Fatal(err)
	}
	sent := maintWait(t, calls, "sendMessage", func(c maintCall) bool { return strings.Contains(c.text, "清空回收站") })
	if !strings.Contains(sent.markup, "确认清空") {
		t.Fatalf("takeover card lost the confirm control: %+v", sent)
	}
	maintWait(t, calls, "editMessageText", func(c maintCall) bool {
		return c.messageID == "42" && strings.Contains(c.text, "已移到聊天底部")
	})
	app.emptyTrashMu.Lock()
	updated := app.emptyTrashPending["trash-1"]
	app.emptyTrashMu.Unlock()
	if updated.message.ID == 42 || updated.plan.snapshot.TrackCount != 3 {
		t.Fatalf("takeover lost the plan or the binding: %+v", updated)
	}
}

func TestEmptyTrashProcessingIsNotTakenOver(t *testing.T) {
	api, _ := maintTestBot(t)
	app := maintTestApp(t, api)
	app.emptyTrashPending = map[string]pendingEmptyTrash{"trash-1": {
		token: "trash-1", userID: 7, chatID: 7, processing: true, expiresAt: time.Now().Add(time.Hour),
	}}
	if _, ok := app.liveEmptyTrashConfirmation(7, 7); ok {
		t.Fatal("a processing confirmation must not be re-sent")
	}
}

func TestPlaylistAssignmentReSendKeepsSongAndProgress(t *testing.T) {
	api, calls := maintTestBot(t)
	app := maintTestApp(t, api)
	old := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 7, Type: tb.ChatPrivate}}
	app.playlistAssignments = map[string]pendingPlaylistAssignment{"assign-1": {
		token: "assign-1", userID: 7, chatID: 7, message: old,
		song:           unplaylistSong{ID: "song-1", Title: "Song", Artist: "Artist"},
		playlists:      []config.PlaylistInfo{{Name: "A"}, {Name: "B"}},
		processedCount: 4, addedCount: 2, handledSongIDs: map[string]struct{}{},
		expiresAt: time.Now().Add(time.Hour),
	}}
	app.playlistAssignmentByChat = map[int64]string{7: "assign-1"}
	live, ok := app.livePlaylistAssignment(7, 7)
	if !ok {
		t.Fatal("live assignment not found")
	}
	if err := app.takeoverPlaylistAssignmentCard(maintContext(api, 7), live); err != nil {
		t.Fatal(err)
	}
	maintWait(t, calls, "sendMessage", func(c maintCall) bool { return strings.Contains(c.text, "Song") })
	maintWait(t, calls, "editMessageText", func(c maintCall) bool {
		return c.messageID == "42" && strings.Contains(c.text, "已移到聊天底部")
	})
	app.playlistAssignmentMu.Lock()
	updated := app.playlistAssignments["assign-1"]
	app.playlistAssignmentMu.Unlock()
	if updated.message.ID == 42 || updated.song.ID != "song-1" || updated.processedCount != 4 {
		t.Fatalf("assignment takeover lost state: %+v", updated)
	}
}
