// 通知模块
package bot

import (
	"log"
	"strconv"
	"sync"

	"github.com/nichuanfang/gymdl/processor/music"
	tb "gopkg.in/telebot.v4"
)

// NotifierInterface 定义发送通知的接口
type Notifier interface {
	Send(msg string)
}

type botNotifier struct {
	Bot    *tb.Bot
	ChatID int64
	App    *BotApp
}

// Send 异步发送消息
func (b *botNotifier) Send(msg string) {
	go func() {
		_, err := b.Bot.Send(&tb.Chat{ID: b.ChatID}, msg, tb.ModeMarkdown)
		if err != nil {
			log.Printf("[Telegram] 发送消息失败: %v", err)
		}
	}()
}

var (
	instance Notifier
	once     sync.Once
)

// InitBotNotifier 初始化全局单例，只能调用一次
func InitBotNotifier(bot *tb.Bot, chatID string, app *BotApp) {
	chatId, _ := strconv.ParseInt(chatID, 10, 64)
	once.Do(func() {
		instance = &botNotifier{
			Bot:    bot,
			ChatID: chatId,
			App:    app,
		}
	})
}

// SendMusicMessage sends an ingestion notification and attaches actions that
// operate on the exact stored file when its location is known.
func SendMusicMessage(msg string, song *music.SongInfo) {
	if instance == nil {
		log.Println("[Notifier] 尚未初始化，无法发送消息")
		return
	}
	notifier, ok := instance.(*botNotifier)
	if !ok || notifier.App == nil {
		instance.Send(msg)
		return
	}
	token, markup := notifier.App.musicActionMarkup(song)
	go func() {
		options := []interface{}{tb.ModeMarkdown}
		if markup != nil {
			options = append(options, markup)
		}
		message, err := notifier.Bot.Send(&tb.Chat{ID: notifier.ChatID}, msg, options...)
		if err != nil {
			log.Printf("[Telegram] 发送音乐入库通知失败: %v", err)
			return
		}
		notifier.App.setMusicActionMessage(token, message)
	}()
}

// GetNotifier 获取全局单例
func GetNotifier() Notifier {
	return instance
}

// SendMessage 方便调用
func SendMessage(msg string) {
	if instance == nil {
		log.Println("[Notifier] 尚未初始化，无法发送消息")
		return
	}
	instance.Send(msg)
}
