package bot

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/internal/playlistcatalog"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v4"
)

type playlistReviewStage int

const (
	playlistReviewList playlistReviewStage = iota
	playlistReviewTheme
	playlistReviewResults
	playlistReviewConfirm
	playlistReviewDone
)

type playlistReviewSession struct {
	stage     playlistReviewStage
	playlists []playlistcatalog.Playlist
	target    playlistcatalog.Playlist
	snapshot  playlistReviewSnapshot
	theme     string
	editing   bool
	page      int
	addTab    bool
	report    playlistReviewReport
	selected  map[string]bool
	notice    string
}

const (
	playlistReviewListPageSize = 8
	playlistReviewPageSize     = 5
)

func (app *BotApp) handlePlaylistReviewCallback(c tb.Context, session pendingAIPlaylistSession, args []string) error {
	review := session.review
	if args[0] == "r_open" && len(args) == 2 && review == nil && session.target.ID == "" && !session.creating {
		review = &playlistReviewSession{stage: playlistReviewList}
		return app.runPlaylistReviewCallback(c, session, review, "正在读取普通歌单…", app.loadPlaylistReviewList)
	}
	if review == nil || len(args) < 3 || len(args) > 4 || args[2] != strconv.FormatUint(session.reviewRevision, 36) {
		return c.Respond(&tb.CallbackResponse{Text: "卡片已更新，请使用当前按钮。"})
	}
	var index int
	if len(args) == 4 {
		var err error
		index, err = strconv.Atoi(args[3])
		if err != nil || index < 0 {
			return c.Respond(&tb.CallbackResponse{Text: "无效的选项"})
		}
	}
	switch args[0] {
	case "r_cancel":
		if len(args) == 3 && app.cancelPlaylistReview(session) {
			_ = c.Respond()
			return c.Edit("已结束普通歌单检查。", &tb.ReplyMarkup{})
		}
	case "r_home":
		if len(args) == 3 {
			return app.showPlaylistReview(c, session, nil)
		}
	case "r_ls":
		if len(args) == 3 {
			return app.runPlaylistReviewCallback(c, session, &playlistReviewSession{}, "正在读取普通歌单…", app.loadPlaylistReviewList)
		}
	case "r_pick":
		if len(args) == 4 && review.stage == playlistReviewList && index < len(review.playlists) && index/playlistReviewListPageSize == review.page {
			next := &playlistReviewSession{stage: playlistReviewTheme, target: review.playlists[index]}
			return app.runPlaylistReviewCallback(c, session, next, "正在读取歌单主题和曲目…", app.loadPlaylistReviewTarget)
		}
	case "r_theme":
		if len(args) == 3 && review.stage == playlistReviewTheme && strings.TrimSpace(review.target.Desc) == "" {
			review.editing = true
			review.notice = ""
			return app.showPlaylistReview(c, session, review)
		}
	case "r_scan":
		if len(args) == 3 && review.stage == playlistReviewTheme && review.theme != "" && !review.editing {
			return app.runPlaylistReviewCallback(c, session, review, "正在检查现有曲目并检索补歌…", app.generatePlaylistReview)
		}
	case "r_redo":
		if len(args) == 3 && review.target.ID != "" && review.stage != playlistReviewList {
			next := &playlistReviewSession{stage: playlistReviewTheme, target: review.target}
			if strings.TrimSpace(next.target.Desc) == "" {
				next.theme = review.theme
			}
			return app.runPlaylistReviewCallback(c, session, next, "正在重新读取歌单…", app.loadPlaylistReviewTarget)
		}
	case "r_remove", "r_add":
		if len(args) == 3 && review.stage == playlistReviewResults {
			review.addTab = args[0] == "r_add"
			review.page = 0
			return app.showPlaylistReview(c, session, review)
		}
	case "r_page":
		if len(args) == 4 && index < review.pages() {
			review.page = index
			return app.showPlaylistReview(c, session, review)
		}
	case "r_toggle":
		options := review.options()
		if len(args) == 4 && review.stage == playlistReviewResults && !review.snapshot.readOnly() && index < len(options) && index/playlistReviewPageSize == review.page {
			id := options[index].ID
			if review.selected[id] {
				delete(review.selected, id)
			} else {
				review.selected[id] = true
			}
			return app.showPlaylistReview(c, session, review)
		}
	case "r_check":
		if len(args) == 3 && review.stage == playlistReviewResults && !review.snapshot.readOnly() && len(review.selected) > 0 {
			review.stage = playlistReviewConfirm
			review.page = 0
			return app.showPlaylistReview(c, session, review)
		}
	case "r_back":
		if len(args) == 3 && review.stage == playlistReviewConfirm {
			review.stage = playlistReviewResults
			review.notice = ""
			review.page = 0
			return app.showPlaylistReview(c, session, review)
		}
	case "r_apply":
		if len(args) == 3 && review.stage == playlistReviewConfirm && !review.snapshot.readOnly() && len(review.selected) > 0 {
			return app.runPlaylistReviewCallback(c, session, review, "正在应用并核对歌单更改…", app.commitPlaylistReview)
		}
	}
	return c.Respond(&tb.CallbackResponse{Text: "当前步骤无法执行此操作。"})
}

func (app *BotApp) cancelPlaylistReview(previous pendingAIPlaylistSession) bool {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.purgeAIPlaylistSessionsLocked(time.Now())
	current, ok := app.aiPlaylistSessions[previous.token]
	if !ok || current.processing || current.message != previous.message || current.reviewRevision != previous.reviewRevision {
		return false
	}
	delete(app.aiPlaylistSessions, previous.token)
	delete(app.aiPlaylistSessionByChat, current.chatID)
	return true
}

func (app *BotApp) updatePlaylistReview(previous pendingAIPlaylistSession, review *playlistReviewSession, processing bool) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.purgeAIPlaylistSessionsLocked(time.Now())
	current, ok := app.aiPlaylistSessions[previous.token]
	if !ok || current.processing != previous.processing || current.reviewRevision != previous.reviewRevision ||
		current.review == nil && (current.target.ID != "" || current.creating) ||
		current.message != previous.message {
		return pendingAIPlaylistSession{}, false
	}
	current.review = review
	current.reviewRevision++
	current.processing = processing
	current.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
	app.aiPlaylistSessions[current.token] = cloneAIPlaylistSession(current)
	return current, true
}

func (app *BotApp) showPlaylistReview(c tb.Context, session pendingAIPlaylistSession, review *playlistReviewSession) error {
	updated, ok := app.updatePlaylistReview(session, review, false)
	if !ok {
		return c.Respond(&tb.CallbackResponse{Text: "卡片已更新，请使用当前按钮。"})
	}
	_ = c.Respond()
	return app.renderPlaylistReview(c, updated)
}

func (app *BotApp) renderPlaylistReview(c tb.Context, session pendingAIPlaylistSession) error {
	err := c.Edit(formatAIPlaylistSession(session), aiPlaylistSessionMarkup(session))
	if err != nil && !telegramMessageUnchanged(err) {
		return c.Send("卡片暂时无法更新，重新发送 /ai_playlist 可继续。")
	}
	return nil
}

func (app *BotApp) handlePlaylistReviewText(c tb.Context, session pendingAIPlaylistSession, request string) error {
	review := session.review
	if review.stage != playlistReviewTheme || !review.editing {
		return c.Send("请用歌单检查卡片上的按钮继续。")
	}
	if len([]rune(request)) > 200 {
		return c.Send("主题最多 200 字，请精简后再发送。")
	}
	review.theme = request
	review.editing = false
	review.notice = ""
	updated, ok := app.updatePlaylistReview(session, review, false)
	if !ok {
		return c.Send("卡片已更新，请使用当前歌单检查卡片。")
	}
	if !app.editAIPlaylistSessionMessage(updated, formatAIPlaylistSession(updated), aiPlaylistSessionMarkup(updated)) {
		return c.Send("卡片暂时无法更新，重新发送 /ai_playlist 可继续。")
	}
	return nil
}

func (app *BotApp) runPlaylistReviewCallback(c tb.Context, session pendingAIPlaylistSession, review *playlistReviewSession, progress string, work func(context.Context, *playlistReviewSession)) error {
	claimed, ok := app.updatePlaylistReview(session, review, true)
	if !ok {
		return c.Respond(&tb.CallbackResponse{Text: "卡片已更新，请使用当前按钮。"})
	}
	if !app.beginAIPlaylistRun(claimed.chatID) {
		restored, ok := app.updatePlaylistReview(claimed, session.review, false)
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "另一项歌单维护正在运行。"})
		}
		_ = app.renderPlaylistReview(c, restored)
		return c.Respond(&tb.CallbackResponse{Text: "另一项歌单维护正在运行，请稍后再试。"})
	}
	_ = c.Respond()
	if err := c.Edit(progress, &tb.ReplyMarkup{}); err != nil && !telegramMessageUnchanged(err) {
		app.finishAIPlaylistRun(claimed.chatID)
		_, _ = app.updatePlaylistReview(claimed, session.review, false)
		return c.Send("卡片暂时无法更新，重新发送 /ai_playlist 可继续。")
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), aiPlaylistRunTimeout)
		work(ctx, review)
		cancel()
		app.finishAIPlaylistRun(claimed.chatID)
		updated, ok := app.updatePlaylistReview(claimed, review, false)
		if ok && !app.editAIPlaylistSessionMessage(updated, formatAIPlaylistSession(updated), aiPlaylistSessionMarkup(updated)) {
			note := "卡片暂时无法更新，重新发送 /ai_playlist 可继续。"
			if updated.review.stage == playlistReviewDone {
				note = updated.review.notice + "\n" + note
			}
			app.notifyAIPlaylistEditFailed(updated, note)
		}
	}()
	return nil
}

func (app *BotApp) loadPlaylistReviewList(ctx context.Context, review *playlistReviewSession) {
	if app.playlistCatalog == nil {
		review.notice = "歌单库暂不可用，请稍后重试。"
		return
	}
	synced, err := app.playlistCatalog.Synced(ctx)
	if err != nil {
		review.notice = "读取歌单同步状态失败，请稍后重试。"
		return
	}
	if !synced {
		review.notice = "请先在 /playlist_desc 同步歌单。"
		return
	}
	review.playlists, err = app.playlistCatalog.Active(ctx)
	if err != nil {
		review.notice = "读取普通歌单失败，请稍后重试。"
	} else if len(review.playlists) == 0 {
		review.notice = "当前没有可检查的普通歌单。"
	}
}

func (app *BotApp) currentPlaylistReviewTarget(ctx context.Context, id string) (playlistcatalog.Playlist, bool, error) {
	rows, err := app.playlistCatalog.Active(ctx)
	if err != nil {
		return playlistcatalog.Playlist{}, false, err
	}
	for _, row := range rows {
		if row.ID == id {
			return row, true, nil
		}
	}
	return playlistcatalog.Playlist{}, false, nil
}

func (app *BotApp) loadPlaylistReviewTarget(ctx context.Context, review *playlistReviewSession) {
	target, exists, err := app.currentPlaylistReviewTarget(ctx, review.target.ID)
	if err != nil || !exists {
		review.stage = playlistReviewDone
		review.notice = "歌单已不可用，请同步歌单后重试。"
		return
	}
	review.target = target
	service, err := app.openAIPlaylistService()
	if err != nil {
		review.stage = playlistReviewDone
		review.notice = "AI 歌单服务暂不可用，请稍后重试。"
		return
	}
	defer service.close()
	review.snapshot, err = service.organizer.readPlaylistReviewSnapshot(ctx, target)
	if err != nil {
		review.stage = playlistReviewDone
		review.notice = "读取歌单失败，请确认 Navidrome 中的歌单后重试。"
		return
	}
	if theme := strings.TrimSpace(target.Desc); theme != "" {
		review.theme = theme
	} else if review.theme == "" {
		review.theme, err = service.inferPlaylistReviewTheme(ctx, target.Name, review.snapshot)
		if err != nil {
			review.notice = "暂时无法推断主题。"
		}
		review.editing = review.theme == ""
	}
}

func (app *BotApp) generatePlaylistReview(ctx context.Context, review *playlistReviewSession) {
	service, err := app.openAIPlaylistService()
	if err != nil {
		review.notice = "AI 歌单服务暂不可用，请稍后重试。"
		return
	}
	defer service.close()
	type recommendations struct {
		songs []playlistReviewSuggestion
		err   error
	}
	results := make(chan recommendations, 1)
	go func() {
		songs, err := service.recommendPlaylistReview(ctx, review.theme, review.snapshot)
		results <- recommendations{songs, err}
	}()
	report := service.checkPlaylistReview(ctx, review.theme, review.snapshot)
	add := <-results
	report.add, report.addFailed = add.songs, add.err != nil
	review.stage = playlistReviewResults
	review.report = report
	review.selected = make(map[string]bool)
	review.addTab = len(report.remove) == 0
	review.page = 0
	review.notice = ""
}

func (app *BotApp) commitPlaylistReview(ctx context.Context, review *playlistReviewSession) {
	target, exists, err := app.currentPlaylistReviewTarget(ctx, review.target.ID)
	if err != nil {
		review.notice = "读取歌单描述失败，请稍后再确认。"
		return
	}
	if !exists || target.Name != review.target.Name || target.Desc != review.target.Desc {
		review.stage = playlistReviewDone
		review.notice = "歌单或描述在预览后发生变化，请重新预览。"
		return
	}
	organizer, err := newPlaylistOrganizerService(app.cfg.PlaylistOrganizer)
	if err != nil {
		review.notice = "Navidrome 暂不可用，请稍后再确认。"
		return
	}
	remove, add := review.selection()
	change := buildPlaylistReviewChange(review.snapshot, remove, add)
	status, err := organizer.applyPlaylistReview(ctx, target, change)
	if err != nil && logger != nil {
		logger.Warn("Apply playlist review", zap.String("playlist_id", target.ID), zap.Int("status", int(status)))
	}
	switch status {
	case playlistReviewApplied:
		review.stage = playlistReviewDone
		review.notice = fmt.Sprintf("已加入 %d 首，移出 %d 个曲目位置。", len(change.add), len(change.removePositions))
	case playlistReviewChanged:
		review.stage = playlistReviewDone
		review.notice = err.Error() + "。"
	case playlistReviewRetry:
		review.notice = "暂时无法核对当前歌单或待加入曲目，请稍后再确认。"
	case playlistReviewUnverified:
		review.stage = playlistReviewDone
		review.notice = "暂时无法确认写入结果。请重新预览当前歌单后再决定是否调整。"
	}
}

func (review *playlistReviewSession) options() []playlistReviewSuggestion {
	if review.addTab {
		return review.report.add
	}
	return review.report.remove
}

func (review *playlistReviewSession) selection() (remove, add []playlistReviewSuggestion) {
	return slices.DeleteFunc(slices.Clone(review.report.remove), func(song playlistReviewSuggestion) bool { return !review.selected[song.ID] }),
		slices.DeleteFunc(slices.Clone(review.report.add), func(song playlistReviewSuggestion) bool { return !review.selected[song.ID] })
}

func (review *playlistReviewSession) pages() int {
	switch review.stage {
	case playlistReviewList:
		return (len(review.playlists) + playlistReviewListPageSize - 1) / playlistReviewListPageSize
	case playlistReviewResults:
		return (len(review.options()) + playlistReviewPageSize - 1) / playlistReviewPageSize
	case playlistReviewConfirm:
		return (len(review.selected) + playlistReviewPageSize - 1) / playlistReviewPageSize
	}
	return 0
}

func formatPlaylistReview(session pendingAIPlaylistSession) (string, *tb.ReplyMarkup) {
	review := session.review
	markup := &tb.ReplyMarkup{}
	button := func(label, action string, args ...string) tb.Btn {
		data := append([]string{action, session.token, strconv.FormatUint(session.reviewRevision, 36)}, args...)
		return markup.Data(label, aiPlaylistCallbackUnique, data...)
	}
	var text strings.Builder
	var rows []tb.Row
	text.WriteString("检查普通歌单")
	if review.target.ID != "" {
		fmt.Fprintf(&text, " · %s", aiPlaylistDisplayValue(review.target.Name, 65))
	}
	if review.notice != "" {
		fmt.Fprintf(&text, "\n\n%s", review.notice)
	}
	if review.snapshot.managed && review.stage != playlistReviewDone {
		text.WriteString("\n\n这张歌单由 Navidrome 管理，仅供查看建议。")
	} else if review.snapshot.incomplete && review.stage != playlistReviewDone {
		text.WriteString("\n\nNavidrome 返回的曲目不完整，仅供查看建议。")
	}
	switch review.stage {
	case playlistReviewList:
		start := review.page * playlistReviewListPageSize
		for i := start; i < min(start+playlistReviewListPageSize, len(review.playlists)); i++ {
			rows = append(rows, markup.Row(button(playlistDescButtonLabel(review.playlists[i]), "r_pick", strconv.Itoa(i))))
		}
		if len(review.playlists) == 0 {
			rows = append(rows, markup.Row(button("刷新列表", "r_ls")))
		}
	case playlistReviewTheme:
		if review.theme != "" {
			label := "建议主题"
			if review.target.Desc != "" {
				label = "歌单描述"
			}
			fmt.Fprintf(&text, "\n\n%s\n%s", label, aiPlaylistDisplayValue(review.theme, 200))
		}
		if review.editing {
			text.WriteString("\n\n发送一句本次检查用的主题。")
		} else {
			label := "开始检查"
			if review.target.Desc == "" {
				label = "按这个主题检查"
			}
			row := markup.Row(button(label, "r_scan"))
			if review.target.Desc == "" {
				row = append(row, button("改主题", "r_theme"))
			}
			rows = append(rows, row)
		}
	case playlistReviewResults:
		report := review.report
		fmt.Fprintf(&text, "\n\n已检查 %d / %d 首", report.checked, len(review.snapshot.Tracks))
		if report.insufficient > 0 {
			fmt.Fprintf(&text, " · 资料不足 %d 首", report.insufficient)
		}
		if report.checkFailed {
			fmt.Fprintf(&text, "\n有 %d 首暂未完成检查。", report.unreviewed)
		}
		if report.addFailed {
			text.WriteString("\n补歌检索暂不可用。")
		}
		label := "疑似放错"
		if review.addTab {
			label = "推荐补充"
		}
		options := review.options()
		fmt.Fprintf(&text, "\n\n%s · %d 首", label, len(options))
		if len(options) == 0 && !(review.addTab && report.addFailed) {
			text.WriteString("\n这次没有建议。")
		}
		rows = append(rows, markup.Row(
			button(fmt.Sprintf("疑似放错 %d", len(report.remove)), "r_remove"),
			button(fmt.Sprintf("推荐补充 %d", len(report.add)), "r_add"),
		))
		start := review.page * playlistReviewPageSize
		for i := start; i < min(start+playlistReviewPageSize, len(options)); i++ {
			song := options[i]
			title := playlistReviewTrackLabel(song)
			fmt.Fprintf(&text, "\n\n%d. %s\n%s", i+1, title, song.reason)
			if !review.snapshot.readOnly() {
				mark := "☐"
				if review.selected[song.ID] {
					mark = "☑"
				}
				rows = append(rows, markup.Row(button(fmt.Sprintf("%s %d. %s", mark, i+1, aiPlaylistDisplayValue(title, 44)), "r_toggle", strconv.Itoa(i))))
			}
		}
	case playlistReviewConfirm:
		remove, add := review.selection()
		change := buildPlaylistReviewChange(review.snapshot, remove, add)
		fmt.Fprintf(&text, "\n\n加入 %d 首 · 移出 %d 个曲目位置", len(change.add), len(change.removePositions))
		if len(remove) > 0 {
			text.WriteString("\n移出仅影响当前歌单。同一首出现多次时，会移出它在这张歌单里的所有位置。")
		}
		selected := append(slices.Clone(remove), add...)
		start := review.page * playlistReviewPageSize
		for i := start; i < min(start+playlistReviewPageSize, len(selected)); i++ {
			verb := "加入"
			if i < len(remove) {
				verb = "移出"
			}
			fmt.Fprintf(&text, "\n%s · %s", verb, playlistReviewTrackLabel(selected[i]))
		}
	}
	if pages := review.pages(); pages > 1 {
		fmt.Fprintf(&text, "\n\n第 %d / %d 页", review.page+1, pages)
		var row tb.Row
		if review.page > 0 {
			row = append(row, button("← 上一页", "r_page", strconv.Itoa(review.page-1)))
		}
		if review.page+1 < pages {
			row = append(row, button("下一页 →", "r_page", strconv.Itoa(review.page+1)))
		}
		rows = append(rows, row)
	}
	switch review.stage {
	case playlistReviewList:
		rows = append(rows, markup.Row(button("返回 AI 歌单", "r_home"), button("取消", "r_cancel")))
	case playlistReviewConfirm:
		rows = append(rows, markup.Row(button("确认应用", "r_apply"), button("返回勾选", "r_back")))
		rows = append(rows, markup.Row(button("取消", "r_cancel")))
	default:
		if review.stage == playlistReviewResults && len(review.selected) > 0 {
			rows = append(rows, markup.Row(button(fmt.Sprintf("应用更改 · 已选 %d 首", len(review.selected)), "r_check")))
		}
		row := markup.Row(button("返回普通歌单", "r_ls"))
		if review.stage == playlistReviewResults || review.stage == playlistReviewDone {
			row = append(row, button("重新预览", "r_redo"))
		}
		rows = append(rows, row)
	}
	markup.Inline(rows...)
	return text.String(), markup
}

func playlistReviewTrackLabel(song playlistReviewSuggestion) string {
	title := displaySongValue(aiPlaylistDisplayValue(song.Title, 45))
	if artist := aiPlaylistDisplayValue(song.Artist, 30); artist != "" {
		title += " · " + artist
	}
	return title
}
