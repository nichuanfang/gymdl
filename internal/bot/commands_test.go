package bot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nichuanfang/gymdl/config"
	tb "gopkg.in/telebot.v4"
)

// Telegram caps command descriptions at 256 chars, but on a phone the "/" menu
// only shows about 20 CJK characters before truncating. Keep every entry short
// so entries stay readable even when the complete menu needs scrolling.
func TestHelpNamesEmptyTrashEffects(t *testing.T) {
	text := helpText()
	for _, part := range []string{"回收站", "歌曲文件", "MusicRAG", "Pinecone"} {
		if !strings.Contains(text, part) {
			t.Fatalf("help omits %s: %s", part, text)
		}
	}
	if strings.Contains(text, "清理 AI 曲库数据") {
		t.Fatal("help still understates emptying the trash playlist")
	}
}

func TestBotCommandsFitPhoneMenu(t *testing.T) {
	commands := botCommands()
	if len(commands) == 0 {
		t.Fatal("command menu is empty")
	}
	if len(commands) > 100 {
		t.Fatalf("%d commands exceeds Telegram's limit", len(commands))
	}
	seen := make(map[string]bool, len(commands))
	for _, cmd := range commands {
		if seen[cmd.Text] {
			t.Fatalf("duplicate command /%s", cmd.Text)
		}
		seen[cmd.Text] = true
		if strings.TrimSpace(cmd.Description) != cmd.Description {
			t.Errorf("/%s description has stray whitespace: %q", cmd.Text, cmd.Description)
		}
		if got := utf8.RuneCountInString(cmd.Description); got < 2 || got > 8 {
			t.Errorf("/%s description is %d chars, want 2-8: %q", cmd.Text, got, cmd.Description)
		}
		for _, r := range cmd.Description {
			// Emoji waste the narrow row width on a phone.
			if (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) || r == 0xFE0F {
				t.Errorf("/%s description must not contain emoji: %q", cmd.Text, cmd.Description)
				break
			}
		}
		for _, r := range cmd.Text {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' {
				t.Errorf("/%s is not a valid Telegram command name", cmd.Text)
				break
			}
		}
	}
}

func TestQQCredentialMessageEscapesSecretsAndRejectsIncompletePayloads(t *testing.T) {
	valid := json.RawMessage(`{"musicid":123,"str_musicid":"str-id","openid":"open-id","unionid":"union-id","loginType":0,"musickey":"music-key","refresh_key":"refresh-key","refresh_token":"refresh-token","access_token":"<access>&token","expired_at":1234567890}`)
	message, err := formatQQCredentialMessage(valid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "&lt;access&gt;&amp;token") || strings.Contains(message, "<access>&token") {
		t.Fatalf("credential should be safely escaped for HTML: %q", message)
	}
	for _, payload := range []string{
		`{"musickey":"mock-key"}`,
		`{"musicid":0,"str_musicid":"","musickey":"mock-key"}`,
		`{"musicid":123,"musickey":" "}`,
		`{"musicid":123,"musickey":456}`,
	} {
		if _, err := formatQQCredentialMessage(json.RawMessage(payload)); err == nil {
			t.Errorf("accepted unusable mock credential %s", payload)
		}
	}
}

func TestQQCredentialMessageAcceptsSparseQRCodeCredentials(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		wantID  string
	}{
		{"numeric ID", `{"musicid":9007199254740993,"str_musicid":"","musickey":"mock-key","openid":"","unionid":null,"login_type":0}`, "9007199254740993"},
		{"string ID", `{"musicid":0,"str_musicid":"9007199254740993","musickey":"mock-key","loginType":0}`, "9007199254740993"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message, err := formatQQCredentialMessage(json.RawMessage(tc.payload))
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []string{"<code>" + tc.wantID + "</code>", "<code>mock-key</code>", "<b>Login Type</b>\n<code>0</code>"} {
				if !strings.Contains(message, part) {
					t.Errorf("credential message omits %q: %q", part, message)
				}
			}
			for _, label := range []string{"OpenID", "UnionID", "Refresh Token"} {
				if strings.Contains(message, label) {
					t.Errorf("credential message includes unavailable field %q", label)
				}
			}
		})
	}
}

func TestQQLoginCommandSendsCredentialsBeforeMarkingSuccess(t *testing.T) {
	type telegramCall struct {
		method string
		fields map[string]string
	}
	calls := make(chan telegramCall, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/qrcode/qq":
			_, _ = io.WriteString(w, `{"code":0,"data":{"identifier":"mock-qr","data":"eA=="}}`)
			return
		case "/login/qrcode/qq/status":
			if r.URL.Query().Get("identifier") != "mock-qr" {
				t.Error("status request did not use the issued QR identifier")
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"event":0,"credential":{"musicid":9007199254740993,"musickey":"mock-key","str_musicid":"","login_type":0}}}`)
			return
		case "/bot1:test/sendPhoto":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":42,"chat":{"id":7,"type":"private"},"photo":[{"file_id":"qr","width":1,"height":1}]}}`)
			return
		case "/bot1:test/sendMessage", "/bot1:test/editMessageCaption":
			call := telegramCall{method: r.URL.Path, fields: make(map[string]string)}
			if err := json.NewDecoder(r.Body).Decode(&call.fields); err != nil {
				t.Error(err)
			}
			calls <- call
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":42,"chat":{"id":7,"type":"private"}}}`)
	}))
	defer server.Close()
	api, err := tb.NewBot(tb.Settings{Offline: true, Synchronous: true, URL: server.URL, Token: "1:test"})
	if err != nil {
		t.Fatal(err)
	}
	app := &BotApp{bot: api, cfg: &config.Config{QQMusicApiConfig: &config.QQMusicApiConfig{Enable: true, Endpoint: server.URL}}}
	api.Handle("/qq_login", app.QQLoginCommand)
	api.ProcessUpdate(tb.Update{Message: &tb.Message{
		Text: "/qq_login", Sender: &tb.User{ID: 7}, Chat: &tb.Chat{ID: 7, Type: tb.ChatPrivate},
	}})

	timeout := time.After(8 * time.Second)
	for _, method := range []string{"/bot1:test/sendMessage", "/bot1:test/editMessageCaption"} {
		select {
		case call := <-calls:
			if call.method != method || call.fields["chat_id"] != "7" {
				t.Fatalf("got %s for chat %s, want %s for chat 7", call.method, call.fields["chat_id"], method)
			}
			if method == "/bot1:test/sendMessage" && (!strings.Contains(call.fields["text"], "<code>9007199254740993</code>") || call.fields["parse_mode"] != "HTML") {
				t.Errorf("expected HTML credentials with the exact music ID, got %v", call.fields)
			}
			if method == "/bot1:test/editMessageCaption" && (call.fields["caption"] != "🎉 登录成功！" || call.fields["message_id"] != "42") {
				t.Errorf("expected a success caption on the QR message, got %v", call.fields)
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", method)
		}
	}
}

// The handlers registered in router.go must each be reachable from the menu,
// except /start which Telegram shows as its own Start button.
func TestEveryMenuCommandHasHandler(t *testing.T) {
	registered := map[string]bool{
		"rediscover": true, "recommend": true, "favorites": true, "start": true, "help": true, "mode": true, "ai": true,
		"clear": true, "new": true, "assign_playlist": true, "playlist_desc": true,
		"ai_playlist": true, "empty_trash": true, "qq_login": true, "status": true, "audio_check": true,
	}
	for _, cmd := range botCommands() {
		if !registered[cmd.Text] {
			t.Errorf("/%s is in the menu but has no handler", cmd.Text)
		}
	}
}

// Preserve the user's chosen menu order and all daily control actions.
func TestBotCommandsKeepUserPreferredOrder(t *testing.T) {
	want := []string{"ai", "mode", "clear", "assign_playlist", "playlist_desc", "ai_playlist", "rediscover", "recommend", "favorites", "audio_check", "empty_trash", "status", "help"}
	commands := botCommands()
	if len(commands) != len(want) {
		t.Fatalf("got %d commands, want %d", len(commands), len(want))
	}
	for i, name := range want {
		if commands[i].Text != name {
			t.Errorf("menu position %d: got /%s, want /%s", i+1, commands[i].Text, name)
		}
	}
}
