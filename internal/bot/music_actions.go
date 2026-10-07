package bot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

const (
	musicActionCallbackUnique = "music_action"
	musicActionTTL            = 24 * time.Hour
)

var musicActionCallbackEndpoint = tb.InlineButton{Unique: musicActionCallbackUnique}

type pendingMusicAction struct {
	path      string
	title     string
	artist    string
	storage   string
	expiresAt time.Time
	origin    *tb.Message
	deleting  bool
}

func (app *BotApp) musicActionMarkup(song *music.SongInfo) (string, *tb.ReplyMarkup) {
	if song == nil || song.MusicPath == "" {
		return "", nil
	}
	storage := strings.ToUpper(strings.TrimSpace(song.Tidy))
	if storage != "LOCAL" && storage != "WEBDAV" {
		return "", nil
	}

	token := uuid.NewString()
	action := pendingMusicAction{
		path:      song.MusicPath,
		title:     song.SongName,
		artist:    song.SongArtists,
		storage:   storage,
		expiresAt: time.Now().Add(musicActionTTL),
	}

	app.musicActionMu.Lock()
	app.purgeMusicActionsLocked(time.Now())
	if app.musicActions == nil {
		app.musicActions = make(map[string]pendingMusicAction)
	}
	app.musicActions[token] = action
	app.musicActionMu.Unlock()

	markup := &tb.ReplyMarkup{}
	play := markup.Data("▶️ 播放", musicActionCallbackUnique, "play", token)
	remove := markup.Data("🗑 删除", musicActionCallbackUnique, "delete", token)
	markup.Inline(markup.Row(play, remove))
	return token, markup
}

func (app *BotApp) setMusicActionMessage(token string, message *tb.Message) {
	if token == "" || message == nil {
		return
	}
	app.musicActionMu.Lock()
	defer app.musicActionMu.Unlock()
	if action, ok := app.musicActions[token]; ok {
		action.origin = message
		app.musicActions[token] = action
	}
}

func (app *BotApp) getMusicAction(token string) (pendingMusicAction, bool) {
	app.musicActionMu.Lock()
	defer app.musicActionMu.Unlock()
	app.purgeMusicActionsLocked(time.Now())
	action, ok := app.musicActions[token]
	return action, ok && !action.deleting
}

func (app *BotApp) purgeMusicActionsLocked(now time.Time) {
	for token, action := range app.musicActions {
		if now.After(action.expiresAt) {
			delete(app.musicActions, token)
		}
	}
}

func (app *BotApp) HandleMusicActionCallback(c tb.Context) error {
	args := c.Args()
	if len(args) != 2 {
		return c.Respond(&tb.CallbackResponse{Text: "操作信息无效或已过期", ShowAlert: true})
	}
	actionName, token := args[0], args[1]
	action, ok := app.getMusicAction(token)
	if !ok {
		return c.Respond(&tb.CallbackResponse{Text: "操作已过期，请重新入库后再试", ShowAlert: true})
	}

	switch actionName {
	case "play":
		if err := app.sendMusicActionAudio(c, action); err != nil {
			utils.WarnWithFormat("[Telegram] 播放入库歌曲失败: %v", err)
			return c.Respond(&tb.CallbackResponse{Text: "播放失败：文件可能已不存在，或 Telegram 无法发送该文件。", ShowAlert: true})
		}
		return c.Respond(&tb.CallbackResponse{Text: "歌曲已发送"})
	case "delete":
		markup := &tb.ReplyMarkup{}
		confirm := markup.Data("确认删除", musicActionCallbackUnique, "confirm_delete", token)
		cancel := markup.Data("取消", musicActionCallbackUnique, "cancel_delete", token)
		markup.Inline(markup.Row(confirm, cancel))
		if err := c.Send(fmt.Sprintf("确定删除《%s》吗？\n将删除已入库的文件（%s），此操作不可撤销。", action.title, action.storage), markup); err != nil {
			_ = c.Respond()
			return err
		}
		return c.Respond()
	case "cancel_delete":
		_ = c.Respond()
		return c.Edit("已取消删除。", &tb.ReplyMarkup{})
	case "confirm_delete":
		_ = c.Respond(&tb.CallbackResponse{Text: "正在删除…"})
		return app.confirmMusicActionDelete(c, token, action)
	default:
		return c.Respond(&tb.CallbackResponse{Text: "未知操作", ShowAlert: true})
	}
}

func (app *BotApp) sendMusicActionAudio(c tb.Context, action pendingMusicAction) error {
	filePath := action.path
	cleanup := func() {}
	if action.storage == "WEBDAV" {
		if core.GlobalWebDAV == nil {
			return fmt.Errorf("WebDAV 未初始化")
		}
		tempDir, err := os.MkdirTemp("", "gymdl-telegram-play-")
		if err != nil {
			return fmt.Errorf("创建播放临时目录失败: %w", err)
		}
		cleanup = func() { _ = os.RemoveAll(tempDir) }
		filePath = filepath.Join(tempDir, filepath.Base(action.path))
		if err := core.GlobalWebDAV.Download(action.path, filePath); err != nil {
			cleanup()
			return fmt.Errorf("从 WebDAV 读取歌曲失败: %w", err)
		}
	}
	defer cleanup()

	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("歌曲文件不可用: %w", err)
	}
	audio := &tb.Audio{
		File:      tb.FromDisk(filePath),
		Title:     action.title,
		Performer: action.artist,
	}
	if _, err := c.Bot().Send(c.Chat(), audio); err != nil {
		return fmt.Errorf("发送 Telegram 音频失败: %w", err)
	}
	return nil
}

func (app *BotApp) confirmMusicActionDelete(c tb.Context, token string, action pendingMusicAction) error {
	app.musicActionMu.Lock()
	current, ok := app.musicActions[token]
	if !ok || time.Now().After(current.expiresAt) {
		delete(app.musicActions, token)
		app.musicActionMu.Unlock()
		return c.Edit("操作已过期，请重新入库后再试。", &tb.ReplyMarkup{})
	}
	if current.deleting {
		app.musicActionMu.Unlock()
		return nil
	}
	current.deleting = true
	app.musicActions[token] = current
	app.musicActionMu.Unlock()

	var err error
	switch action.storage {
	case "LOCAL":
		err = os.Remove(action.path)
	case "WEBDAV":
		if core.GlobalWebDAV == nil {
			err = fmt.Errorf("WebDAV 未初始化")
		} else {
			err = core.GlobalWebDAV.Delete(action.path)
		}
	default:
		err = fmt.Errorf("不支持的存储类型 %q", action.storage)
	}
	if err != nil {
		app.musicActionMu.Lock()
		if current, ok := app.musicActions[token]; ok {
			current.deleting = false
			app.musicActions[token] = current
		}
		app.musicActionMu.Unlock()
		utils.WarnWithFormat("[Telegram] 删除入库歌曲失败: %v", err)
		return c.Edit("❌ 删除失败，文件可能已被移动或存储服务暂不可用。", &tb.ReplyMarkup{})
	}

	app.musicActionMu.Lock()
	delete(app.musicActions, token)
	app.musicActionMu.Unlock()
	if action.origin != nil {
		_, _ = c.Bot().EditReplyMarkup(action.origin, &tb.ReplyMarkup{})
	}
	return c.Edit("✅ 已删除《"+action.title+"》的入库文件。", &tb.ReplyMarkup{})
}
