package bot

import (
	"time"

	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

// takeoverPersistHook is swapped in tests to simulate a failed save.
var takeoverPersistHook = persistAudioCheckSession

// audioCheckMovedText replaces an old health-check card after the live card has
// moved to the bottom of the chat. The old card keeps no buttons, and its token
// is no longer the bound one, so both the text and the callback guard stop it.
const audioCheckMovedText = "🔁 曲库体检已移到聊天底部，请用最新那张卡片继续。"

// currentAudioCheck returns the live session owned by user in this chat. An
// expired finished card is not resumable, so re-sending /audio_check starts a
// fresh scan instead of taking it over.
func (app *BotApp) currentAudioCheck(chat, owner int64) (audioCheckSession, bool) {
	app.audioCheckMu.Lock()
	defer app.audioCheckMu.Unlock()
	current, ok := app.audioCheckSessions[chat]
	if !ok || current.owner != owner || current.message == nil || current.message.ID == 0 {
		return audioCheckSession{}, false
	}
	if !current.running && time.Now().After(current.expires) {
		return audioCheckSession{}, false
	}
	return current, true
}

// takeoverAudioCheckCard re-sends the live card at the bottom of the chat and
// rebinds the task to it, then retires the old card. The scan keeps running on
// its own goroutine: only the message binding changes, so a re-send never
// restarts or double-starts a scan.
func (app *BotApp) takeoverAudioCheckCard(c tb.Context, session audioCheckSession) error {
	if session.busy {
		return c.Send("原卡片的操作还在处理，请稍后再试。")
	}
	// Refresh from the live binding so the new card shows the newest state.
	if live, ok := app.liveAudioCheckBinding(session.message.Chat.ID); ok {
		session = live
	}
	if session.busy {
		return c.Send("原卡片的操作还在处理，请稍后再试。")
	}
	previous := session
	session.token = audioCheckToken()
	session.expires = time.Now().Add(audioCheckTTL)
	// A running scan shows the progress card (with 停止); a finished one shows
	// the review card. Rendering the wrong one would drop the live control.
	var text string
	var markup *tb.ReplyMarkup
	if session.running {
		text, markup = formatAudioCheckProgressCard(session)
	} else {
		text, markup = formatAudioCheckCard(session)
	}
	api := c.Bot()
	message, err := api.Send(c.Chat(), text, tb.ModeHTML, markup)
	if err != nil {
		return err
	}
	if !app.commitAudioCheckBinding(previous, session, message) {
		_ = api.Delete(message)
		return c.Send("体检状态刚刚变化，请再发一次 /audio_check。")
	}
	if !sameMusicActionMessage(previous.message, message) {
		if _, editErr := api.Edit(previous.message, audioCheckMovedText, &tb.ReplyMarkup{}); editErr != nil {
			utils.WarnWithFormat("无法收起旧体检卡片: %v", editErr)
		}
	}
	return nil
}

// commitAudioCheckBinding swaps the message binding only if the session still
// points at the card the caller read. The new binding is persisted outside the
// lock; if the save fails the old binding is restored so the original card
// keeps working.
func (app *BotApp) commitAudioCheckBinding(previous, next audioCheckSession, message *tb.Message) bool {
	chat := previous.message.Chat.ID
	app.audioCheckMu.Lock()
	current, ok := app.audioCheckSessions[chat]
	if !ok || current.token != previous.token || !sameMusicActionMessage(current.message, previous.message) {
		app.audioCheckMu.Unlock()
		return false
	}
	current.message = message
	current.token = next.token
	current.expires = next.expires
	app.audioCheckSessions[chat] = current
	app.audioCheckMu.Unlock()

	if err := takeoverPersistHook(current); err != nil {
		app.audioCheckMu.Lock()
		if latest, ok := app.audioCheckSessions[chat]; ok && latest.token == current.token {
			app.audioCheckSessions[chat] = previous
		}
		app.audioCheckMu.Unlock()
		utils.WarnWithFormat("无法保存体检卡片接管: %v", err)
		return false
	}
	return true
}

// liveAudioCheckBinding returns the card currently bound to the task in chat.
func (app *BotApp) liveAudioCheckBinding(chat int64) (audioCheckSession, bool) {
	app.audioCheckMu.Lock()
	defer app.audioCheckMu.Unlock()
	current, ok := app.audioCheckSessions[chat]
	return current, ok
}

// resumeAudioCheckCard brings the task's current card back to the bottom. It is
// the 返回体检 target on the source/download cards, which carry the card token.
func (app *BotApp) resumeAudioCheckCard(c tb.Context, token string) error {
	chat := c.Chat().ID
	app.audioCheckMu.Lock()
	current, ok := app.audioCheckSessions[chat]
	app.audioCheckMu.Unlock()
	if !ok || current.token != token || current.owner != c.Sender().ID {
		return c.Respond(&tb.CallbackResponse{Text: "体检卡片已更新，请使用最新的那张。", ShowAlert: true})
	}
	if !current.running && time.Now().After(current.expires) {
		return c.Respond(&tb.CallbackResponse{Text: "这次体检已经结束，请重新发送 /audio_check。", ShowAlert: true})
	}
	if current.busy {
		return c.Respond(&tb.CallbackResponse{Text: "体检卡片正在处理上一项操作，请稍后再试。"})
	}
	_ = c.Respond(&tb.CallbackResponse{Text: "正在返回体检…"})
	return app.takeoverAudioCheckCard(c, current)
}
