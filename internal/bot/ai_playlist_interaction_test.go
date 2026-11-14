package bot

import (
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
	return nil
}

func TestAIPlaylistInitialMenuKeepsCancelAndShortResumeCallbacks(t *testing.T) {
	session := pendingAIPlaylistSession{
		token:           strings.Repeat("a", 32),
		resumePlaylists: []aiPlaylist{{ID: strings.Repeat("b", 32), Name: "[AI] 长歌单名称"}},
		resumePlans:     map[string]aiPlaylistResumePlan{strings.Repeat("b", 32): {}},
	}
	markup := aiPlaylistSessionMarkup(session)
	if len(markup.InlineKeyboard) != 2 {
		t.Fatalf("keyboard rows = %d, want resume and cancel", len(markup.InlineKeyboard))
	}
	if got := markup.InlineKeyboard[len(markup.InlineKeyboard)-1][0].Text; got != "取消" {
		t.Fatalf("final button = %q, want cancel", got)
	}
	resume := markup.InlineKeyboard[0][0]
	if !strings.HasSuffix(resume.Data, "|0") {
		t.Fatalf("resume callback should carry a short session index, got %q", resume.Data)
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.Unique != aiPlaylistCallbackUnique || len(button.Data) > 64 {
				t.Errorf("invalid callback button: %#v (data bytes=%d)", button, len(button.Data))
			}
		}
	}
}

func TestAIPlaylistUnavailableMenuStillHasCancelButton(t *testing.T) {
	markup := aiPlaylistSessionMarkup(pendingAIPlaylistSession{token: "token"})
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 || markup.InlineKeyboard[0][0].Text != "取消" {
		t.Fatalf("unavailable menu should still allow cancel: %#v", markup.InlineKeyboard)
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
	for _, expected := range []string{"🤖 AI 智能歌单预览", "──────────────────", "🎵 歌单：[AI] <夜间>", "<Song> - A & B", "贴合安静氛围"} {
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
	if message := formatAIPlaylistSession(menu); !strings.Contains(message, "确认后会按预览替换") {
		t.Fatalf("menu should explain the write behavior: %q", message)
	}
	markup := aiPlaylistSessionMarkup(menu)
	if got := markup.InlineKeyboard[0][0].Text; got != "[AI] 夜间 · 28 首" {
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
	if strings.Join(labels, ",") != "↻ 继续写入,取消" {
		t.Fatalf("pending write controls = %#v", labels)
	}
	if message := formatAIPlaylistSession(session); !strings.Contains(message, "AI 智能歌单续写") || strings.Contains(message, "旧预览") {
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
