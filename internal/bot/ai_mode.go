package bot

import (
	"strings"

	"github.com/nichuanfang/gymdl/internal/aichat"
	tb "gopkg.in/telebot.v4"
)

// AIModeCommand switches between agent mode and the original link-only mode.
// /mode opens a picker; /mode ai and /mode normal switch directly.
func (app *BotApp) AIModeCommand(c tb.Context) error {
	if !app.aiAvailable() {
		return c.Send("AI 模式未启用，请先在配置中开启 ai.enable。")
	}
	arg := strings.ToLower(strings.TrimSpace(c.Message().Payload))
	userID, chatID := aiScope(c)
	switch arg {
	case "":
		markup := &tb.ReplyMarkup{}
		markup.Inline(
			markup.Row(
				markup.Data("AI 模式", aiModeSettingCallback, "ai"),
				markup.Data("普通模式", aiModeSettingCallback, "normal"),
			),
		)
		return c.Send(aichat.ModeSummary(app.aiEnabled(c), app.cfg.AI.DefaultMode), tb.ModeHTML, markup)
	case "ai", "on":
		if err := app.aiStore.SetMode(userID, chatID, true); err != nil {
			return c.Send("切换失败：" + err.Error())
		}
		return c.Send("✅ <b>智能体模式</b>\n直接说话；发链接仍会下载。", tb.ModeHTML)
	case "normal", "off":
		if err := app.aiStore.SetMode(userID, chatID, false); err != nil {
			return c.Send("切换失败：" + err.Error())
		}
		return c.Send("✅ <b>普通模式</b>\n发链接下载；对话用 /ai。", tb.ModeHTML)
	default:
		return c.Send("用法：/mode 打开选择，或 /mode ai、/mode normal")
	}
}

func (app *BotApp) HandleAIModeCallback(c tb.Context) error {
	args := c.Args()
	if len(args) != 1 {
		return c.Respond(&tb.CallbackResponse{Text: "无效操作", ShowAlert: true})
	}
	userID, chatID := aiScope(c)
	// Only the two advertised values are accepted; anything else used to be
	// silently treated as "normal", so a malformed payload changed the mode.
	var enable bool
	switch args[0] {
	case "ai":
		enable = true
	case "normal":
		enable = false
	default:
		return c.Respond(&tb.CallbackResponse{Text: "无效操作", ShowAlert: true})
	}
	if err := app.aiStore.SetMode(userID, chatID, enable); err != nil {
		return c.Respond(&tb.CallbackResponse{Text: "切换失败", ShowAlert: true})
	}
	_ = c.Respond(&tb.CallbackResponse{Text: "已切换到" + aichat.ModeLabel(enable)})
	return c.Edit(aichat.ModeSummary(enable, app.cfg.AI.DefaultMode), tb.ModeHTML)
}

// AICommand runs one agent turn without changing the chat mode. Repeating /ai
// keeps the same conversation, so the user never has to "end" anything.
func (app *BotApp) AICommand(c tb.Context) error {
	text := strings.TrimSpace(c.Message().Payload)
	if text == "" {
		return c.Send("用法：/ai <想做什么>\n例如：/ai 推荐通勤听的歌\n普通模式下每条都要带 /ai；切到 AI 模式后可以直接说话。")
	}
	return app.runAITurn(c, text)
}
