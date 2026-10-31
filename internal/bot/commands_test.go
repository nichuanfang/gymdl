package bot

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Telegram caps command descriptions at 256 chars, but on a phone the "/" menu
// only shows about 20 CJK characters before truncating. Keep every entry short
// so the menu stays one screen and stays tappable.
func TestBotCommandsFitPhoneMenu(t *testing.T) {
	commands := botCommands()
	if len(commands) == 0 {
		t.Fatal("command menu is empty")
	}
	if len(commands) > 7 {
		t.Fatalf("%d commands will scroll off a phone screen", len(commands))
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

// The handlers registered in router.go must each be reachable from the menu,
// except /start which Telegram shows as its own Start button.
func TestEveryMenuCommandHasHandler(t *testing.T) {
	registered := map[string]bool{
		"start": true, "help": true, "mode": true, "ai": true,
		"clear": true, "new": true, "assign_playlist": true,
		"ai_playlist": true, "empty_trash": true, "qq_login": true,
	}
	for _, cmd := range botCommands() {
		if !registered[cmd.Text] {
			t.Errorf("/%s is in the menu but has no handler", cmd.Text)
		}
	}
}
