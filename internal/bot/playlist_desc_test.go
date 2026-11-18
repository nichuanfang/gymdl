package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/playlistcatalog"
	tb "gopkg.in/telebot.v4"
)

func TestSyncedCatalogIsTheOnlyPlaylistList(t *testing.T) {
	store, err := playlistcatalog.Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	before, synced, err := loadTidyPlaylists(ctx, store)
	if err != nil || synced || len(before) != 0 {
		t.Fatalf("before sync = %#v synced=%v err=%v", before, synced, err)
	}
	if _, err = store.Apply(ctx, []playlistcatalog.Remote{{ID: "zh", Name: "中文"}}, []config.PlaylistInfo{{Name: "中文", Desc: "中文歌曲"}}, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	after, synced, err := loadTidyPlaylists(ctx, store)
	if err != nil || !synced || len(after) != 1 || after[0].Name != "中文" || after[0].Desc != "中文歌曲" {
		t.Fatalf("after sync = %#v synced=%v err=%v", after, synced, err)
	}
}

func TestPlaylistDescButtonsFitTelegram(t *testing.T) {
	session := pendingPlaylistDesc{
		token:  strings.Repeat("a", 32),
		synced: true,
		playlists: []playlistcatalog.Playlist{
			{ID: "zh", Name: "中文", Desc: ""},
			{ID: "en", Name: "英文", Desc: "英文歌曲"},
		},
	}
	text, markup := formatPlaylistDesc(session, "")
	if !strings.Contains(text, "还有 1 张没写描述，排在前面。") || !strings.Contains(text, "点下面的名称修改。") || strings.Contains(text, "未写") {
		t.Fatalf("list copy = %q", text)
	}
	if !buttonTexts(markup)["中文"] || !buttonTexts(markup)["英文"] || !buttonTexts(markup)["↻ 同步歌单"] || !buttonTexts(markup)["✕ 取消"] {
		t.Fatalf("labels = %#v", buttonTexts(markup))
	}
	if buttonTexts(markup)["中文 · 未写"] || buttonTexts(markup)["同步歌单"] || buttonTexts(markup)["取消"] {
		t.Fatalf("plain action labels still look like playlists: %#v", buttonTexts(markup))
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			raw, err := json.Marshal(button)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Text string `json:"text"`
				Data string `json:"callback_data"`
			}
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Data) > 64 || len([]rune(wire.Text)) == 0 || len([]rune(wire.Text)) > 64 {
				t.Fatalf("button does not fit Telegram: %s", raw)
			}
		}
	}
}

func TestPlaylistDescActionsStayOffThePlaylistRows(t *testing.T) {
	playlists := make([]playlistcatalog.Playlist, 17)
	for i := range playlists {
		playlists[i] = playlistcatalog.Playlist{ID: fmt.Sprintf("id-%d", i), Name: fmt.Sprintf("歌%02d", i)}
	}
	session := pendingPlaylistDesc{
		token:     strings.Repeat("b", 32),
		synced:    true,
		page:      1,
		playlists: playlists,
	}
	text, markup := formatPlaylistDesc(session, "")
	if !strings.Contains(text, "还有 17 张没写描述，排在前面。") || strings.Contains(text, "未写") {
		t.Fatalf("list copy = %q", text)
	}
	texts := buttonTexts(markup)
	for _, label := range []string{"◀ 上一页", "下一页 ▶", "↻ 同步歌单", "✕ 取消", "歌08"} {
		if !texts[label] {
			t.Fatalf("missing %s in %#v", label, texts)
		}
	}
	if texts["歌08 · 未写"] {
		t.Fatalf("empty description is still stuck on the name: %#v", texts)
	}
	for _, plain := range []string{"上一页", "下一页", "同步歌单", "取消"} {
		if texts[plain] {
			t.Fatalf("plain action label still looks like a playlist: %s", plain)
		}
	}
	var sawNames, sawPaging, sawActions bool
	for _, row := range markup.InlineKeyboard {
		var kinds []string
		for _, button := range row {
			raw, err := json.Marshal(button)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Text string `json:"text"`
				Data string `json:"callback_data"`
			}
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.HasPrefix(wire.Data, "open|"):
				kinds = append(kinds, "open")
				if playlistDescDisplayWidth(wire.Text) > playlistDescButtonWidth {
					t.Fatalf("name is wider than one row: %s", wire.Text)
				}
			case strings.HasPrefix(wire.Data, "page|"):
				kinds = append(kinds, "page")
			case strings.HasPrefix(wire.Data, "sync|"):
				kinds = append(kinds, "sync")
			case strings.HasPrefix(wire.Data, "cancel|"):
				kinds = append(kinds, "cancel")
			}
			if len(wire.Data) > 64 {
				t.Fatalf("callback too long: %s", raw)
			}
		}
		joined := strings.Join(kinds, ",")
		switch joined {
		case "open":
			if len(row) != 1 {
				t.Fatalf("playlist name should occupy the whole row: %s", joined)
			}
			sawNames = true
		case "page,page":
			sawPaging = true
		case "sync,cancel":
			sawActions = true
		default:
			t.Fatalf("unexpected row: %s", joined)
		}
	}
	if !sawNames || !sawPaging || !sawActions {
		t.Fatal("names, paging, and actions should be separate rows")
	}
}

func TestPlaylistDescFirstPageSharesTheSinglePagerWithActions(t *testing.T) {
	playlists := make([]playlistcatalog.Playlist, 10)
	for i := range playlists {
		playlists[i] = playlistcatalog.Playlist{ID: fmt.Sprintf("id-%d", i), Name: fmt.Sprintf("歌%02d", i), Desc: "已写"}
	}
	playlists[0].Desc = ""
	text, markup := formatPlaylistDesc(pendingPlaylistDesc{
		token:     strings.Repeat("c", 32),
		synced:    true,
		playlists: playlists,
	}, "")
	if !strings.Contains(text, "还有 1 张没写描述，排在前面。") || strings.Contains(text, "未写") {
		t.Fatalf("list copy = %q", text)
	}
	var sawToolbar bool
	for _, row := range markup.InlineKeyboard {
		kinds := playlistDescRowKinds(t, row)
		joined := strings.Join(kinds, ",")
		switch joined {
		case "open":
		case "page,sync,cancel":
			sawToolbar = true
			labels := playlistDescRowTexts(t, row)
			if labels[0] != "下一页 ▶" || labels[1] != "↻ 同步" || labels[2] != "✕ 取消" {
				t.Fatalf("toolbar = %#v", labels)
			}
			for _, label := range labels {
				if playlistDescDisplayWidth(label) > 12 {
					t.Fatalf("toolbar label will be ellipsized: %s", label)
				}
			}
		default:
			t.Fatalf("unexpected row: %s", joined)
		}
	}
	if !sawToolbar {
		t.Fatal("the only page button should share a row with sync and cancel")
	}
}

func playlistDescRowKinds(t *testing.T, row []tb.InlineButton) []string {
	t.Helper()
	var kinds []string
	for _, button := range row {
		raw, err := json.Marshal(button)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Data string `json:"callback_data"`
		}
		if err = json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasPrefix(wire.Data, "open|"):
			kinds = append(kinds, "open")
		case strings.HasPrefix(wire.Data, "page|"):
			kinds = append(kinds, "page")
		case strings.HasPrefix(wire.Data, "sync|"):
			kinds = append(kinds, "sync")
		case strings.HasPrefix(wire.Data, "cancel|"):
			kinds = append(kinds, "cancel")
		default:
			t.Fatalf("unknown button: %s", raw)
		}
	}
	return kinds
}

func playlistDescRowTexts(t *testing.T, row []tb.InlineButton) []string {
	t.Helper()
	labels := make([]string, 0, len(row))
	for _, button := range row {
		raw, err := json.Marshal(button)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Text string `json:"text"`
		}
		if err = json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		labels = append(labels, wire.Text)
	}
	return labels
}

func TestPlaylistDescButtonKeepsTheStartOfTheName(t *testing.T) {
	label := playlistDescButtonLabel(playlistcatalog.Playlist{Name: strings.Repeat("歌", 30), Desc: ""})
	if strings.Contains(label, "未写") || !strings.HasPrefix(label, "歌") || !strings.HasSuffix(label, "…") {
		t.Fatalf("label = %q", label)
	}
	if playlistDescDisplayWidth(label) > playlistDescButtonWidth {
		t.Fatalf("width %d label %q", playlistDescDisplayWidth(label), label)
	}
	if got := playlistDescButtonLabel(playlistcatalog.Playlist{Name: "周杰伦 remix"}); got != "周杰伦 remix" {
		t.Fatalf("short name changed: %q", got)
	}
}

type playlistDescTextContext struct {
	tb.Context
	text   string
	sender *tb.User
	chat   *tb.Chat
	sent   []string
}

func (c *playlistDescTextContext) Text() string     { return c.text }
func (c *playlistDescTextContext) Sender() *tb.User { return c.sender }
func (c *playlistDescTextContext) Chat() *tb.Chat   { return c.chat }
func (c *playlistDescTextContext) Bot() tb.API      { return nil }
func (c *playlistDescTextContext) Send(v interface{}, _ ...interface{}) error {
	if text, ok := v.(string); ok {
		c.sent = append(c.sent, text)
	}
	return nil
}

func TestPlaylistDescEditLetsADownloadLinkThrough(t *testing.T) {
	store, err := playlistcatalog.Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &BotApp{playlistCatalog: store}
	app.savePlaylistDesc(pendingPlaylistDesc{
		token:     "desc-token",
		userID:    7,
		chatID:    7,
		editingID: "zh",
		expiresAt: time.Now().Add(time.Minute),
		playlists: []playlistcatalog.Playlist{{ID: "zh", Name: "中文", Desc: "原来的描述"}},
	})
	ctx := &playlistDescTextContext{
		text:   "https://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV",
		sender: &tb.User{ID: 7},
		chat:   &tb.Chat{ID: 7, Type: tb.ChatPrivate},
	}
	handled, err := app.handlePlaylistDescText(ctx)
	if err != nil || handled {
		t.Fatalf("link handled=%v err=%v, want the download path", handled, err)
	}
	if len(ctx.sent) != 1 || !strings.Contains(ctx.sent[0], "这条链接会继续按下载处理") {
		t.Fatalf("edit-mode exit notice = %#v", ctx.sent)
	}
	if _, waiting := app.playlistDescAwaiting(7, 7); waiting {
		t.Fatal("edit mode still captures the next message")
	}
	if reason := app.maintenanceConflict(7, "ai"); !strings.Contains(reason, "歌单描述") {
		t.Fatalf("open description card should block another command, got %q", reason)
	}
	if reason := app.maintenanceConflict(7, "desc"); reason != "" {
		t.Fatalf("refreshing descriptions should not block itself, got %q", reason)
	}
}

func TestPlaylistDescSyncLeavesTheUpdateLoop(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }
	started := make(chan struct{})
	var startOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
		http.Error(w, strings.Repeat("内部错误", 40), http.StatusGatewayTimeout)
	}))
	t.Cleanup(func() {
		letGo()
		server.Close()
	})
	store, err := playlistcatalog.Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := &BotApp{
		cfg: &config.Config{PlaylistOrganizer: &config.PlaylistOrganizerConfig{
			NavidromeURL: server.URL, NavidromeUsername: "user", NavidromePassword: "secret",
		}},
		playlistCatalog: store,
	}
	ctx := &playlistDescSyncContext{message: &tb.Message{ID: 9}}
	session := pendingPlaylistDesc{
		token: "desc-sync", userID: 7, chatID: 7, message: ctx.message,
		editingID: "zh", expiresAt: time.Now().Add(time.Minute),
	}
	app.savePlaylistDesc(session)
	startedAt := time.Now()
	if err = app.syncPlaylistDesc(ctx, session); err != nil {
		t.Fatal(err)
	}
	if time.Since(startedAt) > time.Second {
		t.Fatalf("sync handler blocked the update loop for %s", time.Since(startedAt))
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("sync never reached Navidrome")
	}
	if app.discoveryCommandMu.TryLock() {
		app.discoveryCommandMu.Unlock()
		t.Fatal("background sync released the shared lock before it finished")
	}
	busy := &playlistDescSyncContext{message: ctx.message}
	if err = app.syncPlaylistDesc(busy, session); err != nil {
		t.Fatal(err)
	}
	if len(busy.alerts) != 1 || !strings.Contains(busy.alerts[0], "正在处理上一条请求") {
		t.Fatalf("busy response = %#v", busy.alerts)
	}
	letGo()
	ctx.waitEdited(t, "同步失败")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if app.discoveryCommandMu.TryLock() {
			app.discoveryCommandMu.Unlock()
			deadline = time.Time{}
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !deadline.IsZero() {
		t.Fatal("background sync kept the shared lock")
	}
	ctx.mu.Lock()
	edits := append([]string(nil), ctx.edits...)
	markups := append([]int(nil), ctx.markups...)
	ctx.mu.Unlock()
	if len(edits) < 2 || !strings.Contains(edits[0], "正在从 Navidrome 同步歌单列表") || markups[0] != 0 {
		t.Fatalf("progress card = %q buttons=%v", edits, markups)
	}
	last := edits[len(edits)-1]
	if !strings.Contains(last, "可稍后重试") || strings.Contains(last, "内部错误内部错误") || len([]rune(last)) > 220 {
		t.Fatalf("failure card leaked the raw error: %s", last)
	}
	if _, waiting := app.playlistDescAwaiting(7, 7); waiting {
		t.Fatal("sync left the description editor capturing the next message")
	}
}

type playlistDescSyncContext struct {
	tb.Context
	mu      sync.Mutex
	message *tb.Message
	edits   []string
	markups []int
	alerts  []string
}

func (c *playlistDescSyncContext) Message() *tb.Message { return c.message }
func (c *playlistDescSyncContext) Callback() *tb.Callback {
	return &tb.Callback{ID: "playlist-desc"}
}
func (c *playlistDescSyncContext) Respond(resp ...*tb.CallbackResponse) error {
	if len(resp) > 0 && resp[0] != nil {
		c.alerts = append(c.alerts, resp[0].Text)
	}
	return nil
}
func (c *playlistDescSyncContext) Edit(v interface{}, opts ...interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.edits = append(c.edits, fmt.Sprint(v))
	buttons := 0
	for _, opt := range opts {
		markup, ok := opt.(*tb.ReplyMarkup)
		if !ok || markup == nil {
			continue
		}
		for _, row := range markup.InlineKeyboard {
			buttons += len(row)
		}
	}
	c.markups = append(c.markups, buttons)
	return nil
}
func (c *playlistDescSyncContext) waitEdited(t *testing.T, part string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		joined := strings.Join(c.edits, "\n")
		c.mu.Unlock()
		if strings.Contains(joined, part) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t.Fatalf("timed out waiting for %q, edits: %#v", part, c.edits)
}
