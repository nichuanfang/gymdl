package bot

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/nichuanfang/gymdl/internal/bot/dispatch"
	"github.com/nichuanfang/gymdl/utils"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core/linkparser"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/processor/video"
	tb "gopkg.in/telebot.v4"
)

// HandleText 精简版交互逻辑
func (app *BotApp) HandleText(c tb.Context) error {
	text := c.Text()
	user := c.Sender()
	b := c.Bot()

	// 初始提示
	msg, _ := b.Send(user, "🔍 正在识别链接...")

	// Use a per-message config snapshot so safe WebUI hot-reload fields apply only
	// to new tasks and never change a task while it is running.
	taskConfig := config.WithLiveTaskOverrides(app.cfg)
	link, executor := linkparser.ParseLinkWithConfig(taskConfig, text)
	if link == "" {
		_, _ = b.Edit(msg, "❌ 暂不支持该类型的链接")
		return nil
	}
	utils.InfoWithFormat("[Telegram] 解析成功: %s", link)

	// 创建会话对象
	session := &dispatch.Session{
		Text:                    text,
		Context:                 c,
		User:                    user,
		Bot:                     b,
		Msg:                     msg,
		Link:                    link,
		Start:                   time.Now(),
		Cfg:                     taskConfig,
		CreateMusicActionMarkup: app.musicActionMarkup,
		SetMusicActionMessages:  app.setMusicActionMessages,
	}
	var err error
	switch expr := executor.(type) {
	case music.Processor:
		// 初始化音乐处理器
		expr.Init(taskConfig)
		err = app.handleMusicWithDuplicateCheck(session, expr)
	case video.Processor:
		// 初始化视频处理器
		expr.Init(taskConfig)
		err = session.HandleVideo(expr)
	default:
		err = errors.New(fmt.Sprintf("未知处理器类型: %v", expr))
	}
	if err != nil {
		errMsg := fmt.Sprintf(
			"❌ 处理失败\n\n"+
				"处理器: %s\n"+
				"详情: %s",
			executor.Name(), err.Error(),
		)
		_, _ = b.Edit(msg, errMsg)
	}
	return nil
}

// HandleAudio 处理音频
func (app *BotApp) HandleAudio(c tb.Context) error {
	file := &c.Message().Audio.File

	text := c.Text()
	user := c.Sender()
	b := c.Bot()

	// 初始提示
	msg, _ := b.Send(user, "🎧 正在处理音频...")

	// New Telegram audio tasks also capture the currently active safe overrides.
	taskConfig := config.WithLiveTaskOverrides(app.cfg)
	// 创建会话对象
	session := &dispatch.Session{
		Text:                    text,
		Context:                 c,
		User:                    user,
		Bot:                     b,
		Msg:                     msg,
		Start:                   time.Now(),
		Cfg:                     taskConfig,
		CreateMusicActionMarkup: app.musicActionMarkup,
		SetMusicActionMessages:  app.setMusicActionMessages,
	}

	processor := &music.ForwardProcessor{}
	processor.Init(taskConfig)

	// 获取文件流 最大支持20MB
	closer, err := b.File(file)
	if err != nil {
		_, _ = b.Edit(msg, fmt.Sprintf("处理失败：%s", err.Error()))
		return nil
	}
	defer closer.Close()

	// 读取全部文件内容
	data, err := io.ReadAll(closer)
	if err != nil {
		_, _ = b.Edit(msg, fmt.Sprintf("处理失败：%s", err.Error()))
		return nil
	}

	// data 完整的音频字节数组
	processor.AudioBytes = data
	// file.FilePath
	processor.FileName = filepath.Base(file.FilePath)

	// 处理音频
	err = session.HandleMusic(processor)
	if err != nil {
		_, _ = b.Edit(msg, fmt.Sprintf("处理失败：%s", err.Error()))
		return nil
	}
	return nil
}
