package bot

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
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
	if _, err := formatQQCredentialMessage(json.RawMessage(`{"access_token":"only-one-field"}`)); err == nil {
		t.Fatal("incomplete credentials must be rejected instead of panicking or sending partial data")
	}
}

// The handlers registered in router.go must each be reachable from the menu,
// except /start which Telegram shows as its own Start button.
func TestEveryMenuCommandHasHandler(t *testing.T) {
	registered := map[string]bool{
		"rediscover": true, "favorites": true, "start": true, "help": true, "mode": true, "ai": true,
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
	want := []string{"ai", "mode", "clear", "assign_playlist", "playlist_desc", "ai_playlist", "rediscover", "favorites", "audio_check", "empty_trash", "status", "help"}
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
