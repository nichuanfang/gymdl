package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/config"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v4"
)

const emptyTrashRunTimeout = 10 * time.Minute
const emptyTrashConfirmationTTL = 5 * time.Minute

const emptyTrashCallbackUnique = "empty_trash"

var emptyTrashCallbackEndpoint = tb.InlineButton{Unique: emptyTrashCallbackUnique}

type emptyTrashService struct {
	organizer    *playlistOrganizerService
	stateTable   *musicRAGSink
	pineconeHost string
	pineconeKey  string
	namespace    string
	httpClient   *http.Client
}

type emptyTrashSnapshot struct {
	PlaylistName string
	TrackCount   int
}

type emptyTrashPlan struct {
	snapshot emptyTrashSnapshot
	ids      []string
	paths    []string
}

type pendingEmptyTrash struct {
	token      string
	userID     int64
	chatID     int64
	message    *tb.Message
	service    *emptyTrashService
	plan       emptyTrashPlan
	expiresAt  time.Time
	processing bool
}

type emptyTrashRecord struct {
	PlaylistName string          `json:"playlist_name"`
	TrackIDs     json.RawMessage `json:"track_ids"`
	FilePaths    json.RawMessage `json:"file_paths"`
	TrackCount   json.RawMessage `json:"track_count"`
}

type emptyTrashResult struct {
	NoTracks       bool
	TrackCount     int
	FilesDeleted   int
	StateRowsClean bool
	VectorsDeleted bool
	PreflightError error
	FileError      error
	StateError     error
	VectorError    error
}

func newEmptyTrashService(cfg *config.Config) (*emptyTrashService, error) {
	if cfg == nil || cfg.AIPlaylist == nil {
		return nil, errors.New("请填写 ai_playlist 中的 Pinecone 配置")
	}
	if strings.TrimSpace(cfg.AIPlaylist.PineconeAPIKey) == "" {
		return nil, errors.New("请填写 ai_playlist.pinecone_api_key")
	}
	if strings.TrimSpace(cfg.AIPlaylist.PineconeNamespace) == "" {
		return nil, errors.New("请填写 ai_playlist.pinecone_namespace")
	}
	pineconeHost, err := validatePineconeHost(cfg.AIPlaylist.PineconeHost)
	if err != nil {
		return nil, err
	}
	organizer, err := newPlaylistOrganizerService(cfg.PlaylistOrganizer)
	if err != nil {
		return nil, err
	}
	stateTable := newMusicRAGSink(cfg.N8NConfig, organizer.client)
	if !stateTable.configured() {
		return nil, errors.New("请配置 n8n_config.n8n_base_url、api_key 和 music_rag_table_id")
	}
	return &emptyTrashService{
		organizer:    organizer,
		stateTable:   stateTable,
		pineconeHost: pineconeHost,
		pineconeKey:  strings.TrimSpace(cfg.AIPlaylist.PineconeAPIKey),
		namespace:    strings.TrimSpace(cfg.AIPlaylist.PineconeNamespace),
		httpClient:   organizer.client,
	}, nil
}

func (s *emptyTrashService) run(ctx context.Context, progress func(string)) emptyTrashResult {
	result := emptyTrashResult{}
	plan, err := s.prepare(ctx)
	if err != nil {
		result.PreflightError = err
		return result
	}
	if plan.snapshot.TrackCount == 0 {
		result.NoTracks = true
		return result
	}
	return s.execute(ctx, plan, progress)
}

func (s *emptyTrashService) runConfirmed(ctx context.Context, approvedPlan emptyTrashPlan, progress func(string)) emptyTrashResult {
	currentPlan, err := s.prepare(ctx)
	if err != nil {
		return emptyTrashResult{PreflightError: err}
	}
	if currentPlan.snapshot.TrackCount == 0 {
		return emptyTrashResult{NoTracks: true}
	}
	if !sameEmptyTrashPlan(approvedPlan, currentPlan) {
		return emptyTrashResult{PreflightError: errors.New("确认后回收站内容发生变化，本次未执行删除；请重新发送 /empty_trash 查看最新数量")}
	}
	return s.execute(ctx, currentPlan, progress)
}

func (s *emptyTrashService) prepare(ctx context.Context) (emptyTrashPlan, error) {
	snapshot, ids, paths, err := s.readTrashPlaylist(ctx)
	if err != nil {
		return emptyTrashPlan{}, err
	}
	plan := emptyTrashPlan{snapshot: snapshot, ids: ids, paths: paths}
	if snapshot.TrackCount == 0 {
		return plan, nil
	}
	if err := s.stateTable.deleteRows(ctx, ids, true); err != nil {
		return emptyTrashPlan{}, fmt.Errorf("MusicRAG 状态表删除权限检查失败: %w", err)
	}
	if err := s.checkPinecone(ctx); err != nil {
		return emptyTrashPlan{}, fmt.Errorf("Pinecone 连接检查失败: %w", err)
	}
	return plan, nil
}

func (s *emptyTrashService) execute(ctx context.Context, plan emptyTrashPlan, progress func(string)) emptyTrashResult {
	result := emptyTrashResult{TrackCount: plan.snapshot.TrackCount}

	if progress != nil {
		progress("🤖 清空回收站 · 正在删除 Pinecone 向量…")
	}
	if err := s.deleteVectors(ctx, plan.ids); err != nil {
		result.VectorError = err
		return result
	}
	result.VectorsDeleted = true

	if progress != nil {
		progress("🤖 清空回收站 · 正在清理 MusicRAG 状态记录…")
	}
	if err := s.stateTable.deleteRows(ctx, plan.ids, false); err != nil {
		result.StateError = err
		return result
	}
	result.StateRowsClean = true

	for i, filePath := range plan.paths {
		if err := ctx.Err(); err != nil {
			result.FileError = fmt.Errorf("清空任务超时，已删除 %d/%d 个文件: %w", result.FilesDeleted, len(plan.paths), err)
			return result
		}
		if progress != nil && (i == 0 || (i+1)%10 == 0 || i+1 == len(plan.paths)) {
			progress(fmt.Sprintf("🤖 清空回收站 · 正在删除歌曲文件 %d/%d…", i+1, len(plan.paths)))
		}
		_, err := s.organizer.runOliveTinAction(ctx, oliveTinActionRequest{
			ActionID:  "delete_music_file",
			Arguments: []oliveTinArgument{{Name: "filePath", Value: filePath}},
		})
		if err != nil {
			result.FileError = fmt.Errorf("删除回收站文件失败，已删除 %d/%d 个文件: %w", result.FilesDeleted, len(plan.paths), err)
			return result
		}
		result.FilesDeleted++
	}
	return result
}

func (s *emptyTrashService) checkPinecone(ctx context.Context) error {
	endpoint, err := url.JoinPath(s.pineconeHost, "describe_index_stats")
	if err != nil {
		return fmt.Errorf("构造 Pinecone 检查地址失败: %w", err)
	}
	_, status, err := doPlaylistHTTPRequest(ctx, s.httpClient, http.MethodPost, endpoint, []byte("{}"), map[string]string{
		"Content-Type":           "application/json",
		"Api-Key":                s.pineconeKey,
		"X-Pinecone-Api-Version": "2025-04",
	}, "", "")
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("Pinecone 返回 HTTP %d", status)
	}
	return nil
}

func (s *emptyTrashService) readTrashPlaylist(ctx context.Context) (emptyTrashSnapshot, []string, []string, error) {
	output, err := s.organizer.runOliveTinAction(ctx, oliveTinActionRequest{
		ActionID:  "playlist_tracks",
		Arguments: []oliveTinArgument{{Name: "playlistName", Value: "回收站"}},
	})
	if err != nil {
		return emptyTrashSnapshot{}, nil, nil, fmt.Errorf("查询 Navidrome 回收站失败: %w", err)
	}
	output = strings.TrimSpace(output)
	if output == "" || output == "[]" || output == "null" {
		return emptyTrashSnapshot{}, nil, nil, nil
	}
	var rows []emptyTrashRecord
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		return emptyTrashSnapshot{}, nil, nil, fmt.Errorf("解析回收站数据失败: %w", err)
	}
	if len(rows) == 0 {
		return emptyTrashSnapshot{}, nil, nil, nil
	}
	if len(rows) != 1 || (rows[0].PlaylistName != "" && rows[0].PlaylistName != "回收站") {
		return emptyTrashSnapshot{}, nil, nil, errors.New("OliveTin 返回了异常的回收站记录")
	}
	record := rows[0]
	count, err := parseTrashCount(record.TrackCount)
	if err != nil || count < 0 {
		return emptyTrashSnapshot{}, nil, nil, errors.New("回收站歌曲数量无效")
	}
	if count == 0 {
		return emptyTrashSnapshot{PlaylistName: record.PlaylistName, TrackCount: count}, nil, nil, nil
	}
	ids, err := parseTrashStringArray(record.TrackIDs, "track_ids")
	if err != nil {
		return emptyTrashSnapshot{}, nil, nil, err
	}
	paths, err := parseTrashStringArray(record.FilePaths, "file_paths")
	if err != nil {
		return emptyTrashSnapshot{}, nil, nil, err
	}
	if len(ids) != count || len(paths) != count {
		return emptyTrashSnapshot{}, nil, nil, fmt.Errorf("回收站记录数量不一致：track_count=%d, track_ids=%d, file_paths=%d", count, len(ids), len(paths))
	}
	for i, id := range ids {
		if !safeNavidromeID(id) {
			return emptyTrashSnapshot{}, nil, nil, errors.New("回收站包含无效歌曲 ID，已取消删除")
		}
		if err := validateTrashRelativePath(paths[i]); err != nil {
			return emptyTrashSnapshot{}, nil, nil, fmt.Errorf("回收站文件路径无效，已取消删除: %w", err)
		}
	}
	return emptyTrashSnapshot{PlaylistName: record.PlaylistName, TrackCount: count}, ids, paths, nil
}

func parseTrashCount(raw json.RawMessage) (int, error) {
	var count int
	if err := json.Unmarshal(raw, &count); err == nil {
		return count, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, err
	}
	return strconv.Atoi(text)
}

func parseTrashStringArray(raw json.RawMessage, field string) ([]string, error) {
	var nested string
	if err := json.Unmarshal(raw, &nested); err == nil {
		raw = json.RawMessage(nested)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("回收站字段 %s 不是 JSON 数组: %w", field, err)
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		var text string
		if len(value) == 0 || string(value) == "null" || json.Unmarshal(value, &text) != nil || text == "" {
			return nil, fmt.Errorf("回收站字段 %s 含有空值", field)
		}
		result = append(result, text)
	}
	return result, nil
}

func validateTrashRelativePath(value string) error {
	if value == "" || strings.TrimSpace(value) == "" || value == "." || !filepath.IsLocal(value) || filepath.Clean(value) != value || strings.ContainsRune(value, '\\') {
		return errors.New("歌曲路径必须是 music 根目录下的相对路径")
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return errors.New("歌曲路径包含控制字符")
		}
	}
	return nil
}

func (s *emptyTrashService) deleteVectors(ctx context.Context, songIDs []string) error {
	endpoint, err := url.JoinPath(s.pineconeHost, "vectors", "delete")
	if err != nil {
		return fmt.Errorf("构造 Pinecone 删除地址失败: %w", err)
	}
	const batchSize = 100
	for start := 0; start < len(songIDs); start += batchSize {
		end := start + batchSize
		if end > len(songIDs) {
			end = len(songIDs)
		}
		body, err := json.Marshal(map[string]any{
			"namespace": s.namespace,
			"filter":    map[string]any{"id": map[string]any{"$in": songIDs[start:end]}},
		})
		if err != nil {
			return err
		}
		_, status, err := doPlaylistHTTPRequest(ctx, s.httpClient, http.MethodPost, endpoint, body, map[string]string{
			"Content-Type":           "application/json",
			"Api-Key":                s.pineconeKey,
			"X-Pinecone-Api-Version": "2025-04",
		}, "", "")
		if err != nil {
			return fmt.Errorf("删除 Pinecone 向量失败: %w", err)
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("删除 Pinecone 向量返回 HTTP %d", status)
		}
	}
	return nil
}

func formatEmptyTrashResult(result emptyTrashResult) string {
	if result.NoTracks {
		return "✅ 回收站为空"
	}
	if result.PreflightError != nil {
		return "❌ 清空前检查失败，未执行清理：" + formatEmptyTrashError(result.PreflightError)
	}
	if result.VectorError != nil {
		return "❌ Pinecone 向量删除失败（可能已部分完成）；MusicRAG 状态和歌曲文件尚未清理。重新发送 /empty_trash 可安全重试：" + formatEmptyTrashError(result.VectorError)
	}
	if result.StateError != nil {
		return "⚠️ Pinecone 向量已清理，但 MusicRAG 状态表清理失败，歌曲文件未删除。重新发送 /empty_trash 可安全重试：" + formatEmptyTrashError(result.StateError)
	}
	if result.FileError != nil {
		return fmt.Sprintf("⚠️ MusicRAG 状态和 Pinecone 已清理；%s\n重新发送 /empty_trash 可安全重试剩余步骤。", formatEmptyTrashError(result.FileError))
	}
	complete := result.FilesDeleted == result.TrackCount && result.StateRowsClean && result.VectorsDeleted
	if complete {
		return fmt.Sprintf("✅ 清空回收站完成\n🗑 共删除 %d 首歌曲及其 MusicRAG 状态和 Pinecone 向量。", result.TrackCount)
	}
	var output strings.Builder
	fmt.Fprintf(&output, "⚠️ 回收站文件已删除 %d/%d 首，清理尚未完全完成。", result.FilesDeleted, result.TrackCount)
	if result.StateError != nil {
		fmt.Fprintf(&output, "\nMusicRAG 状态表清理失败：%s", formatEmptyTrashError(result.StateError))
	}
	if result.VectorError != nil {
		fmt.Fprintf(&output, "\nPinecone 向量清理失败：%s", formatEmptyTrashError(result.VectorError))
	}
	return output.String()
}

func formatEmptyTrashError(err error) string {
	if err == nil {
		return "未知错误"
	}
	value := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, err.Error())
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 500 {
		value = string(runes[:499]) + "…"
	}
	return value
}

func (app *BotApp) startEmptyTrash(c tb.Context) error {
	if c.Chat() == nil || c.Sender() == nil {
		return c.Send("无法读取 Telegram 会话信息，请在聊天中重新发送 /empty_trash。")
	}
	if app.hasPlaylistAssignmentForChat(c.Chat().ID) || app.hasAIPlaylistSessionForChat(c.Chat().ID) {
		return c.Send("当前聊天已有一条歌单维护会话，请先完成或结束它。")
	}
	if app.hasPendingEmptyTrash(c.Chat().ID) {
		return c.Send("当前已有一条待确认的清空回收站请求，请先确认、取消或等待过期。")
	}
	service, err := newEmptyTrashService(app.cfg)
	if err != nil {
		return c.Send("清空回收站服务尚未配置：" + err.Error())
	}
	app.playlistMaintenanceStartMu.Lock()
	if app.hasPlaylistAssignmentForChat(c.Chat().ID) || app.hasAIPlaylistSessionForChat(c.Chat().ID) || app.hasPendingEmptyTrash(c.Chat().ID) {
		app.playlistMaintenanceStartMu.Unlock()
		service.organizer.client.CloseIdleConnections()
		return c.Send("当前聊天已有一条歌单维护会话，请先完成或结束它。")
	}
	if !app.beginEmptyTrashRun(c.Chat().ID) {
		app.playlistMaintenanceStartMu.Unlock()
		service.organizer.client.CloseIdleConnections()
		return c.Send("当前有其他维护任务正在运行，请稍后重试。")
	}
	message, err := c.Bot().Send(c.Chat(), "⏳ 正在读取回收站内容…")
	if err != nil {
		app.finishEmptyTrashRun(c.Chat().ID)
		app.playlistMaintenanceStartMu.Unlock()
		service.organizer.client.CloseIdleConnections()
		return err
	}
	go app.prepareEmptyTrashPreview(c.Sender().ID, c.Chat().ID, message, service)
	app.playlistMaintenanceStartMu.Unlock()
	return nil
}

func (app *BotApp) prepareEmptyTrashPreview(userID, chatID int64, message *tb.Message, service *emptyTrashService) {
	defer app.finishEmptyTrashRun(chatID)
	defer service.organizer.client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), emptyTrashRunTimeout)
	defer cancel()
	plan, err := service.prepare(ctx)
	if err != nil {
		_, _ = app.bot.Edit(message, formatEmptyTrashResult(emptyTrashResult{PreflightError: err}), &tb.ReplyMarkup{})
		return
	}
	if plan.snapshot.TrackCount == 0 {
		_, _ = app.bot.Edit(message, formatEmptyTrashResult(emptyTrashResult{NoTracks: true}), &tb.ReplyMarkup{})
		return
	}
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	pending := pendingEmptyTrash{
		token:     token,
		userID:    userID,
		chatID:    chatID,
		message:   message,
		service:   service,
		plan:      cloneEmptyTrashPlan(plan),
		expiresAt: time.Now().Add(emptyTrashConfirmationTTL),
	}
	if !app.registerPendingEmptyTrash(pending) {
		_, _ = app.bot.Edit(message, "当前已有一条待确认的清空回收站请求，请先处理它。", &tb.ReplyMarkup{})
		return
	}
	if _, err := app.bot.Edit(message, formatEmptyTrashPreview(plan), emptyTrashConfirmationMarkup(token)); err != nil {
		app.removePendingEmptyTrash(token)
		logger.Warn("Empty trash confirmation preview could not be shown", zap.Error(err))
	}
}

func formatEmptyTrashPreview(plan emptyTrashPlan) string {
	return fmt.Sprintf("⚠️ 清空回收站\n──────────────────\n🗑 待删除：%d 首歌曲\n确认后会删除对应歌曲文件，并清理 MusicRAG 状态与 Pinecone 向量。\n\n请确认是否继续？此请求将在 %d 分钟后过期。", plan.snapshot.TrackCount, int(emptyTrashConfirmationTTL.Minutes()))
}

func emptyTrashConfirmationMarkup(token string) *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	confirm := markup.Data("🗑 确认清空", emptyTrashCallbackUnique, "confirm", token)
	cancel := markup.Data("取消", emptyTrashCallbackUnique, "cancel", token)
	markup.Inline(markup.Row(confirm, cancel))
	return markup
}

func (app *BotApp) HandleEmptyTrashCallback(c tb.Context) error {
	args := c.Args()
	if len(args) != 2 || (args[0] != "confirm" && args[0] != "cancel") {
		return c.Respond(&tb.CallbackResponse{Text: "清空操作无效或已过期", ShowAlert: true})
	}
	pending, ok := app.getPendingEmptyTrash(args[1])
	if !ok {
		return c.Respond(&tb.CallbackResponse{Text: "确认已过期，请重新发送 /empty_trash", ShowAlert: true})
	}
	if c.Sender() == nil || c.Sender().ID != pending.userID {
		return c.Respond(&tb.CallbackResponse{Text: "只有发起请求的人可以确认清空", ShowAlert: true})
	}
	if !emptyTrashCallbackMatches(c.Callback(), pending) {
		return c.Respond(&tb.CallbackResponse{Text: "按钮与请求消息不匹配，请使用原始消息中的按钮", ShowAlert: true})
	}
	if args[0] == "cancel" {
		if _, ok := app.takePendingEmptyTrash(args[1]); !ok {
			return c.Respond(&tb.CallbackResponse{Text: "确认请求已过期或正在处理", ShowAlert: true})
		}
		_ = c.Respond()
		return c.Edit("已取消清空回收站。", &tb.ReplyMarkup{})
	}
	if !app.claimPendingEmptyTrash(args[1]) {
		return c.Respond(&tb.CallbackResponse{Text: "该请求已过期或正在处理", ShowAlert: true})
	}
	if !app.beginEmptyTrashRun(pending.chatID) {
		app.releasePendingEmptyTrash(args[1])
		return c.Respond(&tb.CallbackResponse{Text: "当前有其他维护任务正在运行，请稍后重试。"})
	}
	_ = c.Respond(&tb.CallbackResponse{Text: "已确认，正在复核回收站并开始清理…"})
	if err := c.Edit("⏳ 已确认，正在复核回收站内容…", &tb.ReplyMarkup{}); err != nil {
		app.finishEmptyTrashRun(pending.chatID)
		app.releasePendingEmptyTrash(args[1])
		return err
	}
	app.removePendingEmptyTrash(args[1])
	go app.runConfirmedEmptyTrashTask(pending)
	return nil
}

func emptyTrashCallbackMatches(callback *tb.Callback, pending pendingEmptyTrash) bool {
	return callback != nil && callback.Message != nil && pending.message != nil &&
		callback.Message.ID == pending.message.ID && callback.Message.Chat != nil && callback.Message.Chat.ID == pending.chatID
}

func (app *BotApp) runConfirmedEmptyTrashTask(pending pendingEmptyTrash) {
	defer app.finishEmptyTrashRun(pending.chatID)
	defer pending.service.organizer.client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), emptyTrashRunTimeout)
	defer cancel()
	result := pending.service.runConfirmed(ctx, pending.plan, func(text string) {
		if _, err := app.bot.Edit(pending.message, text); err != nil {
			logger.Debug("empty trash progress update failed", zap.Error(err))
		}
	})
	app.logEmptyTrashWarning(result)
	_, _ = app.bot.Edit(pending.message, formatEmptyTrashResult(result), &tb.ReplyMarkup{})
}

func cloneEmptyTrashPlan(plan emptyTrashPlan) emptyTrashPlan {
	plan.ids = append([]string(nil), plan.ids...)
	plan.paths = append([]string(nil), plan.paths...)
	return plan
}

func sameEmptyTrashPlan(first, second emptyTrashPlan) bool {
	if first.snapshot.TrackCount != second.snapshot.TrackCount ||
		len(first.ids) != len(first.paths) || len(second.ids) != len(second.paths) ||
		len(first.ids) != len(second.ids) || len(first.paths) != len(second.paths) {
		return false
	}
	tracks := make(map[string]int, len(first.ids))
	for index, id := range first.ids {
		tracks[id+"\x00"+first.paths[index]]++
	}
	for index, id := range second.ids {
		key := id + "\x00" + second.paths[index]
		if tracks[key] == 0 {
			return false
		}
		tracks[key]--
	}
	for _, count := range tracks {
		if count != 0 {
			return false
		}
	}
	return true
}

func (app *BotApp) hasPendingEmptyTrash(chatID int64) bool {
	app.emptyTrashMu.Lock()
	defer app.emptyTrashMu.Unlock()
	app.purgePendingEmptyTrashLocked(time.Now())
	if _, running := app.emptyTrashRunning[chatID]; running {
		return true
	}
	for _, pending := range app.emptyTrashPending {
		if pending.chatID == chatID {
			return true
		}
	}
	return false
}

func (app *BotApp) registerPendingEmptyTrash(pending pendingEmptyTrash) bool {
	app.emptyTrashMu.Lock()
	defer app.emptyTrashMu.Unlock()
	app.purgePendingEmptyTrashLocked(time.Now())
	for _, existing := range app.emptyTrashPending {
		if existing.chatID == pending.chatID {
			return false
		}
	}
	if app.emptyTrashPending == nil {
		app.emptyTrashPending = make(map[string]pendingEmptyTrash)
	}
	app.emptyTrashPending[pending.token] = pending
	return true
}

func (app *BotApp) getPendingEmptyTrash(token string) (pendingEmptyTrash, bool) {
	app.emptyTrashMu.Lock()
	defer app.emptyTrashMu.Unlock()
	app.purgePendingEmptyTrashLocked(time.Now())
	pending, ok := app.emptyTrashPending[token]
	return pending, ok
}

func (app *BotApp) claimPendingEmptyTrash(token string) bool {
	app.emptyTrashMu.Lock()
	defer app.emptyTrashMu.Unlock()
	app.purgePendingEmptyTrashLocked(time.Now())
	pending, ok := app.emptyTrashPending[token]
	if !ok || pending.processing {
		return false
	}
	pending.processing = true
	app.emptyTrashPending[token] = pending
	return true
}

func (app *BotApp) releasePendingEmptyTrash(token string) {
	app.emptyTrashMu.Lock()
	defer app.emptyTrashMu.Unlock()
	if pending, ok := app.emptyTrashPending[token]; ok {
		pending.processing = false
		app.emptyTrashPending[token] = pending
	}
}

func (app *BotApp) takePendingEmptyTrash(token string) (pendingEmptyTrash, bool) {
	app.emptyTrashMu.Lock()
	defer app.emptyTrashMu.Unlock()
	app.purgePendingEmptyTrashLocked(time.Now())
	pending, ok := app.emptyTrashPending[token]
	if !ok || pending.processing {
		return pendingEmptyTrash{}, false
	}
	delete(app.emptyTrashPending, token)
	return pending, true
}

func (app *BotApp) removePendingEmptyTrash(token string) {
	app.emptyTrashMu.Lock()
	delete(app.emptyTrashPending, token)
	app.emptyTrashMu.Unlock()
}

func (app *BotApp) purgePendingEmptyTrashLocked(now time.Time) {
	for token, pending := range app.emptyTrashPending {
		if !now.Before(pending.expiresAt) && !pending.processing {
			delete(app.emptyTrashPending, token)
		}
	}
}

func (app *BotApp) finishEmptyTrashRun(chatID int64) {
	app.emptyTrashMu.Lock()
	delete(app.emptyTrashRunning, chatID)
	app.emptyTrashMu.Unlock()
}

func (app *BotApp) beginEmptyTrashRun(chatID int64) bool {
	app.aiPlaylistMu.Lock()
	defer app.aiPlaylistMu.Unlock()
	app.emptyTrashMu.Lock()
	defer app.emptyTrashMu.Unlock()
	if len(app.aiPlaylistRunning) != 0 || len(app.emptyTrashRunning) != 0 || len(app.playlistAssignmentRunning) != 0 {
		return false
	}
	if app.emptyTrashRunning == nil {
		app.emptyTrashRunning = make(map[int64]struct{})
	}
	app.emptyTrashRunning[chatID] = struct{}{}
	return true
}

func (app *BotApp) logEmptyTrashWarning(result emptyTrashResult) {
	if logger == nil {
		return
	}
	if result.StateError != nil {
		logger.Warn("Empty trash MusicRAG state cleanup incomplete", zap.Error(result.StateError))
	}
	if result.VectorError != nil {
		logger.Error("Empty trash Pinecone cleanup failed", zap.Error(result.VectorError))
	}
}
