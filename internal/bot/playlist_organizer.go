package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/utils"
	"github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v4"
)

const (
	playlistAssignmentCallbackUnique = "playlist_assign"
	playlistAssignmentTTL            = 20 * time.Minute
	playlistAssignmentTimeout        = 90 * time.Second
	playlistOrganizerHTTPTimeout     = 35 * time.Second
	maxPlaylistOptions               = 100
)

var (
	playlistAssignmentCallbackEndpoint = tb.InlineButton{Unique: playlistAssignmentCallbackUnique}
	errNoPendingPlaylistSong           = errors.New("no unassigned songs")
	errMusicRAGNotConfigured           = errors.New("music_rag bridge is not configured")
)

type pendingPlaylistAssignment struct {
	token      string
	userID     int64
	chatID     int64
	message    *tb.Message
	song       unplaylistSong
	playlists  []config.PlaylistInfo
	selected   map[string]bool
	statusNote string
	expiresAt  time.Time
	processing bool
	addedAny   bool
}

type unplaylistSong struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Artist         string `json:"artist"`
	Album          string `json:"album"`
	Year           int    `json:"year"`
	Comment        string `json:"comment"`
	CreatedAt      string `json:"created_at"`
	Playlists      string `json:"playlists"`
	RemainingCount int    `json:"remaining_counts"`
}

type oliveTinArgument struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type oliveTinActionRequest struct {
	ActionID  string             `json:"actionId"`
	Arguments []oliveTinArgument `json:"arguments,omitempty"`
}

type oliveTinActionResponse struct {
	LogEntry struct {
		ExitCode *int   `json:"exitCode"`
		Output   string `json:"output"`
		Stdout   string `json:"stdout"`
		TimedOut bool   `json:"timedOut"`
	} `json:"logEntry"`
}

type playlistOrganizerService struct {
	config *config.PlaylistOrganizerConfig
	client *http.Client
}

type navidromePlaylist struct {
	ID   string `json:"playlist_id"`
	Name string `json:"playlist_name"`
}

type musicRAGSink struct {
	baseURL string
	apiKey  string
	tableID string
	client  *http.Client
}

func newPlaylistOrganizerService(cfg *config.PlaylistOrganizerConfig) (*playlistOrganizerService, error) {
	if cfg == nil {
		return nil, errors.New("歌单整理服务未配置")
	}
	for name, raw := range map[string]string{
		"OliveTin":  cfg.OliveTinURL,
		"Navidrome": cfg.NavidromeURL,
	} {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("%s 地址无效", name)
		}
	}
	if (strings.TrimSpace(cfg.OliveTinUsername) == "") != (strings.TrimSpace(cfg.OliveTinPassword) == "") {
		return nil, errors.New("OliveTin Basic Auth 用户名和密码必须同时配置")
	}
	var transport http.RoundTripper
	if shared := utils.SharedHTTPClient(); shared != nil {
		transport = shared.Transport
	}
	return &playlistOrganizerService{
		config: cfg,
		client: &http.Client{Transport: transport, Timeout: playlistOrganizerHTTPTimeout},
	}, nil
}

func (s *playlistOrganizerService) requireNavidromeCredentials() error {
	if s == nil || s.config == nil || strings.TrimSpace(s.config.NavidromeUsername) == "" || strings.TrimSpace(s.config.NavidromePassword) == "" {
		return errors.New("请配置 playlist_organizer.navidrome_username 和 navidrome_password")
	}
	return nil
}

func (s *playlistOrganizerService) firstUnassignedSong(ctx context.Context) (unplaylistSong, error) {
	output, err := s.runOliveTinAction(ctx, oliveTinActionRequest{ActionID: "songs_unplaylist"})
	if err != nil {
		return unplaylistSong{}, err
	}
	output = strings.TrimSpace(output)
	if output == "" || output == "[]" || output == "null" {
		return unplaylistSong{}, errNoPendingPlaylistSong
	}
	var songs []unplaylistSong
	if strings.HasPrefix(output, "[") {
		if err := json.Unmarshal([]byte(output), &songs); err != nil {
			return unplaylistSong{}, fmt.Errorf("解析待整理歌曲失败: %w", err)
		}
	} else {
		var song unplaylistSong
		if err := json.Unmarshal([]byte(output), &song); err != nil {
			return unplaylistSong{}, fmt.Errorf("解析待整理歌曲失败: %w", err)
		}
		songs = []unplaylistSong{song}
	}
	if len(songs) == 0 {
		return unplaylistSong{}, errNoPendingPlaylistSong
	}
	if !safeNavidromeID(songs[0].ID) {
		return unplaylistSong{}, errors.New("Navidrome 返回了无效歌曲 ID")
	}
	return songs[0], nil
}

func (s *playlistOrganizerService) playlistsWithoutSong(ctx context.Context, songID string, playlists []string) ([]navidromePlaylist, error) {
	if !safeNavidromeID(songID) {
		return nil, errors.New("歌曲 ID 无效")
	}
	if len(playlists) == 0 {
		return nil, nil
	}
	for _, name := range playlists {
		if !safeOliveTinPlaylistName(name) {
			return nil, fmt.Errorf("歌单名称 %q 含有 OliveTin 查询暂不支持的字符", name)
		}
	}
	output, err := s.runOliveTinAction(ctx, oliveTinActionRequest{
		ActionID: "playlists_without_track",
		Arguments: []oliveTinArgument{
			{Name: "songId", Value: songID},
			{Name: "playlists", Value: strings.Join(playlists, ",")},
		},
	})
	if err != nil {
		return nil, err
	}
	output = strings.TrimSpace(output)
	if output == "" || output == "[]" || output == "null" {
		return nil, nil
	}
	var result []navidromePlaylist
	if strings.HasPrefix(output, "[") {
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			return nil, fmt.Errorf("解析歌单查询结果失败: %w", err)
		}
	} else {
		var one navidromePlaylist
		if err := json.Unmarshal([]byte(output), &one); err != nil {
			return nil, fmt.Errorf("解析歌单查询结果失败: %w", err)
		}
		result = []navidromePlaylist{one}
	}
	selected := make(map[string]struct{}, len(playlists))
	for _, name := range playlists {
		selected[name] = struct{}{}
	}
	filtered := make([]navidromePlaylist, 0, len(result))
	seen := make(map[string]struct{}, len(result))
	for _, playlist := range result {
		if playlist.ID == "" || !safeNavidromeID(playlist.ID) {
			return nil, errors.New("Navidrome 返回了无效歌单 ID")
		}
		if _, ok := selected[playlist.Name]; !ok {
			continue
		}
		if _, duplicate := seen[playlist.ID]; duplicate {
			continue
		}
		seen[playlist.ID] = struct{}{}
		filtered = append(filtered, playlist)
	}
	return filtered, nil
}

func (s *playlistOrganizerService) aiPlaylists(ctx context.Context) ([]aiPlaylist, error) {
	output, err := s.runOliveTinAction(ctx, oliveTinActionRequest{ActionID: "ai_playlists"})
	if err != nil {
		return nil, err
	}
	output = strings.TrimSpace(output)
	if output == "" || output == "[]" || output == "null" {
		return nil, nil
	}
	var playlists []aiPlaylist
	if err := json.Unmarshal([]byte(output), &playlists); err != nil {
		return nil, fmt.Errorf("解析 AI 歌单列表失败: %w", err)
	}
	seen := make(map[string]struct{}, len(playlists))
	filtered := make([]aiPlaylist, 0, len(playlists))
	for _, playlist := range playlists {
		if !strings.HasPrefix(playlist.Name, "[AI]") {
			continue
		}
		if !safeNavidromeID(playlist.ID) || playlist.SongCount < 0 {
			return nil, errors.New("OliveTin 返回了无效 AI 歌单记录")
		}
		if _, duplicate := seen[playlist.ID]; duplicate {
			return nil, errors.New("OliveTin 返回了重复的 AI 歌单 ID")
		}
		seen[playlist.ID] = struct{}{}
		filtered = append(filtered, playlist)
	}
	return filtered, nil
}

func (s *playlistOrganizerService) missingSongIDs(ctx context.Context, songIDs []string) (map[string]struct{}, error) {
	unique := make([]string, 0, len(songIDs))
	requested := make(map[string]struct{}, len(songIDs))
	for _, songID := range songIDs {
		if !safeNavidromeID(songID) {
			return nil, errors.New("候选歌曲包含无效 Navidrome ID")
		}
		if _, exists := requested[songID]; exists {
			continue
		}
		requested[songID] = struct{}{}
		unique = append(unique, songID)
	}
	if len(unique) == 0 {
		return map[string]struct{}{}, nil
	}
	output, err := s.runOliveTinAction(ctx, oliveTinActionRequest{
		ActionID:  "check_missing_tracks",
		Arguments: []oliveTinArgument{{Name: "songIds", Value: strings.Join(unique, ",")}},
	})
	if err != nil {
		return nil, err
	}
	output = strings.TrimSpace(output)
	if output == "" || output == "[]" || output == "null" {
		return map[string]struct{}{}, nil
	}
	var missingRows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(output), &missingRows); err != nil {
		return nil, fmt.Errorf("解析失效歌曲 ID 失败: %w", err)
	}
	missing := make(map[string]struct{}, len(missingRows))
	for _, row := range missingRows {
		if !safeNavidromeID(row.ID) {
			return nil, errors.New("OliveTin 返回了无效失效歌曲 ID")
		}
		if _, exists := requested[row.ID]; !exists {
			return nil, errors.New("OliveTin 返回了不属于本次查询的歌曲 ID")
		}
		missing[row.ID] = struct{}{}
	}
	return missing, nil
}

func (s *playlistOrganizerService) addSongsToAIPlaylist(ctx context.Context, playlistID string, songIDs []string) error {
	if !safeNavidromeID(playlistID) {
		return errors.New("AI 歌单 ID 无效")
	}
	if len(songIDs) == 0 {
		return errors.New("AI 歌单没有可写入的歌曲")
	}
	seen := make(map[string]struct{}, len(songIDs))
	unique := make([]string, 0, len(songIDs))
	for _, songID := range songIDs {
		if !safeNavidromeID(songID) {
			return errors.New("AI 歌单包含无效歌曲 ID")
		}
		if _, exists := seen[songID]; exists {
			continue
		}
		seen[songID] = struct{}{}
		unique = append(unique, songID)
	}
	token, err := s.navidromeLogin(ctx)
	if err != nil {
		return err
	}
	return s.addSongsToPlaylist(ctx, token, playlistID, unique)
}

func (s *playlistOrganizerService) runOliveTinAction(ctx context.Context, action oliveTinActionRequest) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(s.config.OliveTinURL), "/")
	endpoint, err := url.JoinPath(base, "api", "StartActionAndWait")
	if err != nil {
		return "", fmt.Errorf("构造 OliveTin 请求地址失败: %w", err)
	}
	body, err := json.Marshal(action)
	if err != nil {
		return "", fmt.Errorf("编码 OliveTin 请求失败: %w", err)
	}
	responseBody, status, err := doPlaylistHTTPRequest(ctx, s.client, http.MethodPost, endpoint, body, map[string]string{"Content-Type": "application/json"}, s.config.OliveTinUsername, s.config.OliveTinPassword)
	if err != nil {
		return "", fmt.Errorf("调用 OliveTin 失败: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("OliveTin 返回 HTTP %d", status)
	}
	var response oliveTinActionResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return "", fmt.Errorf("解析 OliveTin 响应失败: %w", err)
	}
	if response.LogEntry.ExitCode == nil || *response.LogEntry.ExitCode != 0 || response.LogEntry.TimedOut {
		return "", fmt.Errorf("OliveTin action 失败（exitCode=%v, timedOut=%t）", response.LogEntry.ExitCode, response.LogEntry.TimedOut)
	}
	if response.LogEntry.Output != "" {
		return response.LogEntry.Output, nil
	}
	return response.LogEntry.Stdout, nil
}

func (s *playlistOrganizerService) navidromeLogin(ctx context.Context) (string, error) {
	if err := s.requireNavidromeCredentials(); err != nil {
		return "", err
	}
	base := strings.TrimRight(strings.TrimSpace(s.config.NavidromeURL), "/")
	endpoint, err := url.JoinPath(base, "auth", "login")
	if err != nil {
		return "", fmt.Errorf("构造 Navidrome 登录地址失败: %w", err)
	}
	body, err := json.Marshal(map[string]string{
		"username": s.config.NavidromeUsername,
		"password": s.config.NavidromePassword,
	})
	if err != nil {
		return "", err
	}
	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
		"Origin":       strings.TrimRight(base, "/"),
		"Referer":      strings.TrimRight(base, "/") + "/app/",
		"User-Agent":   "gymdl/playlist-organizer",
	}
	responseBody, status, err := doPlaylistHTTPRequest(ctx, s.client, http.MethodPost, endpoint, body, headers, "", "")
	if err != nil {
		return "", fmt.Errorf("Navidrome 登录请求失败: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("Navidrome 登录返回 HTTP %d", status)
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return "", fmt.Errorf("解析 Navidrome 登录响应失败: %w", err)
	}
	if strings.TrimSpace(response.Token) == "" {
		return "", errors.New("Navidrome 登录响应缺少 token")
	}
	return response.Token, nil
}

func (s *playlistOrganizerService) addSongToPlaylist(ctx context.Context, token, songID string, playlist navidromePlaylist) error {
	return s.addSongsToPlaylist(ctx, token, playlist.ID, []string{songID})
}

func (s *playlistOrganizerService) addSongsToPlaylist(ctx context.Context, token, playlistID string, songIDs []string) error {
	if !safeNavidromeID(playlistID) || len(songIDs) == 0 {
		return errors.New("歌曲或歌单 ID 无效")
	}
	for _, songID := range songIDs {
		if !safeNavidromeID(songID) {
			return errors.New("歌曲或歌单 ID 无效")
		}
	}
	base := strings.TrimRight(strings.TrimSpace(s.config.NavidromeURL), "/")
	endpoint, err := url.JoinPath(base, "api", "playlist", playlistID, "tracks")
	if err != nil {
		return fmt.Errorf("构造 Navidrome 歌单地址失败: %w", err)
	}
	body, err := json.Marshal(map[string][]string{"ids": songIDs})
	if err != nil {
		return err
	}
	headers := map[string]string{
		"Content-Type":          "application/json",
		"Accept":                "application/json",
		"User-Agent":            "gymdl/playlist-organizer",
		"X-Nd-Client-Unique-Id": uuid.NewString(),
		"X-Nd-Authorization":    "Bearer " + token,
	}
	responseBody, status, err := doPlaylistHTTPRequest(ctx, s.client, http.MethodPost, endpoint, body, headers, "", "")
	if err != nil {
		return fmt.Errorf("添加到歌单 %q 失败: %w", playlistID, err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("添加到歌单 %q 返回 HTTP %d: %s", playlistID, status, safeResponseSummary(responseBody))
	}
	return nil
}

func (s *playlistOrganizerService) addSongToPlaylists(ctx context.Context, songID string, names []string) (bool, error) {
	if len(names) == 0 {
		return false, errors.New("至少需要选择一个歌单")
	}
	missing, err := s.playlistsWithoutSong(ctx, songID, names)
	if err != nil || len(missing) == 0 {
		return false, err
	}
	token, err := s.navidromeLogin(ctx)
	if err != nil {
		return false, err
	}
	addedAny := false
	for _, playlist := range missing {
		if err := s.addSongToPlaylist(ctx, token, songID, playlist); err != nil {
			return addedAny, err
		}
		addedAny = true
	}
	remaining, err := s.playlistsWithoutSong(ctx, songID, names)
	if err != nil {
		return addedAny, err
	}
	if len(remaining) > 0 {
		return addedAny, fmt.Errorf("Navidrome still reports %d selected playlists without the song", len(remaining))
	}
	return addedAny, nil
}

func newMusicRAGSink(n8n *config.N8NConfig, client *http.Client) *musicRAGSink {
	if client == nil {
		var transport http.RoundTripper
		if shared := utils.SharedHTTPClient(); shared != nil {
			transport = shared.Transport
		}
		client = &http.Client{Transport: transport, Timeout: playlistOrganizerHTTPTimeout}
	}
	if n8n == nil {
		return &musicRAGSink{client: client}
	}
	return &musicRAGSink{
		baseURL: strings.TrimSpace(n8n.N8NBaseUrl),
		apiKey:  strings.TrimSpace(n8n.APIKey),
		tableID: strings.TrimSpace(n8n.MusicRAGTableID),
		client:  client,
	}
}

func (s *musicRAGSink) configured() bool {
	return s != nil && strings.TrimSpace(s.baseURL) != "" && strings.TrimSpace(s.apiKey) != "" && strings.TrimSpace(s.tableID) != ""
}

func (s *musicRAGSink) enqueue(ctx context.Context, song unplaylistSong) error {
	if s.apiKey == "" && s.tableID == "" && s.baseURL == "" {
		return errMusicRAGNotConfigured
	}
	if s.apiKey == "" || s.tableID == "" || s.baseURL == "" {
		return errors.New("n8n MusicRAG bridge configuration is incomplete")
	}
	if !safeNavidromeID(s.tableID) {
		return errors.New("n8n MusicRAG table ID is invalid")
	}
	base, err := parseBaseURL(s.baseURL)
	if err != nil {
		return fmt.Errorf("n8n base URL is invalid: %w", err)
	}
	endpoint, err := url.JoinPath(base.String(), "api", "v1", "data-tables", s.tableID, "rows")
	if err != nil {
		return fmt.Errorf("construct n8n MusicRAG URL: %w", err)
	}
	filter, err := json.Marshal(map[string]any{
		"type": "and",
		"filters": []map[string]string{{
			"columnName": "music_id",
			"condition":  "eq",
			"value":      song.ID,
		}},
	})
	if err != nil {
		return err
	}
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	query := parsedEndpoint.Query()
	query.Set("limit", "1")
	query.Set("filter", string(filter))
	parsedEndpoint.RawQuery = query.Encode()
	headers := map[string]string{"X-N8N-API-KEY": s.apiKey, "Accept": "application/json"}
	responseBody, status, err := doPlaylistHTTPRequest(ctx, s.client, http.MethodGet, parsedEndpoint.String(), nil, headers, "", "")
	if err != nil {
		return fmt.Errorf("query n8n MusicRAG row: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("query n8n MusicRAG row returned HTTP %d", status)
	}
	rows, err := parseN8NDataTableRows(responseBody)
	if err != nil {
		return fmt.Errorf("parse n8n MusicRAG rows: %w", err)
	}
	if len(rows) > 0 {
		return nil
	}
	row := map[string]string{
		"music_id":         song.ID,
		"title":            song.Title,
		"artist":           song.Artist,
		"album":            song.Album,
		"year":             strconv.Itoa(song.Year),
		"music_created_at": song.CreatedAt,
		"status":           "待处理",
		"url":              song.Comment,
	}
	body, err := json.Marshal(map[string]any{"returnType": "all", "data": []map[string]string{row}})
	if err != nil {
		return err
	}
	responseBody, status, err = doPlaylistHTTPRequest(ctx, s.client, http.MethodPost, endpoint, body, map[string]string{
		"X-N8N-API-KEY": s.apiKey,
		"Accept":        "application/json",
		"Content-Type":  "application/json",
	}, "", "")
	if err != nil {
		return fmt.Errorf("insert n8n MusicRAG row: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("insert n8n MusicRAG row returned HTTP %d", status)
	}
	_ = responseBody
	return nil
}

func parseN8NDataTableRows(body []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errors.New("empty response")
	}
	if trimmed[0] == '[' {
		var rows []json.RawMessage
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, err
		}
		return rows, nil
	}

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return nil, err
	}
	data := bytes.TrimSpace(envelope.Data)
	if len(data) == 0 {
		return nil, errors.New("response is missing data")
	}
	if data[0] != '[' {
		return nil, errors.New("response data is not an array")
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *musicRAGSink) deleteRows(ctx context.Context, songIDs []string, dryRun bool) error {
	if len(songIDs) == 0 {
		return nil
	}
	if s.apiKey == "" && s.tableID == "" && s.baseURL == "" {
		return errMusicRAGNotConfigured
	}
	if s.apiKey == "" || s.tableID == "" || s.baseURL == "" {
		return errors.New("n8n MusicRAG bridge configuration is incomplete")
	}
	if !safeNavidromeID(s.tableID) {
		return errors.New("n8n MusicRAG table ID is invalid")
	}
	base, err := parseBaseURL(s.baseURL)
	if err != nil {
		return fmt.Errorf("n8n base URL is invalid: %w", err)
	}
	endpoint, err := url.JoinPath(base.String(), "api", "v1", "data-tables", s.tableID, "rows", "delete")
	if err != nil {
		return fmt.Errorf("construct n8n MusicRAG delete URL: %w", err)
	}
	unique := make([]string, 0, len(songIDs))
	seen := make(map[string]struct{}, len(songIDs))
	for _, songID := range songIDs {
		if !safeNavidromeID(songID) {
			return errors.New("n8n MusicRAG delete contains an invalid song ID")
		}
		if _, exists := seen[songID]; exists {
			continue
		}
		seen[songID] = struct{}{}
		unique = append(unique, songID)
	}
	const batchSize = 40
	for start := 0; start < len(unique); start += batchSize {
		end := start + batchSize
		if end > len(unique) {
			end = len(unique)
		}
		filters := make([]map[string]string, 0, end-start)
		for _, songID := range unique[start:end] {
			filters = append(filters, map[string]string{
				"columnName": "music_id",
				"condition":  "eq",
				"value":      songID,
			})
		}
		filter, err := json.Marshal(map[string]any{"type": "or", "filters": filters})
		if err != nil {
			return err
		}
		parsedEndpoint, err := url.Parse(endpoint)
		if err != nil {
			return err
		}
		query := parsedEndpoint.Query()
		query.Set("filter", string(filter))
		if dryRun {
			query.Set("dryRun", "true")
		}
		parsedEndpoint.RawQuery = query.Encode()
		_, status, err := doPlaylistHTTPRequest(ctx, s.client, http.MethodDelete, parsedEndpoint.String(), nil, map[string]string{
			"X-N8N-API-KEY": s.apiKey,
			"Accept":        "application/json",
		}, "", "")
		if err != nil {
			return fmt.Errorf("delete n8n MusicRAG rows: %w", err)
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("delete n8n MusicRAG rows returned HTTP %d", status)
		}
	}
	return nil
}

func doPlaylistHTTPRequest(ctx context.Context, client *http.Client, method, target string, body []byte, headers map[string]string, basicUser, basicPassword string) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, 0, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if basicUser != "" || basicPassword != "" {
		req.SetBasicAuth(basicUser, basicPassword)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	const maxBody = 2 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(data) > maxBody {
		return nil, resp.StatusCode, errors.New("response body exceeds 2 MiB")
	}
	return data, resp.StatusCode, nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("expected an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	return parsed, nil
}

func safeResponseSummary(body []byte) string {
	value := strings.TrimSpace(string(body))
	if len(value) > 160 {
		value = value[:160] + "…"
	}
	return value
}

func safeNavidromeID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func safeOliveTinPlaylistName(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	for _, char := range value {
		if char == '\'' || char == ',' || char == '\n' || char == '\r' || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func normalizePlaylistOptions(entries []config.PlaylistInfo) ([]config.PlaylistInfo, []string) {
	options := make([]config.PlaylistInfo, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	skipped := make([]string, 0)
	for _, entry := range entries {
		entry.Name = strings.TrimSpace(entry.Name)
		if entry.Name == "" {
			continue
		}
		if !safeOliveTinPlaylistName(entry.Name) {
			skipped = append(skipped, entry.Name)
			continue
		}
		if _, exists := seen[entry.Name]; exists {
			continue
		}
		seen[entry.Name] = struct{}{}
		options = append(options, entry)
	}
	return options, skipped
}

func parsePlaylistSuggestions(output string, options []config.PlaylistInfo) ([]string, bool) {
	output = strings.TrimSpace(output)
	if output == "" || output == "未知" {
		return nil, false
	}
	available := make(map[string]struct{}, len(options))
	for _, option := range options {
		available[option.Name] = struct{}{}
	}
	parts := strings.FieldsFunc(output, func(char rune) bool {
		return char == ',' || char == '，' || char == '、' || char == '\n' || char == '\r'
	})
	if len(parts) == 0 {
		return nil, false
	}
	selected := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" || name == "未知" {
			return nil, false
		}
		if _, exists := available[name]; !exists {
			return nil, false
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		selected = append(selected, name)
	}
	return selected, len(selected) > 0
}

func splitPlaylistNames(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result
}

func playlistClassifierPrompt(song unplaylistSong, options []config.PlaylistInfo) string {
	var prompt strings.Builder
	prompt.WriteString("请为下面这首歌选择最合适的歌单。只能使用可选列表中的名称，可多选；无法判断或没有合适歌单时只输出“未知”。只输出歌单名，以半角逗号分隔，不要解释。\n\n")
	fmt.Fprintf(&prompt, "歌名：%s\n歌手：%s\n专辑：%s\n年份：%d\n同歌手已有歌单：%s\n\n可选歌单：\n", song.Title, song.Artist, song.Album, song.Year, strings.Join(splitPlaylistNames(song.Playlists), "、"))
	for _, option := range options {
		fmt.Fprintf(&prompt, "- %s：%s\n", option.Name, strings.TrimSpace(option.Desc))
	}
	return prompt.String()
}

func playlistAssignmentUsesAI(playlistAssist bool, song unplaylistSong) bool {
	return playlistAssist && len(splitPlaylistNames(song.Playlists)) == 0
}

func (app *BotApp) startPlaylistAssignment(c tb.Context) error {
	if app.cfg == nil {
		return c.Send("歌单整理配置不可用。")
	}
	if c.Chat() == nil || c.Sender() == nil {
		return c.Send("无法读取 Telegram 会话信息，请在聊天中重新发送 /assign_playlist。")
	}
	organizer, err := newPlaylistOrganizerService(app.cfg.PlaylistOrganizer)
	if err != nil {
		return c.Send("歌单整理服务尚未配置，请填写 playlist_organizer 中的 OliveTin 和 Navidrome 连接信息。")
	}
	if err := organizer.requireNavidromeCredentials(); err != nil {
		return c.Send("歌单整理服务尚未配置：" + err.Error())
	}
	var entries []config.PlaylistInfo
	playlistAssist := false
	if app.cfg.PlaylistOrganizer != nil {
		entries = app.cfg.PlaylistOrganizer.TidyPlaylist
		playlistAssist = app.cfg.PlaylistOrganizer.PlaylistAssist
	}
	options, skipped := normalizePlaylistOptions(entries)
	if len(options) == 0 {
		return c.Send("没有可用于整理的歌单，请检查 config.playlists.yaml。")
	}
	if len(options) > maxPlaylistOptions {
		return c.Send("可选歌单超过 100 个，暂时无法生成 Telegram 选择菜单。")
	}
	message, err := c.Bot().Send(c.Chat(), "🔎 正在查找待整理歌曲…")
	if err != nil {
		return err
	}
	chatID, userID := c.Chat().ID, c.Sender().ID
	go app.preparePlaylistAssignment(message, chatID, userID, options, skipped, playlistAssist)
	return nil
}

func (app *BotApp) preparePlaylistAssignment(message *tb.Message, chatID, userID int64, options []config.PlaylistInfo, skipped []string, playlistAssist bool) {
	ctx, cancel := context.WithTimeout(context.Background(), playlistAssignmentTimeout)
	defer cancel()
	service, err := newPlaylistOrganizerService(app.cfg.PlaylistOrganizer)
	if err != nil {
		app.editPlaylistAssignmentMessage(message, "❌ 歌单整理服务配置无效，请检查 playlist_organizer。", &tb.ReplyMarkup{})
		return
	}
	song, err := service.firstUnassignedSong(ctx)
	if errors.Is(err, errNoPendingPlaylistSong) {
		app.editPlaylistAssignmentMessage(message, "🎉 暂无待处理歌曲。", &tb.ReplyMarkup{})
		return
	}
	if err != nil {
		logger.Warn("[Telegram] 读取待整理歌曲失败", zap.Error(err))
		app.editPlaylistAssignmentMessage(message, "❌ 读取待整理歌曲失败，请检查 OliveTin 连接和 songs_unplaylist 动作。", &tb.ReplyMarkup{})
		return
	}
	useAI := playlistAssignmentUsesAI(playlistAssist, song)
	if useAI && app.cfg.AI != nil && app.cfg.AI.Enable && core.GlobalAI != nil {
		app.editPlaylistAssignmentMessage(message, fmt.Sprintf("🤖 正在分析《%s》并准备歌单选择…", displaySongValue(song.Title)), &tb.ReplyMarkup{})
	}
	selected := make(map[string]bool)
	optionNames := make(map[string]struct{}, len(options))
	for _, option := range options {
		optionNames[option.Name] = struct{}{}
	}
	for _, name := range splitPlaylistNames(song.Playlists) {
		if _, ok := optionNames[name]; ok {
			selected[name] = true
		}
	}
	statusNote := "没有同歌手歌单可预选，请选择归档歌单。"
	if len(splitPlaylistNames(song.Playlists)) > 0 {
		if len(selected) > 0 {
			statusNote = "已按同歌手其他歌曲所在歌单预选，可点选调整后确认。"
		} else {
			statusNote = "同歌手其他歌曲的歌单未匹配可选列表，请手动选择。"
		}
	}
	if useAI {
		if app.cfg.AI != nil && app.cfg.AI.Enable && core.GlobalAI != nil {
			output, askErr := core.GlobalAI.Ask(playlistClassifierPrompt(song, options),
				func(request *openai.ChatCompletionRequest) {
					request.Messages[0].Content = "你是严格的歌曲歌单分类器。遵守用户要求，只输出允许的歌单名或未知。"
				},
				core.WithMaxTokens(128),
				core.WithTemperature(0.1),
			)
			if askErr == nil {
				if suggestions, ok := parsePlaylistSuggestions(output, options); ok {
					selected = make(map[string]bool, len(suggestions))
					for _, name := range suggestions {
						selected[name] = true
					}
					statusNote = "🤖 AI 建议已预选；请确认或点选调整。"
				} else {
					statusNote = "🤖 AI 未给出有效建议，已切换人工选择。"
				}
			} else {
				logger.Warn("[Telegram] 歌单 AI 分类失败，改用人工选择", zap.Error(askErr))
				statusNote = "🤖 AI 暂不可用，已切换人工选择。"
			}
		} else {
			statusNote = "🤖 AI 分类未启用，已使用人工选择。"
		}
	} else if playlistAssist && len(splitPlaylistNames(song.Playlists)) > 0 && len(selected) > 0 {
		statusNote = "🎵 已按同歌手其他歌曲所在歌单预选，可点选调整。"
	}
	if len(skipped) > 0 {
		statusNote += " 部分含逗号、引号或控制字符的歌单已隐藏。"
	}
	assignment := pendingPlaylistAssignment{
		token:      strings.ReplaceAll(uuid.NewString(), "-", ""),
		userID:     userID,
		chatID:     chatID,
		message:    message,
		song:       song,
		playlists:  append([]config.PlaylistInfo(nil), options...),
		selected:   selected,
		statusNote: statusNote,
		expiresAt:  time.Now().Add(playlistAssignmentTTL),
	}
	if !app.registerPlaylistAssignment(assignment) {
		app.editPlaylistAssignmentMessage(message, "这首歌已有一个待确认的整理会话，请在之前的 Telegram 消息中继续。", &tb.ReplyMarkup{})
		return
	}
	app.editPlaylistAssignmentMessage(message, formatPlaylistAssignment(assignment), playlistAssignmentMarkup(assignment))
}

func (app *BotApp) HandlePlaylistAssignmentCallback(c tb.Context) error {
	args := c.Args()
	if len(args) < 2 {
		return c.Respond(&tb.CallbackResponse{Text: "整理操作无效或已过期", ShowAlert: true})
	}
	action, token := args[0], args[1]
	assignment, ok := app.getPlaylistAssignment(token)
	if !ok {
		return c.Respond(&tb.CallbackResponse{Text: "整理会话已过期，请重新发送 /assign_playlist", ShowAlert: true})
	}
	if c.Sender() == nil || c.Sender().ID != assignment.userID {
		return c.Respond(&tb.CallbackResponse{Text: "只有发起整理的人可以操作", ShowAlert: true})
	}
	if !playlistAssignmentCallbackMatches(c.Callback(), assignment) {
		return c.Respond(&tb.CallbackResponse{Text: "整理按钮与会话不匹配，请使用原始消息中的按钮", ShowAlert: true})
	}
	if assignment.processing {
		return c.Respond(&tb.CallbackResponse{Text: "正在整理，请稍候。"})
	}
	switch action {
	case "toggle":
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "歌单选择无效", ShowAlert: true})
		}
		index, err := strconv.Atoi(args[2])
		if err != nil || index < 0 || index >= len(assignment.playlists) {
			return c.Respond(&tb.CallbackResponse{Text: "歌单选择无效", ShowAlert: true})
		}
		updated, ok := app.togglePlaylistAssignment(token, assignment.playlists[index].Name)
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "整理会话已过期，请重新发送 /assign_playlist", ShowAlert: true})
		}
		_ = c.Respond()
		return c.Edit(formatPlaylistAssignment(updated), playlistAssignmentMarkup(updated))
	case "confirm":
		if len(args) != 2 {
			return c.Respond(&tb.CallbackResponse{Text: "整理操作无效", ShowAlert: true})
		}
		if len(selectedPlaylistNames(assignment)) == 0 {
			return c.Respond(&tb.CallbackResponse{Text: "请至少选择一个歌单", ShowAlert: true})
		}
		started, ok := app.beginPlaylistAssignment(token)
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "整理会话已过期或正在处理", ShowAlert: true})
		}
		_ = c.Respond(&tb.CallbackResponse{Text: "正在更新 Navidrome…"})
		if err := c.Edit("⏳ 正在检查歌单并更新 Navidrome…", &tb.ReplyMarkup{}); err != nil {
			app.setPlaylistAssignmentProcessing(token, false)
			return err
		}
		go app.completePlaylistAssignment(started.token)
		return nil
	case "cancel":
		if !app.removePlaylistAssignment(token) {
			return c.Respond(&tb.CallbackResponse{Text: "整理会话已过期", ShowAlert: true})
		}
		_ = c.Respond()
		return c.Edit("已取消本次歌单整理。", &tb.ReplyMarkup{})
	default:
		return c.Respond(&tb.CallbackResponse{Text: "未知整理操作", ShowAlert: true})
	}
}

func playlistAssignmentCallbackMatches(callback *tb.Callback, assignment pendingPlaylistAssignment) bool {
	return callback != nil && callback.Message != nil && assignment.message != nil &&
		callback.Message.ID == assignment.message.ID && callback.Message.Chat != nil && callback.Message.Chat.ID == assignment.chatID
}

func (app *BotApp) completePlaylistAssignment(token string) {
	assignment, ok := app.getPlaylistAssignment(token)
	if !ok {
		return
	}
	service, err := newPlaylistOrganizerService(app.cfg.PlaylistOrganizer)
	if err != nil {
		app.failPlaylistAssignment(token, "❌ 整理服务配置无效，请检查配置后重试。", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), playlistAssignmentTimeout)
	defer cancel()
	names := selectedPlaylistNames(assignment)
	added, err := service.addSongToPlaylists(ctx, assignment.song.ID, names)
	if added {
		app.markPlaylistAssignmentAdded(token)
	}
	if err != nil {
		app.failPlaylistAssignment(token, "❌ 歌单更新或核验失败。已成功的歌单不会重复添加，点确认可继续重试。", err)
		return
	}
	assignment, ok = app.getPlaylistAssignment(token)
	if !ok {
		return
	}
	if !assignment.addedAny {
		app.finishPlaylistAssignment(token, "这首歌已经在所选歌单中，没有重复添加。")
		return
	}
	sink := newMusicRAGSink(app.cfg.N8NConfig, nil)
	if err := sink.enqueue(ctx, assignment.song); err != nil {
		if errors.Is(err, errMusicRAGNotConfigured) {
			app.finishPlaylistAssignment(token, playlistAssignmentSuccessText(assignment, true))
			return
		}
		logger.Warn("[Telegram] 写入 MusicRAG 状态失败", zap.Error(err))
		app.failPlaylistAssignment(token, "⚠️ 歌单已更新，但 MusicRAG 状态同步失败。点“重试”可只补写状态，不会重复添加歌曲。", err)
		return
	}
	app.finishPlaylistAssignment(token, playlistAssignmentSuccessText(assignment, false))
}

func (app *BotApp) failPlaylistAssignment(token, note string, cause error) {
	logger.Warn("[Telegram] 歌单整理步骤失败", zap.Error(cause))
	assignment, ok := app.setPlaylistAssignmentProcessing(token, false)
	if !ok {
		return
	}
	assignment.statusNote = note
	app.updatePlaylistAssignment(assignment)
	app.editPlaylistAssignmentMessage(assignment.message, formatPlaylistAssignment(assignment), playlistAssignmentMarkup(assignment))
}

func (app *BotApp) finishPlaylistAssignment(token, result string) {
	assignment, ok := app.takePlaylistAssignment(token)
	if !ok {
		return
	}
	remaining := assignment.song.RemainingCount - 1
	if remaining > 0 && assignment.addedAny {
		result += fmt.Sprintf("\n\n还剩 %d 首待整理。", remaining)
	}
	app.editPlaylistAssignmentMessage(assignment.message, result, &tb.ReplyMarkup{})
}

func playlistAssignmentSuccessText(assignment pendingPlaylistAssignment, ragNotConfigured bool) string {
	names := selectedPlaylistNames(assignment)
	result := fmt.Sprintf("✅ 已将《%s》加入：%s", assignment.song.Title, strings.Join(names, "、"))
	if ragNotConfigured {
		result += "\n\n⚠️ MusicRAG 未同步。若要保留原流程的 RAG 入库，请配置 n8n_config.n8n_base_url、api_key 和 music_rag_table_id。"
	}
	return result
}

func selectedPlaylistNames(assignment pendingPlaylistAssignment) []string {
	names := make([]string, 0, len(assignment.selected))
	for _, playlist := range assignment.playlists {
		if assignment.selected[playlist.Name] {
			names = append(names, playlist.Name)
		}
	}
	return names
}

func formatPlaylistAssignment(assignment pendingPlaylistAssignment) string {
	var text strings.Builder
	text.WriteString("📌 新歌归档\n\n")
	fmt.Fprintf(&text, "🎵 %s\n🎤 %s", displaySongValue(assignment.song.Title), displaySongValue(assignment.song.Artist))
	if album := strings.TrimSpace(assignment.song.Album); album != "" {
		fmt.Fprintf(&text, "\n💿 %s", displaySongValue(album))
	}
	if assignment.song.Year > 0 {
		fmt.Fprintf(&text, " · %d", assignment.song.Year)
	}
	if peers := splitPlaylistNames(assignment.song.Playlists); len(peers) > 0 {
		fmt.Fprintf(&text, "\n\n同歌手其他歌曲已在：%s", strings.Join(peers, "、"))
	}
	if assignment.statusNote != "" {
		text.WriteString("\n\n")
		text.WriteString(assignment.statusNote)
	}
	if assignment.addedAny {
		text.WriteString("\n\n歌曲已写入所选歌单；再次确认会先检查缺项。")
	} else {
		text.WriteString("\n\n点击歌单切换选择，再确认添加。")
	}
	return text.String()
}

func displaySongValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "未知"
	}
	return value
}

func playlistAssignmentMarkup(assignment pendingPlaylistAssignment) *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	rows := make([]tb.Row, 0, (len(assignment.playlists)+1)/2+1)
	for index := 0; index < len(assignment.playlists); index += 2 {
		row := make(tb.Row, 0, 2)
		for current := index; current < len(assignment.playlists) && current < index+2; current++ {
			playlist := assignment.playlists[current]
			prefix := "⬜"
			if assignment.selected[playlist.Name] {
				prefix = "☑️"
			}
			label := prefix + " " + truncatePlaylistLabel(playlist.Name, 24)
			row = append(row, markup.Data(label, playlistAssignmentCallbackUnique, "toggle", assignment.token, strconv.Itoa(current)))
		}
		rows = append(rows, row)
	}
	confirmLabel := "✅ 确认整理"
	if assignment.addedAny {
		confirmLabel = "↻ 重试"
	}
	rows = append(rows, markup.Row(
		markup.Data(confirmLabel, playlistAssignmentCallbackUnique, "confirm", assignment.token),
		markup.Data("取消", playlistAssignmentCallbackUnique, "cancel", assignment.token),
	))
	markup.Inline(rows...)
	return markup
}

func truncatePlaylistLabel(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func (app *BotApp) editPlaylistAssignmentMessage(message *tb.Message, text string, markup *tb.ReplyMarkup) {
	if message == nil || app.bot == nil {
		return
	}
	if _, err := app.bot.Edit(message, text, markup); err != nil {
		logger.Warn("[Telegram] 更新歌单整理消息失败", zap.Error(err))
	}
}

func (app *BotApp) registerPlaylistAssignment(assignment pendingPlaylistAssignment) bool {
	app.playlistAssignmentMu.Lock()
	defer app.playlistAssignmentMu.Unlock()
	app.ensurePlaylistAssignmentMapsLocked()
	app.purgePlaylistAssignmentsLocked(time.Now())
	if _, exists := app.playlistAssignmentBySong[assignment.song.ID]; exists {
		return false
	}
	app.playlistAssignments[assignment.token] = clonePlaylistAssignment(assignment)
	app.playlistAssignmentBySong[assignment.song.ID] = assignment.token
	return true
}

func (app *BotApp) getPlaylistAssignment(token string) (pendingPlaylistAssignment, bool) {
	app.playlistAssignmentMu.Lock()
	defer app.playlistAssignmentMu.Unlock()
	app.ensurePlaylistAssignmentMapsLocked()
	app.purgePlaylistAssignmentsLocked(time.Now())
	assignment, ok := app.playlistAssignments[token]
	return clonePlaylistAssignment(assignment), ok
}

func (app *BotApp) togglePlaylistAssignment(token, playlistName string) (pendingPlaylistAssignment, bool) {
	app.playlistAssignmentMu.Lock()
	defer app.playlistAssignmentMu.Unlock()
	app.ensurePlaylistAssignmentMapsLocked()
	app.purgePlaylistAssignmentsLocked(time.Now())
	assignment, ok := app.playlistAssignments[token]
	if !ok || assignment.processing {
		return pendingPlaylistAssignment{}, false
	}
	if assignment.selected[playlistName] {
		delete(assignment.selected, playlistName)
	} else {
		assignment.selected[playlistName] = true
	}
	app.playlistAssignments[token] = assignment
	return clonePlaylistAssignment(assignment), true
}

func (app *BotApp) beginPlaylistAssignment(token string) (pendingPlaylistAssignment, bool) {
	app.playlistAssignmentMu.Lock()
	defer app.playlistAssignmentMu.Unlock()
	app.ensurePlaylistAssignmentMapsLocked()
	app.purgePlaylistAssignmentsLocked(time.Now())
	assignment, ok := app.playlistAssignments[token]
	if !ok || assignment.processing || len(selectedPlaylistNames(assignment)) == 0 {
		return pendingPlaylistAssignment{}, false
	}
	assignment.processing = true
	app.playlistAssignments[token] = assignment
	return clonePlaylistAssignment(assignment), true
}

func (app *BotApp) setPlaylistAssignmentProcessing(token string, processing bool) (pendingPlaylistAssignment, bool) {
	app.playlistAssignmentMu.Lock()
	defer app.playlistAssignmentMu.Unlock()
	app.ensurePlaylistAssignmentMapsLocked()
	assignment, ok := app.playlistAssignments[token]
	if !ok {
		return pendingPlaylistAssignment{}, false
	}
	assignment.processing = processing
	app.playlistAssignments[token] = assignment
	return clonePlaylistAssignment(assignment), true
}

func (app *BotApp) markPlaylistAssignmentAdded(token string) {
	app.playlistAssignmentMu.Lock()
	defer app.playlistAssignmentMu.Unlock()
	app.ensurePlaylistAssignmentMapsLocked()
	assignment, ok := app.playlistAssignments[token]
	if !ok {
		return
	}
	assignment.addedAny = true
	app.playlistAssignments[token] = assignment
}

func (app *BotApp) updatePlaylistAssignment(assignment pendingPlaylistAssignment) {
	app.playlistAssignmentMu.Lock()
	defer app.playlistAssignmentMu.Unlock()
	app.ensurePlaylistAssignmentMapsLocked()
	if _, exists := app.playlistAssignments[assignment.token]; exists {
		app.playlistAssignments[assignment.token] = clonePlaylistAssignment(assignment)
	}
}

func (app *BotApp) removePlaylistAssignment(token string) bool {
	_, ok := app.takePlaylistAssignment(token)
	return ok
}

func (app *BotApp) takePlaylistAssignment(token string) (pendingPlaylistAssignment, bool) {
	app.playlistAssignmentMu.Lock()
	defer app.playlistAssignmentMu.Unlock()
	app.ensurePlaylistAssignmentMapsLocked()
	assignment, ok := app.playlistAssignments[token]
	if !ok {
		return pendingPlaylistAssignment{}, false
	}
	delete(app.playlistAssignments, token)
	if app.playlistAssignmentBySong[assignment.song.ID] == token {
		delete(app.playlistAssignmentBySong, assignment.song.ID)
	}
	return clonePlaylistAssignment(assignment), true
}

func (app *BotApp) ensurePlaylistAssignmentMapsLocked() {
	if app.playlistAssignments == nil {
		app.playlistAssignments = make(map[string]pendingPlaylistAssignment)
	}
	if app.playlistAssignmentBySong == nil {
		app.playlistAssignmentBySong = make(map[string]string)
	}
}

func (app *BotApp) purgePlaylistAssignmentsLocked(now time.Time) {
	for token, assignment := range app.playlistAssignments {
		if !assignment.processing && !now.Before(assignment.expiresAt) {
			delete(app.playlistAssignments, token)
			if app.playlistAssignmentBySong[assignment.song.ID] == token {
				delete(app.playlistAssignmentBySong, assignment.song.ID)
			}
		}
	}
}

func clonePlaylistAssignment(assignment pendingPlaylistAssignment) pendingPlaylistAssignment {
	assignment.playlists = append([]config.PlaylistInfo(nil), assignment.playlists...)
	selected := assignment.selected
	assignment.selected = make(map[string]bool, len(selected))
	for name, isSelected := range selected {
		assignment.selected[name] = isSelected
	}
	return assignment
}
