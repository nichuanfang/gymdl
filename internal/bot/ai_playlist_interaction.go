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
	creating        bool
	awaitingName    bool
	newPlaylistName string
	createIntentID  string
	createIntents   []aiPlaylistCreateIntent
	request         string
	excludedIDs     []string
	draft           *aiPlaylistDraft
	awaitingInput   bool
	awaitingSeed    bool
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
	{ID: "inst", Label: "器乐纯享", Text: "以纯音乐和器乐为主，适合作为背景聆听，避免明显人声。"},
}

func (app *BotApp) startAIPlaylist(c tb.Context) error {
	if c.Chat() == nil || c.Sender() == nil {
		return c.Send("无法读取 Telegram 会话信息，请在聊天中重新发送 /ai_playlist。")
	}
	if app.hasAIPlaylistSessionForChat(c.Chat().ID) {
		return c.Send("AI 歌单还开着。请在原消息里继续，或点取消。")
	}
	if reason := app.maintenanceConflict(c.Chat().ID, "ai"); reason != "" {
		return c.Send(reason)
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
	if reason := app.maintenanceConflict(session.chatID, "ai"); reason != "" || app.hasAIPlaylistSessionForChat(session.chatID) || !app.registerAIPlaylistSession(session) {
		app.playlistMaintenanceStartMu.Unlock()
		_ = service.close()
		if reason == "" {
			reason = "AI 歌单还开着。请在原消息里继续，或点取消。"
		}
		_, _ = c.Bot().Edit(message, reason, &tb.ReplyMarkup{})
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
	intents, err := service.resumeStore.ListIntents(ctx)
	if err != nil {
		app.failAIPlaylistMenu(token, "❌ 读取未完成的新建歌单状态失败，请稍后重试。", err)
		return
	}
	intentPlaylists := make(map[string]struct{}, len(intents))
	for _, intent := range intents {
		if intent.PlaylistID != "" {
			intentPlaylists[intent.PlaylistID] = struct{}{}
			continue
		}
		for _, playlist := range playlists {
			if playlist.Name == intent.Name && playlist.Description == intent.Marker {
				intentPlaylists[playlist.ID] = struct{}{}
			}
		}
	}
	session.totalPlaylists = len(playlists)
	for _, playlist := range playlists {
		if _, pendingCreate := intentPlaylists[playlist.ID]; pendingCreate {
			continue
		}
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
		session.playlists = append(session.playlists, playlist)
	}
	session.createIntents = intents
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
	case "new":
		if len(args) != 2 || session.target.ID != "" || session.creating {
			return c.Respond(&tb.CallbackResponse{Text: "当前不能开始新建歌单", ShowAlert: true})
		}
		updated, ok := app.beginAIPlaylistNameInput(token)
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "AI 歌单会话已过期", ShowAlert: true})
		}
		_ = c.Respond()
		return c.Edit(formatAIPlaylistSession(updated), aiPlaylistSessionMarkup(updated))

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
			current, exists := app.getAIPlaylistSession(token)
			if exists && current.target.ID != "" && !current.processing {
				return app.showAIPlaylistSession(c, current, "继续这张歌单")
			}
			return c.Respond(&tb.CallbackResponse{Text: "AI 歌单会话已过期，请重新发送 /ai_playlist", ShowAlert: true})
		}
		if err := c.Edit(formatAIPlaylistSession(updated), aiPlaylistSessionMarkup(updated)); err != nil {
			if telegramMessageUnchanged(err) {
				return c.Respond(&tb.CallbackResponse{Text: "已选中这张歌单"})
			}
			_, _ = app.resetAIPlaylistTarget(token)
			return c.Respond(&tb.CallbackResponse{Text: "没能打开这张歌单，请再点一次", ShowAlert: true})
		}
		return c.Respond(&tb.CallbackResponse{Text: "已选中，直接发送要求或点一个主题"})

	case "base":
		if len(args) != 2 || session.target.ID == "" || strings.TrimSpace(session.target.Description) == "" {
			return c.Respond(&tb.CallbackResponse{Text: "请先选一个有备注的 AI 歌单，或直接发送生成要求。", ShowAlert: true})
		}
		return app.beginAIPlaylistGeneration(c, session, session.request, nil)

	case "preset":
		if len(args) != 3 || (session.target.ID == "" && (!session.creating || session.newPlaylistName == "" || session.awaitingName)) {
			return c.Respond(&tb.CallbackResponse{Text: "请先选择歌单或填写新歌单名称", ShowAlert: true})
		}
		preset, ok := findAIPlaylistPreset(args[2])
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "快捷主题无效", ShowAlert: true})
		}
		request := mergeAIPlaylistRequest(session.request, preset.Text)
		return app.beginAIPlaylistGeneration(c, session, request, nil)

	case "seed":
		if len(args) != 2 || session.draft != nil || session.pendingWrite || (session.target.ID == "" && (!session.creating || session.newPlaylistName == "" || session.awaitingName)) {
			return c.Respond(&tb.CallbackResponse{Text: "请先选择歌单或填写新歌单名称", ShowAlert: true})
		}
		updated, ok := app.setAIPlaylistAwaitingSeed(token)
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "AI 歌单会话已过期", ShowAlert: true})
		}
		_ = c.Respond()
		return c.Edit(formatAIPlaylistSession(updated), aiPlaylistSessionMarkup(updated))

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
		if (session.target.ID == "" && !session.creating && !session.pendingWrite) || (session.draft == nil && !session.pendingWrite) {
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

	case "resume_create":
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "新建歌单续写操作无效", ShowAlert: true})
		}
		index, err := strconv.Atoi(args[2])
		if err != nil || index < 0 || index >= len(session.createIntents) {
			return c.Respond(&tb.CallbackResponse{Text: "新建歌单续写计划无效", ShowAlert: true})
		}
		updated, ok := app.selectAIPlaylistCreateIntent(token, session.createIntents[index])
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "新建歌单续写计划已过期，请重新发送 /ai_playlist", ShowAlert: true})
		}
		return app.beginAIPlaylistCommit(c, updated)

	case "cancel":
		if !app.cancelAIPlaylistSession(token) {
			return c.Respond(&tb.CallbackResponse{Text: "AI 歌单会话已过期或正在处理", ShowAlert: true})
		}
		_ = c.Respond()
		if session.pendingWrite || len(session.resumePlaylists) > 0 || session.createIntentID != "" {
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
	target := session.target
	if session.creating {
		target = aiPlaylist{Name: session.newPlaylistName}
	}
	draft, err := service.previewAIPlaylist(ctx, target, session.request, session.excludedIDs)
	if err != nil {
		app.failAIPlaylistPreview(token, err)
		return
	}
	session.draft = &draft
	session.awaitingInput = false
	session.awaitingSeed = false
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
		if _, seeded := splitPlaylistSeed(session.request); seeded {
			session.awaitingSeed = true
		}
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
	if session.creating {
		operationID := session.createIntentID
		if operationID == "" {
			operationID = session.token
		}
		intent, err := service.resumeStore.LoadIntent(ctx, operationID)
		if err != nil {
			app.failAIPlaylistCommitWithStore(token, fmt.Errorf("读取新建歌单续写状态失败: %w", err), service.resumeStore)
			return
		}
		if intent == nil {
			if session.draft == nil || session.newPlaylistName == "" {
				app.failAIPlaylistCommitWithStore(token, errors.New("没有找到已确认的新建歌单预览，请重新生成"), service.resumeStore)
				return
			}
			intent = &aiPlaylistCreateIntent{
				OperationID:  operationID,
				Name:         session.newPlaylistName,
				Description:  session.draft.Description,
				Marker:       "gymdl-ai-playlist-create:" + operationID,
				SongIDs:      aiPlaylistDraftIDs(*session.draft),
				Intro:        session.draft.Plan.Intro,
				UsedFallback: session.draft.UsedFallback,
			}
		}
		playlist, plan, err := service.finishAIPlaylistCreation(ctx, *intent)
		if err != nil {
			app.failAIPlaylistCommitWithStore(token, err, service.resumeStore)
			return
		}
		current, exists := findAIPlaylist([]aiPlaylist{playlist}, playlist.ID)
		if !exists {
			app.failAIPlaylistCommitWithStore(token, errors.New("创建歌单身份校验失败"), service.resumeStore)
			return
		}
		session.target = current
		session.creating = false
		session.awaitingName = false
		session.createIntentID = ""
		session.pendingWrite = true
		if session.resumePlans == nil {
			session.resumePlans = make(map[string]aiPlaylistResumePlan)
		}
		session.resumePlans[current.ID] = plan
		app.updateAIPlaylistSession(session)
		result, err = service.processAIPlaylist(ctx, current, &plan, false)
		if err != nil {
			app.failAIPlaylistCommitWithStore(token, err, service.resumeStore)
			return
		}
		result.Created = true
	} else if session.pendingWrite {
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
		if resume.Replace {
			result, err = service.replaceAIPlaylist(ctx, current, *resume)
		} else {
			result, err = service.processAIPlaylist(ctx, current, resume, false)
		}
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
		if !samePlaylistTrackOrder(trackSnapshot.IDs, session.draft.ExistingSongIDs) {
			app.failAIPlaylistCommitWithStore(token, errors.New("歌单曲目在预览后发生变化，本次未替换；请重新生成预览"), service.resumeStore)
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
			PlaylistID:      current.ID,
			Description:     current.Description,
			SongIDs:         ids,
			Intro:           session.draft.Plan.Intro,
			UsedFallback:    session.draft.UsedFallback,
			Replace:         true,
			OriginalSongIDs: append([]string(nil), session.draft.ExistingSongIDs...),
		}
		if err := service.resumeStore.Create(ctx, resume); err != nil {
			app.failAIPlaylistCommitWithStore(token, fmt.Errorf("保存续写计划失败，未修改歌单: %w", err), service.resumeStore)
			return
		}
		app.markAIPlaylistPendingWrite(token)
		result, err = service.replaceAIPlaylist(ctx, current, resume)
		if err != nil {
			app.failAIPlaylistCommitWithStore(token, err, service.resumeStore)
			return
		}
	}
	finished, ok := app.takeAIPlaylistSession(token)
	if !ok {
		return
	}
	operation := "已补齐歌单"
	if result.Replaced {
		operation = "已替换全部曲目"
	} else if result.Created {
		operation = "已创建并填充"
	}
	text := fmt.Sprintf("✅ 歌单生成完成\n──────────────────\n🎵 歌单：%s\n结果：%s\n生成曲目：%d 首", aiPlaylistDisplayValue(result.Name, 80), operation, result.Count)
	if result.Preserved > 0 {
		text += fmt.Sprintf("\n保留原有曲目：%d 首", result.Preserved)
	}
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
	if session.creating && session.target.ID == "" && session.createIntentID != "" {
		intent, err := store.LoadIntent(checkCtx, session.createIntentID)
		if err != nil {
			logger.Warn("[Telegram] 无法确认 AI 歌单创建状态", zap.Error(err))
		} else if intent != nil {
			session.pendingWrite = true
		} else {
			session.createIntentID = ""
			session.pendingWrite = false
			session.creating = false
			session.newPlaylistName = ""
		}
	} else if session.creating && session.target.ID == "" {
		intent, err := store.LoadIntent(checkCtx, session.token)
		if err != nil {
			logger.Warn("[Telegram] 无法确认 AI 歌单创建状态", zap.Error(err))
		} else if intent != nil {
			session.createIntentID = session.token
			session.pendingWrite = true
		}
	}
	if plan, err := store.Load(checkCtx, session.target.ID); err == nil {
		if session.target.ID != "" {
			session.pendingWrite = plan != nil
			session.awaitingInput = plan == nil && session.draft == nil
		}
		if plan != nil {
			if session.resumePlans == nil {
				session.resumePlans = make(map[string]aiPlaylistResumePlan)
			}
			session.resumePlans[session.target.ID] = *plan
		}
	} else if session.target.ID != "" {
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
	if session.awaitingName {
		name, err := normalizeAIPlaylistName(request)
		if err != nil {
			app.showAIPlaylistNotice(session, "⚠️ 歌单名称无效："+err.Error()+"\n请重新发送名称，或点取消。")
			return true, nil
		}
		updated, ok := app.setAIPlaylistNewName(session.token, name)
		if !ok {
			return true, c.Send("AI 歌单会话已过期，请重新发送 /ai_playlist。")
		}
		app.editAIPlaylistSessionMessage(updated, formatAIPlaylistSession(updated), aiPlaylistSessionMarkup(updated))
		return true, nil
	}
	if !session.awaitingInput {
		return false, nil
	}
	if len([]rune(request)) > aiPlaylistRequestMaxRunes {
		app.showAIPlaylistNotice(session, "⚠️ 生成要求最多 500 个字符，请精简后再发送。")
		return true, nil
	}
	request = composeAIPlaylistFollowup(session, request)
	if len([]rune(request)) > aiPlaylistRequestMaxRunes {
		app.showAIPlaylistNotice(session, "⚠️ 本次生成要求合计最多 500 个字符，请精简后再发送。")
		return true, nil
	}
	claimed, ok := app.claimAIPlaylistSession(session.token, request, nil)
	if !ok {
		return true, c.Send("AI 歌单会话已过期或正在处理，请重新发送 /ai_playlist。")
	}
	if !app.beginAIPlaylistRun(claimed.chatID) {
		restored, _ := app.setAIPlaylistSessionProcessing(claimed.token, false)
		app.showAIPlaylistNotice(restored, "⏳ 另一项歌单维护正在运行，请稍后再发送要求。")
		return true, nil
	}
	if _, err := c.Bot().Edit(claimed.message, "🤖 正在检索曲库并编排候选…", &tb.ReplyMarkup{}); err != nil {
		app.finishAIPlaylistRun(claimed.chatID)
		app.setAIPlaylistSessionProcessing(claimed.token, false)
		return true, err
	}
	go app.generateAIPlaylistPreview(claimed.token)
	return true, nil
}

func (app *BotApp) showAIPlaylistNotice(session pendingAIPlaylistSession, notice string) {
	if session.token == "" {
		return
	}
	session.notice = notice
	app.updateAIPlaylistSession(session)
	app.editAIPlaylistSessionMessage(session, formatAIPlaylistSession(session), aiPlaylistSessionMarkup(session))
}

func normalizeAIPlaylistName(raw string) (string, error) {
	name := aiPlaylistDisplayValue(raw, 0)
	if name == "" {
		return "", errors.New("名称不能为空")
	}
	if strings.HasPrefix(strings.ToUpper(name), "[AI]") {
		name = strings.TrimSpace(name[4:])
	}
	if name == "" {
		return "", errors.New("请在 [AI] 后填写歌单名称")
	}
	if len([]rune(name)) > 88 {
		return "", errors.New("名称最多 88 个字符")
	}
	return "[AI] " + name, nil
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
		session.awaitingSeed = false
		session.notice = ""
		session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
		app.aiPlaylistSessions[token] = session
		return cloneAIPlaylistSession(session), true
	}
	return pendingAIPlaylistSession{}, false
}

func (app *BotApp) beginAIPlaylistNameInput(token string) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	session, ok := app.aiPlaylistSessions[token]
	if !ok || session.processing || session.target.ID != "" || session.creating || session.pendingWrite {
		return pendingAIPlaylistSession{}, false
	}
	session.creating = true
	session.awaitingName = true
	session.awaitingInput = false
	session.awaitingSeed = false
	session.notice = ""
	session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
	app.aiPlaylistSessions[token] = session
	return cloneAIPlaylistSession(session), true
}

func (app *BotApp) setAIPlaylistNewName(token, name string) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	session, ok := app.aiPlaylistSessions[token]
	if !ok || session.processing || !session.creating || !session.awaitingName {
		return pendingAIPlaylistSession{}, false
	}
	session.newPlaylistName = name
	session.awaitingName = false
	session.awaitingInput = true
	session.awaitingSeed = false
	session.notice = ""
	session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
	app.aiPlaylistSessions[token] = session
	return cloneAIPlaylistSession(session), true
}

func (app *BotApp) selectAIPlaylistCreateIntent(token string, intent aiPlaylistCreateIntent) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	session, ok := app.aiPlaylistSessions[token]
	if !ok || session.processing || session.target.ID != "" || session.pendingWrite {
		return pendingAIPlaylistSession{}, false
	}
	for _, configured := range session.createIntents {
		if configured.OperationID != intent.OperationID {
			continue
		}
		session.creating = true
		session.awaitingName = false
		session.awaitingInput = false
		session.newPlaylistName = configured.Name
		session.createIntentID = configured.OperationID
		session.pendingWrite = true
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
	if !ok || session.processing || session.pendingWrite || (session.target.ID == "" && !session.creating) {
		return pendingAIPlaylistSession{}, false
	}
	if session.creating {
		session.newPlaylistName = ""
		session.awaitingName = true
		session.awaitingInput = false
		session.awaitingSeed = false
		session.draft = nil
		session.excludedIDs = nil
		session.notice = ""
		session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
		app.aiPlaylistSessions[token] = session
		return cloneAIPlaylistSession(session), true
	}
	session.target = aiPlaylist{}
	session.draft = nil
	session.excludedIDs = nil
	session.awaitingInput = false
	session.awaitingSeed = false
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
	if !ok || session.processing || (session.target.ID == "" && !(session.creating && session.newPlaylistName != "") && !(session.pendingWrite && session.createIntentID != "")) {
		return pendingAIPlaylistSession{}, false
	}
	session.processing = true
	session.awaitingInput = false
	session.awaitingSeed = false
	session.request = request
	session.excludedIDs = append([]string(nil), excluded...)
	session.notice = ""
	session.expiresAt = time.Now().Add(aiPlaylistSessionTTL)
	app.aiPlaylistSessions[token] = session
	return cloneAIPlaylistSession(session), true
}

func (app *BotApp) setAIPlaylistAwaitingSeed(token string) (pendingAIPlaylistSession, bool) {
	app.aiPlaylistSessionMu.Lock()
	defer app.aiPlaylistSessionMu.Unlock()
	app.ensureAIPlaylistSessionMapsLocked()
	session, ok := app.aiPlaylistSessions[token]
	if !ok || session.processing || session.pendingWrite || session.draft != nil {
		return pendingAIPlaylistSession{}, false
	}
	if session.target.ID == "" && (!session.creating || session.newPlaylistName == "" || session.awaitingName) {
		return pendingAIPlaylistSession{}, false
	}
	session.awaitingSeed = true
	session.awaitingInput = true
	session.notice = "直接发送歌名，或「歌名 - 歌手」。例如：晴天 - 周杰伦"
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
	session.awaitingSeed = false
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
	session.createIntents = append([]aiPlaylistCreateIntent(nil), session.createIntents...)
	for index := range session.createIntents {
		session.createIntents[index].SongIDs = append([]string(nil), session.createIntents[index].SongIDs...)
	}
	session.excludedIDs = append([]string(nil), session.excludedIDs...)
	plans := session.resumePlans
	session.resumePlans = make(map[string]aiPlaylistResumePlan, len(plans))
	for id, plan := range plans {
		plan.SongIDs = append([]string(nil), plan.SongIDs...)
		plan.OriginalSongIDs = append([]string(nil), plan.OriginalSongIDs...)
		session.resumePlans[id] = plan
	}
	if session.draft != nil {
		draft := *session.draft
		draft.Candidates = append([]aiPlaylistCandidate(nil), draft.Candidates...)
		draft.Plan.Playlist = append([]aiPlaylistPlanItem(nil), draft.Plan.Playlist...)
		draft.ExistingSongIDs = append([]string(nil), draft.ExistingSongIDs...)
		session.draft = &draft
	}
	return session
}

func (app *BotApp) showAIPlaylistSession(c tb.Context, session pendingAIPlaylistSession, toast string) error {
	if err := c.Edit(formatAIPlaylistSession(session), aiPlaylistSessionMarkup(session)); err != nil && !telegramMessageUnchanged(err) {
		return c.Respond(&tb.CallbackResponse{Text: "没能更新这张消息，请再点一次", ShowAlert: true})
	}
	return c.Respond(&tb.CallbackResponse{Text: toast})
}

func telegramMessageUnchanged(err error) bool {
	return err != nil && strings.Contains(err.Error(), "message is not modified")
}

func (app *BotApp) editAIPlaylistSessionMessage(session pendingAIPlaylistSession, text string, markup *tb.ReplyMarkup) {
	if session.message == nil || app.bot == nil {
		return
	}
	if _, err := app.bot.Edit(session.message, text, markup); err != nil && !telegramMessageUnchanged(err) {
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

func composeAIPlaylistFollowup(session pendingAIPlaylistSession, incoming string) string {
	incoming = strings.TrimSpace(incoming)
	if !session.awaitingSeed {
		if strings.TrimSpace(session.request) != "" {
			return mergeAIPlaylistRequest(session.request, incoming)
		}
		return incoming
	}
	if _, seeded := splitPlaylistSeed(incoming); !seeded {
		incoming = "像这首歌 " + incoming
	}
	previous := strings.TrimSpace(session.request)
	if previous == "" {
		return incoming
	}
	if seed, seeded := splitPlaylistSeed(previous); seeded && strings.TrimSpace(seed.Title) != "" {
		if seed.Theme != "" {
			return incoming + "\n" + seed.Theme
		}
		return incoming
	}
	return incoming + "\n" + previous
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
	if session.creating && session.pendingWrite {
		fmt.Fprintf(&text, "继续创建\n\n%s\n\n上次已确认。\n继续后会补上预览里的歌。", aiPlaylistDisplayValue(session.newPlaylistName, 80))
		if session.notice != "" {
			text.WriteString("\n\n")
			text.WriteString(session.notice)
		}
		return text.String()
	}
	if session.creating && session.awaitingName {
		text.WriteString("新建歌单\n\n发送名称。\n会自动加上「[AI]」。\n确认预览前不会创建歌单。")
		if session.notice != "" {
			text.WriteString("\n\n")
			text.WriteString(session.notice)
		}
		return text.String()
	}
	if session.target.ID == "" && !session.creating {
		text.WriteString("生成歌单")
		if len(session.playlists) > 0 {
			text.WriteString("\n\n选择一张 [AI] 歌单，或新建。\n确认前不会修改 Navidrome。")
			if len(session.resumePlaylists) > 0 {
				text.WriteString("\n\n也可以继续上次未完成的写入。")
			}
		} else {
			text.WriteString("\n\n还没有 [AI] 歌单。\n可以直接新建。")
		}
		if len(session.createIntents) > 0 {
			text.WriteString("\n\n也可以继续上次确认过的新建。")
		}
		return text.String()
	}
	if session.creating && session.draft == nil {
		fmt.Fprintf(&text, "新建歌单\n\n%s\n\n直接发送主题，或点下面的主题。\n确认后才会创建。", aiPlaylistDisplayValue(session.newPlaylistName, 80))
		if session.notice != "" {
			text.WriteString("\n\n")
			text.WriteString(session.notice)
		}
		return text.String()
	}
	if session.pendingWrite {
		fmt.Fprintf(&text, "继续写入\n\n%s", aiPlaylistDisplayValue(session.target.Name, 80))
		if plan, ok := session.resumePlans[session.target.ID]; ok && plan.Replace {
			text.WriteString("\n\n有一份未完成的替换计划。\n继续后会先复核，再替换。")
		} else {
			text.WriteString("\n\n上次的写入还没完成。\n继续后会先复核，再补上缺少的歌。")
		}
		if session.notice != "" {
			text.WriteString("\n\n")
			text.WriteString(session.notice)
		}
		return text.String()
	}
	if session.draft != nil {
		text.WriteString("歌单预览")
		playlistName := session.target.Name
		if session.creating {
			playlistName = session.newPlaylistName
		}
		fmt.Fprintf(&text, "\n\n%s", aiPlaylistDisplayValue(playlistName, 80))
		if request := aiPlaylistDisplayValue(session.request, 140); request != "" {
			fmt.Fprintf(&text, "\n要求：%s", request)
		}
		if description := aiPlaylistDisplayValue(session.draft.Description, 160); description != "" {
			fmt.Fprintf(&text, "\n检索主题：%s", description)
		}
		fmt.Fprintf(&text, "\n\n%d 首里先看 %d 首", len(session.draft.Plan.Playlist), min(aiPlaylistPreviewLimit, len(session.draft.Plan.Playlist)))
		if intro := aiPlaylistDisplayValue(session.draft.Plan.Intro, 160); intro != "" {
			text.WriteString("\n\n")
			text.WriteString(intro)
		}
		for index, item := range session.draft.Plan.Playlist {
			if index >= aiPlaylistPreviewLimit {
				break
			}
			candidate := findAIPlaylistCandidate(session.draft.Candidates, item.ID)
			fmt.Fprintf(&text, "\n\n%s", aiPlaylistDisplayValue(candidate.Title, 40))
			if artist := aiPlaylistDisplayValue(candidate.Artist, 40); artist != "" {
				fmt.Fprintf(&text, "\n%s", artist)
			}
			if reason := aiPlaylistDisplayValue(item.Reason, 40); reason != "" {
				fmt.Fprintf(&text, "\n%s", reason)
			}
		}
		if session.draft.UsedFallback {
			text.WriteString("\n\n这次没用模型编排，按相关度排了。")
		}
		if session.notice != "" {
			text.WriteString("\n\n")
			text.WriteString(session.notice)
		}
		if session.creating {
			text.WriteString("\n\n确认后才会创建这张歌单。")
		} else if len(session.draft.ExistingSongIDs) > 0 {
			fmt.Fprintf(&text, "\n\n确认后会替换当前 %d 首。", len(session.draft.ExistingSongIDs))
		} else {
			text.WriteString("\n\n这是空歌单；确认后会写入上方曲目。")
		}
		return text.String()
	}
	text.WriteString("生成歌单")
	fmt.Fprintf(&text, "\n\n%s", aiPlaylistDisplayValue(session.target.Name, 80))
	if description := aiPlaylistDisplayValue(session.target.Description, 160); description != "" {
		fmt.Fprintf(&text, "\n%s", description)
	}
	text.WriteString("\n\n发送生成要求，或点下面的主题。")
	if session.target.SongCount > 0 {
		fmt.Fprintf(&text, "\n当前有 %d 首；确认预览后会替换现有曲目。", session.target.SongCount)
	} else {
		text.WriteString("\n这是空歌单；确认预览后才会写入曲目。")
	}
	if session.notice != "" {
		text.WriteString("\n\n")
		text.WriteString(session.notice)
	}
	return text.String()
}

func aiPlaylistSessionMarkup(session pendingAIPlaylistSession) *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	rows := make([]tb.Row, 0, 8)
	if session.creating && (session.awaitingName || session.newPlaylistName == "") {
		rows = append(rows, markup.Row(markup.Data("取消", aiPlaylistCallbackUnique, "cancel", session.token)))
		markup.Inline(rows...)
		return markup
	}
	if session.creating && session.pendingWrite {
		rows = append(rows, markup.Row(
			markup.Data("↻ 继续创建", aiPlaylistCallbackUnique, "confirm", session.token),
			markup.Data("取消", aiPlaylistCallbackUnique, "cancel", session.token),
		))
		markup.Inline(rows...)
		return markup
	}
	if session.target.ID == "" && !session.creating {
		rows = append(rows, markup.Row(markup.Data("＋ 新建 AI 歌单", aiPlaylistCallbackUnique, "new", session.token)))
		for index := 0; index < len(session.playlists); index += 2 {
			row := make(tb.Row, 0, 2)
			for current := index; current < len(session.playlists) && current < index+2; current++ {
				playlist := session.playlists[current]
				row = append(row, markup.Data(aiPlaylistTargetButtonLabel(playlist), aiPlaylistCallbackUnique, "select", session.token, strconv.Itoa(current)))
			}
			rows = append(rows, row)
		}
		for index, playlist := range session.resumePlaylists {
			rows = append(rows, markup.Row(markup.Data("↻ 继续「"+aiPlaylistButtonLabel(playlist.Name)+"」", aiPlaylistCallbackUnique, "resume", session.token, strconv.Itoa(index))))
		}
		for index, intent := range session.createIntents {
			rows = append(rows, markup.Row(markup.Data("↻ 继续创建「"+aiPlaylistButtonLabel(intent.Name)+"」", aiPlaylistCallbackUnique, "resume_create", session.token, strconv.Itoa(index))))
		}
		rows = append(rows, markup.Row(markup.Data("取消", aiPlaylistCallbackUnique, "cancel", session.token)))
		markup.Inline(rows...)
		return markup
	}
	if session.pendingWrite {
		continueLabel := "↻ 继续写入"
		if plan, ok := session.resumePlans[session.target.ID]; ok && plan.Replace {
			continueLabel = "↻ 继续替换"
		}
		rows = append(rows, markup.Row(
			markup.Data(continueLabel, aiPlaylistCallbackUnique, "confirm", session.token),
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
		confirmLabel := "✅ 确认写入"
		if session.creating {
			confirmLabel = "✅ 确认创建"
		} else if len(session.draft.ExistingSongIDs) > 0 {
			confirmLabel = "✅ 确认替换"
		}
		rows = append(rows, markup.Row(
			markup.Data(confirmLabel, aiPlaylistCallbackUnique, "confirm", session.token),
			markup.Data("取消", aiPlaylistCallbackUnique, "cancel", session.token),
		))
		changeLabel := "更换歌单"
		if session.creating {
			changeLabel = "更换名称"
		}
		rows = append(rows, markup.Row(markup.Data(changeLabel, aiPlaylistCallbackUnique, "change", session.token)))
		markup.Inline(rows...)
		return markup
	}
	if session.creating {
		for index := 0; index < len(aiPlaylistPresets); index += 2 {
			row := make(tb.Row, 0, 2)
			for current := index; current < len(aiPlaylistPresets) && current < index+2; current++ {
				preset := aiPlaylistPresets[current]
				row = append(row, markup.Data(preset.Label, aiPlaylistCallbackUnique, "preset", session.token, preset.ID))
			}
			rows = append(rows, row)
		}
		rows = append(rows, markup.Row(markup.Data("像这首歌", aiPlaylistCallbackUnique, "seed", session.token)))
		rows = append(rows, markup.Row(markup.Data("更换名称", aiPlaylistCallbackUnique, "change", session.token), markup.Data("取消", aiPlaylistCallbackUnique, "cancel", session.token)))
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
	rows = append(rows, markup.Row(markup.Data("像这首歌", aiPlaylistCallbackUnique, "seed", session.token)))
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

func aiPlaylistTargetButtonLabel(playlist aiPlaylist) string {
	return fmt.Sprintf("%s · %d 首", aiPlaylistButtonLabel(playlist.Name), playlist.SongCount)
}
