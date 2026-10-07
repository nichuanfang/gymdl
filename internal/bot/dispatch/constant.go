package dispatch

import (
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor/music"
	tb "gopkg.in/telebot.v4"
)

type Session struct {
	Text              string         // 用户发送的消息
	Context           tb.Context     // tg上下文
	User              *tb.User       // 用户
	Bot               tb.API         // 机器人
	Msg               *tb.Message    // 初始化消息对象
	Link              string         // 有效链接
	Start             time.Time      // 开始处理时间
	Cfg               *config.Config // 配置文件
	OnMusicDownloaded func([]*music.SongInfo)
}
