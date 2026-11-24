package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/audioquality"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/storage"
	"github.com/nichuanfang/gymdl/utils"
	"github.com/studio-b12/gowebdav"
	tb "gopkg.in/telebot.v4"
)

func writeAudioCheckFixture(t *testing.T, file string, args ...string) {
	t.Helper()
	for _, tool := range []string{"ffprobe", "ffmpeg"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("requires " + tool)
		}
	}
	cmd := exec.CommandContext(t.Context(), "ffmpeg", append(append([]string{
		"-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-metadata", "title=Test & Song", "-metadata", "artist=Artist",
	}, args...), file)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create audio fixture: %v %s", err, output)
	}
}

func TestAudioCheckTelegramCommandAndReview(t *testing.T) {
	t.Chdir(t.TempDir())
	library := t.TempDir()
	for _, name := range []string{"a.mp3", "b.mp3"} {
		writeAudioCheckFixture(t, filepath.Join(library, name), "-ac", "2", "-c:a", "libmp3lame", "-b:a", "96k")
	}
	type request struct {
		method string
		text   string
		markup string
	}
	requests := make(chan request, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Text   string
			Markup string `json:"reply_markup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("invalid Telegram request: %v", err)
		}
		requests <- request{method: filepath.Base(r.URL.Path), text: payload.Text, markup: payload.Markup}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "answerCallbackQuery") {
			fmt.Fprint(w, `{"ok":true,"result":true}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42,"chat":{"id":7,"type":"private"}}}`)
	}))
	defer server.Close()
	api, err := tb.NewBot(tb.Settings{Offline: true, Synchronous: true, Token: "1:test", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	previousLogger := logger
	logger = utils.Logger()
	t.Cleanup(func() { logger = previousLogger })
	app := &BotApp{bot: api, cfg: &config.Config{
		Tidy:     &config.TidyConfig{Mode: 1, DistDir: library},
		Telegram: &config.TelegramConfig{AllowedUsers: []string{"7", "8"}},
	}}
	app.registerHandlers()
	t.Cleanup(app.stopAudioCheck)
	chat := &tb.Chat{ID: 7, Type: tb.ChatPrivate}
	user := &tb.User{ID: 7}
	command := func() {
		api.ProcessUpdate(tb.Update{Message: &tb.Message{ID: 10, Text: "/audio_check", Sender: user, Chat: chat}})
	}
	next := func(method string) request {
		t.Helper()
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		for {
			select {
			case r := <-requests:
				if r.method == method {
					return r
				}
			case <-timer.C:
				t.Fatalf("Telegram received no %s", method)
			}
		}
	}
	button := func(r request, label string) string {
		t.Helper()
		var markup tb.ReplyMarkup
		if err := json.Unmarshal([]byte(r.markup), &markup); err != nil {
			t.Fatal(err)
		}
		for _, row := range markup.InlineKeyboard {
			for _, b := range row {
				if b.Text == label {
					if len(b.Data) > 64 || !strings.HasPrefix(b.Data, "\faudio_check|") {
						t.Fatalf("invalid callback payload %q", b.Data)
					}
					return b.Data
				}
			}
		}
		t.Fatalf("missing button %q", label)
		return ""
	}
	click := func(data string, sender *tb.User, messageID int) {
		api.ProcessUpdate(tb.Update{Callback: &tb.Callback{ID: "callback", Data: data, Sender: sender,
			Message: &tb.Message{ID: messageID, Chat: chat}}})
	}
	waitScan := func() {
		t.Helper()
		app.audioCheckMu.Lock()
		done := app.audioCheckDone
		app.audioCheckMu.Unlock()
		if done == nil {
			return
		}
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Fatal("background scan did not finish")
		}
	}
	command()
	first := next("editMessageText")
	waitScan()
	if !strings.Contains(first.text, "1/2") || !strings.Contains(first.text, "Test &amp; Song") || !strings.Contains(first.text, "低码率") {
		t.Fatalf("unexpected first card: %s", first.text)
	}
	button(first, "播放")
	button(first, "其他音源")
	normal := button(first, "标记正常")
	click(normal, &tb.User{ID: 8}, 42)
	if got := next("answerCallbackQuery"); !strings.Contains(got.text, "已过期") {
		t.Fatalf("wrong user callback accepted: %q", got.text)
	}
	click(normal, user, 43)
	if got := next("answerCallbackQuery"); !strings.Contains(got.text, "已过期") {
		t.Fatalf("wrong message callback accepted: %q", got.text)
	}
	click(normal, user, 42)
	if got := next("editMessageText"); !strings.Contains(got.text, "2/2") {
		t.Fatalf("review did not open next finding: %q", got.text)
	}
	click(normal, user, 42)
	if got := next("answerCallbackQuery"); !strings.Contains(got.text, "已过期") {
		t.Fatalf("old callback moved the new card: %q", got.text)
	}
	command()
	rescan := next("editMessageText")
	waitScan()
	if !strings.Contains(rescan.text, "1/1") || !strings.Contains(rescan.text, "已检查 2/2") {
		t.Fatalf("reviewed file reappeared on next scan: %q", rescan.text)
	}
}

func TestAudioCheckCardsOfferStopAndResumableFullScan(t *testing.T) {
	session := audioCheckSession{
		token:  "scan-token",
		report: audioCheckReport{total: 10, checked: 4, remaining: 3, deferred: 2, referenceQueue: 1},
	}
	text, markup := formatAudioCheckCard(session)
	if !strings.Contains(text, "已检查 4/10 首") {
		t.Fatalf("result card lost progress: %s", text)
	}
	buttons := map[string]string{}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			buttons[button.Text] = button.Data
		}
	}
	for _, label := range []string{"继续扫描", "深度扫描", "全库体检"} {
		if buttons[label] == "" {
			t.Errorf("result card is missing %q: %#v", label, markup.InlineKeyboard)
		}
	}
	if !strings.HasSuffix(buttons["全库体检"], "full|scan-token") {
		t.Errorf("full scan callback = %q", buttons["全库体检"])
	}

	session.full = true
	text, markup = formatAudioCheckProgressCard(session)
	if !strings.Contains(text, "包含快速初筛暂缓的文件") {
		t.Fatalf("full scan mode is not explained: %s", text)
	}
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
		t.Fatalf("progress card should have one stop control: %#v", markup.InlineKeyboard)
	}
	stop := markup.InlineKeyboard[0][0]
	if stop.Text != "终止并查看结果" || !strings.HasSuffix(stop.Data, "stop|scan-token") {
		t.Fatalf("unexpected stop control: %#v", stop)
	}

	session.running = false
	_, markup = formatAudioCheckCard(session)
	if got := markup.InlineKeyboard[len(markup.InlineKeyboard)-1][0].Text; got != "继续全库体检" {
		t.Errorf("stopped full scan button = %q", got)
	}
}

type audioCheckCallbackTestContext struct {
	tb.Context
	args    []string
	sender  *tb.User
	chat    *tb.Chat
	message *tb.Message
	answer  *tb.CallbackResponse
}

func (c *audioCheckCallbackTestContext) Args() []string       { return c.args }
func (c *audioCheckCallbackTestContext) Sender() *tb.User     { return c.sender }
func (c *audioCheckCallbackTestContext) Chat() *tb.Chat       { return c.chat }
func (c *audioCheckCallbackTestContext) Message() *tb.Message { return c.message }
func (c *audioCheckCallbackTestContext) Respond(response ...*tb.CallbackResponse) error {
	if len(response) > 0 {
		c.answer = response[0]
	}
	return nil
}

func TestAudioCheckStopCallbackCancelsCurrentRun(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	message := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 7, Type: tb.ChatPrivate}}
	app := &BotApp{
		cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"7"}}},
		audioCheckSessions: map[int64]audioCheckSession{7: {
			token: "token", expires: time.Now().Add(time.Minute), owner: 7, message: message, running: true,
		}},
		audioCheckDone: make(chan struct{}), audioCheckCancel: cancel,
	}
	if err := persistAudioCheckSession(app.audioCheckSessions[7]); err != nil {
		t.Fatal(err)
	}
	callback := &audioCheckCallbackTestContext{
		args: []string{"stop", "token"}, sender: &tb.User{ID: 7}, chat: message.Chat,
		message: message,
	}
	if err := app.HandleAudioCheckCallback(callback); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("stop callback did not cancel run: %v", ctx.Err())
	}
	if callback.answer == nil || !strings.Contains(callback.answer.Text, "正在停止") {
		t.Fatalf("stop callback response = %#v", callback.answer)
	}
	store, err := audioquality.Open(storage.DefaultDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records, err := store.Sessions(t.Context())
	if err != nil || len(records) != 1 || records[0].Phase != audioquality.SessionPaused {
		t.Fatalf("stop was not saved: %+v, %v", records, err)
	}
}

func TestCachedAudioCheckReportShowsCompletedFindingsWhenIndexStops(t *testing.T) {
	store, err := audioquality.Open(filepath.Join(t.TempDir(), "audio-check.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := audioquality.CachedResult{
		FileVersion: audioquality.FileVersion{Path: "Artist/Album/Song.mp3", Size: 123, Mod: time.Now().UnixNano()},
		Result:      audioquality.Result{Title: "Song", Artist: "Artist", Album: "Album", Codec: "mp3", Bitrate: 96},
	}
	if err := store.Save(t.Context(), "library", item); err != nil {
		t.Fatal(err)
	}
	report, err := cachedAudioCheckReport(t.Context(), store, "library", 7)
	if err != nil {
		t.Fatal(err)
	}
	if !report.resumeAvailable || report.total != 1 || report.checked != 1 || len(report.findings) != 1 {
		t.Fatalf("cached partial report = %#v", report)
	}
	if report.findings[0].file.Path != item.Path || report.findings[0].Bitrate != 96 {
		t.Fatalf("cached finding was not restored: %#v", report.findings[0])
	}
}

func TestAudioCheckWebDAVDefersHealthyFilesAndCanDecodeThem(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "song.flac")
	writeAudioCheckFixture(t, fixture, "-c:a", "flac")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var downloads atomic.Int32
	var partial atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/search3.view":
			fmt.Fprintf(w, `{"subsonic-response":{"status":"ok","searchResult3":{"song":[{"id":"id","path":"/library/song.flac","duration":180,"bitRate":450,"size":%d}]}}}`, len(data))
		case r.Method == "PROPFIND":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:">`)
			if r.URL.Path == "/library/" || r.URL.Path == "/library" {
				fmt.Fprint(w, `<d:response><d:href>/library/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
			}
			fmt.Fprintf(w, `<d:response><d:href>/library/song.flac</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>%d</d:getcontentlength><d:getlastmodified>Sat, 21 Nov 2026 12:00:00 GMT</d:getlastmodified></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, len(data))
		case r.Method == "GET" && r.URL.Path == "/library/song.flac":
			downloads.Add(1)
			if partial.Load() {
				_, _ = w.Write(data[:len(data)/2])
			} else {
				_, _ = w.Write(data)
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	cfg := &config.Config{
		Tidy:              &config.TidyConfig{Mode: 2},
		WebDAV:            &config.WebDAVConfig{WebDAVDir: "library", WebDAVUrl: server.URL},
		PlaylistOrganizer: &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "user", NavidromePassword: "password"},
	}
	old := core.GlobalWebDAV
	core.GlobalWebDAV = &core.WebDAV{Config: cfg.WebDAV, Client: gowebdav.NewClient(server.URL, "", "")}
	t.Cleanup(func() { core.GlobalWebDAV = old })
	store, err := audioquality.Open(filepath.Join(t.TempDir(), "audio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fast, err := scanAudioCheckBatch(ctx, cfg, store, "webdav", 1, false)
	if err != nil || downloads.Load() != 0 || fast.total != 1 || fast.deferred != 1 {
		t.Fatalf("ordinary batch downloaded an indexed clean file: %#v, downloads=%d err=%v", fast, downloads.Load(), err)
	}
	fullState, err := newFullAudioCheckScanState(ctx, cfg, store, "webdav", 1)
	if err != nil {
		t.Fatal(err)
	}
	if pending := fullState.cachedReport(); pending.remaining != 1 || pending.deferred != 0 {
		t.Fatalf("full scan should include quick-scan deferred files: %#v", pending)
	}
	deep, err := fullState.scanBatch(ctx, cfg, store, "webdav", true)
	if err != nil || downloads.Load() != 1 || deep.checked != 1 || len(deep.findings) != 0 || deep.unreadable != 0 {
		t.Fatalf("deep batch did not check the stored file: %#v, downloads=%d err=%v", deep, downloads.Load(), err)
	}
	if _, err := fullState.scanBatch(ctx, cfg, store, "webdav", true); err != nil || downloads.Load() != 1 {
		t.Fatalf("unchanged WebDAV file downloaded again: %d %v", downloads.Load(), err)
	}
	partial.Store(true)
	unreadable, err := scanAudioCheckBatch(ctx, cfg, store, "partial-transfer", 1, true)
	if err != nil || unreadable.unreadable != 1 || unreadable.checked != 0 || len(unreadable.findings) != 0 {
		t.Fatalf("short WebDAV read was incorrectly reported as file damage: %#v %v", unreadable, err)
	}
	if _, err := scanAudioCheckBatch(ctx, cfg, store, "partial-transfer", 1, true); err != nil || downloads.Load() != 2 {
		t.Fatalf("temporary transfer failure was immediately retried: downloads=%d err=%v", downloads.Load(), err)
	}
}

func TestAudioCheckOnlyUsesTheSameSourceAndEditionForDuration(t *testing.T) {
	original := "https://music.163.com/song?id=10"
	tags := audioquality.Result{Title: "同名 Live", Artist: "歌手", Album: "演唱会"}
	items := []web.SearchResultItem{
		{URL: "https://music.163.com/song?id=11", Name: tags.Title, Artists: tags.Artist, Album: tags.Album, DurationSec: 300},
		{URL: original, Name: "同名", Artists: tags.Artist, Album: tags.Album, DurationSec: 260},
		{URL: original, Name: tags.Title, Artists: tags.Artist, Album: "其他专辑", DurationSec: 250},
		{URL: original, Name: tags.Title, Artists: tags.Artist, Album: tags.Album, DurationSec: 180},
	}
	if got := matchingAudioSourceDuration(original, tags, items[:3]); got != 0 {
		t.Fatalf("different source or recording used as reference: %.1f", got)
	}
	if got := matchingAudioSourceDuration(original, tags, items); got != 180 {
		t.Fatalf("exact reference missed: %.1f", got)
	}
}

func TestAudioCheckUsesFilenameForDamagedSongWithoutTags(t *testing.T) {
	finding := audioCheckFinding{file: web.FileEntry{Name: "Artist - Song.flac"}, Result: audioquality.Result{Damaged: true}}
	song := finding.musicAction("/music/Artist - Song.flac", 1)
	if song.title != "Song" || song.artist != "Artist" {
		t.Fatalf("could not reconstruct an alternate source query: %#v", song)
	}
}

func TestAudioCheckReplacesAnUneditableResultCard(t *testing.T) {
	t.Chdir(t.TempDir())
	requests := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/editMessageText") {
			requests <- "edit"
			fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: message to edit not found"}`)
			return
		}
		requests <- "send"
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":43,"chat":{"id":7,"type":"private"}}}`)
	}))
	defer server.Close()
	bot, err := tb.NewBot(tb.Settings{Offline: true, Token: "1:test", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	original := audioCheckSession{owner: 7, index: 1, library: "test", message: &tb.Message{ID: 42, Chat: &tb.Chat{ID: 7}}}
	app := &BotApp{}
	if err := app.presentAudioCheck(bot, original); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || <-requests != "edit" || <-requests != "send" {
		t.Fatal("expected one edit followed by a replacement card")
	}
	session := app.audioCheckSessions[7]
	if session.message.ID != 43 || session.index != 1 || session.token == "" {
		t.Fatalf("replacement session = %+v", session)
	}
	store, err := audioquality.Open(storage.DefaultDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records, err := store.Sessions(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatalf("saved replacement sessions = %v, %v", records, err)
	}
	restored, err := decodeAudioCheckSession(records[0])
	if err != nil || restored.message.ID != 43 || restored.token != session.token {
		t.Fatalf("restored replacement = %+v, %v", restored, err)
	}
}
