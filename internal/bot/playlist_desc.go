package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core/linkparser"
	"github.com/nichuanfang/gymdl/internal/playlistcatalog"
	"github.com/nichuanfang/gymdl/internal/storage"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

const (
	playlistDescTTL            = 20 * time.Minute
	playlistDescPageSize       = 8
	playlistDescCallbackUnique = "pldesc"
)

var playlistDescEndpoint = tb.InlineButton{Unique: playlistDescCallbackUnique}

type pendingPlaylistDesc struct {
	token     string
	userID    int64
	chatID    int64
	message   *tb.Message
	playlists []playlistcatalog.Playlist
	synced    bool
	page      int
	editingID string
	expiresAt time.Time
}

func (app *BotApp) initPlaylistCatalog() {
	store, err := playlistcatalog.Open(storage.DefaultDatabasePath)
	if err != nil {
		utils.WarnWithFormat("[Playlist] 歌单库初始化失败: %v", err)
		return
	}
	app.playlistCatalog = store
}

func loadTidyPlaylists(ctx context.Context, catalog *playlistcatalog.Store) ([]config.PlaylistInfo, bool, error) {
	if catalog == nil {
		return nil, false, nil
	}
	synced, err := catalog.Synced(ctx)
	if err != nil {
		return nil, false, err
	}
	if !synced {
		return nil, false, nil
	}
	rows, err := catalog.Active(ctx)
	if err != nil {
		return nil, true, err
	}
	entries := make([]config.PlaylistInfo, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, config.PlaylistInfo{Name: row.Name, Desc: row.Desc})
	}
	return tidyPlaylistEntries(entries), true, nil
}

func tidyPlaylistEntries(entries []config.PlaylistInfo) []config.PlaylistInfo {
	cleaned := make([]config.PlaylistInfo, 0, len(entries))
	for _, entry := range entries {
		if playlistcatalog.TidyName(entry.Name) {
			cleaned = append(cleaned, entry)
		}
	}
	return cleaned
}

func (app *BotApp) PlaylistDescCommand(c tb.Context) error {
	if c.Chat() == nil || c.Sender() == nil || c.Chat().Type != tb.ChatPrivate {
		return c.Send("请在私聊中修改歌单描述。")
	}
	if app.playlistCatalog == nil {
		return c.Send("歌单库还没准备好，请检查数据库。")
	}
	if reason := app.maintenanceConflict(c.Chat().ID, "desc"); reason != "" {
		return c.Send(reason)
	}
	session := pendingPlaylistDesc{
		token:     strings.ReplaceAll(uuid.NewString(), "-", ""),
		userID:    c.Sender().ID,
		chatID:    c.Chat().ID,
		expiresAt: time.Now().Add(playlistDescTTL),
	}
	if err := app.reloadPlaylistDesc(&session); err != nil {
		return c.Send(err.Error())
	}
	text, markup := formatPlaylistDesc(session, "")
	message, err := c.Bot().Send(c.Chat(), text, markup)
	if err != nil {
		return err
	}
	session.message = message
	app.savePlaylistDesc(session)
	return nil
}

func (app *BotApp) HandlePlaylistDescCallback(c tb.Context) error {
	args := c.Args()
	if len(args) < 2 {
		return c.Respond(&tb.CallbackResponse{Text: "按钮无效，请重新发送 /playlist_desc", ShowAlert: true})
	}
	session, ok := app.getPlaylistDesc(args[1], c.Sender().ID, chatID(c))
	if !ok {
		return c.Respond(&tb.CallbackResponse{Text: "会话已过期，请重新发送 /playlist_desc", ShowAlert: true})
	}
	switch args[0] {
	case "cancel":
		app.deletePlaylistDesc(session.token)
		if err := c.Respond(); err != nil {
			return err
		}
		return c.Edit("已取消。", &tb.ReplyMarkup{})
	case "page":
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "页码无效", ShowAlert: true})
		}
		page, err := strconv.Atoi(args[2])
		if err != nil || page < 0 {
			return c.Respond(&tb.CallbackResponse{Text: "页码无效", ShowAlert: true})
		}
		session.page = page
		session.editingID = ""
		app.savePlaylistDesc(session)
		if err := c.Respond(&tb.CallbackResponse{Text: "已换页"}); err != nil {
			return err
		}
		text, markup := formatPlaylistDesc(session, "")
		return c.Edit(text, markup)
	case "open":
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "请重新选择歌单", ShowAlert: true})
		}
		index, err := strconv.Atoi(args[2])
		if err != nil || index < 0 || index >= len(session.playlists) {
			return c.Respond(&tb.CallbackResponse{Text: "这张歌单不在当前列表里", ShowAlert: true})
		}
		session.editingID = session.playlists[index].ID
		app.savePlaylistDesc(session)
		if err := c.Respond(&tb.CallbackResponse{Text: "直接发送新描述"}); err != nil {
			return err
		}
		text, markup := formatPlaylistDesc(session, "")
		return c.Edit(text, markup)
	case "back":
		session.editingID = ""
		app.savePlaylistDesc(session)
		if err := c.Respond(); err != nil {
			return err
		}
		text, markup := formatPlaylistDesc(session, "")
		return c.Edit(text, markup)
	case "clear":
		if session.editingID == "" {
			return c.Respond(&tb.CallbackResponse{Text: "请先选择一张歌单", ShowAlert: true})
		}
		if err := app.playlistCatalog.SetDesc(context.Background(), session.editingID, ""); err != nil {
			return c.Respond(&tb.CallbackResponse{Text: err.Error(), ShowAlert: true})
		}
		session.editingID = ""
		if err := app.reloadPlaylistDesc(&session); err != nil {
			return c.Respond(&tb.CallbackResponse{Text: err.Error(), ShowAlert: true})
		}
		app.savePlaylistDesc(session)
		if err := c.Respond(&tb.CallbackResponse{Text: "已清空"}); err != nil {
			return err
		}
		text, markup := formatPlaylistDesc(session, "描述已清空。")
		return c.Edit(text, markup)
	case "sync":
		return app.syncPlaylistDesc(c, session)
	default:
		return c.Respond(&tb.CallbackResponse{Text: "按钮无效", ShowAlert: true})
	}
}

func (app *BotApp) handlePlaylistDescText(c tb.Context) (bool, error) {
	if c.Chat() == nil || c.Sender() == nil || app.playlistCatalog == nil {
		return false, nil
	}
	session, ok := app.playlistDescAwaiting(c.Chat().ID, c.Sender().ID)
	if !ok {
		return false, nil
	}
	// A link always downloads. Leaving edit mode keeps the description unchanged
	// and lets HandleText continue with the normal download path.
	if link, _ := linkparser.ParseLink(c.Text()); link != "" {
		session.editingID = ""
		app.savePlaylistDesc(session)
		return false, nil
	}
	if err := app.playlistCatalog.SetDesc(context.Background(), session.editingID, c.Text()); err != nil {
		return true, c.Send(err.Error())
	}
	session.editingID = ""
	if err := app.reloadPlaylistDesc(&session); err != nil {
		return true, c.Send(err.Error())
	}
	app.savePlaylistDesc(session)
	text, markup := formatPlaylistDesc(session, "描述已保存。整理歌单时会用这条描述。")
	if session.message != nil {
		if _, err := c.Bot().Edit(session.message, text, markup); err == nil {
			return true, nil
		}
	}
	message, err := c.Bot().Send(c.Chat(), text, markup)
	if err != nil {
		return true, err
	}
	session.message = message
	app.savePlaylistDesc(session)
	return true, nil
}

func (app *BotApp) syncPlaylistDesc(c tb.Context, session pendingPlaylistDesc) error {
	if app.cfg == nil || app.cfg.PlaylistOrganizer == nil {
		return c.Respond(&tb.CallbackResponse{Text: "尚未配置 Navidrome", ShowAlert: true})
	}
	if !app.discoveryCommandMu.TryLock() {
		return c.Respond(&tb.CallbackResponse{Text: "正在处理上一条请求", ShowAlert: true})
	}
	defer app.discoveryCommandMu.Unlock()
	if err := c.Respond(&tb.CallbackResponse{Text: "开始同步"}); err != nil {
		return err
	}
	if c.Message() != nil {
		_, _ = c.Bot().Edit(c.Message(), "⌛ 正在从 Navidrome 同步歌单…")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	remotes, err := playlistcatalog.Fetch(ctx, app.cfg.PlaylistOrganizer)
	if err != nil {
		return editPlaylistDescMessage(c, "同步失败："+err.Error())
	}
	result, err := app.playlistCatalog.Apply(ctx, remotes, nil, time.Now())
	if err != nil {
		return editPlaylistDescMessage(c, "同步失败："+err.Error())
	}
	session.editingID = ""
	session.page = 0
	if err = app.reloadPlaylistDesc(&session); err != nil {
		return editPlaylistDescMessage(c, err.Error())
	}
	app.savePlaylistDesc(session)
	notice := fmt.Sprintf("同步完成。新增 %d，更新 %d，停用 %d。", result.Added, result.Updated, result.Inactive)
	text, markup := formatPlaylistDesc(session, notice)
	if c.Message() == nil {
		_, err = c.Bot().Send(c.Chat(), text, markup)
		return err
	}
	_, err = c.Bot().Edit(c.Message(), text, markup)
	return err
}

func editPlaylistDescMessage(c tb.Context, text string) error {
	if c.Message() != nil {
		_, err := c.Bot().Edit(c.Message(), text, &tb.ReplyMarkup{})
		return err
	}
	return c.Send(text)
}

func (app *BotApp) reloadPlaylistDesc(session *pendingPlaylistDesc) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	synced, err := app.playlistCatalog.Synced(ctx)
	if err != nil {
		return err
	}
	session.synced = synced
	if !synced {
		session.playlists = nil
		return nil
	}
	rows, err := app.playlistCatalog.Active(ctx)
	if err != nil {
		return err
	}
	session.playlists = rows
	pages := playlistDescPages(len(rows))
	if session.page >= pages {
		session.page = pages - 1
	}
	if session.page < 0 {
		session.page = 0
	}
	return nil
}

func formatPlaylistDesc(session pendingPlaylistDesc, notice string) (string, *tb.ReplyMarkup) {
	markup := &tb.ReplyMarkup{}
	var text strings.Builder
	text.WriteString("歌单描述")
	if notice != "" {
		text.WriteString("\n")
		text.WriteString(notice)
	}
	if !session.synced {
		text.WriteString("\n还没同步。同步后，Navidrome 里的新歌单会出现在这里。原来 yaml 里的描述会留给同名新歌单。")
		markup.Inline(
			markup.Row(markup.Data("↻ 同步歌单", playlistDescCallbackUnique, "sync", session.token)),
			markup.Row(markup.Data("✕ 取消", playlistDescCallbackUnique, "cancel", session.token)),
		)
		return text.String(), markup
	}
	if session.editingID != "" {
		current := playlistByID(session.playlists, session.editingID)
		fmt.Fprintf(&text, "\n%s", utils.TruncateString(current.Name, 40))
		if strings.TrimSpace(current.Desc) == "" {
			text.WriteString("\n当前还没有描述。")
		} else {
			fmt.Fprintf(&text, "\n当前：%s", utils.TruncateString(current.Desc, 180))
		}
		text.WriteString("\n\n直接发送新描述，最多 200 字。")
		markup.Inline(
			markup.Row(markup.Data("↩ 返回", playlistDescCallbackUnique, "back", session.token), markup.Data("⌫ 清空", playlistDescCallbackUnique, "clear", session.token)),
			markup.Row(markup.Data("✕ 取消", playlistDescCallbackUnique, "cancel", session.token)),
		)
		return text.String(), markup
	}
	fmt.Fprintf(&text, "\n%d 张在用。点名称修改。没有描述的排在前面。", len(session.playlists))
	start := session.page * playlistDescPageSize
	if start > len(session.playlists) {
		start = 0
	}
	end := min(start+playlistDescPageSize, len(session.playlists))
	var rows []tb.Row
	for index := start; index < end; index += 2 {
		row := tb.Row{markup.Data(playlistDescButtonLabel(session.playlists[index]), playlistDescCallbackUnique, "open", session.token, strconv.Itoa(index))}
		if index+1 < end {
			row = append(row, markup.Data(playlistDescButtonLabel(session.playlists[index+1]), playlistDescCallbackUnique, "open", session.token, strconv.Itoa(index+1)))
		}
		rows = append(rows, row)
	}
	var nav tb.Row
	if session.page > 0 {
		nav = append(nav, markup.Data("◀ 上一页", playlistDescCallbackUnique, "page", session.token, strconv.Itoa(session.page-1)))
	}
	if end < len(session.playlists) {
		nav = append(nav, markup.Data("下一页 ▶", playlistDescCallbackUnique, "page", session.token, strconv.Itoa(session.page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	// 翻页、同步、取消各占操作行，不和歌单名称挤在同一行。
	rows = append(rows,
		markup.Row(markup.Data("↻ 同步歌单", playlistDescCallbackUnique, "sync", session.token)),
		markup.Row(markup.Data("✕ 取消", playlistDescCallbackUnique, "cancel", session.token)),
	)
	markup.Inline(rows...)
	return text.String(), markup
}

func playlistDescButtonLabel(item playlistcatalog.Playlist) string {
	label := item.Name
	if strings.TrimSpace(item.Desc) == "" {
		label += " · 未写"
	}
	return utils.TruncateString(label, 30)
}

func playlistDescPages(count int) int {
	if count <= 0 {
		return 1
	}
	return (count + playlistDescPageSize - 1) / playlistDescPageSize
}

func playlistByID(rows []playlistcatalog.Playlist, id string) playlistcatalog.Playlist {
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	return playlistcatalog.Playlist{ID: id, Name: "这张歌单"}
}

func chatID(c tb.Context) int64 {
	if c.Chat() == nil {
		return 0
	}
	return c.Chat().ID
}

func (app *BotApp) savePlaylistDesc(session pendingPlaylistDesc) {
	app.playlistDescMu.Lock()
	defer app.playlistDescMu.Unlock()
	app.ensurePlaylistDescLocked(time.Now())
	if previous, ok := app.playlistDescByChat[session.chatID]; ok && previous != session.token {
		delete(app.playlistDescSessions, previous)
	}
	app.playlistDescSessions[session.token] = session
	app.playlistDescByChat[session.chatID] = session.token
}

func (app *BotApp) getPlaylistDesc(token string, userID, chatID int64) (pendingPlaylistDesc, bool) {
	app.playlistDescMu.Lock()
	defer app.playlistDescMu.Unlock()
	app.ensurePlaylistDescLocked(time.Now())
	session, ok := app.playlistDescSessions[token]
	if !ok || session.userID != userID || session.chatID != chatID {
		return pendingPlaylistDesc{}, false
	}
	return session, true
}

// maintenanceConflict names the other open maintenance card so the user knows
// which message to finish. opening is the command being started and is not
// treated as a conflict with itself.
func (app *BotApp) maintenanceConflict(chatID int64, opening string) string {
	if opening != "trash" && app.hasPendingEmptyTrash(chatID) {
		return "清空回收站还等你确认。请先在那条消息里确认或取消。"
	}
	if opening != "assign" && app.hasPlaylistAssignmentForChat(chatID) {
		return "歌单整理还开着。请先在那条消息里完成或点取消。"
	}
	if opening != "ai" && app.hasAIPlaylistSessionForChat(chatID) {
		return "AI 歌单还开着。请先在那条消息里完成或点取消。"
	}
	if opening != "desc" && app.hasPlaylistDescForChat(chatID) {
		return "歌单描述还开着。请先在那条消息里保存或点取消。"
	}
	return ""
}

func (app *BotApp) hasPlaylistDescForChat(chatID int64) bool {
	app.playlistDescMu.Lock()
	defer app.playlistDescMu.Unlock()
	app.ensurePlaylistDescLocked(time.Now())
	_, ok := app.playlistDescByChat[chatID]
	return ok
}

func (app *BotApp) playlistDescAwaiting(chatID, userID int64) (pendingPlaylistDesc, bool) {
	app.playlistDescMu.Lock()
	defer app.playlistDescMu.Unlock()
	app.ensurePlaylistDescLocked(time.Now())
	token, ok := app.playlistDescByChat[chatID]
	if !ok {
		return pendingPlaylistDesc{}, false
	}
	session := app.playlistDescSessions[token]
	if session.userID != userID || session.editingID == "" {
		return pendingPlaylistDesc{}, false
	}
	return session, true
}

func (app *BotApp) deletePlaylistDesc(token string) {
	app.playlistDescMu.Lock()
	defer app.playlistDescMu.Unlock()
	session, ok := app.playlistDescSessions[token]
	if !ok {
		return
	}
	delete(app.playlistDescSessions, token)
	if app.playlistDescByChat[session.chatID] == token {
		delete(app.playlistDescByChat, session.chatID)
	}
}

func (app *BotApp) ensurePlaylistDescLocked(now time.Time) {
	if app.playlistDescSessions == nil {
		app.playlistDescSessions = map[string]pendingPlaylistDesc{}
	}
	if app.playlistDescByChat == nil {
		app.playlistDescByChat = map[int64]string{}
	}
	for token, session := range app.playlistDescSessions {
		if now.After(session.expiresAt) {
			delete(app.playlistDescSessions, token)
			if app.playlistDescByChat[session.chatID] == token {
				delete(app.playlistDescByChat, session.chatID)
			}
		}
	}
}
