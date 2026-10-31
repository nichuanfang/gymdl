package bot

import (
	tb "gopkg.in/telebot.v4"
)

// AIClearCommand clears only the conversation; the selected mode is kept.
func (app *BotApp) AIClearCommand(c tb.Context) error {
	if !app.aiAvailable() {
		return c.Send("AI 模式未启用。")
	}
	userID, chatID := aiScope(c)
	if err := app.aiStore.Reset(userID, chatID); err != nil {
		return c.Send("清空失败：" + err.Error())
	}
	return c.Send("上下文已清空")
}
