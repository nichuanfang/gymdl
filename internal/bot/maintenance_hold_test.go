package bot

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/internal/playlistcatalog"
	tb "gopkg.in/telebot.v4"
)

func TestMaintenanceHoldRepliesToTheOpenCardAndOffersOneCancel(t *testing.T) {
	app := &BotApp{}
	card := &tb.Message{ID: 41, Chat: &tb.Chat{ID: 7}}
	token := strings.Repeat("ab", 16)
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: token, userID: 7, chatID: 7, message: card, expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("register")
	}
	hold, ok := app.chatMaintenanceHold(7, "")
	if !ok || hold.kind != "i" || hold.processing || hold.text != "AI 歌单还开着。" || hold.message != card {
		t.Fatalf("hold = %+v ok=%t", hold, ok)
	}
	if _, self := app.chatMaintenanceHold(7, "ai"); self {
		t.Fatal("opening an AI playlist should not report its own card as another command")
	}
	text, markup := maintenanceHoldNotice(hold, "a")
	if text != "AI 歌单还开着。" || markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
		t.Fatalf("notice = %q markup=%v", text, markup)
	}
	button := markup.InlineKeyboard[0][0]
	if button.Text != "取消并继续" || button.Unique != maintenanceHoldCallbackUnique || button.Data != "i|"+token+"|a" {
		t.Fatalf("button = %#v", button)
	}
	if payload := "\f" + button.Unique + "|" + button.Data; len(payload) > 64 {
		t.Fatalf("callback is %d bytes: %q", len(payload), payload)
	}
	ctx := &holdContext{sender: &tb.User{ID: 7}, chat: &tb.Chat{ID: 7}}
	if err := app.presentMaintenanceHold(ctx, hold, "a", nil); err != nil {
		t.Fatal(err)
	}
	if len(ctx.sent) != 1 || ctx.sent[0] != "AI 歌单还开着。" || ctx.opts == nil || ctx.opts.ReplyTo != card || ctx.opts.ReplyMarkup == nil {
		t.Fatalf("sent=%#v opts=%#v", ctx.sent, ctx.opts)
	}

	busy := hold
	busy.processing = true
	busyText, busyMarkup := maintenanceHoldNotice(busy, "a")
	if busyText != "AI 歌单正在处理，请稍等。" || busyMarkup != nil {
		t.Fatalf("busy = %q markup=%v", busyText, busyMarkup)
	}
}

func TestMaintenanceHoldCancelContinuesTheCommandThatWasBlocked(t *testing.T) {
	app := &BotApp{}
	token := strings.Repeat("cd", 16)
	card := &tb.Message{ID: 8, Chat: &tb.Chat{ID: 7}}
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: token, userID: 7, chatID: 7, message: card,
		pendingWrite: true, expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("register")
	}
	ctx := &holdContext{
		sender:  &tb.User{ID: 7},
		chat:    &tb.Chat{ID: 7, Type: tb.ChatPrivate},
		message: &tb.Message{ID: 90, Chat: &tb.Chat{ID: 7}},
		args:    []string{"i", token, "d"},
	}
	if err := app.HandleMaintenanceHoldCallback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.getAIPlaylistSession(token); ok {
		t.Fatal("cancelled AI playlist session is still open")
	}
	if len(ctx.edits) != 1 || ctx.edits[0] != "已取消。" {
		t.Fatalf("notice edits = %#v", ctx.edits)
	}
	if len(ctx.sent) != 1 || !strings.Contains(ctx.sent[0], "歌单库还没准备好") {
		t.Fatalf("continued command sent %#v", ctx.sent)
	}
	note, ok := app.releaseMaintenanceHold(maintenanceHold{kind: "i", token: token, chatID: 7})
	if ok || note != "" {
		t.Fatal("second release succeeded")
	}
}

func TestMaintenanceHoldLeavesARunningCardAlone(t *testing.T) {
	app := &BotApp{}
	token := strings.Repeat("ef", 16)
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: token, userID: 7, chatID: 7, processing: true, expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("register")
	}
	ctx := &holdContext{sender: &tb.User{ID: 7}, chat: &tb.Chat{ID: 7}, args: []string{"i", token, "a"}}
	if err := app.HandleMaintenanceHoldCallback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.getAIPlaylistSession(token); !ok || len(ctx.sent) != 0 || len(ctx.alerts) != 1 || ctx.alerts[0] != "正在处理，请稍等。" {
		t.Fatalf("session still open=%v sent=%#v alerts=%#v", ok, ctx.sent, ctx.alerts)
	}
	other := &holdContext{sender: &tb.User{ID: 8}, chat: &tb.Chat{ID: 7}, args: []string{"i", token, "a"}}
	if err := app.HandleMaintenanceHoldCallback(other); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.getAIPlaylistSession(token); !ok || len(other.alerts) != 1 || other.alerts[0] != "只有发起的人可以取消" {
		t.Fatalf("other alerts=%#v", other.alerts)
	}
}

func TestMaintenanceCommandAutoEndsAnIdleCardInPlace(t *testing.T) {
	app := &BotApp{}
	card := &tb.Message{ID: 51, Chat: &tb.Chat{ID: 7}}
	token := strings.Repeat("12", 16)
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: token, userID: 7, chatID: 7, message: card, expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("register")
	}
	api := &maintenanceTestBot{}
	ctx := &holdContext{sender: &tb.User{ID: 7}, chat: &tb.Chat{ID: 7}, bot: api}

	if hold, blocked := app.beginMaintenanceCommand(ctx, "a"); blocked {
		t.Fatalf("idle card still blocks the next command: %+v", hold)
	}
	if app.hasAIPlaylistSessionForChat(7) {
		t.Fatal("idle AI playlist session is still open")
	}
	if len(api.edits) != 1 || api.edits[0].message != card || api.edits[0].text != "已结束上一个会话。" {
		t.Fatalf("old card edits = %+v", api.edits)
	}
	if len(ctx.sent) != 0 {
		t.Fatalf("auto-switch should not send another message: %#v", ctx.sent)
	}
}

func TestMaintenanceCommandAutoEndsAnIdleDescriptionCard(t *testing.T) {
	app := &BotApp{}
	card := &tb.Message{ID: 52, Chat: &tb.Chat{ID: 7}}
	app.savePlaylistDesc(pendingPlaylistDesc{
		token: "desc", userID: 7, chatID: 7, message: card, expiresAt: time.Now().Add(time.Minute),
	})
	api := &maintenanceTestBot{}
	ctx := &holdContext{sender: &tb.User{ID: 7}, chat: &tb.Chat{ID: 7}, bot: api}

	if hold, blocked := app.beginMaintenanceCommand(ctx, "i"); blocked {
		t.Fatalf("idle description card still blocks the next command: %+v", hold)
	}
	if app.hasPlaylistDescForChat(7) {
		t.Fatal("idle description session is still open")
	}
	if len(api.edits) != 1 || api.edits[0].message != card || api.edits[0].text != "已结束上一个会话。" {
		t.Fatalf("old card edits = %+v", api.edits)
	}
}

func TestPlaylistDescCommandSwitchesFromAnIdleAICardDirectly(t *testing.T) {
	app := &BotApp{}
	store, err := playlistcatalog.Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app.playlistCatalog = store

	card := &tb.Message{ID: 55, Chat: &tb.Chat{ID: 7}}
	token := strings.Repeat("56", 16)
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: token, userID: 7, chatID: 7, message: card, expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("register")
	}
	api := &maintenanceTestBot{}
	ctx := &holdContext{
		sender: &tb.User{ID: 7},
		chat:   &tb.Chat{ID: 7, Type: tb.ChatPrivate},
		bot:    api,
	}
	if err := app.PlaylistDescCommand(ctx); err != nil {
		t.Fatal(err)
	}
	if app.hasAIPlaylistSessionForChat(7) {
		t.Fatal("idle AI playlist session is still open")
	}
	if !app.hasPlaylistDescForChat(7) {
		t.Fatal("description command did not start")
	}
	if len(api.edits) != 1 || api.edits[0].message != card || api.edits[0].text != maintenanceEndedText {
		t.Fatalf("old card edits = %+v", api.edits)
	}
	if len(api.sent) != 1 || !strings.Contains(api.sent[0].text, "歌单描述") {
		t.Fatalf("new command messages = %+v", api.sent)
	}
	if len(ctx.sent) != 0 {
		t.Fatalf("unexpected extra messages: %#v", ctx.sent)
	}
}

func TestMaintenanceCommandKeepsEmptyTrashConfirmation(t *testing.T) {
	app := &BotApp{}
	card := &tb.Message{ID: 70, Chat: &tb.Chat{ID: 7}}
	token := strings.Repeat("ab", 8)
	if !app.registerPendingEmptyTrash(pendingEmptyTrash{
		token: token, userID: 7, chatID: 7, message: card, expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("register")
	}
	api := &maintenanceTestBot{}
	ctx := &holdContext{sender: &tb.User{ID: 7}, chat: &tb.Chat{ID: 7}, bot: api}
	hold, blocked := app.beginMaintenanceCommand(ctx, "a")
	if !blocked || hold.kind != "t" || hold.processing || !strings.Contains(hold.text, "请先确认或取消") {
		t.Fatalf("hold=%+v blocked=%t", hold, blocked)
	}
	if _, ok := app.getPendingEmptyTrash(token); !ok {
		t.Fatal("confirmation was released")
	}
	if len(api.edits) != 0 {
		t.Fatalf("card was edited: %+v", api.edits)
	}
	notice, markup := maintenanceHoldNotice(hold, "a")
	if !strings.Contains(notice, "请先确认或取消") || markup == nil || markup.InlineKeyboard[0][0].Text != "取消并继续" {
		t.Fatalf("notice=%q markup=%v", notice, markup)
	}
}

func TestMaintenanceCommandKeepsAProcessingCard(t *testing.T) {
	app := &BotApp{}
	card := &tb.Message{ID: 53, Chat: &tb.Chat{ID: 7}}
	token := strings.Repeat("34", 16)
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: token, userID: 7, chatID: 7, message: card, processing: true, expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("register")
	}
	api := &maintenanceTestBot{}
	ctx := &holdContext{sender: &tb.User{ID: 7}, chat: &tb.Chat{ID: 7}, bot: api}

	hold, blocked := app.beginMaintenanceCommand(ctx, "a")
	if !blocked || !hold.processing || hold.busyText != "AI 歌单正在处理，请稍等。" {
		t.Fatalf("hold = %+v blocked=%t", hold, blocked)
	}
	if !app.hasAIPlaylistSessionForChat(7) {
		t.Fatal("processing AI playlist session was released")
	}
	if len(api.edits) != 0 {
		t.Fatalf("processing card was edited: %+v", api.edits)
	}
}

func TestMaintenanceHoldCancelMatchesTheCardCancelCopy(t *testing.T) {
	app := &BotApp{}
	now := time.Now().Add(time.Minute)
	if !app.registerPlaylistAssignment(pendingPlaylistAssignment{
		token: "assign", userID: 3, chatID: 3, processedCount: 2, expiresAt: now,
	}) {
		t.Fatal("assign")
	}
	if !app.registerPendingEmptyTrash(pendingEmptyTrash{token: "trash", userID: 3, chatID: 4, expiresAt: now}) {
		t.Fatal("trash")
	}
	app.savePlaylistDesc(pendingPlaylistDesc{token: "desc", userID: 3, chatID: 5, expiresAt: now})
	note, ok := app.releaseMaintenanceHold(maintenanceHold{kind: "a", token: "assign", chatID: 3})
	if !ok || !strings.Contains(note, "已确认：2 首") || app.hasPlaylistAssignmentForChat(3) {
		t.Fatalf("assign note=%q ok=%t", note, ok)
	}
	note, ok = app.releaseMaintenanceHold(maintenanceHold{kind: "t", token: "trash", chatID: 4})
	if !ok || note != "已取消清空回收站。" || app.hasPendingEmptyTrash(4) {
		t.Fatalf("trash note=%q ok=%t", note, ok)
	}
	app.setPlaylistDescSyncing("desc", true)
	if _, ok = app.releaseMaintenanceHold(maintenanceHold{kind: "d", token: "desc", chatID: 5}); ok {
		t.Fatal("syncing description was cancelled")
	}
	app.setPlaylistDescSyncing("desc", false)
	note, ok = app.releaseMaintenanceHold(maintenanceHold{kind: "d", token: "desc", chatID: 5})
	if !ok || note != "已取消。" || app.hasPlaylistDescForChat(5) {
		t.Fatalf("desc note=%q ok=%t", note, ok)
	}
	if reason := app.maintenanceConflict(5, "ai"); reason != "" {
		t.Fatalf("conflict after cancel = %q", reason)
	}
}

type holdContext struct {
	tb.Context
	sender  *tb.User
	chat    *tb.Chat
	message *tb.Message
	bot     tb.API
	args    []string
	sent    []string
	edits   []string
	alerts  []string
	opts    *tb.SendOptions
}

func (c *holdContext) Sender() *tb.User     { return c.sender }
func (c *holdContext) Chat() *tb.Chat       { return c.chat }
func (c *holdContext) Message() *tb.Message { return c.message }
func (c *holdContext) Args() []string       { return c.args }
func (c *holdContext) Bot() tb.API          { return c.bot }
func (c *holdContext) Send(v interface{}, opts ...interface{}) error {
	c.sent = append(c.sent, fmt.Sprint(v))
	for _, opt := range opts {
		if send, ok := opt.(*tb.SendOptions); ok {
			c.opts = send
		}
	}
	return nil
}
func (c *holdContext) Edit(v interface{}, _ ...interface{}) error {
	c.edits = append(c.edits, fmt.Sprint(v))
	return nil
}
func (c *holdContext) Respond(resp ...*tb.CallbackResponse) error {
	if len(resp) > 0 && resp[0] != nil {
		c.alerts = append(c.alerts, resp[0].Text)
	}
	return nil
}

type maintenanceTestEdit struct {
	message *tb.Message
	text    string
}

type maintenanceTestBot struct {
	tb.API
	edits []maintenanceTestEdit
	sent  []maintenanceTestEdit
}

func (b *maintenanceTestBot) Edit(editable tb.Editable, value interface{}, _ ...interface{}) (*tb.Message, error) {
	message, _ := editable.(*tb.Message)
	b.edits = append(b.edits, maintenanceTestEdit{message: message, text: fmt.Sprint(value)})
	return message, nil
}

func (b *maintenanceTestBot) Send(to tb.Recipient, value interface{}, _ ...interface{}) (*tb.Message, error) {
	message := &tb.Message{ID: 70 + len(b.sent), Chat: &tb.Chat{ID: 7}}
	b.sent = append(b.sent, maintenanceTestEdit{message: message, text: fmt.Sprint(value)})
	return message, nil
}
