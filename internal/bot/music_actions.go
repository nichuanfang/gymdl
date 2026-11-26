package bot

import (
	"context"
	"fmt"
	"os"
	"path"
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
	userID        int64
	chatID        int64
	path          string
	title         string
	artist        string
	album         string
	extension     string
	size          int64
	sourceURL     string
	busy          bool
	choice        string
	confirmSource bool
	storage       string
	expiresAt     time.Time
	origin        *tb.Message
	markup        *tb.ReplyMarkup
	deleting      bool
	// checkToken points back at the health-check card that opened this action,
	// so the source/confirm cards can offer a 返回体检 button.
	checkToken string
}

func (app *BotApp) musicActionMarkup(song *music.SongInfo, sourceURL string) (string, *tb.ReplyMarkup) {
	if song == nil || song.MusicPath == "" {
		return "", nil
	}
	storage := strings.ToUpper(strings.TrimSpace(song.Tidy))
	if storage != "LOCAL" && storage != "WEBDAV" {
		return "", nil
	}

	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	if song.SourceURL != "" {
		sourceURL = song.SourceURL
	}
	action := pendingMusicAction{
		path:      song.MusicPath,
		title:     song.SongName,
		artist:    song.SongArtists,
		album:     song.SongAlbum,
		extension: song.FileExt,
		size:      song.MusicSize,
		sourceURL: sourceURL,
		storage:   storage,
		expiresAt: time.Now().Add(musicActionTTL),
	}

	app.storeMusicAction(token, action)

	markup := &tb.ReplyMarkup{}
	play := markup.Data("▶️ 播放", musicActionCallbackUnique, "play", token)
	remove := markup.Data("🗑 删除", musicActionCallbackUnique, "delete", token)
	tidy := markup.Data("整理", musicActionCallbackUnique, "tidy", token)
	sources := markup.Data("其他音源", musicActionCallbackUnique, "sources", token)
	markup.Inline(markup.Row(play, remove, tidy, sources))
	return token, markup
}

func (app *BotApp) storeMusicAction(token string, action pendingMusicAction) {
	app.musicActionMu.Lock()
	defer app.musicActionMu.Unlock()
	app.purgeMusicActionsLocked(time.Now())
	if app.musicActions == nil {
		app.musicActions = make(map[string]pendingMusicAction)
	}
	app.musicActions[token] = action
}

func (app *BotApp) setMusicActionMessages(tokens []string, message *tb.Message, markup *tb.ReplyMarkup, userID, chatID int64) {
	if len(tokens) == 0 || message == nil {
		return
	}
	markupCopy := cloneMusicActionMarkup(markup)
	app.musicActionMu.Lock()
	defer app.musicActionMu.Unlock()
	for _, token := range tokens {
		if action, ok := app.musicActions[token]; ok {
			action.userID = userID
			action.chatID = chatID
			action.origin = message
			action.markup = cloneMusicActionMarkup(markupCopy)
			app.musicActions[token] = action
		}
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

func cloneMusicActionMarkup(markup *tb.ReplyMarkup) *tb.ReplyMarkup {
	cloned := &tb.ReplyMarkup{}
	if markup == nil {
		return cloned
	}
	cloned.InlineKeyboard = make([][]tb.InlineButton, len(markup.InlineKeyboard))
	for i, row := range markup.InlineKeyboard {
		cloned.InlineKeyboard[i] = append([]tb.InlineButton(nil), row...)
	}
	return cloned
}

func removeMusicActionRow(markup *tb.ReplyMarkup, token string) *tb.ReplyMarkup {
	updated := &tb.ReplyMarkup{}
	if markup == nil {
		return updated
	}
	needle := "|" + token
	for _, row := range markup.InlineKeyboard {
		belongsToAction := false
		for _, button := range row {
			if button.Unique == musicActionCallbackUnique && strings.HasSuffix(button.Data, needle) {
				belongsToAction = true
				break
			}
		}
		if !belongsToAction {
			updated.InlineKeyboard = append(updated.InlineKeyboard, append([]tb.InlineButton(nil), row...))
		}
	}
	return updated
}

func sameMusicActionMessage(left, right *tb.Message) bool {
	if left == nil || right == nil || left.ID != right.ID || left.Chat == nil || right.Chat == nil {
		return false
	}
	return left.Chat.ID == right.Chat.ID
}

func (app *BotApp) updateMusicActionMarkupForMessage(message *tb.Message, markup *tb.ReplyMarkup) {
	app.musicActionMu.Lock()
	defer app.musicActionMu.Unlock()
	for token, action := range app.musicActions {
		if sameMusicActionMessage(action.origin, message) {
			action.markup = cloneMusicActionMarkup(markup)
			app.musicActions[token] = action
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
	if action.userID > 0 && (c.Sender() == nil || c.Sender().ID != action.userID) {
		return c.Respond(&tb.CallbackResponse{Text: "只有发起入库的人可以操作这首歌", ShowAlert: true})
	}
	if action.chatID != 0 && (c.Chat() == nil || c.Chat().ID != action.chatID) {
		return c.Respond(&tb.CallbackResponse{Text: "请在原来的聊天中操作这首歌", ShowAlert: true})
	}
	var callbackMessage *tb.Message
	if callback := c.Callback(); callback != nil {
		callbackMessage = callback.Message
	}
	if actionName != "confirm_delete" && actionName != "cancel_delete" && !sameMusicActionMessage(action.origin, callbackMessage) {
		return c.Respond(&tb.CallbackResponse{Text: "请使用入库成功消息中的按钮", ShowAlert: true})
	}
	if (actionName == "download") != (action.choice != "") {
		return c.Respond(&tb.CallbackResponse{Text: "操作无效", ShowAlert: true})
	}

	switch actionName {
	case "tidy":
		_ = c.Respond(&tb.CallbackResponse{Text: "正在打开歌单整理…"})
		return app.startPlaylistAssignmentForSong(c, &action)
	case "sources":
		return app.showOtherMusicSources(c, token, action)
	case "download":
		if !app.setMusicActionBusy(token, true) {
			return c.Respond(&tb.CallbackResponse{Text: "已提交，请查看下载进度。"})
		}
		defer app.setMusicActionBusy(token, false)
		if action.confirmSource {
			_ = c.Respond()
			return app.confirmMusicSourceChoice(c, action)
		}
		_ = c.Respond(&tb.CallbackResponse{Text: "正在提交下载…"})
		return app.downloadOtherMusicSource(c, action)
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
		if err := c.Send(fmt.Sprintf("确定删除《%s》吗？\n将删除已入库的文件（%s），此操作不可撤销。", displaySongValue(action.title), action.storage), markup); err != nil {
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

func (app *BotApp) setMusicActionBusy(token string, busy bool) bool {
	app.musicActionMu.Lock()
	defer app.musicActionMu.Unlock()
	app.purgeMusicActionsLocked(time.Now())
	action, ok := app.musicActions[token]
	if !ok || action.deleting || action.busy == busy {
		return false
	}
	action.busy = busy
	app.musicActions[token] = action
	return true
}

func (app *BotApp) showOtherMusicSources(c tb.Context, token string, action pendingMusicAction) error {
	if strings.TrimSpace(action.title) == "" {
		return c.Respond(&tb.CallbackResponse{Text: "歌曲缺少标题，无法搜索音源。", ShowAlert: true})
	}
	if !app.setMusicActionBusy(token, true) {
		return c.Respond(&tb.CallbackResponse{Text: "正在查找其他音源，请稍候。"})
	}
	defer app.setMusicActionBusy(token, false)
	_ = c.Respond(&tb.CallbackResponse{Text: "正在搜索其他音源…"})
	message, err := c.Bot().Send(c.Chat(), "🔎 正在搜索《"+displaySongValue(action.title)+"》的其他音源…")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return app.presentOtherMusicSources(ctx, c, message, action)
}

func (app *BotApp) sendMusicActionAudio(c tb.Context, action pendingMusicAction) error {
	filePath := action.path
	cleanup := func() {}
	if action.storage == "WEBDAV" {
		if core.GlobalWebDAV == nil {
			return fmt.Errorf("WebDAV 未初始化")
		}
		remotePath := filepath.ToSlash(action.path)
		if core.GlobalWebDAV.Config != nil {
			baseDir := strings.Trim(core.GlobalWebDAV.Config.WebDAVDir, "/")
			if baseDir != "" && !strings.HasPrefix(remotePath, baseDir+"/") {
				remotePath = path.Join(baseDir, remotePath)
			}
		}
		tempDir, err := os.MkdirTemp("", "gymdl-telegram-play-")
		if err != nil {
			return fmt.Errorf("创建播放临时目录失败: %w", err)
		}
		cleanup = func() { _ = os.RemoveAll(tempDir) }
		filePath = filepath.Join(tempDir, filepath.Base(remotePath))
		downloadErr := core.GlobalWebDAV.Download(remotePath, filePath)
		if downloadErr != nil {
			// A staged song while NAS is offline is still playable from the
			// durable outbox; never discard it just because the remote is down.
			if staged := stagedMusicPath(remotePath); staged != "" {
				filePath = staged
			} else {
				cleanup()
				return fmt.Errorf("从 WebDAV 读取歌曲失败: %w", downloadErr)
			}
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

func stagedMusicPath(remotePath string) string {
	if core.GlobalWebDAV == nil || core.GlobalWebDAV.Outbox == nil {
		return ""
	}
	remotePath = strings.Trim(filepath.ToSlash(remotePath), "/")
	remoteDir, fileName := path.Split(remotePath)
	items, err := core.GlobalWebDAV.Outbox.List()
	if err != nil {
		return ""
	}
	remoteDir = strings.Trim(remoteDir, "/")
	for _, item := range items {
		if item.FileName == fileName && item.RemoteDir == remoteDir {
			return item.PayloadPath
		}
	}
	return ""
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
			if core.GlobalWebDAV.Outbox != nil {
				if _, removeErr := core.GlobalWebDAV.Outbox.RemoveByRemote(
					path.Dir(action.path), path.Base(action.path),
				); removeErr != nil {
					err = fmt.Errorf("清理待上传任务失败: %w", removeErr)
				}
			}
			if err == nil {
				err = core.GlobalWebDAV.Delete(action.path)
			}
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
		updatedMarkup := removeMusicActionRow(action.markup, token)
		app.updateMusicActionMarkupForMessage(action.origin, updatedMarkup)
		if _, err := c.Bot().EditReplyMarkup(action.origin, updatedMarkup); err != nil {
			utils.WarnWithFormat("[Telegram] 更新歌曲操作按钮失败: %v", err)
		}
	}
	return c.Edit("✅ 已删除《"+displaySongValue(action.title)+"》的入库文件。", &tb.ReplyMarkup{})
}
