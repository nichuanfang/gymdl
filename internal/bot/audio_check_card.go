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

// currentAudioCheck returns a card the command can still bring back. A closed
// summary has no actions, so re-sending the command starts a new scan.
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
	_, hasFinding := currentAudioCheckFinding(current)
	return current, current.running || current.busy || hasFinding || audioCheckCanContinue(current)
}

// takeoverAudioCheckCard re-sends the live card at the bottom of the chat and
// rebinds the task to it, then retires the old card. The scan keeps running on
// its own goroutine: only the message binding changes, so a re-send never
// restarts or double-starts a scan. The caller holds audioCheckCardMu.
func (app *BotApp) takeoverAudioCheckCard(c tb.Context, session audioCheckSession) error {
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

// commitAudioCheckBinding publishes the new card only after saving it, while
// keeping callbacks from changing the session during the swap.
func (app *BotApp) commitAudioCheckBinding(previous, next audioCheckSession, message *tb.Message) bool {
	chat := previous.message.Chat.ID
	app.audioCheckMu.Lock()
	defer app.audioCheckMu.Unlock()
	current, ok := app.audioCheckSessions[chat]
	if !ok || current.busy || current.token != previous.token || !sameMusicActionMessage(current.message, previous.message) {
		return false
	}
	current.message = message
	current.token = next.token
	current.expires = next.expires
	if err := takeoverPersistHook(current); err != nil {
		utils.WarnWithFormat("无法保存体检卡片接管: %v", err)
		return false
	}
	app.audioCheckSessions[chat] = current
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
	app.audioCheckCardMu.Lock()
	defer app.audioCheckCardMu.Unlock()
	current, ok := app.liveAudioCheckBinding(c.Chat().ID)
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
