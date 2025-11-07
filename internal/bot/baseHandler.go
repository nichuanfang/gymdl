package bot

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/nichuanfang/gymdl/internal/bot/dispatch"
	"github.com/nichuanfang/gymdl/utils"

	"github.com/nichuanfang/gymdl/core/linkparser"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/processor/video"
	tb "gopkg.in/telebot.v4"
)

// HandleText 精简版交互逻辑
func HandleText(c tb.Context) error {
	text := c.Text()
	user := c.Sender()
	b := c.Bot()

	// 初始提示
	msg, _ := b.Send(user, "🔍 正在识别链接...")

	// 解析链接link:有效链接 linkType:链接类型
	link, executor := linkparser.ParseLink(text)
	if link == "" {
		_, _ = b.Edit(msg, "❌ 暂不支持该类型的链接")
		return nil
	}
	utils.InfoWithFormat("[Telegram] 解析成功: %s", link)

	// 创建会话对象
	session := &dispatch.Session{
		Text:    text,
		Context: c,
		User:    user,
		Bot:     b,
		Msg:     msg,
		Link:    link,
		Start:   time.Now(),
		Cfg:     app.cfg,
	}
	var err error
	switch expr := executor.(type) {
	case music.Processor:
		// 初始化音乐处理器
		expr.Init(app.cfg)
		err = session.HandleMusic(expr)
	case video.Processor:
		// 初始化视频处理器
		expr.Init(app.cfg)
		err = session.HandleVideo(expr)
	default:
		err = errors.New(fmt.Sprintf("未知处理器类型: %v", expr))
	}
	if err != nil {
		_ = c.Send(fmt.Sprintf("处理失败：%s", err.Error()))
	}
	return nil
}

// HandleAudio 处理音频
func HandleAudio(c tb.Context) error {
	file := &c.Message().Audio.File

	text := c.Text()
	user := c.Sender()
	b := c.Bot()

	// 初始提示
	msg, _ := b.Send(user, "🎧 正在处理音频...")

	// 创建会话对象
	session := &dispatch.Session{
		Text:    text,
		Context: c,
		User:    user,
		Bot:     b,
		Msg:     msg,
		Start:   time.Now(),
		Cfg:     app.cfg,
	}

	processor := &music.ForwardProcessor{}
	processor.Init(app.cfg)

	// 获取文件流
	closer, err := b.File(file)
	if err != nil {
		_ = c.Send(fmt.Sprintf("处理失败：%s", err.Error()))
		return nil
	}
	defer closer.Close()

	// 读取全部文件内容
	data, err := io.ReadAll(closer)
	if err != nil {
		_ = c.Send(fmt.Sprintf("处理失败：%s", err.Error()))
		return nil
	}

	// data 完整的音频字节数组
	processor.AudioBytes = data
	// file.FilePath
	processor.FileName = filepath.Base(file.FilePath)

	// 处理音频
	err = session.HandleMusic(processor)
	if err != nil {
		_ = c.Send(fmt.Sprintf("处理失败：%s", err.Error()))
		return nil
	}
	return nil
}
