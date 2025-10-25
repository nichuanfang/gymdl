package bot

import (
	"fmt"
	"strconv"

	tb "gopkg.in/telebot.v4"
)

// LoggingMiddleware 日志中间件
func (app *BotApp) LoggingMiddleware(next tb.HandlerFunc) tb.HandlerFunc {
	return func(c tb.Context) error {
		logger.Info("[Telegram] Received message from userID=" + fmt.Sprint(c.Sender().ID))
		return next(c)
	}
}

// AuthMiddleware 鉴权中间件
func (app *BotApp) AuthMiddleware(next tb.HandlerFunc) tb.HandlerFunc {
	return func(c tb.Context) error {
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
