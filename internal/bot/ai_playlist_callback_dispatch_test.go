package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v4"
)

type telegramTestCall struct {
	method  string
	payload map[string]any
}

func TestAIPlaylistGeneratedButtonsDispatchThroughTelebot(t *testing.T) {
	for _, test := range []struct {
		name       string
		action     string
		selection  int
		wantText   string
		wantEdit   string
		wantAlert  bool
		wantKeys   bool
		wantTarget string
		wantCreate bool
		wantCancel bool
	}{
		{name: "select first existing playlist", action: "select", selection: 0, wantText: "已选中「[AI] 夜间」", wantEdit: "🤖 AI 智能歌单", wantKeys: true, wantTarget: "playlist-1"},
		{name: "select second existing playlist", action: "select", selection: 1, wantText: "已选中「[AI] 开车电音」", wantEdit: "🤖 AI 智能歌单", wantKeys: true, wantTarget: "playlist-2"},
		{name: "new playlist", action: "new", wantText: "请发送新歌单名称", wantEdit: "请发送歌单名称", wantKeys: true, wantCreate: true},
		{name: "cancel", action: "cancel", wantEdit: "已取消本次 AI 歌单生成", wantCancel: true},
		{name: "expired button", action: "new", wantText: "AI 歌单会话已过期，旧按钮已失效。", wantEdit: "本次 AI 歌单会话已过期", wantAlert: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []telegramTestCall
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode Telegram API request: %v", err)
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				calls = append(calls, telegramTestCall{method: method, payload: payload})
				w.Header().Set("Content-Type", "application/json")
				if method == "editMessageText" {
					_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42,"date":1,"chat":{"id":10,"type":"private"},"text":"edited"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			}))
			defer server.Close()

			var handlerErr error
			bot, err := tb.NewBot(tb.Settings{
				Token:       "1:test",
				URL:         server.URL,
				Offline:     true,
				Synchronous: true,
				OnError: func(err error, _ tb.Context) {
					handlerErr = err
				},
			})
			if err != nil {
				t.Fatalf("create offline Telegram bot: %v", err)
			}

			previousLogger := logger
			logger = zap.NewNop()
			defer func() { logger = previousLogger }()
			app := &BotApp{
				bot:                     bot,
				cfg:                     &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"20"}}},
				aiPlaylistSessions:      make(map[string]pendingAIPlaylistSession),
				aiPlaylistSessionByChat: make(map[int64]string),
			}
			app.registerHandlers()

			message := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 10}}
			session := pendingAIPlaylistSession{
				token:   "dispatch-token",
				userID:  20,
				chatID:  10,
				message: message,
				playlists: []aiPlaylist{
					{ID: "playlist-1", Name: "[AI] 夜间", SongCount: 12},
					{ID: "playlist-2", Name: "[AI] 开车电音", SongCount: 13},
				},
				resumePlans: make(map[string]aiPlaylistResumePlan),
				expiresAt:   time.Now().Add(time.Minute),
			}
			if test.name != "expired button" && !app.registerAIPlaylistSession(session) {
				t.Fatal("register AI playlist session")
			}

			var button *tb.InlineButton
			for _, row := range aiPlaylistSessionMarkup(session).InlineKeyboard {
				for index := range row {
					candidate := &row[index]
					matches := strings.HasPrefix(candidate.Data, test.action+"|")
					if test.action == "select" {
						matches = strings.HasSuffix(candidate.Data, "|"+strconv.Itoa(test.selection))
					}
					if matches {
						button = candidate
						break
					}
				}
			}
			if button == nil {
				t.Fatal("generated menu did not contain the requested button")
			}

			callbackData := "\f" + button.Unique + "|" + button.Data
			bot.ProcessUpdate(tb.Update{Callback: &tb.Callback{
				ID:      "callback-" + test.action,
				Sender:  &tb.User{ID: 20},
				Message: message,
				Data:    callbackData,
			}})
			if handlerErr != nil {
				t.Fatalf("callback handler returned an error: %v", handlerErr)
			}
			if len(calls) != 2 || calls[0].method != "answerCallbackQuery" || calls[1].method != "editMessageText" {
				t.Fatalf("Telegram API calls = %#v, want callback acknowledgement followed by message edit", calls)
			}
			if test.wantText != "" && calls[0].payload["text"] != test.wantText {
				t.Errorf("callback acknowledgement text = %v, want %q", calls[0].payload["text"], test.wantText)
			}
			gotAlert, _ := calls[0].payload["show_alert"].(bool)
			if gotAlert != test.wantAlert {
				t.Errorf("callback alert flag = %t, want %t", gotAlert, test.wantAlert)
			}
			if got := calls[1].payload["text"]; got == nil || !strings.Contains(got.(string), test.wantEdit) {
				t.Errorf("edited message text = %v, does not show expected state", got)
			}
			rawMarkup, ok := calls[1].payload["reply_markup"].(string)
			if !ok {
				t.Fatal("edit request did not include reply_markup")
			}
			var editedMarkup tb.ReplyMarkup
			if err := json.Unmarshal([]byte(rawMarkup), &editedMarkup); err != nil {
				t.Fatalf("decode edited reply markup: %v", err)
			}
			if gotKeys := len(editedMarkup.InlineKeyboard) > 0; gotKeys != test.wantKeys {
				t.Errorf("edited keyboard present = %t, want %t", gotKeys, test.wantKeys)
			}

			current, exists := app.getAIPlaylistSession(session.token)
			switch {
			case test.wantTarget != "" && (!exists || current.target.ID != test.wantTarget):
				t.Errorf("selected target = %#v, exists=%t; want %q", current.target, exists, test.wantTarget)
			case test.wantCreate && (!exists || !current.creating || !current.awaitingName):
				t.Errorf("new playlist state = %#v, exists=%t", current, exists)
			case test.wantCancel && exists:
				t.Error("cancel button did not remove its session")
			case test.name == "expired button" && exists:
				t.Error("expired callback unexpectedly created a session")
			}
		})
	}
}
