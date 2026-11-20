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
	"golang.org/x/text/width"
	tb "gopkg.in/telebot.v4"
)

const (
	playlistDescTTL            = 20 * time.Minute
	playlistDescPageSize       = 6
	playlistDescCallbackUnique = "pldesc"
	// 整行按钮的显示宽度。超出就在末尾省略，避免客户端从歌名中间截断。
	playlistDescButtonWidth = 40
)

// playlistDescSyncTimeout bounds the Navidrome fetch. Tests shorten it.
var playlistDescSyncTimeout = 45 * time.Second

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
	lastClear *playlistDescUndo
	syncing   bool
	expiresAt time.Time
}

type playlistDescUndo struct {
	id   string
	desc string
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
	if hold, ok := app.chatMaintenanceHold(c.Chat().ID, "desc"); ok {
		return app.presentMaintenanceHold(c, hold, "d", nil)
	}
	session := pendingPlaylistDesc{
		token:     strings.ReplaceAll(uuid.NewString(), "-", ""),
		userID:    c.Sender().ID,
		chatID:    c.Chat().ID,
		expiresAt: time.Now().Add(playlistDescTTL),
	}
	if err := app.reloadPlaylistDesc(&session); err != nil {
		return c.Send("读取歌单列表失败：" + discoveryPublicError(err))
	}
	text, markup := formatPlaylistDesc(session, "")
	message, err := c.Bot().Send(c.Chat(), text, tb.ModeHTML, markup)
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
		return c.Edit(text, tb.ModeHTML, markup)
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
		return c.Edit(text, tb.ModeHTML, markup)
	case "back":
		session.editingID = ""
		app.savePlaylistDesc(session)
		if err := c.Respond(); err != nil {
			return err
		}
		text, markup := formatPlaylistDesc(session, "")
		return c.Edit(text, tb.ModeHTML, markup)
	case "clear":
		if session.editingID == "" {
			return c.Respond(&tb.CallbackResponse{Text: "请先选择一张歌单", ShowAlert: true})
		}
		if len(args) != 3 || args[2] != "yes" {
			current := playlistByID(session.playlists, session.editingID)
			if err := c.Respond(&tb.CallbackResponse{Text: "请确认清空"}); err != nil {
				return err
			}
			text, markup := formatPlaylistDescClearConfirm(session, current)
			return c.Edit(text, tb.ModeHTML, markup)
		}
		clearedID := session.editingID
		previous, err := app.playlistCatalog.Description(context.Background(), clearedID)
		if err != nil {
			utils.WarnWithFormat("[Playlist] 读取待清空歌单描述失败 id=%s: %v", clearedID, err)
			return c.Respond(&tb.CallbackResponse{Text: discoveryPublicError(err), ShowAlert: true})
		}
		if err = app.playlistCatalog.SetDesc(context.Background(), clearedID, ""); err != nil {
			utils.WarnWithFormat("[Playlist] 清空歌单描述失败 id=%s: %v", clearedID, err)
			return c.Respond(&tb.CallbackResponse{Text: discoveryPublicError(err), ShowAlert: true})
		}
		utils.InfoWithFormat("[Playlist] 已清空歌单描述 id=%s", clearedID)
		session.editingID = ""
		session.lastClear = &playlistDescUndo{id: clearedID, desc: previous}
		if err = app.reloadPlaylistDesc(&session); err != nil {
			app.deletePlaylistDesc(session.token)
			if respondErr := c.Respond(&tb.CallbackResponse{Text: "描述已清空，但列表刷新失败"}); respondErr != nil {
				return respondErr
			}
			return editPlaylistDescCard(c, "✅ 描述已清空，但列表刷新失败。重新发送 /playlist_desc 查看最新状态。", nil, false)
		}
		app.savePlaylistDesc(session)
		if err := c.Respond(&tb.CallbackResponse{Text: "已清空"}); err != nil {
			return err
		}
		text, markup := formatPlaylistDesc(session, "✅ 描述已清空；可点“撤销清空”恢复上一版。")
		return c.Edit(text, tb.ModeHTML, markup)
	case "clear_cancel":
		if session.editingID == "" {
			return c.Respond(&tb.CallbackResponse{Text: "请先选择一张歌单", ShowAlert: true})
		}
		if err := c.Respond(); err != nil {
			return err
		}
		text, markup := formatPlaylistDesc(session, "")
		return c.Edit(text, tb.ModeHTML, markup)
	case "clear_undo":
		if session.lastClear == nil || session.lastClear.id == "" {
			return c.Respond(&tb.CallbackResponse{Text: "没有可撤销的清空操作", ShowAlert: true})
		}
		undo := *session.lastClear
		if err := app.playlistCatalog.SetDesc(context.Background(), undo.id, undo.desc); err != nil {
			utils.WarnWithFormat("[Playlist] 撤销清空歌单描述失败 id=%s: %v", undo.id, err)
			return c.Respond(&tb.CallbackResponse{Text: discoveryPublicError(err), ShowAlert: true})
		}
		session.lastClear = nil
		if err := app.reloadPlaylistDesc(&session); err != nil {
			return c.Respond(&tb.CallbackResponse{Text: "描述已恢复，但列表刷新失败", ShowAlert: true})
		}
		app.savePlaylistDesc(session)
		if err := c.Respond(&tb.CallbackResponse{Text: "已恢复描述"}); err != nil {
			return err
		}
		text, markup := formatPlaylistDesc(session, "✅ 已恢复清空前的描述。")
		return c.Edit(text, tb.ModeHTML, markup)
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
		if err := app.updatePlaylistDescCard(c, session, "已退出描述编辑；这条链接会继续按下载处理。", false); err != nil {
			return true, err
		}
		return false, nil
	}
	savedID := session.editingID
	if session.lastClear != nil && session.lastClear.id == savedID {
		session.lastClear = nil
	}
	if err := app.playlistCatalog.SetDesc(context.Background(), savedID, c.Text()); err != nil {
		utils.WarnWithFormat("[Playlist] 保存歌单描述失败 id=%s: %v", savedID, err)
		return true, app.updatePlaylistDescCard(c, session, "❌ 保存失败："+discoveryPublicError(err)+"。请修正后重新发送。", false)
	}
	utils.InfoWithFormat("[Playlist] 已保存歌单描述 id=%s", savedID)
	session.editingID = ""
	if err := app.reloadPlaylistDesc(&session); err != nil {
		app.deletePlaylistDesc(session.token)
		return true, app.updatePlaylistDescCard(c, session, "✅ 描述已保存，但列表刷新失败。重新发送 /playlist_desc 查看最新状态。", true)
	}
	return true, app.updatePlaylistDescCard(c, session, "✅ 描述已保存；整理歌单时会使用这条说明。", false)
}

func (app *BotApp) updatePlaylistDescCard(c tb.Context, session pendingPlaylistDesc, notice string, closed bool) error {
	text, markup := formatPlaylistDesc(session, notice)
	if closed {
		text = notice
		markup = &tb.ReplyMarkup{}
	}
	bot := c.Bot()
	if session.message != nil && bot != nil {
		var err error
		if closed {
			_, err = bot.Edit(session.message, text, markup)
		} else {
			_, err = bot.Edit(session.message, text, tb.ModeHTML, markup)
		}
		if err == nil || telegramMessageUnchanged(err) {
			if !closed {
				app.savePlaylistDesc(session)
			}
			return nil
		}
	}
	if bot == nil {
		if closed {
			return c.Send(text, markup)
		}
		return c.Send(text, tb.ModeHTML, markup)
	}
	if c.Chat() == nil {
		return fmt.Errorf("无法更新歌单描述消息，请重新发送 /playlist_desc")
	}
	var message *tb.Message
	var err error
	if closed {
		message, err = bot.Send(c.Chat(), text, markup)
	} else {
		message, err = bot.Send(c.Chat(), text, tb.ModeHTML, markup)
	}
	if err != nil {
		return err
	}
	if !closed {
		session.message = message
		app.savePlaylistDesc(session)
	}
	return nil
}

func (app *BotApp) syncPlaylistDesc(c tb.Context, session pendingPlaylistDesc) error {
	if app.cfg == nil || app.cfg.PlaylistOrganizer == nil {
		return c.Respond(&tb.CallbackResponse{Text: "尚未配置 Navidrome", ShowAlert: true})
	}
	if app.playlistCatalog == nil {
		return c.Respond(&tb.CallbackResponse{Text: "歌单库还没准备好", ShowAlert: true})
	}
	if !app.discoveryCommandMu.TryLock() {
		return c.Respond(&tb.CallbackResponse{Text: "正在处理上一条请求", ShowAlert: true})
	}
	if err := c.Respond(&tb.CallbackResponse{Text: "正在同步歌单列表…"}); err != nil {
		app.discoveryCommandMu.Unlock()
		return err
	}
	if c.Message() == nil {
		app.discoveryCommandMu.Unlock()
		return c.Send("找不到原来的消息，请重新发送 /playlist_desc。")
	}
	if err := editPlaylistDescCard(c, "⌛ 正在从 Navidrome 同步歌单列表…\n完成后会回到这张卡片。", nil, false); err != nil {
		app.discoveryCommandMu.Unlock()
		return err
	}
	session.editingID = ""
	session.syncing = true
	app.savePlaylistDesc(session)
	organizer := app.cfg.PlaylistOrganizer
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				utils.WarnWithFormat("[Playlist] 同步歌单列表失败: %v", recovered)
			}
		}()
		defer app.discoveryCommandMu.Unlock()
		defer app.setPlaylistDescSyncing(session.token, false)
		ctx, cancel := context.WithTimeout(context.Background(), playlistDescSyncTimeout)
		defer cancel()
		remotes, err := playlistcatalog.Fetch(ctx, organizer)
		if err != nil {
			utils.WarnWithFormat("[Playlist] 同步歌单列表失败: %v", err)
			app.finishPlaylistDescSync(c, session, "❌ 同步失败："+discoveryPublicError(err)+"\n可稍后重试。", false)
			return
		}
		result, err := app.playlistCatalog.Apply(ctx, remotes, nil, time.Now())
		if err != nil {
			utils.WarnWithFormat("[Playlist] 写入歌单列表失败: %v", err)
			app.finishPlaylistDescSync(c, session, "❌ 同步失败："+discoveryPublicError(err)+"\n可稍后重试。", false)
			return
		}
		session.editingID = ""
		session.page = 0
		session.syncing = false
		if err = app.reloadPlaylistDesc(&session); err != nil {
			utils.WarnWithFormat("[Playlist] 歌单列表已同步，但重新读取失败: %v", err)
			app.deletePlaylistDesc(session.token)
			app.finishPlaylistDescSync(c, session, "✅ Navidrome 同步已完成，但重新读取歌单列表失败："+discoveryPublicError(err)+"\n重新发送 /playlist_desc 查看最新状态。", true)
			return
		}
		app.savePlaylistDesc(session)
		utils.InfoWithFormat("[Playlist] 歌单列表已同步，新增 %d，更新 %d，停用 %d", result.Added, result.Updated, result.Inactive)
		notice := fmt.Sprintf("✅ 同步完成 · 新增 %d · 更新 %d · 停用 %d", result.Added, result.Updated, result.Inactive)
		app.finishPlaylistDescSync(c, session, notice, false)
	}()
	return nil
}

func (app *BotApp) finishPlaylistDescSync(c tb.Context, session pendingPlaylistDesc, notice string, closed bool) {
	text, markup := formatPlaylistDesc(session, notice)
	html := true
	if closed {
		text = notice
		markup = nil
		html = false
	}
	if err := editPlaylistDescCard(c, text, markup, html); err != nil {
		utils.WarnWithFormat("[Playlist] 更新歌单描述失败: %v", err)
	}
}

func editPlaylistDescCard(c tb.Context, text string, markup *tb.ReplyMarkup, html bool) error {
	if markup == nil {
		markup = &tb.ReplyMarkup{}
	}
	var err error
	if c.Message() == nil {
		if html {
			err = c.Send(text, tb.ModeHTML, markup)
		} else {
			err = c.Send(text, markup)
		}
	} else if html {
		err = c.Edit(text, tb.ModeHTML, markup)
	} else {
		err = c.Edit(text, markup)
	}
	if telegramMessageUnchanged(err) {
		return nil
	}
	return err
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
	text.WriteString("🎵 <b>歌单描述</b>\n")
	text.WriteString("──────────────────")
	if notice != "" {
		text.WriteString("\n")
		text.WriteString(utils.EscapeHTML(notice))
	}
	if !session.synced {
		text.WriteString("\n歌单列表尚未同步。\n同步后会显示可编辑歌单。")
		markup.Inline(playlistDescActionRows(markup, session, false)...)
		return text.String(), markup
	}
	if session.editingID != "" {
		current := playlistByID(session.playlists, session.editingID)
		fmt.Fprintf(&text, "\n\n<b>%s</b>", utils.EscapeHTML(utils.TruncateString(current.Name, 40)))
		if strings.TrimSpace(current.Desc) == "" {
			text.WriteString("\n\n还没有描述")
		} else {
			fmt.Fprintf(&text, "\n\n%s", utils.EscapeHTML(utils.TruncateString(current.Desc, 180)))
		}
		fmt.Fprintf(&text, "\n\n当前 %d / 200 字\n发送新描述即可保存", len([]rune(current.Desc)))
		editRow := tb.Row{markup.Data("↩ 返回列表", playlistDescCallbackUnique, "back", session.token)}
		if strings.TrimSpace(current.Desc) != "" {
			editRow = append(editRow, markup.Data("⌫ 清空描述", playlistDescCallbackUnique, "clear", session.token))
		}
		markup.Inline(
			editRow,
			markup.Row(markup.Data("✕ 取消", playlistDescCallbackUnique, "cancel", session.token)),
		)
		return text.String(), markup
	}
	if len(session.playlists) == 0 {
		text.WriteString("\n当前没有可编辑的歌单。")
		markup.Inline(playlistDescActionRows(markup, session, false)...)
		return text.String(), markup
	}
	fmt.Fprintf(&text, "\n第 %d / %d 页 · 共 %d 张", session.page+1, playlistDescPages(len(session.playlists)), len(session.playlists))
	if missing := playlistDescMissingCount(session.playlists); missing > 0 {
		fmt.Fprintf(&text, "\n还有 %d 张没写描述，排在前面。", missing)
	}
	start := session.page * playlistDescPageSize
	if start > len(session.playlists) {
		start = 0
	}
	end := min(start+playlistDescPageSize, len(session.playlists))
	var rows []tb.Row
	for index := start; index < end; index++ {
		rows = append(rows, markup.Row(markup.Data(playlistDescButtonLabel(session.playlists[index]), playlistDescCallbackUnique, "open", session.token, strconv.Itoa(index))))
	}
	rows = append(rows, playlistDescActionRows(markup, session, end < len(session.playlists))...)
	if session.lastClear != nil && session.lastClear.id != "" {
		rows = append(rows, markup.Row(markup.Data("↶ 撤销清空", playlistDescCallbackUnique, "clear_undo", session.token)))
	}
	markup.Inline(rows...)
	return text.String(), markup
}

func formatPlaylistDescClearConfirm(session pendingPlaylistDesc, current playlistcatalog.Playlist) (string, *tb.ReplyMarkup) {
	text := fmt.Sprintf("清空歌单描述？\n\n<b>%s</b>\n\n当前描述：\n%s\n\n清空后 /assign_playlist 和 /ai_playlist 将不再参考这条说明。", utils.EscapeHTML(utils.TruncateString(current.Name, 40)), utils.EscapeHTML(utils.TruncateString(current.Desc, 180)))
	markup := &tb.ReplyMarkup{}
	markup.Inline(
		markup.Row(
			markup.Data("确认清空", playlistDescCallbackUnique, "clear", session.token, "yes"),
			markup.Data("返回", playlistDescCallbackUnique, "clear_cancel", session.token),
		),
	)
	return text, markup
}

// playlistDescActionRows 把翻页、同步、取消放在名称列表下面。
// 只有一个翻页键时和操作挤在同一行，否则它会拉满整行，看起来像另一张歌单。
// 三个按键挤在一行时，「同步歌单」会从中间被截断，所以改成「同步」。
func playlistDescActionRows(markup *tb.ReplyMarkup, session pendingPlaylistDesc, hasNext bool) []tb.Row {
	token := session.token
	cancel := markup.Data("✕ 取消", playlistDescCallbackUnique, "cancel", token)
	var nav tb.Row
	if session.page > 0 {
		nav = append(nav, markup.Data("◀ 上一页", playlistDescCallbackUnique, "page", token, strconv.Itoa(session.page-1)))
	}
	if hasNext {
		nav = append(nav, markup.Data("下一页 ▶", playlistDescCallbackUnique, "page", token, strconv.Itoa(session.page+1)))
	}
	switch len(nav) {
	case 0:
		return []tb.Row{markup.Row(markup.Data("↻ 同步歌单", playlistDescCallbackUnique, "sync", token), cancel)}
	case 1:
		return []tb.Row{markup.Row(nav[0], markup.Data("↻ 同步", playlistDescCallbackUnique, "sync", token), cancel)}
	default:
		return []tb.Row{
			nav,
			markup.Row(markup.Data("↻ 同步歌单", playlistDescCallbackUnique, "sync", token), cancel),
		}
	}
}

func playlistDescMissingCount(rows []playlistcatalog.Playlist) int {
	missing := 0
	for _, item := range rows {
		if strings.TrimSpace(item.Desc) == "" {
			missing++
		}
	}
	return missing
}

func playlistDescButtonLabel(item playlistcatalog.Playlist) string {
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = "未命名歌单"
	}
	return truncateDisplayWidth(name, playlistDescButtonWidth)
}

func truncateDisplayWidth(s string, max int) string {
	if max <= 1 || playlistDescDisplayWidth(s) <= max {
		return s
	}
	budget := max - 1
	used := 0
	var b strings.Builder
	for _, r := range s {
		w := playlistDescRuneWidth(r)
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	b.WriteString("…")
	return b.String()
}

func playlistDescDisplayWidth(s string) int {
	total := 0
	for _, r := range s {
		total += playlistDescRuneWidth(r)
	}
	return total
}

func playlistDescRuneWidth(r rune) int {
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	default:
		return 1
	}
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
	// Callers often pass an older copy of the session after reloading. Keep
	// undo state from the stored session so it is not silently dropped.
	if stored, ok := app.playlistDescSessions[session.token]; ok && session.lastClear == nil {
		session.lastClear = stored.lastClear
	}
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

// maintenanceConflict names the other open maintenance card. opening is the
// command being started and is not treated as a conflict with itself.
func (app *BotApp) maintenanceConflict(chatID int64, opening string) string {
	hold, ok := app.chatMaintenanceHold(chatID, opening)
	if !ok {
		return ""
	}
	if hold.processing {
		return hold.busyText
	}
	return hold.text
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

func (app *BotApp) setPlaylistDescSyncing(token string, syncing bool) {
	app.playlistDescMu.Lock()
	defer app.playlistDescMu.Unlock()
	session, ok := app.playlistDescSessions[token]
	if !ok {
		return
	}
	session.syncing = syncing
	app.playlistDescSessions[token] = session
}

func (app *BotApp) cancelPlaylistDesc(token string) bool {
	app.playlistDescMu.Lock()
	defer app.playlistDescMu.Unlock()
	session, ok := app.playlistDescSessions[token]
	if !ok || session.syncing {
		return false
	}
	delete(app.playlistDescSessions, token)
	if app.playlistDescByChat[session.chatID] == token {
		delete(app.playlistDescByChat, session.chatID)
	}
	return true
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
