package bot

import (
	"time"

	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

const maintenanceHoldCallbackUnique = "hold"
const maintenanceEndedText = "已结束上一个会话。"
const emptyTrashConfirmText = "清空回收站还等你确认。请先确认或取消。"

var maintenanceHoldEndpoint = tb.InlineButton{Unique: maintenanceHoldCallbackUnique}

// maintenanceHold is an open card that blocks another maintenance command.
// The reminder replies to its message. A quiet card can be cancelled; a card
// that is reading, writing, or syncing only asks the user to wait.
type maintenanceHold struct {
	kind       string
	token      string
	userID     int64
	chatID     int64
	message    *tb.Message
	processing bool
	text       string
	busyText   string
}

// chatMaintenanceHold returns the card blocking this chat. opening is the
// command being started (trash, assign, ai, desc) and is not a conflict
// with itself. An empty opening blocks on every open card.
func (app *BotApp) chatMaintenanceHold(chatID int64, opening string) (maintenanceHold, bool) {
	if opening != "trash" {
		if hold, ok := app.emptyTrashHold(chatID); ok {
			return hold, true
		}
	}
	if opening != "assign" {
		if hold, ok := app.assignmentHold(chatID); ok {
			return hold, true
		}
	}
	if opening != "ai" {
		if hold, ok := app.aiPlaylistHold(chatID); ok {
			return hold, true
		}
	}
	if opening != "desc" {
		if hold, ok := app.playlistDescHold(chatID); ok {
			return hold, true
		}
	}
	return maintenanceHold{}, false
}

// beginMaintenanceCommand clears idle cards from other commands so the next
// command can start directly. A card that is processing is never interrupted.
func (app *BotApp) beginMaintenanceCommand(c tb.Context, next string) (maintenanceHold, bool) {
	if c == nil || c.Chat() == nil {
		return maintenanceHold{}, false
	}
	app.playlistMaintenanceStartMu.Lock()
	hold, ended, blocked := app.clearIdleMaintenanceConflictsLocked(c.Chat().ID, next)
	app.playlistMaintenanceStartMu.Unlock()
	app.editEndedMaintenanceCards(c, ended)
	return hold, blocked
}

// clearIdleMaintenanceConflictsLocked must be called with
// playlistMaintenanceStartMu held. Returning ended cards separately lets the
// caller release the lock before Telegram network I/O.
func (app *BotApp) clearIdleMaintenanceConflictsLocked(chatID int64, next string) (maintenanceHold, []maintenanceHold, bool) {
	var ended []maintenanceHold
	for range 4 {
		hold, ok := app.chatMaintenanceHold(chatID, "")
		if !ok {
			return maintenanceHold{}, ended, false
		}
		if hold.processing || hold.token == "" {
			return hold, ended, true
		}
		// A trash confirmation is destructive. Another command must not dismiss
		// it silently; the user confirms on the original card or presses
		// "取消并继续". Re-sending /empty_trash still replaces its own card.
		if hold.kind == "t" && next != "t" {
			return hold, ended, true
		}
		note, released := app.releaseMaintenanceHold(hold)
		if !released {
			if refreshed, stillOpen := app.chatMaintenanceHold(chatID, ""); stillOpen {
				return refreshed, ended, true
			}
			continue
		}
		utils.InfoWithFormat("[Telegram] 自动结束%s并开始%s chat=%d note=%s", maintenanceHoldLabel(hold.kind), maintenanceHoldLabel(next), chatID, note)
		ended = append(ended, hold)
	}
	if hold, ok := app.chatMaintenanceHold(chatID, ""); ok {
		return hold, ended, true
	}
	return maintenanceHold{}, ended, false
}

func (app *BotApp) editEndedMaintenanceCards(c tb.Context, ended []maintenanceHold) {
	if len(ended) == 0 {
		return
	}
	var api tb.API
	if c != nil {
		api = c.Bot()
	}
	if api == nil && app.bot != nil {
		api = app.bot
	}
	for _, hold := range ended {
		if api == nil || hold.message == nil {
			continue
		}
		if _, err := api.Edit(hold.message, maintenanceEndedText, &tb.ReplyMarkup{}); err != nil {
			utils.WarnWithFormat("[Telegram] 结束上一张维护卡片失败 chat=%d: %v", hold.chatID, err)
		}
	}
}

func (app *BotApp) emptyTrashHold(chatID int64) (maintenanceHold, bool) {
	app.emptyTrashMu.Lock()
	defer app.emptyTrashMu.Unlock()
	app.purgePendingEmptyTrashLocked(time.Now())
	var pending pendingEmptyTrash
	var found bool
	for _, item := range app.emptyTrashPending {
		if item.chatID == chatID {
			pending = item
			found = true
			break
		}
	}
	if found {
		return maintenanceHold{
			kind: "t", token: pending.token, userID: pending.userID, chatID: pending.chatID,
			message: pending.message, processing: pending.processing,
			text: emptyTrashConfirmText, busyText: "清空回收站正在处理，请稍等。",
		}, true
	}
	if _, running := app.emptyTrashRunning[chatID]; running {
		return maintenanceHold{
			kind: "t", chatID: chatID, processing: true,
			text: "清空回收站正在读取，请稍等。", busyText: "清空回收站正在读取，请稍等。",
		}, true
	}
	return maintenanceHold{}, false
}

func (app *BotApp) assignmentHold(chatID int64) (maintenanceHold, bool) {
	app.playlistAssignmentMu.Lock()
	defer app.playlistAssignmentMu.Unlock()
	app.ensurePlaylistAssignmentMapsLocked()
	app.purgePlaylistAssignmentsLocked(time.Now())
	token, ok := app.playlistAssignmentByChat[chatID]
	if !ok {
		return maintenanceHold{}, false
	}
	assignment, ok := app.playlistAssignments[token]
	if !ok {
		return maintenanceHold{}, false
	}
	return maintenanceHold{
		kind: "a", token: assignment.token, userID: assignment.userID, chatID: assignment.chatID,
		message: assignment.message, processing: assignment.processing,
		text: "歌单整理还开着。", busyText: "歌单整理正在处理，请稍等。",
	}, true
}

func (app *BotApp) aiPlaylistHold(chatID int64) (maintenanceHold, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	app.purgeAIPlaylistSessionsLocked(time.Now())
	token, ok := app.aiPlaylistSessionByChat[chatID]
	if !ok {
		return maintenanceHold{}, false
	}
	session, ok := app.aiPlaylistSessions[token]
	if !ok {
		return maintenanceHold{}, false
	}
	return maintenanceHold{
		kind: "i", token: session.token, userID: session.userID, chatID: session.chatID,
		message: session.message, processing: session.processing,
		text: "AI 歌单还开着。", busyText: "AI 歌单正在处理，请稍等。",
	}, true
}

func (app *BotApp) playlistDescHold(chatID int64) (maintenanceHold, bool) {
	app.playlistDescMu.Lock()
	defer app.playlistDescMu.Unlock()
	app.ensurePlaylistDescLocked(time.Now())
	token, ok := app.playlistDescByChat[chatID]
	if !ok {
		return maintenanceHold{}, false
	}
	session, ok := app.playlistDescSessions[token]
	if !ok {
		return maintenanceHold{}, false
	}
	return maintenanceHold{
		kind: "d", token: session.token, userID: session.userID, chatID: session.chatID,
		message: session.message, processing: session.syncing,
		text: "歌单描述还开着。", busyText: "歌单描述正在同步，请稍等。",
	}, true
}

func (app *BotApp) maintenanceHoldByToken(kind, token string) (maintenanceHold, bool) {
	switch kind {
	case "t":
		pending, ok := app.getPendingEmptyTrash(token)
		if !ok {
			return maintenanceHold{}, false
		}
		return maintenanceHold{
			kind: "t", token: pending.token, userID: pending.userID, chatID: pending.chatID,
			message: pending.message, processing: pending.processing,
			text: emptyTrashConfirmText, busyText: "清空回收站正在处理，请稍等。",
		}, true
	case "a":
		assignment, ok := app.getPlaylistAssignment(token)
		if !ok {
			return maintenanceHold{}, false
		}
		return maintenanceHold{
			kind: "a", token: assignment.token, userID: assignment.userID, chatID: assignment.chatID,
			message: assignment.message, processing: assignment.processing,
			text: "歌单整理还开着。", busyText: "歌单整理正在处理，请稍等。",
		}, true
	case "i":
		session, ok := app.getAIPlaylistSession(token)
		if !ok {
			return maintenanceHold{}, false
		}
		return maintenanceHold{
			kind: "i", token: session.token, userID: session.userID, chatID: session.chatID,
			message: session.message, processing: session.processing,
			text: "AI 歌单还开着。", busyText: "AI 歌单正在处理，请稍等。",
		}, true
	case "d":
		app.playlistDescMu.Lock()
		defer app.playlistDescMu.Unlock()
		app.ensurePlaylistDescLocked(time.Now())
		session, ok := app.playlistDescSessions[token]
		if !ok {
			return maintenanceHold{}, false
		}
		return maintenanceHold{
			kind: "d", token: session.token, userID: session.userID, chatID: session.chatID,
			message: session.message, processing: session.syncing,
			text: "歌单描述还开着。", busyText: "歌单描述正在同步，请稍等。",
		}, true
	default:
		return maintenanceHold{}, false
	}
}

func maintenanceHoldNotice(hold maintenanceHold, next string) (string, *tb.ReplyMarkup) {
	if hold.processing || hold.token == "" || !validMaintenanceStep(hold.kind) || !validMaintenanceStep(next) {
		text := hold.busyText
		if text == "" {
			text = hold.text
		}
		return text, nil
	}
	markup := &tb.ReplyMarkup{}
	markup.Inline(markup.Row(markup.Data("取消并继续", maintenanceHoldCallbackUnique, hold.kind, hold.token, next)))
	return hold.text, markup
}

func maintenanceHoldOptions(hold maintenanceHold, next string) *tb.SendOptions {
	_, markup := maintenanceHoldNotice(hold, next)
	opts := &tb.SendOptions{AllowWithoutReply: true, ReplyMarkup: markup}
	if hold.message != nil && hold.message.ID != 0 {
		opts.ReplyTo = hold.message
	}
	return opts
}

func validMaintenanceStep(value string) bool {
	switch value {
	case "a", "i", "d", "t":
		return true
	default:
		return false
	}
}

func (app *BotApp) presentMaintenanceHold(c tb.Context, hold maintenanceHold, next string, placeholder *tb.Message) error {
	busy := hold.processing || hold.token == "" || !validMaintenanceStep(hold.kind) || !validMaintenanceStep(next)
	utils.InfoWithFormat("[Telegram] %s还开着，挡住了%s chat=%d busy=%t", maintenanceHoldLabel(hold.kind), maintenanceHoldLabel(next), hold.chatID, busy)
	var err error
	if c.Bot() == nil || c.Chat() == nil {
		text, _ := maintenanceHoldNotice(hold, next)
		err = c.Send(text, maintenanceHoldOptions(hold, next))
	} else {
		err = sendMaintenanceHold(c.Bot(), c.Chat(), hold, next, placeholder)
	}
	if err != nil {
		utils.WarnWithFormat("[Telegram] 维护占用提示发送失败 chat=%d: %v", hold.chatID, err)
	}
	return err
}

func sendMaintenanceHold(bot tb.API, chat tb.Recipient, hold maintenanceHold, next string, placeholder *tb.Message) error {
	text, markup := maintenanceHoldNotice(hold, next)
	if placeholder != nil {
		if err := bot.Delete(placeholder); err != nil {
			_, editErr := bot.Edit(placeholder, text, markupOrEmpty(markup))
			return editErr
		}
	}
	_, err := bot.Send(chat, text, maintenanceHoldOptions(hold, next))
	return err
}

func markupOrEmpty(markup *tb.ReplyMarkup) *tb.ReplyMarkup {
	if markup == nil {
		return &tb.ReplyMarkup{}
	}
	return markup
}

func (app *BotApp) HandleMaintenanceHoldCallback(c tb.Context) error {
	args := c.Args()
	if len(args) != 3 || c.Sender() == nil || c.Chat() == nil || !validMaintenanceStep(args[0]) || !validMaintenanceStep(args[2]) {
		return c.Respond(&tb.CallbackResponse{Text: "按钮已失效，请重新发送指令", ShowAlert: true})
	}
	kind, token, next := args[0], args[1], args[2]
	hold, ok := app.maintenanceHoldByToken(kind, token)
	if !ok {
		utils.InfoWithFormat("[Telegram] %s已经关掉，继续%s chat=%d", maintenanceHoldLabel(kind), maintenanceHoldLabel(next), c.Chat().ID)
		_ = c.Respond()
		_ = c.Edit("已经关掉了。", &tb.ReplyMarkup{})
		return logMaintenanceContinue(next, app.continueAfterMaintenanceHold(c, next))
	}
	if hold.userID != c.Sender().ID || hold.chatID != c.Chat().ID {
		return c.Respond(&tb.CallbackResponse{Text: "只有发起的人可以取消", ShowAlert: true})
	}
	if hold.processing {
		return c.Respond(&tb.CallbackResponse{Text: "正在处理，请稍等。", ShowAlert: true})
	}
	note, released := app.releaseMaintenanceHold(hold)
	if !released {
		return c.Respond(&tb.CallbackResponse{Text: "正在处理，请稍等。", ShowAlert: true})
	}
	if hold.message != nil && c.Bot() != nil {
		_, _ = c.Bot().Edit(hold.message, note, &tb.ReplyMarkup{})
	}
	utils.InfoWithFormat("[Telegram] 取消%s并继续%s chat=%d", maintenanceHoldLabel(kind), maintenanceHoldLabel(next), hold.chatID)
	_ = c.Respond()
	_ = c.Edit("已取消。", &tb.ReplyMarkup{})
	return logMaintenanceContinue(next, app.continueAfterMaintenanceHold(c, next))
}

func logMaintenanceContinue(next string, err error) error {
	if err != nil {
		utils.WarnWithFormat("[Telegram] 取消后继续%s失败: %v", maintenanceHoldLabel(next), err)
	}
	return err
}

func maintenanceHoldLabel(kind string) string {
	switch kind {
	case "t":
		return "清空回收站"
	case "a":
		return "整理歌单"
	case "i":
		return "AI 歌单"
	case "d":
		return "歌单描述"
	default:
		return "歌单维护"
	}
}

func (app *BotApp) releaseMaintenanceHold(hold maintenanceHold) (string, bool) {
	switch hold.kind {
	case "i":
		session, ok := app.getAIPlaylistSession(hold.token)
		if !ok || session.processing || session.chatID != hold.chatID {
			return "", false
		}
		if !app.cancelAIPlaylistSession(hold.token) {
			return "", false
		}
		return aiPlaylistCancelText(session), true
	case "a":
		assignment, ok := app.getPlaylistAssignment(hold.token)
		if !ok || assignment.processing || assignment.chatID != hold.chatID {
			return "", false
		}
		finished, ok := app.cancelPlaylistAssignment(hold.token)
		if !ok {
			return "", false
		}
		return playlistAssignmentCancelText(finished), true
	case "d":
		app.playlistDescMu.Lock()
		session, ok := app.playlistDescSessions[hold.token]
		app.playlistDescMu.Unlock()
		if !ok || session.syncing || session.chatID != hold.chatID {
			return "", false
		}
		if !app.cancelPlaylistDesc(hold.token) {
			return "", false
		}
		return "已取消。", true
	case "t":
		pending, ok := app.getPendingEmptyTrash(hold.token)
		if !ok || pending.processing || pending.chatID != hold.chatID {
			return "", false
		}
		if _, ok := app.takePendingEmptyTrash(hold.token); !ok {
			return "", false
		}
		return "已取消清空回收站。", true
	default:
		return "", false
	}
}

func (app *BotApp) continueAfterMaintenanceHold(c tb.Context, next string) error {
	switch next {
	case "a":
		return app.startPlaylistAssignment(c)
	case "i":
		return app.startAIPlaylist(c)
	case "d":
		return app.PlaylistDescCommand(c)
	case "t":
		return app.startEmptyTrash(c)
	default:
		return c.Send("请重新发送刚才的指令。")
	}
}

func aiPlaylistCancelText(session pendingAIPlaylistSession) string {
	if session.pendingWrite || len(session.resumePlaylists) > 0 || session.createIntentID != "" {
		return "已结束本次 AI 歌单操作。未完成的续写计划已保留，可重新发送 /ai_playlist 继续。"
	}
	return "已取消本次 AI 歌单生成。"
}

func playlistAssignmentCancelText(assignment pendingPlaylistAssignment) string {
	if assignment.processedCount == 0 && assignment.skippedCount == 0 && !assignment.addedAny {
		return "已取消本次歌单整理。"
	}
	return playlistAssignmentSummary(assignment, false)
}
