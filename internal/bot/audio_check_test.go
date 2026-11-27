package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/nichuanfang/gymdl/internal/audiodupe"
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
	api, err := tb.NewBot(tb.Settings{Offline: true, Synchronous: true, Token: "1:test", URL: server.URL, OnError: func(err error, _ tb.Context) { t.Errorf("Telegram handler: %v", err) }})
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
	if !strings.Contains(first.text, "已检查 1/2 首") || !strings.Contains(first.text, "Test &amp; Song") || !strings.Contains(first.text, "低码率") {
		t.Fatalf("unexpected first card: %s", first.text)
	}
	button(first, "▶️ 试听")
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
	next("editMessageText") // Progress on the original card.
	second := next("editMessageText")
	if !strings.Contains(second.text, "已检查 2/2 首") {
		t.Fatalf("review did not open next finding: %q", second.text)
	}
	waitScan()
	click(normal, user, 42)
	if got := next("answerCallbackQuery"); !strings.Contains(got.text, "卡片已更新") {
		t.Fatalf("old callback moved the new card: %q", got.text)
	}
	app.audioCheckMu.Lock()
	session := app.audioCheckSessions[chat.ID]
	session.expires = time.Now().Add(-time.Second)
	app.audioCheckSessions[chat.ID] = session
	app.audioCheckMu.Unlock()
	click(button(second, "标记正常"), user, 42)
	if got := next("answerCallbackQuery"); !strings.Contains(got.text, "已过期") {
		t.Fatalf("expired callback accepted: %q", got.text)
	}
	command()
	rescan := next("editMessageText")
	waitScan()
	if !strings.Contains(rescan.text, "已检查 1/2 首") || !strings.Contains(rescan.text, "低码率") {
		t.Fatalf("reviewed file reappeared on next scan: %q", rescan.text)
	}
}

func TestAudioCheckCardsUseOneContinueActionAndFocusedReviewControls(t *testing.T) {
	session := audioCheckSession{
		token:  "scan-token",
		report: audioCheckReport{total: 10, checked: 4, remaining: 3, deferred: 2, referenceQueue: 1},
	}
	text, markup := formatAudioCheckCard(session)
	if !strings.Contains(text, "已检查 4/10 首") || !strings.Contains(text, "5 首待检查") {
		t.Fatalf("result card lost progress: %s", text)
	}
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
		t.Fatalf("result card should offer one next action: %#v", markup.InlineKeyboard)
	}
	next := markup.InlineKeyboard[0][0]
	if next.Text != "继续体检" || !strings.HasSuffix(next.Data, "continue|scan-token") {
		t.Fatalf("unexpected next action: %#v", next)
	}
	for _, hidden := range []string{"继续扫描", "深度扫描", "全库体检"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("result card still exposes scan mode %q: %s", hidden, text)
		}
	}

	text, markup = formatAudioCheckProgressCard(session)
	if strings.Contains(text, "全库") || strings.Contains(text, "深度") {
		t.Fatalf("progress card still exposes scan mode: %s", text)
	}
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
		t.Fatalf("progress card should have one stop control: %#v", markup.InlineKeyboard)
	}
	stop := markup.InlineKeyboard[0][0]
	if stop.Text != "停止" || !strings.HasSuffix(stop.Data, "stop|scan-token") {
		t.Fatalf("unexpected stop control: %#v", stop)
	}

	session.running = false
	_, markup = formatAudioCheckCard(session)
	if got := markup.InlineKeyboard[len(markup.InlineKeyboard)-1][0].Text; got != "继续体检" {
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

func writeAudioCheckFingerprintFixture(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"ffprobe", "ffmpeg", "fpcalc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("requires " + tool)
		}
	}
	file := filepath.Join(t.TempDir(), "recording.flac")
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "anoisesrc=seed=23:d=12", "-c:a", "flac", file)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create fingerprint fixture: %v %s", err, output)
	}
	return file
}

func TestAudioCheckWebDAVReusesFileAndHandlesPartialTransfers(t *testing.T) {
	fixture := writeAudioCheckFingerprintFixture(t)
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var downloads, listings atomic.Int32
	var partial, blockListing atomic.Bool
	listingStarted := make(chan struct{}, 1)
	releaseListing := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "OPTIONS":
			w.WriteHeader(http.StatusOK)
		case r.Method == "PROPFIND":
			if blockListing.Load() && r.Header.Get("Depth") == "1" {
				_, _ = io.Copy(io.Discard, r.Body)
				listingStarted <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-releaseListing:
				}
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:">`)
			if r.URL.Path == "/library/" || r.URL.Path == "/library" {
				if r.Header.Get("Depth") == "1" {
					listings.Add(1)
				}
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
	defer close(releaseListing)
	cfg := &config.Config{
		Tidy:              &config.TidyConfig{Mode: 2},
		WebDAV:            &config.WebDAVConfig{WebDAVDir: "library", WebDAVUrl: server.URL},
		PlaylistOrganizer: &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "user", NavidromePassword: "password"},
	}
	old := core.GlobalWebDAV
	client := gowebdav.NewClient(server.URL, "", "")
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	core.GlobalWebDAV = &core.WebDAV{Config: cfg.WebDAV, Client: client}
	t.Cleanup(func() { core.GlobalWebDAV = old })
	store, err := audioquality.Open(filepath.Join(t.TempDir(), "audio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fingerprints, err := audiodupe.Open(filepath.Join(t.TempDir(), "fingerprints.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fingerprints.Close()
	load := func(library string) *audioCheckScanState {
		t.Helper()
		state, err := newAudioCheckScanState(ctx, cfg, store, library, 1, true)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	scan := func(state *audioCheckScanState, library string) {
		t.Helper()
		finding, err := state.scanNext(ctx, cfg, store, fingerprints, audioCheckSession{library: library}, func(audioCheckReport) {})
		if err != nil || finding != nil {
			t.Fatalf("finding = %+v, err = %v", finding, err)
		}
	}
	state := load("webdav")
	scan(state, "webdav")
	scan(state, "webdav")
	if downloads.Load() != 1 || listings.Load() != 1 || state.report.checked != 1 || state.report.unfingerprinted != 0 {
		t.Fatalf("inspection and fingerprint should share one download and one directory snapshot: %+v, GET=%d PROPFIND=%d", state.report, downloads.Load(), listings.Load())
	}
	scan(load("webdav"), "webdav")
	if downloads.Load() != 1 {
		t.Fatalf("unchanged WebDAV file downloaded again: %d", downloads.Load())
	}
	// A short read is a transfer failure, not a verdict on the audio: it must
	// not be counted as unreadable, and the next scan must try again instead
	// of trusting the cached result.
	partial.Store(true)
	before := downloads.Load()
	state = load("partial-transfer")
	scan(state, "partial-transfer")
	if state.report.deferred != 1 || state.report.unreadable != 0 || state.report.checked != 0 {
		t.Fatalf("short WebDAV read should be deferred, not reported as damage: %+v", state.report)
	}
	if downloads.Load() != before+1 {
		t.Fatalf("short read should have downloaded once: GET=%d", downloads.Load())
	}
	state = load("partial-transfer")
	scan(state, "partial-transfer")
	if downloads.Load() != before+2 {
		t.Fatalf("a transfer failure must be retried on the next scan: GET=%d", downloads.Load())
	}
	blockListing.Store(true)
	listingCtx, cancelListing := context.WithCancel(ctx)
	defer cancelListing()
	listed := make(chan error, 1)
	go func() {
		_, err := newAudioCheckScanState(listingCtx, cfg, store, "webdav", 1, true)
		listed <- err
	}()
	select {
	case <-listingStarted:
		cancelListing()
	case <-ctx.Done():
		t.Fatal("WebDAV listing did not start")
	}
	select {
	case err := <-listed:
		if err != context.Canceled {
			t.Fatalf("canceling the listing returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled WebDAV listing did not return")
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
	started := time.Now()
	if err := app.presentAudioCheck(bot, original); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || <-requests != "edit" || <-requests != "send" {
		t.Fatal("expected one edit followed by a replacement card")
	}
	session := app.audioCheckSessions[7]
	if session.message.ID != 43 || session.index != 0 || session.token == "" {
		t.Fatalf("replacement session = %+v", session)
	}
	if session.expires.Before(started.Add(12*time.Hour)) || session.expires.After(time.Now().Add(12*time.Hour)) {
		t.Fatalf("replacement expiry = %v, want 12 hours after presentation", session.expires)
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
	if err != nil || restored.message.ID != 43 || restored.token != session.token || restored.expires.Unix() != session.expires.Unix() {
		t.Fatalf("restored replacement = %+v, %v", restored, err)
	}
}

func TestAudioCheckCardHidesTailGlitches(t *testing.T) {
	session := audioCheckSession{
		token: "card",
		report: audioCheckReport{
			total: 2, checked: 1, unreadable: 1,
			findings: []audioCheckFinding{{
				file:   web.FileEntry{Name: "A - Song.flac", Artist: "A"},
				Result: audioquality.Result{Title: "Song", Artist: "A", Codec: "flac", Damaged: true, Duration: 264.736, Decoded: 264.707},
			}},
		},
	}
	text, _ := formatAudioCheckCard(session)
	if strings.Contains(text, "文件损坏") || strings.Contains(text, "Song") || strings.Contains(text, "未能检查") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "已检查的文件暂未发现问题") || !strings.Contains(text, "暂时没读到") {
		t.Fatal(text)
	}
	session.report.findings[0].Result = audioquality.Result{Title: "Cut", Artist: "A", Codec: "flac", Damaged: true, Duration: 200, Decoded: 120}
	text, _ = formatAudioCheckCard(session)
	if !strings.Contains(text, "文件损坏") || !strings.Contains(text, "解码 2:00／标注 3:20") {
		t.Fatal(text)
	}
}

func TestAudioCheckReviewsDuplicateBeforeScanningNextFile(t *testing.T) {
	fixture := writeAudioCheckFingerprintFixture(t)
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"a.flac", "b.flac"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "c.flac"), []byte("broken audio"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Tidy: &config.TidyConfig{Mode: 1, DistDir: dir}}
	database := filepath.Join(t.TempDir(), "audit.sqlite")
	store, err := audioquality.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fingerprints, err := audiodupe.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer fingerprints.Close()
	ctx := t.Context()
	state, err := newAudioCheckScanState(ctx, cfg, store, "library", 7, true)
	if err != nil {
		t.Fatal(err)
	}
	session := audioCheckSession{library: "library", owner: 7}
	progress := func(audioCheckReport) {}
	finding, err := state.scanNext(ctx, cfg, store, fingerprints, session, progress)
	if err != nil || finding == nil || finding.duplicate == nil || finding.duplicate.Kind != audiodupe.KindSame || state.cursor != 2 {
		t.Fatalf("expected the duplicate before reading c.flac, cursor=%d, finding=%+v, err=%v", state.cursor, finding, err)
	}
	cached, err := store.Results(ctx, "library", 7)
	if err != nil || len(cached) != 2 {
		t.Fatalf("expected exactly two inspected files, got %d, err=%v", len(cached), err)
	}
	pair := audioCheckFilePair(finding.file, finding.duplicate.File)
	review := audioquality.DuplicateReview{First: pair.First, Second: pair.Second}
	if err := store.SaveDuplicateReview(ctx, "library", 7, review); err != nil {
		t.Fatal(err)
	}
	state.duplicates.review(review)
	finding, err = state.scanNext(ctx, cfg, store, fingerprints, session, progress)
	if err != nil || finding == nil || finding.file.Name != "c.flac" || !finding.Damaged {
		t.Fatalf("continue did not reach the next finding: %+v, %v", finding, err)
	}
	session.skipped = map[audioquality.FileVersion]struct{}{audioFileVersion(finding.file): {}}
	state, err = newAudioCheckScanState(ctx, cfg, store, "library", 7, true)
	if err != nil {
		t.Fatal(err)
	}
	finding, err = state.scanNext(ctx, cfg, store, fingerprints, session, progress)
	if err != nil || finding != nil {
		t.Fatalf("a reviewed pair reappeared after reloading the index: %+v, %v", finding, err)
	}
}
