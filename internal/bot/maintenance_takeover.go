package bot

import (
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

// retireMaintenanceCard replaces an older maintenance card with a short
// "moved to the bottom" notice and removes its buttons. Re-sending a
// maintenance command sends the current state as a new bottom card and calls
// this for the card it replaced, so stale buttons cannot drive the task.
func (app *BotApp) retireMaintenanceCard(message *tb.Message, text string) {
	if message == nil || message.ID == 0 || app.bot == nil {
		return
	}
	if _, err := app.bot.Edit(message, text, &tb.ReplyMarkup{}); err != nil {
		utils.WarnWithFormat("[Telegram] 收起旧维护卡片失败 id=%d: %v", message.ID, err)
	}
}

// maintenanceMovedText is shown on the card a bottom takeover replaced. Each
// command names itself so the user knows which card is still live.
func maintenanceMovedText(kind string) string {
	switch kind {
	case "a":
		return "🔁 歌单整理已移到聊天底部，请用最新那张卡片继续。"
	case "i":
		return "🔁 AI 歌单已移到聊天底部，请用最新那张卡片继续。"
	case "d":
		return "🔁 歌单描述已移到聊天底部，请用最新那张卡片继续。"
	case "t":
		return "🔁 清空回收站已移到聊天底部，请用最新那张卡片继续。"
	default:
		return "🔁 这张卡片已移到聊天底部，请用最新那张卡片继续。"
	}
}
