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
	"github.com/nichuanfang/gymdl/internal/audiodupe"
	"github.com/nichuanfang/gymdl/internal/audioquality"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/storage"
	tb "gopkg.in/telebot.v4"
)

func TestAudioCheckRestoresCursorAndPendingDuplicateAfterTakeover(t *testing.T) {
	if tool := missingAudioCheckTool(); tool != "" {
		t.Skipf("missing %s", tool)
	}
	t.Chdir(t.TempDir())
	api, calls := checkTestBot(t)
	app := checkTestApp(t, api)
	library, err := audioCheckLibrary(app.cfg)
	if err != nil {
		t.Fatal(err)
	}
	store, err := audioquality.Open(storage.DefaultDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fingerprints, err := audiodupe.Open(storage.DefaultDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer fingerprints.Close()
	files := []web.FileEntry{{Path: "a.mp3", Name: "a.mp3", Size: 100, ModTime: time.Unix(100, 0)},
		{Path: "b.mp3", Name: "b.mp3", Size: 100, ModTime: time.Unix(100, 0)}}
	fp := audiodupe.Fingerprint{Duration: 180, Items: make([]uint32, 300)}
	for i := range fp.Items {
		fp.Items[i] = uint32(i) * 2654435761
	}
	for _, file := range files {
		version := audioFileVersion(file)
		result := audioquality.Result{Title: "Saved recording", Codec: "mp3", Bitrate: 320, Duration: 180}
		if err := store.Save(t.Context(), library, audioquality.CachedResult{FileVersion: version, Result: result}); err != nil {
			t.Fatal(err)
		}
		if err := fingerprints.SaveFingerprint(t.Context(), file.Path, version.Size, version.Mod, fp); err != nil {
			t.Fatal(err)
		}
	}
	chat := &tb.Chat{ID: 7, Type: tb.ChatPrivate}
	user := &tb.User{ID: 7}
	session := audioCheckSession{token: "old", owner: 7, library: library, expires: time.Now().Add(time.Hour),
		message: &tb.Message{ID: 42, Chat: chat}, running: true, processed: 3,
		report:     audioCheckReport{total: 2, checked: 2},
		checkpoint: &savedAudioCheckScan{Files: files, Cursor: 2, Fingerprint: true, Pending: true}}
	app.setAudioCheckSession(session)
	api.Handle("/audio_check", app.AudioCheckCommand)
	api.ProcessUpdate(tb.Update{Message: &tb.Message{ID: 90, Text: "/audio_check", Sender: user, Chat: chat}})
	moved := checkWait(t, calls, "sendMessage", nil)
	if !strings.Contains(moved.text, "已检查 2/2 首") {
		t.Fatalf("takeover lost progress: %+v", moved)
	}
	checkWait(t, calls, "editMessageText", func(call checkCall) bool { return call.messageID == "42" })
	restarted := &BotApp{cfg: app.cfg, bot: api}
	defer restarted.stopAudioCheck()
	restarted.restoreAudioChecks(api)
	progress := checkWait(t, calls, "editMessageText", func(call checkCall) bool {
		return strings.Contains(call.text, "容器重启后")
	})
	if progress.messageID != "101" || !strings.Contains(progress.text, "已检查 2/2 首") {
		t.Fatalf("restart restored the wrong message or progress: %+v", progress)
	}
	result := checkWait(t, calls, "editMessageText", func(call checkCall) bool {
		return strings.Contains(call.text, "疑似重复录音")
	})
	waitForAudioCheckScan(t, restarted)
	current, _ := restarted.liveAudioCheckBinding(7)
	finding, ok := currentAudioCheckFinding(current)
	if result.messageID != "101" || !ok || finding.duplicate == nil || finding.file.Path != "b.mp3" ||
		finding.duplicate.File.Path != "a.mp3" || current.report.checked != 2 || current.processed != 3 {
		t.Fatalf("restart lost review state: %+v", current)
	}
}

func TestAudioCheckRenewsUnfinishedReviewSavedByOldVersion(t *testing.T) {
	t.Chdir(t.TempDir())
	api, calls := checkTestBot(t)
	app := checkTestApp(t, api)
	library, err := audioCheckLibrary(app.cfg)
	if err != nil {
		t.Fatal(err)
	}
	store, err := audioquality.Open(storage.DefaultDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chat := &tb.Chat{ID: 7, Type: tb.ChatPrivate}
	oldExpiry := time.Now().Add(time.Minute)
	session := audioCheckSession{token: "legacy", owner: 7, library: library, expires: oldExpiry,
		message: &tb.Message{ID: 42, Chat: chat},
		report: audioCheckReport{total: 2, checked: 2, resumeAvailable: true,
			findings: []audioCheckFinding{{file: web.FileEntry{Path: "song.mp3"}, Result: audioquality.Result{Codec: "mp3", Bitrate: 96}}},
		},
	}
	if err := checkpointAudioCheck(store, t.Context(), session); err != nil {
		t.Fatal(err)
	}
	app.restoreAudioChecks(api)
	checkWait(t, calls, "editMessageText", func(call checkCall) bool { return call.messageID == "42" })
	restored := app.audioCheckSessions[7]
	if restored.expires.Before(time.Now().Add(audioCheckTTL - time.Minute)) {
		t.Fatalf("unfinished review was not renewed: %v", restored.expires)
	}
}

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
				message: message, running: true,
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
				next("已检查 1/2 首")
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
			var want = "低码率"
			if paused {
				want = "已处理 1 个问题"
			}
			next(want)
			restored.audioCheckMu.Lock()
			done = restored.audioCheckDone
			restored.audioCheckMu.Unlock()
			if done != nil {
				select {
				case <-done:
				case <-time.After(8 * time.Second):
					t.Fatal("continued scan did not finish")
				}
			}
			reopened := &BotApp{cfg: cfg, bot: api}
			reopened.restoreAudioChecks(api)
			after := reopened.audioCheckSessions[7]
			if _, skipped := after.skipped[version]; after.processed != 1 || !skipped {
				t.Fatalf("review position or skipped file lost on restart: %+v", after)
			}
		})
	}
}
