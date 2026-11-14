package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core/linkparser"
	"github.com/nichuanfang/gymdl/internal/aichat"
	"github.com/nichuanfang/gymdl/internal/bot/dispatch"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

const (
	aiCandidateCallbackUnique = "ai_candidate"
	aiModeSettingCallback     = "ai_mode"
)

var (
	errMusicDedupeUnavailable         = errors.New("查重数据库不可用")
	errCandidateNotDownloadable       = errors.New("候选链接无法作为音乐下载")
	errDuplicateConfirmationPending   = errors.New("等待用户确认可能重复的歌曲")
	errMusicDownloadAlreadyProcessing = errors.New("歌曲已有下载或确认任务正在处理")
	errCandidateDownloadFailed        = errors.New("候选歌曲未成功入库")
)

var aiCandidateEndpoint = tb.InlineButton{Unique: aiCandidateCallbackUnique}
var aiModeSettingEndpoint = tb.InlineButton{Unique: aiModeSettingCallback}

func (app *BotApp) initAI() {
	cfg := app.cfg.AI
	if cfg == nil || !cfg.Enable {
		return
	}
	store, err := aichat.OpenStore("data/gymdl.sqlite3")
	if err != nil {
		utils.WarnWithFormat("[AI] 会话存储初始化失败，AI 模式不可用: %v", err)
		return
	}
	app.aiStore = store
	app.aiSkills = aichat.NewSkillRegistry(cfg.SkillsDirs, cfg.EnabledSkills)
	app.aiMCP = aichat.NewMCPClients(cfg.MCPServers)
	app.loadAIMCPTools(context.Background())
	// The bot may run without the Web server, but provider searches read the
	// web runtime config; publish it so QQ/Netease credentials are available.
	web.EnsureAgentRuntime(app.cfg)
}

func (app *BotApp) loadAIMCPTools(ctx context.Context) []aichat.Tool {
	app.aiMCPMu.Lock()
	defer app.aiMCPMu.Unlock()

	tools, failures := aichat.LoadMCPToolsWithErrors(ctx, app.aiMCP)
	app.aiMCPTools = tools
	for _, err := range failures {
		utils.WarnWithFormat("[AI] MCP 工具加载失败，将在歌单整理时重试: %v", err)
	}
	return append([]aichat.Tool(nil), tools...)
}

func (app *BotApp) currentAIMCPTools() []aichat.Tool {
	app.aiMCPMu.Lock()
	defer app.aiMCPMu.Unlock()
	return append([]aichat.Tool(nil), app.aiMCPTools...)
}

// aiInflight serialises turns per chat. telebot runs every handler in its own
// goroutine, so two quick messages used to run two overlapping LLM + provider
// searches that both wrote the same history row set.
func (app *BotApp) aiTurnLock(chatID int64) func() {
	app.aiTurnMu.Lock()
	if app.aiTurnLocks == nil {
		app.aiTurnLocks = make(map[int64]*aiTurnLockEntry)
	}
	entry, ok := app.aiTurnLocks[chatID]
	if !ok {
		entry = &aiTurnLockEntry{}
		app.aiTurnLocks[chatID] = entry
	}
	entry.refs++
	app.aiTurnMu.Unlock()
	entry.mu.Lock()
	var unlockOnce sync.Once
	return func() {
		unlockOnce.Do(func() {
			entry.mu.Unlock()
			app.aiTurnMu.Lock()
			entry.refs--
			// Count both the current holder and queued turns. Removing an entry
			// while a waiter still references it would let the next turn create a
			// second mutex for the same chat and run concurrently.
			if entry.refs == 0 && app.aiTurnLocks[chatID] == entry {
				delete(app.aiTurnLocks, chatID)
			}
			app.aiTurnMu.Unlock()
		})
	}
}

func (app *BotApp) aiAvailable() bool {
	return app.aiStore != nil && app.cfg.AI != nil && app.cfg.AI.Enable
}

// aiEnabled resolves the current chat mode, falling back to the configured
// default until the user switches explicitly.
func (app *BotApp) aiEnabled(c tb.Context) bool {
	if !app.aiAvailable() {
		return false
	}
	userID, chatID := aiScope(c)
	enabled, found, err := app.aiStore.ModeState(userID, chatID)
	if err == nil && found {
		return enabled
	}
	// ai.default_mode is documented as "private chat enters AI mode by
	// default". Applying it to groups turned every whitelisted group message
	// into a paid LLM turn plus multi-provider search.
	if !isPrivateChat(c) {
		return false
	}
	return app.cfg.AI.DefaultMode
}

func isPrivateChat(c tb.Context) bool {
	chat := c.Chat()
	return chat != nil && chat.Type == tb.ChatPrivate
}

func aiScope(c tb.Context) (int64, int64) {
	var userID, chatID int64
	if c.Sender() != nil {
		userID = c.Sender().ID
	}
	if c.Chat() != nil {
		chatID = c.Chat().ID
	}
	return userID, chatID
}

func (app *BotApp) runAITurn(c tb.Context, text string) error {
	if !app.aiAvailable() {
		return c.Send("AI 模式未启用，请先在配置中开启 ai.enable。")
	}
	userID, chatID := aiScope(c)
	unlock := app.aiTurnLock(chatID)
	defer unlock()
	collector := aichat.NewCollector()
	builtins := app.aiTools()
	if app.aiSkills != nil {
		builtins = append(builtins, app.aiSkills.Tool())
	}
	tools := aichat.MergeTools(builtins, app.currentAIMCPTools())
	engine, err := aichat.NewEngine(app.cfg.AI, app.aiStore, aichat.HistorySource{
		DSN: app.cfg.AI.HistoryDSN, Table: app.cfg.AI.HistoryTable, WindowDay: app.cfg.AI.HistoryWindowDays,
	}, app.aiSkills, tools, app.ownedChecker)
	if err != nil {
		return c.Send("AI 不可用：" + err.Error())
	}
	msg, err := c.Bot().Send(c.Chat(), "🤖 正在理解你的需求…")
	if err != nil {
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			_, _ = c.Bot().Edit(msg, "❌ AI 处理异常，请稍后重试。")
		}
	}()
	// Show a friendly failure in the chat and keep the provider error in the log:
	// raw upstream text can contain the base URL, model name and request ids.
	fail := func(cause error) error {
		utils.WarnWithFormat("[AI] 会话失败 chat=%d: %v", chatID, cause)
		_, _ = c.Bot().Edit(msg, "❌ "+friendlyAIError(cause))
		return nil
	}

	// Progress edits are throttled: Telegram rate-limits message edits, and a
	// tool-heavy turn can emit several stages in a row.
	var progressMu sync.Mutex
	lastProgress := time.Time{}
	progress := func(stage string) {
		progressMu.Lock()
		defer progressMu.Unlock()
		if time.Since(lastProgress) < 1200*time.Millisecond {
			return
		}
		lastProgress = time.Now()
		_, _ = c.Bot().Edit(msg, "🤖 "+stage)
	}
	ctx := aichat.WithProgress(aichat.WithCollector(context.Background(), collector), progress)
	out, err := engine.Respond(ctx, aichat.TurnInput{UserID: userID, ChatID: chatID, Text: text})
	if err != nil {
		return fail(err)
	}
	reply := aichat.Render(out, app.cfg.AI.DefaultMode, app.aiEnabled(c))
	markup := app.candidateMarkup(out.Candidates)
	if chunks := aichat.SplitMessage(reply); len(chunks) > 1 {
		// Telegram rejects messages over 4096 characters. Send the answer as
		// several messages and keep the buttons on the last one.
		for index, chunk := range chunks {
			options := []any{tb.ModeHTML}
			if index == len(chunks)-1 && markup != nil {
				options = append(options, markup)
			}
			if index == 0 {
				if _, editErr := c.Bot().Edit(msg, chunk, options...); editErr != nil {
					if _, sendErr := c.Bot().Send(c.Chat(), chunk, options...); sendErr != nil {
						utils.WarnWithFormat("[AI] 发送分片失败: %v", sendErr)
					}
				}
				continue
			}
			if _, sendErr := c.Bot().Send(c.Chat(), chunk, options...); sendErr != nil {
				utils.WarnWithFormat("[AI] 发送分片失败: %v", sendErr)
			}
		}
		return nil
	}
	options := []any{tb.ModeHTML}
	if markup != nil {
		options = append(options, markup)
	}
	if _, editErr := c.Bot().Edit(msg, reply, options...); editErr != nil {
		// Never leave the user staring at "正在理解你的需求…": fall back to a
		// fresh message and delete the placeholder.
		utils.WarnWithFormat("[AI] 编辑回复失败，改为新消息发送: %v", editErr)
		if _, sendErr := c.Bot().Send(c.Chat(), reply, options...); sendErr != nil {
			utils.WarnWithFormat("[AI] 发送回复失败: %v", sendErr)
			_, _ = c.Bot().Edit(msg, "❌ 回复发送失败，请重试。")
			return nil
		}
		_ = c.Bot().Delete(msg)
	}
	return nil
}

// friendlyAIError keeps provider internals out of the chat while still telling
// the user what to do next.
func friendlyAIError(cause error) string {
	text := cause.Error()
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(text, "401"), strings.Contains(text, "403"):
		return "AI 服务鉴权失败，请检查 ai.api_key 配置。"
	case strings.Contains(text, "429"):
		return "AI 服务限流了，稍后再试。"
	case strings.Contains(text, "超时"), strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timeout"):
		return "AI 服务响应超时，请稍后重试。"
	case strings.Contains(text, "配置不完整"):
		return "AI 配置不完整，请先在配置中填好 model 和 api_key。"
	case strings.Contains(lower, "context length"), strings.Contains(lower, "maximum context"), strings.Contains(lower, "too many tokens"), strings.Contains(lower, "token limit"):
		return "这次对话太长了，用 /clear 清空上下文后再试。"
	}
	return "AI 这次没能完成回答，请稍后再试或换个说法。"
}

// candidateMarkup turns grounded hits into per-track download and preview
// buttons. Owned tracks stay visible but cannot be downloaded again.
func (app *BotApp) candidateMarkup(items []aichat.Candidate) *tb.ReplyMarkup {
	if len(items) == 0 {
		return nil
	}
	markup := &tb.ReplyMarkup{}
	rows := make([]tb.Row, 0, len(items))
	for index, item := range items {
		if item.Token == "" || item.URL == "" {
			continue
		}
		row := markup.Row(
			markup.Data(aichat.CandidateLabel(index, item), aiCandidateCallbackUnique, "download", item.Token),
			markup.Data("试听", aiCandidateCallbackUnique, "preview", item.Token),
		)
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	// Offer a batch action only when there is more than one track: pressing ten
	// download buttons one by one is the main friction in a recommendation.
	if len(rows) > 1 {
		batchID := items[0].BatchID
		completeBatch := batchID != ""
		for _, item := range items[1:] {
			if item.BatchID != batchID {
				completeBatch = false
				break
			}
		}
		if completeBatch {
			rows = append(rows, markup.Row(
				markup.Data("下载前 3 首", aiCandidateCallbackUnique, "batch", batchID, "3"),
				markup.Data("全部下载", aiCandidateCallbackUnique, "batch", batchID, "0"),
			))
		}
	}
	if items[0].BatchID != "" {
		rows = append(rows, markup.Row(markup.Data("换一批", aiCandidateCallbackUnique, "more", items[0].BatchID)))
	}
	markup.Inline(rows...)
	return markup
}

func (app *BotApp) HandleAICandidateCallback(c tb.Context) error {
	if !app.aiAvailable() {
		return c.Respond(&tb.CallbackResponse{Text: "AI 推荐已不可用，请重新发送 /ai。", ShowAlert: true})
	}
	args := c.Args()
	if len(args) < 2 {
		return c.Respond(&tb.CallbackResponse{Text: "操作信息无效", ShowAlert: true})
	}
	action, token := args[0], args[1]
	userID, chatID := aiScope(c)
	if action == "more" {
		if len(args) != 2 || token == "" {
			return app.expireCandidateMarkup(c)
		}
		items, err := app.aiStore.Candidates(userID, chatID)
		if err != nil || len(items) == 0 {
			return app.expireCandidateMarkup(c)
		}
		if _, valid := latestCandidatesForBatch(items, token); !valid {
			return c.Respond(&tb.CallbackResponse{Text: "这批推荐已不是最新一批，请从最新推荐消息继续。", ShowAlert: true})
		}
		_ = c.Respond()
		return app.runAITurn(c, "换一批，风格保持一致，换一些没推荐过的歌。")
	}
	if action == "batch" {
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "这批下载按钮已失效，请从最新推荐中重新操作。", ShowAlert: true})
		}
		return app.startAIBatchDownload(c, token, args[2])
	}
	candidate, err := app.aiStore.Candidate(token, userID, chatID)
	if err != nil || candidate == nil {
		_ = c.Respond(&tb.CallbackResponse{Text: "候选已过期，请重新提问", ShowAlert: true})
		return app.expireCandidateMarkup(c)
	}
	if !candidate.ExpiresAt.IsZero() && time.Now().After(candidate.ExpiresAt) {
		_ = c.Respond(&tb.CallbackResponse{Text: "候选已过期，请重新提问", ShowAlert: true})
		return app.expireCandidateMarkup(c)
	}
	switch action {
	case "preview":
		_ = c.Respond()
		return c.Send(candidate.URL)
	case "download":
		if candidate.Owned {
			return c.Respond(&tb.CallbackResponse{Text: "这首已经入库了", ShowAlert: true})
		}
		_ = c.Respond(&tb.CallbackResponse{Text: "开始下载…"})
		err := app.startAIDownload(c, candidate)
		if errors.Is(err, errDuplicateConfirmationPending) ||
			errors.Is(err, errMusicDownloadAlreadyProcessing) ||
			errors.Is(err, errMusicDedupeUnavailable) ||
			errors.Is(err, errCandidateNotDownloadable) ||
			errors.Is(err, errCandidateDownloadFailed) {
			return nil // 用户可见的确认/处理中提示已经发出
		}
		return err
	default:
		return c.Respond(&tb.CallbackResponse{Text: "未知操作", ShowAlert: true})
	}
}

// expireCandidateMarkup rewrites the keyboard so stale buttons stop inviting
// clicks, instead of leaving a button that only ever shows an alert.
func (app *BotApp) expireCandidateMarkup(c tb.Context) error {
	markup := &tb.ReplyMarkup{}
	return c.Edit("⌛ 这批推荐已过期，按钮失效了。请发送 /ai 并补充新的需求。", tb.ModeHTML, markup)
}

// startAIBatchDownload queues downloads for several candidates. limit<=0 means
// every candidate of the latest answer. Downloads run sequentially in the
// background: each one reuses the pasted-link path (metadata preflight,
// duplicate confirmation, recording), and this box only has 2 vCPU, so firing
// them in parallel would thrash. The callback returns immediately instead of
// holding the handler open for the whole batch.
func (app *BotApp) startAIBatchDownload(c tb.Context, batchID, limitArg string) error {
	if !app.aiAvailable() {
		return c.Respond(&tb.CallbackResponse{Text: "AI 模式未启用", ShowAlert: true})
	}
	userID, chatID := aiScope(c)
	items, err := app.aiStore.Candidates(userID, chatID)
	if err != nil || len(items) == 0 {
		return c.Respond(&tb.CallbackResponse{Text: "没有可下载的候选，请重新提问", ShowAlert: true})
	}
	// Only the latest answer is a valid batch target: older turns' candidates
	// are still clickable individually but mixing them would download a set the
	// user never saw together.
	current, validBatch := latestCandidatesForBatch(items, batchID)
	if !validBatch {
		return c.Respond(&tb.CallbackResponse{Text: "这批推荐已过期，请从最新推荐中重新操作。", ShowAlert: true})
	}
	limit := len(current)
	n, convErr := strconv.Atoi(limitArg)
	if convErr != nil || n < 0 {
		return c.Respond(&tb.CallbackResponse{Text: "这批下载按钮无效，请从最新推荐中重新操作。", ShowAlert: true})
	}
	if n > 0 && n < limit {
		limit = n
	}
	// Candidates come back newest-first; restore the order the model wrote so
	// "前 3 首" means the first three recommendations.
	batch := append([]aichat.Candidate(nil), current[:limit]...)
	for i, j := 0, len(batch)-1; i < j; i, j = i+1, j-1 {
		batch[i], batch[j] = batch[j], batch[i]
	}
	queued := 0
	for _, item := range batch {
		if item.Owned {
			continue
		}
		queued++
	}
	if queued == 0 {
		return c.Respond(&tb.CallbackResponse{Text: "这些歌都已经入库了", ShowAlert: true})
	}
	// One batch per chat at a time: two overlapping batches would fight over
	// the same reservation keys and produce confusing duplicate prompts.
	app.aiTurnMu.Lock()
	if app.aiBatchRunning == nil {
		app.aiBatchRunning = make(map[int64]struct{})
	}
	if _, running := app.aiBatchRunning[chatID]; running {
		app.aiTurnMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "上一批还在下载，请稍等", ShowAlert: true})
	}
	app.aiBatchRunning[chatID] = struct{}{}
	app.aiTurnMu.Unlock()
	_ = c.Respond(&tb.CallbackResponse{Text: fmt.Sprintf("开始下载 %d 首…", queued)})
	status, err := c.Bot().Send(c.Chat(), fmt.Sprintf("📦 已加入下载队列：%d 首，正在依次处理…", queued))
	if err != nil {
		app.aiTurnMu.Lock()
		delete(app.aiBatchRunning, chatID)
		app.aiTurnMu.Unlock()
		return err
	}
	go app.runAIBatch(c, status, batch)
	return nil
}

func latestCandidatesForBatch(items []aichat.Candidate, batchID string) ([]aichat.Candidate, bool) {
	if len(items) == 0 || batchID == "" || items[0].BatchID != batchID {
		return nil, false
	}
	current := make([]aichat.Candidate, 0, len(items))
	for _, item := range items {
		if item.BatchID == batchID {
			current = append(current, item)
		}
	}
	return current, len(current) > 0
}

// runAIBatch downloads candidates one by one. The queue message tracks overall
// progress; each track keeps its own result message and playback actions.
func (app *BotApp) runAIBatch(c tb.Context, status *tb.Message, batch []aichat.Candidate) {
	_, chatID := aiScope(c)
	succeeded, alreadyOwned, alreadyProcessing, awaitingConfirmation, failed := 0, 0, 0, 0, 0
	processed := 0
	for _, item := range batch {
		if item.Owned {
			alreadyOwned++
			processed++
			continue
		}
		processed++
		_, _ = c.Bot().Edit(status, fmt.Sprintf("📦 正在处理《%s》\n进度：%d/%d", utils.EscapeHTML(utils.TruncateString(item.Name, 80)), processed, len(batch)), tb.ModeHTML)
		err := app.downloadCandidate(c, item)
		switch {
		case errors.Is(err, errDuplicateConfirmationPending):
			awaitingConfirmation++
		case errors.Is(err, errMusicDownloadAlreadyProcessing):
			alreadyProcessing++
		case err != nil:
			failed++
		default:
			succeeded++
		}
	}
	summary := fmt.Sprintf("📦 批量处理完成：成功入库 %d 首", succeeded)
	if alreadyOwned > 0 {
		summary += fmt.Sprintf("，已入库跳过 %d 首", alreadyOwned)
	}
	if alreadyProcessing > 0 {
		summary += fmt.Sprintf("，已有任务跳过 %d 首", alreadyProcessing)
	}
	if awaitingConfirmation > 0 {
		summary += fmt.Sprintf("，待确认重复项 %d 首", awaitingConfirmation)
	}
	if failed > 0 {
		summary += fmt.Sprintf("，失败 %d 首", failed)
	}
	_, _ = c.Bot().Edit(status, summary)
	app.aiTurnMu.Lock()
	delete(app.aiBatchRunning, chatID)
	app.aiTurnMu.Unlock()
}

// downloadCandidate runs one candidate through the shared download path. It is
// the same code the single-button flow uses, so duplicate checks cannot drift.
func (app *BotApp) downloadCandidate(c tb.Context, candidate aichat.Candidate) error {
	msg, err := c.Bot().Send(c.Chat(), fmt.Sprintf("⬇️ 开始下载《%s》", candidate.Name))
	if err != nil {
		return err
	}
	if app.musicDedupe == nil {
		_, _ = c.Bot().Edit(msg, "❌ 查重数据库不可用，已取消下载。")
		return errMusicDedupeUnavailable
	}
	taskConfig := config.WithLiveTaskOverrides(app.cfg)
	link, executor := linkparser.ParseLinkWithConfig(taskConfig, candidate.URL)
	processor, ok := executor.(music.Processor)
	if link == "" || !ok {
		_, _ = c.Bot().Edit(msg, "❌ 这个候选链接当前无法作为音乐下载。")
		return errCandidateNotDownloadable
	}
	processor.Init(taskConfig)
	downloaded := false
	session := &dispatch.Session{
		Text:           candidate.URL,
		Context:        c,
		User:           c.Sender(),
		Bot:            c.Bot(),
		Msg:            msg,
		Link:           link,
		Start:          time.Now(),
		Cfg:            taskConfig,
		CandidateToken: candidate.Token,
		OnMusicDownloaded: func(songs []*music.SongInfo) {
			downloaded = len(songs) > 0
		},
		CreateMusicActionMarkup: app.musicActionMarkup,
		SetMusicActionMessages:  app.setMusicActionMessages,
	}
	if err := app.handleMusicWithDuplicateCheck(session, processor); err != nil {
		return err
	}
	if !downloaded {
		return errCandidateDownloadFailed
	}
	return nil
}

func (app *BotApp) startAIDownload(c tb.Context, candidate *aichat.Candidate) error {
	// Reuse the same duplicate check, confirmation and recording path as a
	// pasted link so the "已入库" badge can never disagree with real downloads.
	return app.downloadCandidate(c, *candidate)
}
