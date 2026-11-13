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

	"github.com/nichuanfang/gymdl/config"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v4"
)

const emptyTrashRunTimeout = 10 * time.Minute

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
	snapshot, ids, paths, err := s.readTrashPlaylist(ctx)
	if err != nil {
		result.PreflightError = err
		return result
	}
	if snapshot.TrackCount == 0 {
		result.NoTracks = true
		return result
	}
	result.TrackCount = snapshot.TrackCount
	if err := s.stateTable.deleteRows(ctx, ids, true); err != nil {
		result.PreflightError = fmt.Errorf("MusicRAG 状态表删除权限检查失败: %w", err)
		return result
	}
	if err := s.checkPinecone(ctx); err != nil {
		result.PreflightError = fmt.Errorf("Pinecone 连接检查失败: %w", err)
		return result
	}

	if progress != nil {
		progress("🤖 清空回收站 · 正在清理 MusicRAG 状态记录…")
	}
	if err := s.stateTable.deleteRows(ctx, ids, false); err != nil {
		result.StateError = err
	} else {
		result.StateRowsClean = true
	}
	if result.StateError != nil {
		return result
	}

	if progress != nil {
		progress("🤖 清空回收站 · 正在删除 Pinecone 向量…")
	}
	if err := s.deleteVectors(ctx, ids); err != nil {
		result.VectorError = err
	} else {
		result.VectorsDeleted = true
	}
	if result.StateError != nil || result.VectorError != nil {
		return result
	}

	for i, filePath := range paths {
		if err := ctx.Err(); err != nil {
			result.FileError = fmt.Errorf("清空任务超时，已删除 %d/%d 个文件: %w", result.FilesDeleted, len(paths), err)
			return result
		}
		if progress != nil && (i == 0 || (i+1)%10 == 0 || i+1 == len(paths)) {
			progress(fmt.Sprintf("🤖 清空回收站 · 正在删除歌曲文件 %d/%d…", i+1, len(paths)))
		}
		_, err := s.organizer.runOliveTinAction(ctx, oliveTinActionRequest{
			ActionID:  "delete_music_file",
			Arguments: []oliveTinArgument{{Name: "filePath", Value: filePath}},
		})
		if err != nil {
			result.FileError = fmt.Errorf("删除回收站文件失败，已删除 %d/%d 个文件: %w", result.FilesDeleted, len(paths), err)
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
		return "❌ 清空前检查失败，未执行清理：" + result.PreflightError.Error()
	}
	if result.StateError != nil {
		return "❌ MusicRAG 状态表清理失败，歌曲文件未删除：" + result.StateError.Error()
	}
	if result.VectorError != nil {
		return "❌ Pinecone 向量清理失败，歌曲文件未删除：" + result.VectorError.Error()
	}
	if result.FileError != nil {
		return fmt.Sprintf("⚠️ MusicRAG 状态和 Pinecone 已清理；%s", result.FileError)
	}
	complete := result.FilesDeleted == result.TrackCount && result.StateRowsClean && result.VectorsDeleted
	if complete {
		return fmt.Sprintf("✅ 清空回收站完成\n🗑 共删除 %d 首歌曲及其 MusicRAG 状态和 Pinecone 向量。", result.TrackCount)
	}
	var output strings.Builder
	fmt.Fprintf(&output, "⚠️ 回收站文件已删除 %d/%d 首，清理尚未完全完成。", result.FilesDeleted, result.TrackCount)
	if result.StateError != nil {
		fmt.Fprintf(&output, "\nMusicRAG 状态表清理失败：%v", result.StateError)
	}
	if result.VectorError != nil {
		fmt.Fprintf(&output, "\nPinecone 向量清理失败：%v", result.VectorError)
	}
	return output.String()
}

func (app *BotApp) startEmptyTrash(c tb.Context) error {
	if c.Chat() == nil || c.Sender() == nil {
		return c.Send("无法读取 Telegram 会话信息，请在聊天中重新发送 /empty_trash。")
	}
	service, err := newEmptyTrashService(app.cfg)
	if err != nil {
		return c.Send("清空回收站服务尚未配置：" + err.Error())
	}
	chatID := c.Chat().ID
	if !app.beginEmptyTrashRun(chatID) {
		return c.Send("当前已有一个清空回收站任务正在运行，请稍后再试。")
	}

	message, err := c.Bot().Send(c.Chat(), "🚀 清空回收站任务已启动…")
	if err != nil {
		app.finishEmptyTrashRun(chatID)
		return err
	}
	go app.runEmptyTrashTask(chatID, message, service)
	return nil
}

func (app *BotApp) runEmptyTrashTask(chatID int64, message *tb.Message, service *emptyTrashService) {
	defer app.finishEmptyTrashRun(chatID)
	ctx, cancel := context.WithTimeout(context.Background(), emptyTrashRunTimeout)
	defer cancel()
	result := service.run(ctx, func(text string) {
		if _, err := app.bot.Edit(message, text); err != nil {
			logger.Debug("empty trash progress update failed", zap.Error(err))
		}
	})
	app.logEmptyTrashWarning(result)
	_, _ = app.bot.Edit(message, formatEmptyTrashResult(result))
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
	if len(app.aiPlaylistRunning) != 0 || len(app.emptyTrashRunning) != 0 {
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
