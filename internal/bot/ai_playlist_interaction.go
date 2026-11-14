package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v4"
)

const (
	aiPlaylistSessionTTL      = 20 * time.Minute
	aiPlaylistPreviewLimit    = 10
	aiPlaylistRequestMaxRunes = 500
	aiPlaylistCallbackUnique  = "ai_playlist"
)

var aiPlaylistCallbackEndpoint = tb.InlineButton{Unique: aiPlaylistCallbackUnique}

type pendingAIPlaylistSession struct {
	token           string
	userID          int64
	chatID          int64
	message         *tb.Message
	totalPlaylists  int
	playlists       []aiPlaylist
	resumePlaylists []aiPlaylist
	resumePlans     map[string]aiPlaylistResumePlan
	target          aiPlaylist
	request         string
	excludedIDs     []string
	draft           *aiPlaylistDraft
	awaitingInput   bool
	processing      bool
	pendingWrite    bool
	notice          string
	expiresAt       time.Time
}

type aiPlaylistPreset struct {
	ID    string
	Label string
	Text  string
}

var aiPlaylistPresets = []aiPlaylistPreset{
	{ID: "commute", Label: "通勤节奏", Text: "适合通勤聆听，节奏明快但不吵，旋律容易进入状态。"},
	{ID: "focus", Label: "专注工作", Text: "适合专注工作，编曲克制、节奏稳定，避免过强的人声和情绪起伏。"},
	{ID: "night", Label: "夜间放松", Text: "适合夜晚放松，温柔舒缓、氛围安静，适合慢慢听。"},
	{ID: "workout", Label: "运动提神", Text: "适合运动，节奏有推动力、情绪积极，听起来有活力。"},
	{ID: "rain", Label: "雨天氛围", Text: "适合雨天独处，氛围感强、情绪细腻，整体不宜太激烈。"},
	{ID: "instrumental", Label: "器乐纯享", Text: "以纯音乐和器乐为主，适合作为背景聆听，避免明显人声。"},
}

func (app *BotApp) startAIPlaylist(c tb.Context) error {
	if c.Chat() == nil || c.Sender() == nil {
		return c.Send("无法读取 Telegram 会话信息，请在聊天中重新发送 /ai_playlist。")
	}
	if app.hasPlaylistAssignmentForChat(c.Chat().ID) || app.hasPendingEmptyTrash(c.Chat().ID) {
		return c.Send("当前聊天已有一条歌单维护会话，请先完成或结束它。")
	}
	if app.hasAIPlaylistSessionForChat(c.Chat().ID) {
		return c.Send("当前聊天已有一条 AI 歌单会话，请继续原消息中的操作或取消它。")
	}
	service, err := newAIPlaylistService(app.cfg)
	if err != nil {
		return c.Send("AI 智能歌单服务尚未配置：" + err.Error())
	}
	message, err := c.Bot().Send(c.Chat(), "🤖 正在读取 [AI] 歌单…")
	if err != nil {
		_ = service.close()
		return err
	}
	session := pendingAIPlaylistSession{
		token:       strings.ReplaceAll(uuid.NewString(), "-", ""),
		userID:      c.Sender().ID,
		chatID:      c.Chat().ID,
		message:     message,
		resumePlans: make(map[string]aiPlaylistResumePlan),
		processing:  true,
		expiresAt:   time.Now().Add(aiPlaylistSessionTTL),
	}
	app.playlistMaintenanceStartMu.Lock()
	if app.hasPlaylistAssignmentForChat(session.chatID) || app.hasPendingEmptyTrash(session.chatID) || app.hasAIPlaylistSessionForChat(session.chatID) || !app.registerAIPlaylistSession(session) {
		app.playlistMaintenanceStartMu.Unlock()
		_ = service.close()
		_, _ = c.Bot().Edit(message, "当前聊天已有一条歌单维护会话，请先完成或结束它。", &tb.ReplyMarkup{})
		return nil
	}
	app.playlistMaintenanceStartMu.Unlock()
	go app.prepareAIPlaylistMenu(session.token, service)
	return nil
}

func (app *BotApp) prepareAIPlaylistMenu(token string, service *aiPlaylistService) {
	defer func() {
		if err := service.close(); err != nil {
			logger.Warn("AI playlist resume store close failed", zap.Error(err))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), playlistAssignmentTimeout)
	defer cancel()
	session, ok := app.getAIPlaylistSession(token)
	if !ok {
		return
	}
	playlists, err := service.organizer.aiPlaylists(ctx)
	if err != nil {
		app.failAIPlaylistMenu(token, "❌ 读取 [AI] 歌单失败，请检查 Navidrome 连接。", err)
		return
	}
	session.totalPlaylists = len(playlists)
	for _, playlist := range playlists {
		resume, err := service.resumeStore.Load(ctx, playlist.ID)
		if err != nil {
			app.failAIPlaylistMenu(token, "❌ 读取歌单续写状态失败，请稍后重试。", err)
			return
		}
		if resume != nil {
			session.resumePlaylists = append(session.resumePlaylists, playlist)
			session.resumePlans[playlist.ID] = *resume
			continue
		}
		if playlist.SongCount == 0 {
			session.playlists = append(session.playlists, playlist)
		}
	}
	session.processing = false
	session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
	app.updateAIPlaylistSession(session)
	app.editAIPlaylistSessionMessage(session, formatAIPlaylistSession(session), aiPlaylistSessionMarkup(session))
}

func (app *BotApp) failAIPlaylistMenu(token, note string, cause error) {
	if cause != nil {
		logger.Warn("[Telegram] 准备 AI 歌单会话失败", zap.Error(cause))
	}
	session, ok := app.takeAIPlaylistSession(token)
	if !ok {
		return
	}
	app.editAIPlaylistSessionMessage(session, note, &tb.ReplyMarkup{})
}

func (app *BotApp) HandleAIPlaylistCallback(c tb.Context) error {
	args := c.Args()
	if len(args) < 2 {
		return c.Respond(&tb.CallbackResponse{Text: "AI 歌单操作无效或已过期", ShowAlert: true})
	}
	action, token := args[0], args[1]
	session, ok := app.getAIPlaylistSession(token)
	if !ok {
		return c.Respond(&tb.CallbackResponse{Text: "AI 歌单会话已过期，请重新发送 /ai_playlist", ShowAlert: true})
	}
	if c.Sender() == nil || c.Sender().ID != session.userID {
		return c.Respond(&tb.CallbackResponse{Text: "只有发起会话的人可以操作", ShowAlert: true})
	}
	if !aiPlaylistCallbackMatches(c.Callback(), session) {
		return c.Respond(&tb.CallbackResponse{Text: "按钮与 AI 歌单消息不匹配，请使用原消息中的按钮", ShowAlert: true})
	}
	if session.processing {
		return c.Respond(&tb.CallbackResponse{Text: "正在处理，请稍候。"})
	}

	switch action {
	case "select":
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "歌单选择无效", ShowAlert: true})
		}
		index, err := strconv.Atoi(args[2])
		if err != nil || index < 0 || index >= len(session.playlists) {
			return c.Respond(&tb.CallbackResponse{Text: "歌单选择无效", ShowAlert: true})
		}
		updated, ok := app.selectAIPlaylistTarget(token, session.playlists[index])
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "AI 歌单会话已过期", ShowAlert: true})
		}
		_ = c.Respond()
		return c.Edit(formatAIPlaylistSession(updated), aiPlaylistSessionMarkup(updated))

	case "base":
		if len(args) != 2 || session.target.ID == "" || strings.TrimSpace(session.target.Description) == "" {
			return c.Respond(&tb.CallbackResponse{Text: "请先选一个有备注的空歌单，或直接发送生成要求。", ShowAlert: true})
		}
		return app.beginAIPlaylistGeneration(c, session, session.request, nil)

	case "preset":
		if len(args) != 3 || session.target.ID == "" {
			return c.Respond(&tb.CallbackResponse{Text: "请先选择 AI 歌单", ShowAlert: true})
		}
		preset, ok := findAIPlaylistPreset(args[2])
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "快捷主题无效", ShowAlert: true})
		}
		request := mergeAIPlaylistRequest(session.request, preset.Text)
		return app.beginAIPlaylistGeneration(c, session, request, nil)

	case "refine":
		if session.draft == nil {
			return c.Respond(&tb.CallbackResponse{Text: "请先生成预览，再补充调整要求", ShowAlert: true})
		}
		updated, ok := app.setAIPlaylistAwaitingInput(token, true, "请直接发送补充要求，系统会按新要求重新生成预览。")
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "AI 歌单会话已过期", ShowAlert: true})
		}
		_ = c.Respond()
		return c.Edit(formatAIPlaylistSession(updated), aiPlaylistSessionMarkup(updated))

	case "change":
		updated, ok := app.resetAIPlaylistTarget(token)
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "当前不能更换目标歌单", ShowAlert: true})
		}
		_ = c.Respond()
		return c.Edit(formatAIPlaylistSession(updated), aiPlaylistSessionMarkup(updated))

	case "reroll":
		if session.draft == nil {
			return c.Respond(&tb.CallbackResponse{Text: "当前没有可更换的预览", ShowAlert: true})
		}
		excluded := append([]string(nil), session.excludedIDs...)
		for _, item := range session.draft.Plan.Playlist {
			excluded = append(excluded, item.ID)
		}
		return app.beginAIPlaylistGeneration(c, session, session.request, excluded)

	case "confirm":
		if session.target.ID == "" || (session.draft == nil && !session.pendingWrite) {
			return c.Respond(&tb.CallbackResponse{Text: "请先生成歌单预览", ShowAlert: true})
		}
		return app.beginAIPlaylistCommit(c, session)

	case "resume":
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "续写操作无效", ShowAlert: true})
		}
		index, err := strconv.Atoi(args[2])
		if err != nil || index < 0 || index >= len(session.resumePlaylists) {
			return c.Respond(&tb.CallbackResponse{Text: "续写计划无效", ShowAlert: true})
		}
		updated, ok := app.selectAIPlaylistResume(token, session.resumePlaylists[index].ID)
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "续写计划已过期，请重新发送 /ai_playlist", ShowAlert: true})
		}
		return app.beginAIPlaylistCommit(c, updated)

	case "cancel":
		if !app.cancelAIPlaylistSession(token) {
			return c.Respond(&tb.CallbackResponse{Text: "AI 歌单会话已过期或正在处理", ShowAlert: true})
		}
		_ = c.Respond()
		if session.pendingWrite || len(session.resumePlaylists) > 0 {
			return c.Edit("已结束本次 AI 歌单操作。未完成的续写计划已保留，可重新发送 /ai_playlist 继续。", &tb.ReplyMarkup{})
		}
		return c.Edit("已取消本次 AI 歌单生成。", &tb.ReplyMarkup{})

	default:
		return c.Respond(&tb.CallbackResponse{Text: "未知 AI 歌单操作", ShowAlert: true})
	}
}

func (app *BotApp) beginAIPlaylistGeneration(c tb.Context, session pendingAIPlaylistSession, request string, excluded []string) error {
	if session.pendingWrite {
		return c.Respond(&tb.CallbackResponse{Text: "请先完成或取消上次的续写计划", ShowAlert: true})
	}
	if len([]rune(request)) > aiPlaylistRequestMaxRunes {
		return c.Respond(&tb.CallbackResponse{Text: "生成要求最多 500 个字符", ShowAlert: true})
	}
	claimed, ok := app.claimAIPlaylistSession(session.token, request, excluded)
	if !ok {
		return c.Respond(&tb.CallbackResponse{Text: "AI 歌单会话已过期或正在处理", ShowAlert: true})
	}
	if !app.beginAIPlaylistRun(claimed.chatID) {
		app.setAIPlaylistSessionProcessing(claimed.token, false)
		return c.Respond(&tb.CallbackResponse{Text: "另一项歌单维护正在运行，请稍后重试。"})
	}
	_ = c.Respond(&tb.CallbackResponse{Text: "正在生成预览…"})
	if err := c.Edit("🤖 正在检索曲库并编排候选…", &tb.ReplyMarkup{}); err != nil {
		app.finishAIPlaylistRun(claimed.chatID)
		app.setAIPlaylistSessionProcessing(claimed.token, false)
		return err
	}
	go app.generateAIPlaylistPreview(claimed.token)
	return nil
}

func (app *BotApp) generateAIPlaylistPreview(token string) {
	session, ok := app.getAIPlaylistSession(token)
	if !ok {
		return
	}
	defer app.finishAIPlaylistRun(session.chatID)
	service, err := newAIPlaylistService(app.cfg)
	if err != nil {
		app.failAIPlaylistPreview(token, err)
		return
	}
	defer func() {
		if err := service.close(); err != nil {
			logger.Warn("AI playlist resume store close failed", zap.Error(err))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), aiPlaylistRunTimeout)
	defer cancel()
	draft, err := service.previewAIPlaylist(ctx, session.target, session.request, session.excludedIDs)
	if err != nil {
		app.failAIPlaylistPreview(token, err)
		return
	}
	session.draft = &draft
	session.awaitingInput = false
	session.processing = false
	session.notice = ""
	session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
	app.updateAIPlaylistSession(session)
	app.editAIPlaylistSessionMessage(session, formatAIPlaylistSession(session), aiPlaylistSessionMarkup(session))
}

func (app *BotApp) failAIPlaylistPreview(token string, cause error) {
	logger.Warn("[Telegram] AI 歌单预览生成失败", zap.Error(cause))
	session, ok := app.setAIPlaylistSessionProcessing(token, false)
	if !ok {
		return
	}
	session.notice = "❌ " + aiPlaylistDisplayValue(cause.Error(), 500)
	if session.draft == nil {
		session.awaitingInput = true
	}
	app.updateAIPlaylistSession(session)
	app.editAIPlaylistSessionMessage(session, formatAIPlaylistSession(session), aiPlaylistSessionMarkup(session))
}

func (app *BotApp) beginAIPlaylistCommit(c tb.Context, session pendingAIPlaylistSession) error {
	claimed, ok := app.claimAIPlaylistSession(session.token, session.request, session.excludedIDs)
	if !ok {
		return c.Respond(&tb.CallbackResponse{Text: "AI 歌单会话已过期或正在处理", ShowAlert: true})
	}
	if !app.beginAIPlaylistRun(claimed.chatID) {
		app.setAIPlaylistSessionProcessing(claimed.token, false)
		return c.Respond(&tb.CallbackResponse{Text: "另一项歌单维护正在运行，请稍后重试。"})
	}
	_ = c.Respond(&tb.CallbackResponse{Text: "已确认，正在复核并生成歌单…"})
	if err := c.Edit("⏳ 已确认，正在复核目标歌单并写入曲目…", &tb.ReplyMarkup{}); err != nil {
		app.finishAIPlaylistRun(claimed.chatID)
		app.setAIPlaylistSessionProcessing(claimed.token, false)
		return err
	}
	go app.commitAIPlaylistDraft(claimed.token)
	return nil
}

func (app *BotApp) commitAIPlaylistDraft(token string) {
	session, ok := app.getAIPlaylistSession(token)
	if !ok {
		return
	}
	defer app.finishAIPlaylistRun(session.chatID)
	service, err := newAIPlaylistService(app.cfg)
	if err != nil {
		app.failAIPlaylistCommit(token, err)
		return
	}
	defer func() {
		if err := service.close(); err != nil {
			logger.Warn("AI playlist resume store close failed", zap.Error(err))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), aiPlaylistRunTimeout)
	defer cancel()
	var result aiPlaylistResult
	if session.pendingWrite {
		live, err := service.organizer.aiPlaylists(ctx)
		if err != nil {
			app.failAIPlaylistCommitWithStore(token, fmt.Errorf("复核续写歌单失败: %w", err), service.resumeStore)
			return
		}
		current, exists := findAIPlaylist(live, session.target.ID)
		if !exists {
			app.failAIPlaylistCommitWithStore(token, errors.New("续写歌单已不存在或名称不再以 [AI] 开头，请恢复歌单后重试"), service.resumeStore)
			return
		}
		resume, err := service.resumeStore.Load(ctx, session.target.ID)
		if err != nil {
			app.failAIPlaylistCommitWithStore(token, err, service.resumeStore)
			return
		}
		if resume == nil {
			app.failAIPlaylistCommitWithStore(token, errors.New("没有找到可续写的计划，请重新生成预览"), service.resumeStore)
			return
		}
		if strings.TrimSpace(resume.Description) != strings.TrimSpace(current.Description) {
			app.failAIPlaylistCommitWithStore(token, errors.New("歌单备注已变化，未执行旧计划；请恢复原备注后重试，或联系管理员清理续写计划"), service.resumeStore)
			return
		}
		result, err = service.processAIPlaylist(ctx, current, resume, false)
		if err != nil {
			app.failAIPlaylistCommitWithStore(token, err, service.resumeStore)
			return
		}
	} else {
		live, err := service.organizer.aiPlaylists(ctx)
		if err != nil {
			app.failAIPlaylistCommitWithStore(token, fmt.Errorf("复核 Navidrome 歌单失败: %w", err), service.resumeStore)
			return
		}
		current, exists := findAIPlaylist(live, session.target.ID)
		if !exists {
			app.failAIPlaylistCommitWithStore(token, errors.New("目标歌单已不存在，请重新发送 /ai_playlist"), service.resumeStore)
			return
		}
		if current.SongCount != 0 {
			app.failAIPlaylistCommitWithStore(token, errors.New("目标歌单在预览后已有歌曲，本次未写入；请选择空歌单重新生成"), service.resumeStore)
			return
		}
		if current.Description != session.target.Description {
			app.failAIPlaylistCommitWithStore(token, errors.New("目标歌单备注在预览后发生变化，请重新生成预览"), service.resumeStore)
			return
		}
		if session.draft == nil || len(session.draft.Plan.Playlist) == 0 {
			app.failAIPlaylistCommitWithStore(token, errors.New("预览里没有可写入的歌曲，请重新生成"), service.resumeStore)
			return
		}
		trackSnapshot, err := service.readAIPlaylistTracks(ctx, current)
		if err != nil {
			app.failAIPlaylistCommitWithStore(token, fmt.Errorf("复核目标歌单曲目失败: %w", err), service.resumeStore)
			return
		}
		if trackSnapshot.Count != 0 {
			app.failAIPlaylistCommitWithStore(token, errors.New("目标歌单在预览后已有歌曲，本次未写入；请选择空歌单重新生成"), service.resumeStore)
			return
		}
		ids := aiPlaylistDraftIDs(*session.draft)
		missing, err := service.organizer.missingSongIDs(ctx, ids)
		if err != nil {
			app.failAIPlaylistCommitWithStore(token, fmt.Errorf("复核候选歌曲失败: %w", err), service.resumeStore)
			return
		}
		if len(missing) > 0 {
			app.failAIPlaylistCommitWithStore(token, errors.New("部分候选歌曲已不在曲库中，请重新生成预览"), service.resumeStore)
			return
		}
		resume := aiPlaylistResumePlan{
			PlaylistID:   current.ID,
			Description:  current.Description,
			SongIDs:      ids,
			Intro:        session.draft.Plan.Intro,
			UsedFallback: session.draft.UsedFallback,
		}
		if err := service.resumeStore.Create(ctx, resume); err != nil {
			app.failAIPlaylistCommitWithStore(token, fmt.Errorf("保存续写计划失败，未修改歌单: %w", err), service.resumeStore)
			return
		}
		app.markAIPlaylistPendingWrite(token)
		result, err = service.processAIPlaylist(ctx, current, &resume, true)
		if err != nil {
			app.failAIPlaylistCommitWithStore(token, err, service.resumeStore)
			return
		}
	}
	finished, ok := app.takeAIPlaylistSession(token)
	if !ok {
		return
	}
	text := fmt.Sprintf("✅ AI 智能歌单已完成\n──────────────────\n🎵 歌单：%s\n📁 已生成：%d 首", aiPlaylistDisplayValue(result.Name, 80), result.Count)
	if result.Intro != "" {
		text += "\n\n" + aiPlaylistDisplayValue(result.Intro, 300)
	}
	if result.UsedFallback {
		text += "\n\n⚠️ 本次使用候选排序兜底完成编排。"
	}
	app.editAIPlaylistSessionMessage(finished, text, &tb.ReplyMarkup{})
}

func (app *BotApp) failAIPlaylistCommit(token string, cause error) {
	logger.Warn("[Telegram] AI 歌单写入失败", zap.Error(cause))
	session, ok := app.setAIPlaylistSessionProcessing(token, false)
	if !ok {
		return
	}
	session.notice = "❌ " + aiPlaylistDisplayValue(cause.Error(), 500)
	app.updateAIPlaylistSession(session)
	app.editAIPlaylistSessionMessage(session, formatAIPlaylistSession(session), aiPlaylistSessionMarkup(session))
}

func (app *BotApp) failAIPlaylistCommitWithStore(token string, cause error, store aiPlaylistResumeStore) {
	session, ok := app.setAIPlaylistSessionProcessing(token, false)
	if !ok {
		return
	}
	checkCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if plan, err := store.Load(checkCtx, session.target.ID); err == nil {
		session.pendingWrite = plan != nil
		session.awaitingInput = plan == nil && session.draft == nil && session.target.ID != ""
	} else {
		logger.Warn("[Telegram] 无法确认 AI 歌单续写状态", zap.Error(err))
	}
	logger.Warn("[Telegram] AI 歌单写入失败", zap.Error(cause))
	session.notice = "❌ " + aiPlaylistDisplayValue(cause.Error(), 500)
	app.updateAIPlaylistSession(session)
	app.editAIPlaylistSessionMessage(session, formatAIPlaylistSession(session), aiPlaylistSessionMarkup(session))
}

func (app *BotApp) handleAIPlaylistText(c tb.Context) (bool, error) {
	if c.Chat() == nil || c.Sender() == nil {
		return false, nil
	}
	request := strings.TrimSpace(c.Text())
	if request == "" || strings.HasPrefix(request, "/") {
		return false, nil
	}
	session, ok := app.getAIPlaylistSessionByChat(c.Chat().ID)
	if !ok || c.Sender().ID != session.userID {
		return false, nil
	}
	if session.processing {
		return true, c.Send("AI 歌单正在处理中，请稍候。")
	}
	if session.pendingWrite {
		return true, c.Send("请先继续或取消上次的写入计划。")
	}
	if !session.awaitingInput {
		return false, nil
	}
	if len([]rune(request)) > aiPlaylistRequestMaxRunes {
		return true, c.Send("生成要求最多 500 个字符，请精简后再发送。")
	}
	if session.request != "" {
		request = mergeAIPlaylistRequest(session.request, request)
	}
	if len([]rune(request)) > aiPlaylistRequestMaxRunes {
		return true, c.Send("本次生成要求合计最多 500 个字符，请精简后再发送。")
	}
	claimed, ok := app.claimAIPlaylistSession(session.token, request, nil)
	if !ok {
		return true, c.Send("AI 歌单会话已过期或正在处理，请重新发送 /ai_playlist。")
	}
	if !app.beginAIPlaylistRun(claimed.chatID) {
		app.setAIPlaylistSessionProcessing(claimed.token, false)
		return true, c.Send("另一项歌单维护正在运行，请稍后再发送要求。")
	}
	if _, err := c.Bot().Edit(claimed.message, "🤖 正在检索曲库并编排候选…", &tb.ReplyMarkup{}); err != nil {
		app.finishAIPlaylistRun(claimed.chatID)
		app.setAIPlaylistSessionProcessing(claimed.token, false)
		return true, err
	}
	go app.generateAIPlaylistPreview(claimed.token)
	return true, nil
}

func (app *BotApp) registerAIPlaylistSession(session pendingAIPlaylistSession) bool {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	app.purgeAIPlaylistSessionsLocked(time.Now())
	if _, exists := app.aiPlaylistSessionByChat[session.chatID]; exists {
		return false
	}
	app.aiPlaylistSessions[session.token] = cloneAIPlaylistSession(session)
	app.aiPlaylistSessionByChat[session.chatID] = session.token
	return true
}

func (app *BotApp) hasAIPlaylistSessionForChat(chatID int64) bool {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	app.purgeAIPlaylistSessionsLocked(time.Now())
	_, exists := app.aiPlaylistSessionByChat[chatID]
	return exists
}

func (app *BotApp) getAIPlaylistSession(token string) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	app.purgeAIPlaylistSessionsLocked(time.Now())
	session, ok := app.aiPlaylistSessions[token]
	return cloneAIPlaylistSession(session), ok
}

func (app *BotApp) getAIPlaylistSessionByChat(chatID int64) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	app.purgeAIPlaylistSessionsLocked(time.Now())
	token, ok := app.aiPlaylistSessionByChat[chatID]
	if !ok {
		return pendingAIPlaylistSession{}, false
	}
	session, ok := app.aiPlaylistSessions[token]
	return cloneAIPlaylistSession(session), ok
}

func (app *BotApp) selectAIPlaylistTarget(token string, playlist aiPlaylist) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	session, ok := app.aiPlaylistSessions[token]
	if !ok || session.processing || session.target.ID != "" {
		return pendingAIPlaylistSession{}, false
	}
	for _, configured := range session.playlists {
		if configured.ID != playlist.ID {
			continue
		}
		session.target = configured
		session.awaitingInput = true
		session.notice = ""
		session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
		app.aiPlaylistSessions[token] = session
		return cloneAIPlaylistSession(session), true
	}
	return pendingAIPlaylistSession{}, false
}

func (app *BotApp) selectAIPlaylistResume(token, playlistID string) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	session, ok := app.aiPlaylistSessions[token]
	if !ok || session.processing || session.pendingWrite {
		return pendingAIPlaylistSession{}, false
	}
	if _, exists := session.resumePlans[playlistID]; !exists {
		return pendingAIPlaylistSession{}, false
	}
	for _, playlist := range session.resumePlaylists {
		if playlist.ID == playlistID {
			session.target = playlist
			session.pendingWrite = true
			session.request = ""
			session.draft = nil
			session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
			app.aiPlaylistSessions[token] = session
			return cloneAIPlaylistSession(session), true
		}
	}
	return pendingAIPlaylistSession{}, false
}

func (app *BotApp) resetAIPlaylistTarget(token string) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	session, ok := app.aiPlaylistSessions[token]
	if !ok || session.processing || session.pendingWrite || session.target.ID == "" {
		return pendingAIPlaylistSession{}, false
	}
	session.target = aiPlaylist{}
	session.draft = nil
	session.excludedIDs = nil
	session.awaitingInput = false
	session.notice = ""
	session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
	app.aiPlaylistSessions[token] = session
	return cloneAIPlaylistSession(session), true
}

func (app *BotApp) claimAIPlaylistSession(token, request string, excluded []string) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	app.purgeAIPlaylistSessionsLocked(time.Now())
	session, ok := app.aiPlaylistSessions[token]
	if !ok || session.processing || session.target.ID == "" {
		return pendingAIPlaylistSession{}, false
	}
	session.processing = true
	session.awaitingInput = false
	session.request = request
	session.excludedIDs = append([]string(nil), excluded...)
	session.notice = ""
	session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
	app.aiPlaylistSessions[token] = session
	return cloneAIPlaylistSession(session), true
}

func (app *BotApp) setAIPlaylistAwaitingInput(token string, awaiting bool, notice string) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	session, ok := app.aiPlaylistSessions[token]
	if !ok || session.processing || session.pendingWrite {
		return pendingAIPlaylistSession{}, false
	}
	session.awaitingInput = awaiting
	session.notice = notice
	session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
	app.aiPlaylistSessions[token] = session
	return cloneAIPlaylistSession(session), true
}

func (app *BotApp) setAIPlaylistSessionProcessing(token string, processing bool) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	session, ok := app.aiPlaylistSessions[token]
	if !ok {
		return pendingAIPlaylistSession{}, false
	}
	session.processing = processing
	if !processing {
		session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
		if session.target.ID != "" && session.draft == nil && !session.pendingWrite {
			session.awaitingInput = true
		}
	}
	app.aiPlaylistSessions[token] = session
	return cloneAIPlaylistSession(session), true
}

func (app *BotApp) markAIPlaylistPendingWrite(token string) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	if session, ok := app.aiPlaylistSessions[token]; ok {
		session.pendingWrite = true
		app.aiPlaylistSessions[token] = session
	}
}

func (app *BotApp) updateAIPlaylistSession(session pendingAIPlaylistSession) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	if _, exists := app.aiPlaylistSessions[session.token]; exists {
		app.aiPlaylistSessions[session.token] = cloneAIPlaylistSession(session)
	}
}

func (app *BotApp) cancelAIPlaylistSession(token string) bool {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	app.purgeAIPlaylistSessionsLocked(time.Now())
	session, ok := app.aiPlaylistSessions[token]
	if !ok || session.processing {
		return false
	}
	delete(app.aiPlaylistSessions, token)
	if app.aiPlaylistSessionByChat[session.chatID] == token {
		delete(app.aiPlaylistSessionByChat, session.chatID)
	}
	return true
}

func (app *BotApp) takeAIPlaylistSession(token string) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	session, ok := app.aiPlaylistSessions[token]
	if !ok {
		return pendingAIPlaylistSession{}, false
	}
	delete(app.aiPlaylistSessions, token)
	if app.aiPlaylistSessionByChat[session.chatID] == token {
		delete(app.aiPlaylistSessionByChat, session.chatID)
	}
	return cloneAIPlaylistSession(session), true
}

func (app *BotApp) ensureAIPlaylistSessionMapsLocked() {
	if app.aiPlaylistSessions == nil {
		app.aiPlaylistSessions = make(map[string]pendingAIPlaylistSession)
	}
	if app.aiPlaylistSessionByChat == nil {
		app.aiPlaylistSessionByChat = make(map[int64]string)
	}
}

func (app *BotApp) purgeAIPlaylistSessionsLocked(now time.Time) {
	for token, session := range app.aiPlaylistSessions {
		if !session.processing && !now.Before(session.expiresAt) {
			delete(app.aiPlaylistSessions, token)
			if app.aiPlaylistSessionByChat[session.chatID] == token {
				delete(app.aiPlaylistSessionByChat, session.chatID)
			}
		}
	}
}

func cloneAIPlaylistSession(session pendingAIPlaylistSession) pendingAIPlaylistSession {
	session.playlists = append([]aiPlaylist(nil), session.playlists...)
	session.resumePlaylists = append([]aiPlaylist(nil), session.resumePlaylists...)
	session.excludedIDs = append([]string(nil), session.excludedIDs...)
	plans := session.resumePlans
	session.resumePlans = make(map[string]aiPlaylistResumePlan, len(plans))
	for id, plan := range plans {
		plan.SongIDs = append([]string(nil), plan.SongIDs...)
		session.resumePlans[id] = plan
	}
	if session.draft != nil {
		draft := *session.draft
		draft.Candidates = append([]aiPlaylistCandidate(nil), draft.Candidates...)
		draft.Plan.Playlist = append([]aiPlaylistPlanItem(nil), draft.Plan.Playlist...)
		session.draft = &draft
	}
	return session
}

func (app *BotApp) editAIPlaylistSessionMessage(session pendingAIPlaylistSession, text string, markup *tb.ReplyMarkup) {
	if session.message == nil || app.bot == nil {
		return
	}
	if _, err := app.bot.Edit(session.message, text, markup); err != nil {
		logger.Warn("[Telegram] 更新 AI 歌单消息失败", zap.Error(err))
	}
}

func aiPlaylistCallbackMatches(callback *tb.Callback, session pendingAIPlaylistSession) bool {
	return callback != nil && callback.Message != nil && session.message != nil &&
		callback.Message.ID == session.message.ID && callback.Message.Chat != nil && callback.Message.Chat.ID == session.chatID
}

func findAIPlaylist(playlists []aiPlaylist, id string) (aiPlaylist, bool) {
	for _, playlist := range playlists {
		if playlist.ID == id {
			return playlist, true
		}
	}
	return aiPlaylist{}, false
}

func findAIPlaylistPreset(id string) (aiPlaylistPreset, bool) {
	for _, preset := range aiPlaylistPresets {
		if preset.ID == id {
			return preset, true
		}
	}
	return aiPlaylistPreset{}, false
}

func mergeAIPlaylistRequest(previous, next string) string {
	previous, next = strings.TrimSpace(previous), strings.TrimSpace(next)
	if previous == "" {
		return next
	}
	if next == "" {
		return previous
	}
	return previous + "\n补充要求：" + next
}

func aiPlaylistDraftIDs(draft aiPlaylistDraft) []string {
	ids := make([]string, 0, len(draft.Plan.Playlist))
	for _, item := range draft.Plan.Playlist {
		if safeNavidromeID(item.ID) {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func formatAIPlaylistSession(session pendingAIPlaylistSession) string {
	var text strings.Builder
	if session.target.ID == "" {
		text.WriteString("🤖 AI 智能歌单\n──────────────────\n")
		if len(session.playlists) > 0 {
			text.WriteString("选择一个空 [AI] 歌单开始生成。确认前不会修改曲目。")
			if len(session.resumePlaylists) > 0 {
				text.WriteString("\n\n也可以继续上次未完成的写入。")
			}
		} else if len(session.resumePlaylists) > 0 {
			text.WriteString("没有空歌单；可以继续上次未完成的写入。")
		} else if session.totalPlaylists == 0 {
			text.WriteString("当前没有 [AI] 歌单。请在 Navidrome 新建名称以 [AI] 开头的歌单，再重新开始。")
		} else {
			text.WriteString("当前 [AI] 歌单都已有歌曲。请清空一个歌单后再重新开始。")
		}
		return text.String()
	}
	if session.pendingWrite {
		text.WriteString("🤖 AI 智能歌单续写\n──────────────────")
		fmt.Fprintf(&text, "\n🎵 歌单：%s", aiPlaylistDisplayValue(session.target.Name, 80))
		text.WriteString("\n\n存在上次已确认但未完成的写入计划。继续后会复核歌单，再安全补齐缺少的歌曲。")
		if session.notice != "" {
			text.WriteString("\n\n")
			text.WriteString(session.notice)
		}
		return text.String()
	}
	if session.draft != nil {
		text.WriteString("🤖 AI 智能歌单预览\n──────────────────")
		fmt.Fprintf(&text, "\n🎵 歌单：%s\n📝 主题：%s\n📁 共 %d 首，预览前 %d 首", aiPlaylistDisplayValue(session.target.Name, 80), aiPlaylistDisplayValue(session.draft.Description, 260), len(session.draft.Plan.Playlist), min(aiPlaylistPreviewLimit, len(session.draft.Plan.Playlist)))
		if session.draft.Plan.Intro != "" {
			text.WriteString("\n\n")
			text.WriteString(aiPlaylistDisplayValue(session.draft.Plan.Intro, 220))
		}
		for index, item := range session.draft.Plan.Playlist {
			if index >= aiPlaylistPreviewLimit {
				break
			}
			candidate := findAIPlaylistCandidate(session.draft.Candidates, item.ID)
			fmt.Fprintf(&text, "\n\n%d. %s - %s\n   %s", index+1, aiPlaylistDisplayValue(candidate.Title, 70), aiPlaylistDisplayValue(candidate.Artist, 70), aiPlaylistDisplayValue(item.Reason, 50))
		}
		if session.draft.UsedFallback {
			text.WriteString("\n\n⚠️ 本次使用候选排序兜底完成编排。")
		}
		if session.notice != "" {
			text.WriteString("\n\n")
			text.WriteString(session.notice)
		}
		text.WriteString("\n\n可补充要求重新预览、确认写入，或更换歌单。")
		return text.String()
	}
	text.WriteString("🤖 AI 智能歌单\n──────────────────")
	fmt.Fprintf(&text, "\n🎵 歌单：%s", aiPlaylistDisplayValue(session.target.Name, 80))
	if description := strings.TrimSpace(session.target.Description); description != "" {
		fmt.Fprintf(&text, "\n📝 备注：%s", aiPlaylistDisplayValue(description, 260))
	}
	text.WriteString("\n\n直接发送本次需求，或选择一个快捷主题。确认前不会修改曲目。")
	if session.notice != "" {
		text.WriteString("\n\n")
		text.WriteString(session.notice)
	}
	return text.String()
}

func aiPlaylistSessionMarkup(session pendingAIPlaylistSession) *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	rows := make([]tb.Row, 0, 8)
	if session.target.ID == "" {
		for index := 0; index < len(session.playlists); index += 2 {
			row := make(tb.Row, 0, 2)
			for current := index; current < len(session.playlists) && current < index+2; current++ {
				playlist := session.playlists[current]
				row = append(row, markup.Data(aiPlaylistButtonLabel(playlist.Name), aiPlaylistCallbackUnique, "select", session.token, strconv.Itoa(current)))
			}
			rows = append(rows, row)
		}
		for index, playlist := range session.resumePlaylists {
			rows = append(rows, markup.Row(markup.Data("↻ 继续「"+aiPlaylistButtonLabel(playlist.Name)+"」", aiPlaylistCallbackUnique, "resume", session.token, strconv.Itoa(index))))
		}
		rows = append(rows, markup.Row(markup.Data("取消", aiPlaylistCallbackUnique, "cancel", session.token)))
		markup.Inline(rows...)
		return markup
	}
	if session.pendingWrite {
		rows = append(rows, markup.Row(
			markup.Data("↻ 继续写入", aiPlaylistCallbackUnique, "confirm", session.token),
			markup.Data("取消", aiPlaylistCallbackUnique, "cancel", session.token),
		))
		markup.Inline(rows...)
		return markup
	}
	if session.draft != nil {
		rows = append(rows, markup.Row(
			markup.Data("✏️ 调整要求", aiPlaylistCallbackUnique, "refine", session.token),
			markup.Data("🔄 换一版", aiPlaylistCallbackUnique, "reroll", session.token),
		))
		rows = append(rows, markup.Row(
			markup.Data("✅ 确认写入", aiPlaylistCallbackUnique, "confirm", session.token),
			markup.Data("取消", aiPlaylistCallbackUnique, "cancel", session.token),
		))
		rows = append(rows, markup.Row(markup.Data("更换歌单", aiPlaylistCallbackUnique, "change", session.token)))
		markup.Inline(rows...)
		return markup
	}
	if strings.TrimSpace(session.target.Description) != "" {
		rows = append(rows, markup.Row(markup.Data("按备注生成预览", aiPlaylistCallbackUnique, "base", session.token)))
	}
	for index := 0; index < len(aiPlaylistPresets); index += 2 {
		row := make(tb.Row, 0, 2)
		for current := index; current < len(aiPlaylistPresets) && current < index+2; current++ {
			preset := aiPlaylistPresets[current]
			row = append(row, markup.Data(preset.Label, aiPlaylistCallbackUnique, "preset", session.token, preset.ID))
		}
		rows = append(rows, row)
	}
	rows = append(rows, markup.Row(
		markup.Data("更换歌单", aiPlaylistCallbackUnique, "change", session.token),
		markup.Data("取消", aiPlaylistCallbackUnique, "cancel", session.token),
	))
	markup.Inline(rows...)
	return markup
}

func findAIPlaylistCandidate(candidates []aiPlaylistCandidate, id string) aiPlaylistCandidate {
	for _, candidate := range candidates {
		if candidate.ID == id {
			return candidate
		}
	}
	return aiPlaylistCandidate{Title: "未知歌曲", Artist: "未知艺人"}
}

func aiPlaylistDisplayValue(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if limit > 0 && len(runes) > limit {
		value = string(runes[:limit-1]) + "…"
	}
	return value
}

func aiPlaylistButtonLabel(value string) string {
	return aiPlaylistDisplayValue(value, 26)
}
