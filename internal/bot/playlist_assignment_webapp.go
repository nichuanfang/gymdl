package bot

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

const playlistWebAppCallbackPath = "/api/playlist-assignment/"

func playlistWebAppBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("playlist_organizer.web_app_url 必须是 Nginx 歌单选择页面的 HTTPS 根地址")
	}
	parsed.Host = canonicalPlaylistWebAppHost(parsed)
	parsed.Path = ""
	return parsed, nil
}

func canonicalPlaylistWebAppHost(parsed *url.URL) string {
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if strings.Contains(hostname, ":") {
		if port != "" && port != "443" {
			return net.JoinHostPort(hostname, port)
		}
		return "[" + hostname + "]"
	}
	if port != "" && port != "443" {
		return hostname + ":" + port
	}
	return hostname
}

func playlistAssignmentWebAppURL(baseURL string, assignment pendingPlaylistAssignment, selectionToken string) (string, error) {
	base, err := playlistWebAppBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	options := make([]string, 0, len(assignment.playlists))
	for _, playlist := range assignment.playlists {
		options = append(options, playlist.Name)
	}
	data, err := json.Marshal(struct {
		Title   string   `json:"title"`
		Options []string `json:"options"`
		Default []string `json:"default"`
	}{
		Title:   "请选择歌单",
		Options: options,
		Default: playlistAssignmentWebAppDefaults(assignment),
	})
	if err != nil {
		return "", fmt.Errorf("编码歌单选择页面数据失败: %w", err)
	}
	page := *base
	query := page.Query()
	query.Set("data", string(data))
	callback := *base
	callback.Path = playlistWebAppCallbackPath + url.PathEscape(selectionToken)
	query.Set("callbackUrl", callback.String())
	page.RawQuery = query.Encode()
	result := page.String()
	if len(result) > 8192 {
		return "", errors.New("歌单选项过多，Nginx 歌单选择页面地址超过长度限制")
	}
	return result, nil
}

func playlistAssignmentWebAppDefaults(assignment pendingPlaylistAssignment) []string {
	if assignment.selectionComplete && len(assignment.selectedPlaylists) > 0 {
		return append([]string(nil), assignment.selectedPlaylists...)
	}
	return append([]string(nil), assignment.aiSuggestedPlaylists...)
}

func validateTelegramWebAppInitData(initData, botToken string, expectedUserID int64, now time.Time) error {
	if strings.TrimSpace(initData) == "" || strings.TrimSpace(botToken) == "" || expectedUserID <= 0 {
		return errors.New("Telegram WebApp 身份信息缺失")
	}
	values, err := url.ParseQuery(initData)
	if err != nil {
		return errors.New("Telegram WebApp 身份信息无效")
	}
	for _, entries := range values {
		if len(entries) != 1 {
			return errors.New("Telegram WebApp 身份信息包含重复字段")
		}
	}
	providedHash := values.Get("hash")
	providedBytes, err := hex.DecodeString(providedHash)
	if err != nil || len(providedBytes) != sha256.Size {
		return errors.New("Telegram WebApp 签名无效")
	}
	authDate, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return errors.New("Telegram WebApp 授权时间无效")
	}
	authorizedAt := time.Unix(authDate, 0)
	if authorizedAt.After(now.Add(30*time.Second)) || now.Sub(authorizedAt) > playlistAssignmentTTL {
		return errors.New("Telegram WebApp 授权已过期")
	}
	userRaw := values.Get("user")
	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(userRaw), &user); err != nil || user.ID != expectedUserID {
		return errors.New("Telegram WebApp 用户与整理发起人不匹配")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+values.Get(key))
	}
	dataCheckString := strings.Join(lines, "\n")
	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretMAC.Write([]byte(botToken))
	checkMAC := hmac.New(sha256.New, secretMAC.Sum(nil))
	checkMAC.Write([]byte(dataCheckString))
	if subtle.ConstantTimeCompare(providedBytes, checkMAC.Sum(nil)) != 1 {
		return errors.New("Telegram WebApp 签名校验失败")
	}
	return nil
}

func (app *BotApp) HandlePlaylistAssignmentWebAppSelection(selectionToken string, selectedPlaylists []string, initData string) error {
	if app == nil {
		return errors.New("歌单整理会话不可用")
	}
	assignment, _, ok := app.getPlaylistAssignmentBySelectionToken(selectionToken)
	if !ok {
		return errors.New("歌单选择链接已过期")
	}
	if app == nil || app.cfg == nil || app.cfg.Telegram == nil {
		return errors.New("Telegram 配置不可用")
	}
	if err := validateTelegramWebAppInitData(initData, app.cfg.Telegram.BotToken, assignment.userID, time.Now()); err != nil {
		return err
	}
	selected, err := normalizePlaylistSelection(selectedPlaylists, assignment.playlists)
	if err != nil {
		return err
	}
	updated, err := app.acceptPlaylistAssignmentSelection(assignment.token, selectionToken, selected)
	if err != nil {
		return err
	}
	return app.editPlaylistAssignmentMessage(updated.message, formatPlaylistAssignment(updated), playlistAssignmentMarkup(updated))
}

func normalizePlaylistSelection(selected []string, options []config.PlaylistInfo) ([]string, error) {
	if len(selected) == 0 || len(selected) > maxPlaylistOptions {
		return nil, errors.New("必须至少选择一个歌单")
	}
	allowed := make(map[string]struct{}, len(options))
	for _, option := range options {
		allowed[option.Name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(selected))
	result := make([]string, 0, len(selected))
	for _, raw := range selected {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, errors.New("歌单名称无效")
		}
		if _, ok := allowed[name]; !ok {
			return nil, errors.New("选择列表中包含未知歌单")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, errors.New("选择列表中包含重复歌单")
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result, nil
}

func samePlaylistSelection(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
