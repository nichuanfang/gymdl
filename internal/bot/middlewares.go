package bot

import (
	"fmt"
	"strconv"

	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
	"gopkg.in/telebot.v4/middleware"
)

// recoverMiddleware 包住整条处理链。telebot 的更新循环本身不恢复 panic，
// 任一按钮或消息处理崩溃都会让进程退出。
func (app *BotApp) recoverMiddleware() tb.MiddlewareFunc {
	return middleware.Recover(func(err error, c tb.Context) {
		utils.WarnWithFormat("[Telegram] 处理更新时出现异常: %v", err)
		if c == nil || c.Callback() == nil {
			return
		}
		_ = c.Respond(&tb.CallbackResponse{Text: "操作没有完成，请再试一次", ShowAlert: true})
	})
}

// LoggingMiddleware 日志中间件
func (app *BotApp) LoggingMiddleware(next tb.HandlerFunc) tb.HandlerFunc {
	return func(c tb.Context) error {
		if c.Sender() != nil {
			logger.Info("[Telegram] Received message from userID=" + fmt.Sprint(c.Sender().ID))
		} else {
			logger.Info("[Telegram] Received update without a user sender")
		}
		return next(c)
	}
}

// AuthMiddleware 鉴权中间件
func (app *BotApp) AuthMiddleware(next tb.HandlerFunc) tb.HandlerFunc {
	return func(c tb.Context) error {
		if c.Sender() == nil {
			return c.Send("无法识别发送者，请在私聊或群聊中重新操作。")
		}
		userID := c.Sender().ID
		for _, allowed := range app.cfg.Telegram.AllowedUsers {
			if strconv.FormatInt(userID, 10) == allowed {
				return next(c)
			}
		}
		logger.Error(fmt.Sprintf("[Telegram] Unauthorized access from userID=%d", userID))
		return c.Send("Unauthorized")
	}
}
