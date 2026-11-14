package bot

import (
	tb "gopkg.in/telebot.v4"

	"github.com/nichuanfang/gymdl/utils"
)

// AIClearCommand clears only the conversation; the selected mode is kept.
func (app *BotApp) AIClearCommand(c tb.Context) error {
	if !app.aiAvailable() {
		return c.Send("AI 模式未启用。")
	}
	userID, chatID := aiScope(c)
	message, err := c.Bot().Send(c.Chat(), "🧹 正在清空当前对话…")
	if err != nil {
		return err
	}
	unlock := app.aiTurnLock(chatID)
	defer unlock()
	if err := app.aiStore.Reset(userID, chatID); err != nil {
		utils.WarnWithFormat("[AI] 清空对话失败 chat=%d: %v", chatID, err)
		if _, editErr := c.Bot().Edit(message, "❌ 清空失败，请稍后重试。"); editErr != nil {
			return c.Send("❌ 清空失败，请稍后重试。")
		}
		return nil
	}
	if _, err := c.Bot().Edit(message, "✅ 上下文已清空"); err != nil {
		return c.Send("✅ 上下文已清空")
	}
	return nil
}
