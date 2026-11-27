package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/audioquality"
	"github.com/nichuanfang/gymdl/internal/storage"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

type checkCall struct {
	method    string
	messageID string
	text      string
	markup    string
	parseMode string
}

func checkTestBot(t *testing.T) (*tb.Bot, <-chan checkCall) {
	t.Helper()
	calls := make(chan checkCall, 64)
	var nextID atomic.Int64
	nextID.Store(100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Text      string `json:"text"`
			Markup    string `json:"reply_markup"`
			MessageID string `json:"message_id"`
			ParseMode string `json:"parse_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode Telegram request: %v", err)
		}
		method := filepath.Base(r.URL.Path)
		calls <- checkCall{method: method, messageID: payload.MessageID, text: payload.Text, markup: payload.Markup, parseMode: payload.ParseMode}
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

func checkWait(t *testing.T, calls <-chan checkCall, method string, match func(checkCall) bool) checkCall {
	t.Helper()
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	for {
		select {
		case call := <-calls:
			if call.method == method && (match == nil || match(call)) {
				return call
			}
		case <-timer.C:
			t.Fatalf("Telegram received no matching %s", method)
			return checkCall{}
		}
	}
}

func checkTestApp(t *testing.T, api *tb.Bot) *BotApp {
	t.Helper()
	previous := logger
	logger = utils.Logger()
	t.Cleanup(func() { logger = previous })
	return &BotApp{bot: api, cfg: &config.Config{
		Tidy:     &config.TidyConfig{Mode: 1, DistDir: t.TempDir()},
		Telegram: &config.TelegramConfig{AllowedUsers: []string{"7"}},
	}}
}

func TestAudioCheckReSendMovesCardToTheBottom(t *testing.T) {
	t.Chdir(t.TempDir())
	api, calls := checkTestBot(t)
	app := checkTestApp(t, api)
	api.Handle("/audio_check", app.AudioCheckCommand)
	chat := &tb.Chat{ID: 7, Type: tb.ChatPrivate}
	user := &tb.User{ID: 7}
	old := &tb.Message{ID: 42, Chat: chat}
	app.audioCheckSessions = map[int64]audioCheckSession{
		7: {
			token: "old-token", expires: time.Now().Add(time.Hour), owner: 7, message: old,
			library: "lib", report: audioCheckReport{total: 10, checked: 3, remaining: 7},
		},
	}

	api.ProcessUpdate(tb.Update{Message: &tb.Message{ID: 90, Text: "/audio_check", Sender: user, Chat: chat}})

	moved := checkWait(t, calls, "sendMessage", func(c checkCall) bool {
		return strings.Contains(c.text, "曲库体检")
	})
	if !strings.Contains(moved.text, "已检查 3/10 首") || !strings.Contains(moved.markup, "继续体检") {
		t.Fatalf("takeover card lost the current state: %q", moved.text)
	}
	retired := checkWait(t, calls, "editMessageText", func(c checkCall) bool { return c.messageID == "42" })
	if !strings.Contains(retired.text, "已移到聊天底部") || strings.Contains(retired.markup, "inline_keyboard") {
		t.Fatalf("old card was not retired cleanly: %+v", retired)
	}

	app.audioCheckMu.Lock()
	live := app.audioCheckSessions[7]
	app.audioCheckMu.Unlock()
	if live.token == "old-token" {
		t.Fatal("takeover did not rotate the card token")
	}
	if live.message == nil || live.message.ID == 42 {
		t.Fatalf("takeover did not rebind to the new card: %+v", live.message)
	}
	if live.report.checked != 3 || live.report.remaining != 7 {
		t.Fatalf("takeover lost the task state: %+v", live.report)
	}

	// The old card's buttons must be rejected.
	api.Handle(&audioCheckEndpoint, app.HandleAudioCheckCallback)
	api.ProcessUpdate(tb.Update{Callback: &tb.Callback{
		ID: "c", Sender: user, Message: old,
		Data: "\faudio_check|continue|old-token",
	}})
	answer := checkWait(t, calls, "answerCallbackQuery", nil)
	if !strings.Contains(answer.text, "过期") && !strings.Contains(answer.text, "已更新") {
		t.Fatalf("stale card callback was accepted: %q", answer.text)
	}
}

func TestAudioCheckReSendStartsNewScanAfterClosedSummary(t *testing.T) {
	t.Chdir(t.TempDir())
	api, calls := checkTestBot(t)
	app := checkTestApp(t, api)
	t.Cleanup(app.stopAudioCheck)
	name := "needs-review.mp3"
	file := filepath.Join(app.cfg.Tidy.DistDir, name)
	writeAudioCheckFixture(t, file, "-ac", "2", "-c:a", "libmp3lame", "-b:a", "96k")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	library, err := audioCheckLibrary(app.cfg)
	if err != nil {
		t.Fatal(err)
	}
	store, err := audioquality.Open(storage.DefaultDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// An old transfer failure is indistinguishable from a retryable unreadable file.
	version := audioquality.FileVersion{Path: name, Size: info.Size(), Mod: info.ModTime().UnixNano()}
	if err := store.Save(t.Context(), library, audioquality.CachedResult{
		FileVersion: version, Result: audioquality.Result{Unreadable: true}, RetryAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	chat := &tb.Chat{ID: 7, Type: tb.ChatPrivate}
	old := audioCheckSession{
		token: "old-summary", expires: time.Now().Add(time.Hour), owner: 7, library: library,
		message: &tb.Message{ID: 42, Chat: chat}, processed: 3, notice: "已保存保留两版的决定。",
		report: audioCheckReport{total: 1149, checked: 595, unreadable: 554, unfingerprinted: 592},
	}
	if err := checkpointAudioCheck(store, t.Context(), old); err != nil {
		t.Fatal(err)
	}
	app.restoreAudioChecks(api)
	checkWait(t, calls, "editMessageText", func(c checkCall) bool { return c.messageID == "42" })
	api.Handle("/audio_check", app.AudioCheckCommand)
	user := &tb.User{ID: 7}
	api.ProcessUpdate(tb.Update{Message: &tb.Message{ID: 90, Text: "/audio_check", Sender: user, Chat: chat}})
	progress := checkWait(t, calls, "sendMessage", nil)
	if !strings.Contains(progress.text, "正在检查曲库") || strings.Contains(progress.text, "595/1149") {
		t.Fatalf("command did not start a new scan: %+v", progress)
	}
	review := checkWait(t, calls, "editMessageText", nil)
	if review.messageID != "101" || !strings.Contains(review.text, "Test &amp; Song") ||
		!strings.Contains(review.text, "低码率") || !strings.Contains(review.markup, "跳过并继续") {
		t.Fatalf("new scan did not stop at the first finding: %+v", review)
	}
	waitForAudioCheckScan(t, app)

	api.ProcessUpdate(tb.Update{Message: &tb.Message{ID: 91, Text: "/audio_check", Sender: user, Chat: chat}})
	moved := checkWait(t, calls, "sendMessage", nil)
	if moved.text != review.text || !strings.Contains(moved.markup, "跳过并继续") {
		t.Fatalf("command did not restore the pending finding: %+v", moved)
	}
}

func waitForAudioCheckScan(t *testing.T, app *BotApp) {
	t.Helper()
	app.audioCheckMu.Lock()
	done := app.audioCheckDone
	app.audioCheckMu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Fatal("background scan did not finish")
		}
	}
}

func TestAudioCheckReSendKeepsRunningScanSingle(t *testing.T) {
	t.Chdir(t.TempDir())
	api, calls := checkTestBot(t)
	app := checkTestApp(t, api)
	api.Handle("/audio_check", app.AudioCheckCommand)
	chat := &tb.Chat{ID: 7, Type: tb.ChatPrivate}
	done := make(chan struct{})
	cancelled := atomic.Bool{}
	app.audioCheckDone = done
	app.audioCheckCancel = func() { cancelled.Store(true) }
	app.audioCheckSessions = map[int64]audioCheckSession{
		7: {
			token: "old-token", expires: time.Now().Add(time.Hour), owner: 7, running: true,
			message: &tb.Message{ID: 42, Chat: chat}, library: "lib",
		},
	}

	api.ProcessUpdate(tb.Update{Message: &tb.Message{ID: 90, Text: "/audio_check", Sender: &tb.User{ID: 7}, Chat: chat}})
	progress := checkWait(t, calls, "sendMessage", func(c checkCall) bool {
		return strings.Contains(c.text, "曲库体检")
	})
	if !strings.Contains(progress.markup, "停止") {
		t.Fatalf("running takeover card lost the stop control: %+v", progress)
	}
	if cancelled.Load() {
		t.Fatal("takeover cancelled the running scan")
	}
	app.audioCheckMu.Lock()
	defer app.audioCheckMu.Unlock()
	if app.audioCheckDone != done {
		t.Fatal("takeover replaced the running scan handle")
	}
}

func TestAudioCheckReSendRollsBackWhenPersistFails(t *testing.T) {
	t.Chdir(t.TempDir())
	api, calls := checkTestBot(t)
	app := checkTestApp(t, api)
	api.Handle("/audio_check", app.AudioCheckCommand)
	original := takeoverPersistHook
	takeoverPersistHook = func(audioCheckSession) error { return fmt.Errorf("db locked") }
	t.Cleanup(func() { takeoverPersistHook = original })

	chat := &tb.Chat{ID: 7, Type: tb.ChatPrivate}
	old := &tb.Message{ID: 42, Chat: chat}
	app.audioCheckSessions = map[int64]audioCheckSession{
		7: {token: "old-token", expires: time.Now().Add(time.Hour), owner: 7, message: old, library: "lib",
			report: audioCheckReport{resumeAvailable: true}},
	}

	api.ProcessUpdate(tb.Update{Message: &tb.Message{ID: 90, Text: "/audio_check", Sender: &tb.User{ID: 7}, Chat: chat}})
	checkWait(t, calls, "sendMessage", func(c checkCall) bool { return strings.Contains(c.text, "曲库体检") })
	checkWait(t, calls, "deleteMessage", nil)
	notice := checkWait(t, calls, "sendMessage", func(c checkCall) bool {
		return strings.Contains(c.text, "刚刚变化")
	})
	if notice.text == "" {
		t.Fatal("persist failure did not tell the user")
	}
	app.audioCheckMu.Lock()
	live := app.audioCheckSessions[7]
	app.audioCheckMu.Unlock()
	if live.token != "old-token" || live.message.ID != 42 {
		t.Fatalf("persist failure did not roll back: %+v", live)
	}
}

func TestAudioCheckExpiredCardStartsFreshScan(t *testing.T) {
	t.Chdir(t.TempDir())
	api, _ := checkTestBot(t)
	app := checkTestApp(t, api)
	app.audioCheckSessions = map[int64]audioCheckSession{
		7: {token: "old", expires: time.Now().Add(-time.Minute), owner: 7,
			message: &tb.Message{ID: 42, Chat: &tb.Chat{ID: 7}}, library: "lib"},
	}
	if _, ok := app.currentAudioCheck(7, 7); ok {
		t.Fatal("an expired card must not be taken over")
	}
}

func TestAudioCheckReturnButtonFitsTelegramLimit(t *testing.T) {
	data := "\faudio_check|resume|" + audioCheckToken()
	if len(data) > 64 {
		t.Fatalf("resume callback %q is %d bytes, over Telegram's 64-byte limit", data, len(data))
	}
}

func TestAudioCheckTakesOverConcurrentlyWithoutCorruption(t *testing.T) {
	t.Chdir(t.TempDir())
	api, _ := checkTestBot(t)
	app := checkTestApp(t, api)
	chat := &tb.Chat{ID: 7, Type: tb.ChatPrivate}
	app.audioCheckSessions = map[int64]audioCheckSession{
		7: {token: "t0", expires: time.Now().Add(time.Hour), owner: 7,
			message: &tb.Message{ID: 42, Chat: chat}, library: "lib", report: audioCheckReport{resumeAvailable: true}},
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = app.startAudioCheck(checkContext{api: api, chat: chat, sender: &tb.User{ID: 7}}, nil)
		}()
	}
	wg.Wait()
	app.audioCheckMu.Lock()
	live := app.audioCheckSessions[7]
	app.audioCheckMu.Unlock()
	if live.message == nil || live.message.ID == 42 {
		t.Fatal("concurrent takeovers never bound a new card")
	}
	if live.token == "" || live.token == "t0" {
		t.Fatalf("winning takeover left a stale token: %q", live.token)
	}
}

type checkContext struct {
	tb.Context
	api    tb.API
	chat   *tb.Chat
	sender *tb.User
}

func (c checkContext) Bot() tb.API            { return c.api }
func (c checkContext) Chat() *tb.Chat         { return c.chat }
func (c checkContext) Sender() *tb.User       { return c.sender }
func (c checkContext) Send(any, ...any) error { return nil }
func (c checkContext) Respond(...*tb.CallbackResponse) error {
	return nil
}
