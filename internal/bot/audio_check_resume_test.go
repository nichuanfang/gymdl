package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/audioquality"
	"github.com/nichuanfang/gymdl/internal/storage"
	tb "gopkg.in/telebot.v4"
)

func TestAudioCheckRecoversAfterRestart(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprintf("paused=%t", paused), func(t *testing.T) {
			t.Chdir(t.TempDir())
			dir := t.TempDir()
			firstPath := filepath.Join(dir, "a.mp3")
			// The saved inspection represents this exact size and mtime.
			// Reopening the file would produce damage rather than this result.
			if err := os.WriteFile(firstPath, []byte("cached inspection"), 0o600); err != nil {
				t.Fatal(err)
			}
			if !paused {
				writeAudioCheckFixture(t, filepath.Join(dir, "b.mp3"), "-ac", "2", "-c:a", "libmp3lame", "-b:a", "96k")
			}
			file, err := os.Stat(firstPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{
				Tidy:     &config.TidyConfig{Mode: 1, DistDir: dir},
				Telegram: &config.TelegramConfig{AllowedUsers: []string{"7"}},
			}
			library, err := audioCheckLibrary(cfg)
			if err != nil {
				t.Fatal(err)
			}
			chat := &tb.Chat{ID: 7, Type: tb.ChatPrivate}
			message := &tb.Message{ID: 42, Chat: chat}
			version := audioquality.FileVersion{Path: "a.mp3", Size: file.Size(), Mod: file.ModTime().UnixNano()}
			store, err := audioquality.Open(storage.DefaultDatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			original := audioquality.CachedResult{FileVersion: version, Result: audioquality.Result{Title: "Saved result", Bitrate: 96, Codec: "mp3"}}
			if err := store.Save(t.Context(), library, original); err != nil {
				t.Fatal(err)
			}
			saved := audioCheckSession{
				token: "before-restart", expires: time.Now().Add(time.Hour), owner: 7, library: library,
				message: message, running: true, full: true, deep: true,
			}
			if err := checkpointAudioCheck(store, t.Context(), saved); err != nil {
				t.Fatal(err)
			}
			if paused {
				if err := store.PauseSession(t.Context(), 7, saved.token); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			type edit struct {
				Text      string
				MessageID string `json:"message_id"`
				Markup    string `json:"reply_markup"`
			}
			edits := make(chan edit, 16)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if filepath.Base(r.URL.Path) == "answerCallbackQuery" {
					fmt.Fprint(w, `{"ok":true,"result":true}`)
					return
				}
				if filepath.Base(r.URL.Path) != "editMessageText" {
					t.Errorf("unexpected Telegram call %s", r.URL.Path)
				}
				var change edit
				if err := json.NewDecoder(r.Body).Decode(&change); err != nil {
					t.Errorf("parse restored card: %v", err)
				}
				edits <- change
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":42,"chat":{"id":7,"type":"private"}}}`)
			}))
			defer server.Close()
			api, err := tb.NewBot(tb.Settings{Offline: true, Synchronous: true, Token: "1:test", URL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			next := func(want string) edit {
				t.Helper()
				timer := time.NewTimer(8 * time.Second)
				defer timer.Stop()
				for {
					select {
					case change := <-edits:
						if change.MessageID != "42" {
							t.Fatalf("scan updated the wrong card: %+v", change)
						}
						if strings.Contains(change.Text, want) {
							return change
						}
					case <-timer.C:
						t.Fatalf("no original card update containing %q", want)
						return edit{}
					}
				}
			}
			app := &BotApp{cfg: cfg, bot: api}
			app.restoreAudioChecks(api)
			app.audioCheckMu.Lock()
			done := app.audioCheckDone
			app.audioCheckMu.Unlock()
			if paused {
				result := next("已恢复停止前的体检记录")
				if done != nil || !strings.Contains(result.Text, "Saved result") || !strings.Contains(result.Text, "已恢复停止前的体检记录") {
					t.Fatalf("paused session automatically started or lost its result: %+v", result)
				}
			} else {
				defer app.stopAudioCheck()
				progress := next("容器重启后正在恢复扫描进度")
				if !strings.Contains(progress.Markup, "before-restart") {
					t.Fatalf("running card did not keep its stop token: %+v", progress)
				}
				next("已检查 2/2 首")
				if done != nil {
					select {
					case <-done:
					case <-time.After(8 * time.Second):
						t.Fatal("restored scan did not finish")
					}
				}
			}

			// Start another BotApp against the same SQLite file. Its result
			// card and controls should work without rerunning any scan.
			restored := &BotApp{cfg: cfg, bot: api}
			restored.restoreAudioChecks(api)
			result := next("Saved result")
			session := restored.audioCheckSessions[7]
			if restored.audioCheckDone != nil || session.running || session.report.findings[0].Damaged {
				t.Fatalf("saved result was not restored: %+v", session)
			}
			if !strings.Contains(result.Markup, "跳过") {
				t.Fatalf("restored result missing review button: %+v", result)
			}
			api.Handle(&audioCheckEndpoint, restored.HandleAudioCheckCallback)
			api.ProcessUpdate(tb.Update{Callback: &tb.Callback{
				ID: "skip", Sender: &tb.User{ID: 7}, Message: message,
				Data: "\faudio_check|skip|" + session.token,
			}})
			want := "· 2/2"
			if paused {
				want = "已查看 1 个问题"
			}
			next(want)
			reopened := &BotApp{cfg: cfg, bot: api}
			reopened.restoreAudioChecks(api)
			after := reopened.audioCheckSessions[7]
			if _, skipped := after.skipped[version]; after.index != 1 || !skipped {
				t.Fatalf("review position or skipped file lost on restart: %+v", after)
			}
		})
	}
}
