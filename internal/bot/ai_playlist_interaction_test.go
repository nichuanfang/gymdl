package bot

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tb "gopkg.in/telebot.v4"
)

type aiPlaylistInteractionTestContext struct {
	tb.Context
	args     []string
	sender   *tb.User
	callback *tb.Callback
	chat     *tb.Chat
	response int
	edited   interface{}
	editErr  error
}

func (c *aiPlaylistInteractionTestContext) Args() []string { return c.args }

func (c *aiPlaylistInteractionTestContext) Sender() *tb.User { return c.sender }

func (c *aiPlaylistInteractionTestContext) Callback() *tb.Callback { return c.callback }

func (c *aiPlaylistInteractionTestContext) Chat() *tb.Chat { return c.chat }

func (c *aiPlaylistInteractionTestContext) Respond(...*tb.CallbackResponse) error {
	c.response++
	return nil
}

func (c *aiPlaylistInteractionTestContext) Edit(what interface{}, _ ...interface{}) error {
	c.edited = what
	return c.editErr
}

func TestAIPlaylistInitialMenuKeepsCancelAndShortResumeCallbacks(t *testing.T) {
	session := pendingAIPlaylistSession{
		token:           strings.Repeat("a", 32),
		resumePlaylists: []aiPlaylist{{ID: strings.Repeat("b", 32), Name: "[AI] 长歌单名称"}},
		resumePlans:     map[string]aiPlaylistResumePlan{strings.Repeat("b", 32): {}},
	}
	markup := aiPlaylistSessionMarkup(session)
	if len(markup.InlineKeyboard) != 4 {
		t.Fatalf("keyboard rows = %d, want new playlist, review, resume, cancel", len(markup.InlineKeyboard))
	}
	if got := markup.InlineKeyboard[len(markup.InlineKeyboard)-1][0].Text; got != "取消" {
		t.Fatalf("final button = %q, want cancel", got)
	}
	if got := markup.InlineKeyboard[0][0].Text; got != "＋ 新建 AI 歌单" {
		t.Fatalf("first menu action = %q, want create playlist", got)
	}
	if got := markup.InlineKeyboard[1][0].Text; got != "检查普通歌单" {
		t.Fatalf("review action = %q", got)
	}
	resume := markup.InlineKeyboard[2][0]
	if !strings.HasSuffix(resume.Data, "|0") {
		t.Fatalf("resume callback should carry a short session index, got %q", resume.Data)
	}
	assertTelegramButtons(t, markup)
}

func TestAIPlaylistThemeKeyboardFitsTelegramCallbackLimit(t *testing.T) {
	session := pendingAIPlaylistSession{
		token:  strings.Repeat("a", 32),
		target: aiPlaylist{ID: "playlist", Name: "[AI] 夜间", Description: "安静", SongCount: 12},
	}
	markup := aiPlaylistSessionMarkup(session)
	assertTelegramButtons(t, markup)
	found := false
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if strings.HasSuffix(button.Data, "|inst") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("instrumental theme callback was not shortened")
	}
}

func assertTelegramButtons(t *testing.T, markup *tb.ReplyMarkup) {
	t.Helper()
	if markup == nil {
		t.Fatal("missing keyboard")
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.URL != "" {
				continue
			}
			wire := "\f" + button.Unique + "|" + button.Data
			if len(wire) == 0 || len(wire) > 64 {
				t.Errorf("callback_data is %d bytes, Telegram allows 1-64: %q", len(wire), wire)
			}
			if n := len([]rune(button.Text)); n == 0 || n > 64 {
				t.Errorf("button text is %d characters, Telegram allows 1-64: %q", n, button.Text)
			}
		}
	}
}

func TestAIPlaylistUnavailableMenuStillHasCancelButton(t *testing.T) {
	markup := aiPlaylistSessionMarkup(pendingAIPlaylistSession{token: "token"})
	if len(markup.InlineKeyboard) != 3 || markup.InlineKeyboard[0][0].Text != "＋ 新建 AI 歌单" || markup.InlineKeyboard[2][0].Text != "取消" {
		t.Fatalf("unavailable menu should still allow cancel: %#v", markup.InlineKeyboard)
	}
}

func TestAIPlaylistNewFlowAsksNameBeforeThemeAndKeepsPreviewNonMutating(t *testing.T) {
	app := &BotApp{}
	message := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 10}}
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: "token", userID: 20, chatID: 10, message: message,
		playlists: []aiPlaylist{{ID: "existing", Name: "[AI] Existing"}},
		expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("failed to register AI playlist session")
	}
	ctx := &aiPlaylistInteractionTestContext{
		args: []string{"new", "token"}, sender: &tb.User{ID: 20},
		callback: &tb.Callback{Message: message}, chat: message.Chat,
	}
	if err := app.HandleAIPlaylistCallback(ctx); err != nil {
		t.Fatalf("new playlist callback failed: %v", err)
	}
	session, ok := app.getAIPlaylistSession("token")
	if !ok || !session.creating || !session.awaitingName || session.newPlaylistName != "" {
		t.Fatalf("new playlist should request a name first: %#v, exists=%t", session, ok)
	}
	if messageText := formatAIPlaylistSession(session); !strings.Contains(messageText, "确认预览前不会创建歌单") {
		t.Fatalf("name prompt should explain the no-side-effect preview: %q", messageText)
	}
	name, err := normalizeAIPlaylistName("  夜间散步  ")
	if err != nil || name != "[AI] 夜间散步" {
		t.Fatalf("normalize playlist name = %q, err=%v", name, err)
	}
	if name, err := normalizeAIPlaylistName("[ai] Chill"); err != nil || name != "[AI] Chill" {
		t.Fatalf("existing prefix should be normalized once: %q, err=%v", name, err)
	}
	for _, invalid := range []string{"", "[AI]", strings.Repeat("名", 89)} {
		if _, err := normalizeAIPlaylistName(invalid); err == nil {
			t.Errorf("invalid name %q was accepted", invalid)
		}
	}
	if _, ok := app.setAIPlaylistNewName("token", name); !ok {
		t.Fatal("failed to set new playlist name")
	}
	session, _ = app.getAIPlaylistSession("token")
	if !session.creating || !session.awaitingInput || session.newPlaylistName != name {
		t.Fatalf("new playlist should now request a theme: %#v", session)
	}
	if markup := aiPlaylistSessionMarkup(session); len(markup.InlineKeyboard) < 2 {
		t.Fatalf("new playlist theme should offer presets: %#v", markup.InlineKeyboard)
	}
	preview := session
	preview.draft = &aiPlaylistDraft{Description: "quiet evening", Plan: aiPlaylistPlan{Playlist: []aiPlaylistPlanItem{{ID: "song1"}}}}
	messageText := formatAIPlaylistSession(preview)
	if !strings.Contains(messageText, "确认后才会创建这张歌单") || strings.Contains(messageText, "替换当前") {
		t.Fatalf("new playlist preview should describe create-on-confirm accurately: %q", messageText)
	}
	if got := aiPlaylistSessionMarkup(preview).InlineKeyboard[1][0].Text; got != "✅ 确认创建" {
		t.Fatalf("new playlist confirmation button = %q", got)
	}
}

func TestAIPlaylistSeedButtonKeepsTheNextLineAsASong(t *testing.T) {
	app := &BotApp{}
	message := &tb.Message{ID: 7, Chat: &tb.Chat{ID: 10}}
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: "token", userID: 20, chatID: 10, message: message,
		target:    aiPlaylist{ID: "playlist-1", Name: "[AI] 夜间"},
		expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("failed to register session")
	}
	ctx := &aiPlaylistInteractionTestContext{
		args: []string{"seed", "token"}, sender: &tb.User{ID: 20},
		callback: &tb.Callback{Message: message}, chat: message.Chat,
	}
	if err := app.HandleAIPlaylistCallback(ctx); err != nil {
		t.Fatal(err)
	}
	session, ok := app.getAIPlaylistSession("token")
	if !ok || !session.awaitingSeed || !session.awaitingInput || !strings.Contains(session.notice, "晴天 - 周杰伦") {
		t.Fatalf("seed prompt = %#v", session)
	}
	if got := composeAIPlaylistFollowup(session, "晴天"); got != "像这首歌 晴天" {
		t.Fatalf("plain title = %q", got)
	}
	if got := composeAIPlaylistFollowup(session, "像这首歌 晴天 - 周杰伦"); got != "像这首歌 晴天 - 周杰伦" {
		t.Fatalf("prefixed title = %q", got)
	}
	session.request = "适合通勤"
	if got := composeAIPlaylistFollowup(session, "晴天 - 周杰伦"); got != "像这首歌 晴天 - 周杰伦\n适合通勤" {
		t.Fatalf("kept theme = %q", got)
	}
	ctx.args = []string{"themes", "token"}
	if err := app.HandleAIPlaylistCallback(ctx); err != nil {
		t.Fatal(err)
	}
	returned, ok := app.getAIPlaylistSession("token")
	if !ok || returned.awaitingSeed || returned.notice != "" || !returned.awaitingInput {
		t.Fatalf("returning to themes = %#v", returned)
	}
	returned.awaitingSeed = true
	returned.notice = aiPlaylistSeedNotice()
	app.updateAIPlaylistSession(returned)
	session = returned
	session.draft = &aiPlaylistDraft{Description: "seed", Plan: aiPlaylistPlan{Playlist: []aiPlaylistPlanItem{{ID: "song-near"}}}}
	session.awaitingSeed = true
	app.updateAIPlaylistSession(session)
	updated, ok := app.setAIPlaylistAwaitingInput("token", true, "请补充")
	if !ok || updated.awaitingSeed {
		t.Fatalf("refine should leave seed mode: %#v", updated)
	}
	markup := aiPlaylistSessionMarkup(pendingAIPlaylistSession{token: strings.Repeat("a", 32), target: aiPlaylist{ID: "playlist", Name: "[AI] 夜间"}})
	assertTelegramButtons(t, markup)
	foundSeed := false
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.Text == "像这首歌" && strings.Contains(button.Data, "seed|") {
				foundSeed = true
			}
			if button.Text == "像这首歌" && len(row) != 2 {
				t.Fatalf("seed button should share its row, got %#v", row)
			}
		}
	}
	if !foundSeed {
		t.Fatal("seed button missing")
	}
	waiting := pendingAIPlaylistSession{
		token: strings.Repeat("b", 32), awaitingSeed: true, awaitingInput: true,
		notice: aiPlaylistSeedNotice(),
		target: aiPlaylist{ID: "playlist", Name: "[AI]中国风", Description: "国风歌单", SongCount: 74},
	}
	seedMarkup := aiPlaylistSessionMarkup(waiting)
	assertTelegramButtons(t, seedMarkup)
	var seedLabels []string
	for _, row := range seedMarkup.InlineKeyboard {
		for _, button := range row {
			seedLabels = append(seedLabels, button.Text)
			if button.Text == "通勤节奏" || button.Text == "像这首歌" || button.Text == "删除歌单" || button.Text == "按备注生成预览" {
				t.Fatalf("seed mode should hide themes and delete, got %q", button.Text)
			}
		}
	}
	if strings.Join(seedLabels, ",") != "返回主题,更换歌单,取消" {
		t.Fatalf("seed controls = %#v", seedLabels)
	}
	card := formatAIPlaylistSession(waiting)
	if strings.Contains(card, "点下面的主题") || !strings.Contains(card, "例如：晴天 - 周杰伦") || strings.Count(card, "晴天") != 1 {
		t.Fatalf("seed card = %q", card)
	}
	if !strings.Contains(card, "当前有 74 首") {
		t.Fatalf("seed card should keep the replace warning: %q", card)
	}
}

func TestAIPlaylistPresetCallbacksFitTelegramLimit(t *testing.T) {
	markup := aiPlaylistSessionMarkup(pendingAIPlaylistSession{
		token:  strings.Repeat("a", 32),
		target: aiPlaylist{ID: "playlist", Name: "[AI] 夜间", Description: "舒缓音乐"},
	})
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if len(button.Data) > 64 {
				t.Errorf("callback data exceeds Telegram limit: %d bytes (%q)", len(button.Data), button.Data)
			}
		}
	}
}

func TestAIPlaylistCallbackSelectsOnlyItsOriginalSession(t *testing.T) {
	app := &BotApp{}
	message := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 10}}
	session := pendingAIPlaylistSession{
		token:     "token",
		userID:    20,
		chatID:    10,
		message:   message,
		playlists: []aiPlaylist{{ID: "playlist-1", Name: "[AI] 夜间"}},
		expiresAt: time.Now().Add(time.Minute),
	}
	if !app.registerAIPlaylistSession(session) {
		t.Fatal("failed to register AI playlist session")
	}
	ctx := &aiPlaylistInteractionTestContext{
		args:     []string{"select", "token", "0"},
		sender:   &tb.User{ID: 20},
		callback: &tb.Callback{Message: message},
		chat:     message.Chat,
	}
	if err := app.HandleAIPlaylistCallback(ctx); err != nil {
		t.Fatalf("select callback failed: %v", err)
	}
	updated, ok := app.getAIPlaylistSession("token")
	if !ok || updated.target.ID != "playlist-1" || !updated.awaitingInput {
		t.Fatalf("selected session = %#v, exists=%t", updated, ok)
	}
	if ctx.response != 1 || ctx.edited == nil {
		t.Fatalf("callback response/edit missing: response=%d edited=%v", ctx.response, ctx.edited)
	}
}

func TestAIPlaylistRejectsUnauthorizedAndMismatchedCallbacks(t *testing.T) {
	app := &BotApp{}
	message := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 10}}
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token:     "token",
		userID:    20,
		chatID:    10,
		message:   message,
		playlists: []aiPlaylist{{ID: "playlist-1", Name: "[AI] 夜间"}},
		expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("failed to register AI playlist session")
	}
	for name, test := range map[string]struct {
		senderID int64
		message  *tb.Message
	}{
		"another user":    {senderID: 99, message: message},
		"another message": {senderID: 20, message: &tb.Message{ID: 43, Chat: message.Chat}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := &aiPlaylistInteractionTestContext{
				args:     []string{"select", "token", "0"},
				sender:   &tb.User{ID: test.senderID},
				callback: &tb.Callback{Message: test.message},
				chat:     message.Chat,
			}
			if err := app.HandleAIPlaylistCallback(ctx); err != nil {
				t.Fatalf("callback handling failed: %v", err)
			}
			if ctx.response != 1 || ctx.edited != nil {
				t.Fatalf("rejected callback changed message: response=%d edited=%v", ctx.response, ctx.edited)
			}
			if _, ok := app.getAIPlaylistSession("token"); !ok {
				t.Fatal("rejected callback consumed the session")
			}
		})
	}
}

func TestSelectingPlaylistRollsBackWhenTelegramRejectsTheKeyboard(t *testing.T) {
	app := &BotApp{}
	message := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 10}}
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token:     "token",
		userID:    20,
		chatID:    10,
		message:   message,
		playlists: []aiPlaylist{{ID: "playlist-1", Name: "[AI] 夜间"}},
		expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("failed to register")
	}
	ctx := &aiPlaylistInteractionTestContext{
		args:     []string{"select", "token", "0"},
		sender:   &tb.User{ID: 20},
		callback: &tb.Callback{Message: message},
		chat:     message.Chat,
		editErr:  errors.New("telegram: BUTTON_DATA_INVALID (400)"),
	}
	if err := app.HandleAIPlaylistCallback(ctx); err != nil {
		t.Fatal(err)
	}
	session, ok := app.getAIPlaylistSession("token")
	if !ok || session.target.ID != "" {
		t.Fatalf("rejected keyboard left the session selected: %#v", session.target)
	}
}

func TestAIPlaylistMessageUsesPlainTextHouseStyle(t *testing.T) {
	session := pendingAIPlaylistSession{
		target: aiPlaylist{ID: "playlist", Name: "[AI] <夜间>", Description: "雨天 & 安静"},
		draft: &aiPlaylistDraft{
			Description: "雨天 & 安静",
			Plan: aiPlaylistPlan{
				Intro:    "适合夜间聆听",
				Playlist: []aiPlaylistPlanItem{{ID: "song", Reason: "贴合安静氛围"}},
			},
			Candidates: []aiPlaylistCandidate{{ID: "song", Title: "<Song>", Artist: "A & B"}},
		},
	}
	message := formatAIPlaylistSession(session)
	for _, expected := range []string{"歌单预览", "[AI] <夜间>", "<Song>", "A & B", "贴合安静氛围"} {
		if !strings.Contains(message, expected) {
			t.Errorf("message %q does not contain %q", message, expected)
		}
	}
	if strings.Contains(message, "&lt;") || strings.Contains(message, "&amp;") {
		t.Fatalf("plain-text message should not HTML-escape values: %q", message)
	}
}

func TestAIPlaylistCanSelectPopulatedTargetAndClearlyConfirmReplacement(t *testing.T) {
	playlist := aiPlaylist{ID: "playlist", Name: "[AI] 夜间", Description: "舒缓的夜间音乐", SongCount: 28}
	menu := pendingAIPlaylistSession{token: "token", playlists: []aiPlaylist{playlist}}
	if message := formatAIPlaylistSession(menu); message != "🎵 AI 歌单" {
		t.Fatalf("menu should stay a title and the playlist buttons: %q", message)
	}
	markup := aiPlaylistSessionMarkup(menu)
	if got := markup.InlineKeyboard[2][0].Text; got != "夜间 · 28 首" {
		t.Fatalf("populated target label = %q", got)
	}
	selected := pendingAIPlaylistSession{
		target: playlist,
		draft: &aiPlaylistDraft{
			Description:     "夜间舒缓",
			ExistingSongIDs: []string{"old1", "old2"},
			Plan:            aiPlaylistPlan{Playlist: []aiPlaylistPlanItem{{ID: "new1"}}},
			Candidates:      []aiPlaylistCandidate{{ID: "new1", Title: "New song", Artist: "Artist"}},
		},
	}
	if message := formatAIPlaylistSession(selected); !strings.Contains(message, "当前 2 首") || !strings.Contains(message, "确认后会替换当前 2 首") {
		t.Fatalf("preview should state the exact replacement impact: %q", message)
	}
	if got := aiPlaylistSessionMarkup(selected).InlineKeyboard[1][0].Text; got != "✅ 确认替换" {
		t.Fatalf("confirmation button = %q, want explicit replacement", got)
	}
}

func TestAIPlaylistTargetCanChangeBeforeWriteButNotDuringResume(t *testing.T) {
	app := &BotApp{}
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token:     "token",
		userID:    20,
		chatID:    10,
		playlists: []aiPlaylist{{ID: "one"}, {ID: "two"}},
		expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("failed to register AI playlist session")
	}
	if _, ok := app.selectAIPlaylistTarget("token", aiPlaylist{ID: "one"}); !ok {
		t.Fatal("failed to select first target")
	}
	if _, ok := app.resetAIPlaylistTarget("token"); !ok {
		t.Fatal("expected user to be able to change an unwritten target")
	}
	if _, ok := app.selectAIPlaylistTarget("token", aiPlaylist{ID: "two"}); !ok {
		t.Fatal("failed to select replacement target")
	}
	app.aiPlaylistSessionMu.Lock()
	current := app.aiPlaylistSessions["token"]
	current.pendingWrite = true
	app.aiPlaylistSessions["token"] = current
	app.aiPlaylistSessionMu.Unlock()
	if _, ok := app.resetAIPlaylistTarget("token"); ok {
		t.Fatal("a resumable write must remain bound to its original target")
	}
}

func TestAIPlaylistPendingWriteShowsOnlyContinueAndCancel(t *testing.T) {
	session := pendingAIPlaylistSession{
		token:        "token",
		target:       aiPlaylist{ID: "playlist", Name: "[AI] 夜间"},
		pendingWrite: true,
		draft: &aiPlaylistDraft{
			Plan: aiPlaylistPlan{Playlist: []aiPlaylistPlanItem{{ID: "song", Reason: "旧预览"}}},
		},
	}
	markup := aiPlaylistSessionMarkup(session)
	var labels []string
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			labels = append(labels, button.Text)
		}
	}
	if strings.Join(labels, ",") != "↻ 继续写入,取消,删除歌单" {
		t.Fatalf("pending write controls = %#v", labels)
	}
	if message := formatAIPlaylistSession(session); !strings.Contains(message, "继续写入") || strings.Contains(message, "旧预览") {
		t.Fatalf("pending write should not show a stale preview: %q", message)
	}
}

func TestAIPlaylistPendingReplacementShowsReplaceResumeAction(t *testing.T) {
	session := pendingAIPlaylistSession{
		token:        "token",
		target:       aiPlaylist{ID: "playlist", Name: "[AI] 夜间"},
		pendingWrite: true,
		resumePlans: map[string]aiPlaylistResumePlan{
			"playlist": {Replace: true, OriginalSongIDs: []string{"old1"}},
		},
	}
	markup := aiPlaylistSessionMarkup(session)
	if got := markup.InlineKeyboard[0][0].Text; got != "↻ 继续替换" {
		t.Fatalf("replacement retry button = %q", got)
	}
	if message := formatAIPlaylistSession(session); !strings.Contains(message, "未完成的替换计划") {
		t.Fatalf("resume copy should describe the interrupted replacement: %q", message)
	}
}

func TestAIPlaylistCancellationCannotDropInFlightTask(t *testing.T) {
	app := &BotApp{}
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: "token", chatID: 10, processing: true, expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("failed to register processing session")
	}
	if app.cancelAIPlaylistSession("token") {
		t.Fatal("in-flight AI playlist task must not be cancelled by dropping its session")
	}
	if _, ok := app.getAIPlaylistSession("token"); !ok {
		t.Fatal("in-flight session was removed")
	}
}

func TestAIPlaylistBlockedPreviewCanAcceptInputAgain(t *testing.T) {
	app := &BotApp{}
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: "token", chatID: 10, target: aiPlaylist{ID: "playlist"},
		awaitingInput: true, expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("failed to register input session")
	}
	if _, ok := app.claimAIPlaylistSession("token", "夜间氛围", nil); !ok {
		t.Fatal("failed to claim preview session")
	}
	if _, ok := app.setAIPlaylistSessionProcessing("token", false); !ok {
		t.Fatal("failed to release preview session")
	}
	updated, ok := app.getAIPlaylistSession("token")
	if !ok || !updated.awaitingInput || updated.processing {
		t.Fatalf("released session should accept text again: %#v, exists=%t", updated, ok)
	}
}

func TestAIPlaylistDeleteAsksBeforeRemovingThePlaylist(t *testing.T) {
	app := &BotApp{}
	message := &tb.Message{ID: 9, Chat: &tb.Chat{ID: 10}}
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: "token", userID: 20, chatID: 10, message: message,
		target:    aiPlaylist{ID: "playlist-1", Name: "[AI]中国风", Description: "国风歌单", SongCount: 74},
		expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("failed to register session")
	}
	ctx := &aiPlaylistInteractionTestContext{
		args: []string{"delete_yes", "token"}, sender: &tb.User{ID: 20},
		callback: &tb.Callback{Message: message}, chat: message.Chat,
	}
	if err := app.HandleAIPlaylistCallback(ctx); err != nil {
		t.Fatal(err)
	}
	session, ok := app.getAIPlaylistSession("token")
	if !ok || session.confirmingDelete || session.processing || session.target.ID != "playlist-1" {
		t.Fatalf("confirm must come before deletion: %#v", session)
	}

	ctx.args = []string{"delete", "token"}
	if err := app.HandleAIPlaylistCallback(ctx); err != nil {
		t.Fatal(err)
	}
	session, ok = app.getAIPlaylistSession("token")
	if !ok || !session.confirmingDelete || session.processing || session.awaitingInput {
		t.Fatalf("delete should wait for confirmation: %#v", session)
	}
	card, _ := ctx.edited.(string)
	if !strings.Contains(card, "会从 Navidrome 删掉这张歌单") || !strings.Contains(card, "歌还在曲库里") || !strings.Contains(card, "[AI]中国风") {
		t.Fatalf("confirm card = %q", card)
	}
	markup := aiPlaylistSessionMarkup(session)
	var labels []string
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			labels = append(labels, button.Text)
		}
	}
	if strings.Join(labels, ",") != "确认删除,返回" {
		t.Fatalf("confirm controls = %#v", labels)
	}
	assertTelegramButtons(t, markup)

	ctx.args = []string{"delete_no", "token"}
	if err := app.HandleAIPlaylistCallback(ctx); err != nil {
		t.Fatal(err)
	}
	session, ok = app.getAIPlaylistSession("token")
	if !ok || session.confirmingDelete || !session.awaitingInput || session.target.ID != "playlist-1" {
		t.Fatalf("return should restore the playlist card: %#v", session)
	}

	ctx.args = []string{"delete", "token"}
	if err := app.HandleAIPlaylistCallback(ctx); err != nil {
		t.Fatal(err)
	}
	ctx.args = []string{"delete_yes", "token"}
	if err := app.HandleAIPlaylistCallback(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		session, ok = app.getAIPlaylistSession("token")
		if ok && !session.processing && session.confirmingDelete && strings.Contains(session.notice, "ai.enable") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delete should report the configuration failure and keep the playlist: %#v exists=%t", session, ok)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if session.target.ID != "playlist-1" {
		t.Fatalf("failed delete changed the target: %#v", session)
	}
}

func TestAIPlaylistDeleteRejectsOrdinaryPlaylists(t *testing.T) {
	app := &BotApp{}
	message := &tb.Message{ID: 11, Chat: &tb.Chat{ID: 10}}
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: "token", userID: 20, chatID: 10, message: message,
		playlists: []aiPlaylist{{ID: "playlist-1", Name: "中文"}},
		expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("failed to register session")
	}
	if _, ok := app.selectAIPlaylistTarget("token", aiPlaylist{ID: "playlist-1", Name: "中文"}); !ok {
		t.Fatal("failed to select playlist")
	}
	ctx := &aiPlaylistInteractionTestContext{
		args: []string{"delete", "token"}, sender: &tb.User{ID: 20},
		callback: &tb.Callback{Message: message}, chat: message.Chat,
	}
	if err := app.HandleAIPlaylistCallback(ctx); err != nil {
		t.Fatal(err)
	}
	session, ok := app.getAIPlaylistSession("token")
	if !ok || session.confirmingDelete || session.target.Name != "中文" {
		t.Fatalf("ordinary playlist must not enter delete confirmation: %#v", session)
	}
}

func TestForgetDeletedAIPlaylistStateDropsResumeAndMatchingIntent(t *testing.T) {
	store, err := openAIPlaylistResumeStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	if err := store.Create(ctx, aiPlaylistResumePlan{
		PlaylistID: "playlist1", Description: "国风", SongIDs: []string{"song1"}, Intro: "夜间",
	}); err != nil {
		t.Fatal(err)
	}
	matching := aiPlaylistCreateIntent{
		OperationID: "operation1", Name: "[AI] 中国风", Description: "国风",
		Marker: "gymdl-ai-playlist-create:operation1", SongIDs: []string{"song1"}, Intro: "夜间",
		PlaylistID: "playlist1",
	}
	other := matching
	other.OperationID = "operation2"
	other.Marker = "gymdl-ai-playlist-create:operation2"
	other.PlaylistID = ""
	if err := store.CreateIntent(ctx, matching); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateIntent(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := forgetDeletedAIPlaylistState(ctx, store, "playlist1"); err != nil {
		t.Fatal(err)
	}
	plan, err := store.Load(ctx, "playlist1")
	if err != nil || plan != nil {
		t.Fatalf("resume plan remains: %#v err=%v", plan, err)
	}
	gone, err := store.LoadIntent(ctx, "operation1")
	if err != nil || gone != nil {
		t.Fatalf("matching intent remains: %#v err=%v", gone, err)
	}
	kept, err := store.LoadIntent(ctx, "operation2")
	if err != nil || kept == nil {
		t.Fatalf("unrelated intent = %#v err=%v", kept, err)
	}
}
