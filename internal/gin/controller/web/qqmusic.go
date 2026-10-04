package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/gin/response"
)

// QQLoginState 扫码会话状态
type QQLoginState struct {
	Identifier  string    `json:"identifier"`
	ImageBase64 string    `json:"image_base64"`
	Status      string    `json:"status"` // waiting_scan / scanned / confirmed / done / timeout / refused / error
	Message     string    `json:"message,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

var (
	qqLoginMu       sync.Mutex
	qqLoginSessions = make(map[string]*QQLoginState)
	qqLoginCancels  = make(map[string]context.CancelFunc)
	qqCleanupOnce   sync.Once
)

// qqMusicClient QQ 音乐 API 客户端（复用 Bot 里的逻辑）
type qqMusicClient struct {
	httpClient *http.Client
	baseURL    string
	loginType  string
}

func newQQMusicClient(cfg *config.QQMusicApiConfig) *qqMusicClient {
	lt := "qq"
	switch cfg.LoginType {
	case 1:
		lt = "wx"
	case 2:
		lt = "mobile"
	}
	return &qqMusicClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    strings.TrimSuffix(cfg.Endpoint, "/"),
		loginType:  lt,
	}
}

type qqMusicAPIResponse struct {
	Code *int        `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data"`
}

func (q *qqMusicClient) get(path string, params map[string]string) (map[string]interface{}, error) {
	data, err := q.getAny(path, params)
	if err != nil {
		return nil, err
	}
	object, ok := data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("QQMusicApi 返回的数据类型无效")
	}
	return object, nil
}

func (q *qqMusicClient) getAny(path string, params map[string]string) (interface{}, error) {
	response, err := q.getEnvelopeWithContext(context.Background(), path, params, "")
	if err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (q *qqMusicClient) getWithContext(ctx context.Context, path string, params map[string]string) (map[string]interface{}, error) {
	response, err := q.getEnvelopeWithContext(ctx, path, params, "")
	if err != nil {
		return nil, err
	}
	object, ok := response.Data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("QQMusicApi 返回的数据类型无效")
	}
	return object, nil
}

func (q *qqMusicClient) getEnvelope(path string, params map[string]string, cookie string) (qqMusicAPIResponse, error) {
	return q.getEnvelopeWithContext(context.Background(), path, params, cookie)
}

func (q *qqMusicClient) getEnvelopeWithContext(ctx context.Context, path string, params map[string]string, cookie string) (qqMusicAPIResponse, error) {
	requestURL := q.baseURL + path
	if len(params) > 0 {
		requestURL += "?" + buildQuery(params)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return qqMusicAPIResponse{}, err
	}
	req.Header.Set("Referer", "https://y.qq.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0...")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return qqMusicAPIResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return qqMusicAPIResponse{}, fmt.Errorf("QQMusicApi 返回状态码: %d", resp.StatusCode)
	}

	var result qqMusicAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return qqMusicAPIResponse{}, err
	}
	return result, nil
}

func qqMusicCredentialCookie(cfg *config.QQMusicApiConfig) string {
	if cfg == nil || strings.TrimSpace(cfg.MusicId) == "" || strings.TrimSpace(cfg.MusicKey) == "" {
		return ""
	}
	fields := []struct{ key, value string }{
		{"login_type", strconv.Itoa(cfg.LoginType)},
		{"musicid", cfg.MusicId},
		{"musickey", cfg.MusicKey},
		{"str_musicid", cfg.StrMusicId},
		{"refresh_key", cfg.RefreshKey},
		{"refresh_token", cfg.RefreshToken},
		{"openid", cfg.OpenID},
		{"unionid", cfg.UnionID},
		{"access_token", cfg.AccessToken},
		{"expired_at", strconv.Itoa(cfg.ExpiredAt)},
	}
	pairs := make([]string, 0, len(fields))
	for _, field := range fields {
		if field.value != "" {
			pairs = append(pairs, field.key+"="+field.value)
		}
	}
	return strings.Join(pairs, "; ")
}

// classifyQQExpired handles both plain-bool responses and QQMusicApi's legacy
// ApiResponse wrapper, which encodes bool false as code=-1 / msg=操作失败.
func classifyQQExpired(result qqMusicAPIResponse) (expired bool, known bool) {
	if value, ok := result.Data.(bool); ok {
		return value, true
	}
	if object, ok := result.Data.(map[string]interface{}); ok {
		for _, key := range []string{"expired", "is_expired", "isExpired"} {
			if value, ok := object[key].(bool); ok {
				return value, true
			}
		}
		for _, key := range []string{"logged_in", "authenticated"} {
			if value, ok := object[key].(bool); ok {
				return !value, true
			}
		}
	}
	if result.Code == nil {
		return false, false
	}
	if *result.Code == 0 && result.Data == nil {
		return true, true
	}
	if *result.Code == -1 && result.Msg == "操作失败" {
		return false, true
	}
	return false, false
}

func buildQuery(params map[string]string) string {
	parts := make([]string, 0, len(params))
	for k, v := range params {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "&")
}

// HandleQQLoginStatusOverview reports whether the runtime QQ credential is valid.
func HandleQQLoginStatusOverview(c *gin.Context) {
	cfg := GetWebConfig()
	if cfg == nil || cfg.QQMusicApiConfig == nil || !cfg.QQMusicApiConfig.Enable || cfg.QQMusicApiConfig.Endpoint == "" {
		response.Success(c, gin.H{"enabled": false, "status": "disabled"})
		return
	}
	credential, source, err := currentQQMusicCredential(qqMusicCredentialPath, cfg.QQMusicApiConfig)
	if err != nil {
		response.Success(c, gin.H{"enabled": true, "status": "unknown", "message": "无法读取本地 QQ 登录凭证"})
		return
	}
	if credential == nil {
		response.Success(c, gin.H{"enabled": true, "status": "logged_out"})
		return
	}
	applyQQMusicCredentialToRuntime(credential)
	client := newQQMusicClient(credential)
	client.httpClient.Timeout = 6 * time.Second
	ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer cancel()
	result, err := client.getEnvelopeWithContext(ctx, "/login/check_expired", nil, qqMusicCredentialCookie(credential))
	if err != nil {
		response.Success(c, gin.H{"enabled": true, "status": "unknown", "message": "无法查询 QQ 登录状态"})
		return
	}
	expired, known := classifyQQExpired(result)
	if !known {
		response.Success(c, gin.H{"enabled": true, "status": "unknown", "message": "QQMusicApi 未返回可识别的凭证状态"})
		return
	}
	account := maskQQAccount(credential.MusicId)
	// config.yaml is an initialization source only. Seed musickey.json from it
	// once, and write all later refreshes back to the runtime cache.
	if expired || source == "config.yaml" {
		refreshed, refreshErr := refreshQQMusicCredential(ctx, client, credential)
		if refreshErr == nil {
			if saveErr := persistQQLoginCredential(refreshed); saveErr == nil {
				response.Success(c, gin.H{
					"enabled":   true,
					"status":    "logged_in",
					"account":   maskQQAccount(credentialString(refreshed, "str_musicid", "str_music_id", "musicid", "music_id")),
					"refreshed": true,
				})
				return
			}
			response.Success(c, gin.H{"enabled": true, "status": "unknown", "account": account, "message": "QQ 登录凭证已续期，但无法保存本地凭证缓存"})
			return
		}
	}
	if expired {
		response.Success(c, gin.H{
			"enabled": true,
			"status":  "logged_out",
			"account": account,
			"message": "登录凭证已过期且无法自动续期，请重新扫码登录",
		})
		return
	}
	response.Success(c, gin.H{"enabled": true, "status": "logged_in", "account": account})
}

func qqCredentialConfigured(cfg *config.QQMusicApiConfig) bool {
	return cfg != nil && strings.TrimSpace(cfg.MusicId) != "" && strings.TrimSpace(cfg.MusicKey) != ""
}

func refreshQQMusicCredential(ctx context.Context, client *qqMusicClient, cfg *config.QQMusicApiConfig) (map[string]interface{}, error) {
	result, err := client.getEnvelopeWithContext(ctx, "/login/refresh_credential", nil, qqMusicCredentialCookie(cfg))
	if err != nil {
		return nil, err
	}
	credential, ok := result.Data.(map[string]interface{})
	if !ok || credentialString(credential, "musicid", "music_id") == "" || credentialString(credential, "musickey", "music_key") == "" {
		return nil, fmt.Errorf("QQMusicApi 未返回有效凭证")
	}
	return credential, nil
}

func maskQQAccount(account string) string {
	account = strings.TrimSpace(account)
	if account == "" {
		return ""
	}
	if len(account) <= 4 {
		return "****"
	}
	return "****" + account[len(account)-4:]
}

// HandleQQLoginStart POST /api/web/qqmusic/qrcode
func HandleQQLoginStart(c *gin.Context) {
	startQQSessionCleanup()
	cfg := GetWebConfig()
	if cfg == nil || cfg.QQMusicApiConfig == nil || !cfg.QQMusicApiConfig.Enable {
		response.Fail(c, http.StatusBadRequest, "QQ Music API 未启用")
		return
	}

	if cfg.QQMusicApiConfig.LoginType == 2 {
		response.Fail(c, http.StatusBadRequest, "当前 WebUI 仅支持 QQ 或微信扫码登录")
		return
	}
	client := newQQMusicClient(cfg.QQMusicApiConfig)
	data, err := client.get("/login/qrcode/"+client.loginType, nil)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, "获取二维码失败: "+err.Error())
		return
	}

	b64Data, _ := data["data"].(string)
	identifier, _ := data["identifier"].(string)
	if b64Data == "" || identifier == "" {
		response.Fail(c, http.StatusInternalServerError, "QQMusicApi 返回数据异常")
		return
	}

	// 保存会话，并为轮询建立可被“取消”接口中断的上下文。
	pollContext, cancelPoll := context.WithTimeout(context.Background(), 3*time.Minute)
	qqLoginMu.Lock()
	if oldCancel := qqLoginCancels[identifier]; oldCancel != nil {
		oldCancel()
	}
	qqLoginCancels[identifier] = cancelPoll
	qqLoginSessions[identifier] = &QQLoginState{
		Identifier:  identifier,
		ImageBase64: b64Data,
		Status:      "waiting_scan",
		CreatedAt:   time.Now(),
	}
	qqLoginMu.Unlock()

	// 启动后台轮询
	go pollQQLoginStatus(pollContext, client, identifier)

	response.Success(c, gin.H{
		"identifier":   identifier,
		"image_base64": b64Data,
	})
}

// HandleQQLoginStatus GET /api/web/qqmusic/qrcode/:identifier
func HandleQQLoginStatus(c *gin.Context) {
	identifier := c.Param("identifier")
	qqLoginMu.Lock()
	state, ok := qqLoginSessions[identifier]
	if ok {
		copy := *state
		state = &copy
	}
	qqLoginMu.Unlock()
	if !ok {
		response.Fail(c, http.StatusNotFound, "会话不存在或已过期")
		return
	}
	response.Success(c, state)
}

// HandleQQLoginCancel DELETE /api/web/qqmusic/qrcode/:identifier
func HandleQQLoginCancel(c *gin.Context) {
	identifier := c.Param("identifier")
	qqLoginMu.Lock()
	delete(qqLoginSessions, identifier)
	cancelPoll := qqLoginCancels[identifier]
	delete(qqLoginCancels, identifier)
	qqLoginMu.Unlock()
	if cancelPoll != nil {
		cancelPoll()
	}
	response.Success(c, gin.H{"cancelled": true})
}

// pollQQLoginStatus 后台轮询
func pollQQLoginStatus(ctx context.Context, client *qqMusicClient, identifier string) {
	defer func() {
		qqLoginMu.Lock()
		delete(qqLoginCancels, identifier)
		qqLoginMu.Unlock()
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				updateQQLoginState(identifier, "timeout", "登录超时")
			}
			return
		case <-ticker.C:
			data, err := client.getWithContext(ctx, fmt.Sprintf("/login/qrcode/%s/status", client.loginType), map[string]string{
				"identifier": identifier,
			})
			if err != nil {
				updateQQLoginState(identifier, "error", "查询状态失败: "+err.Error())
				return
			}

			event, _ := data["event"].(float64)

			switch event {
			case 0: // DONE
				credential, _ := data["credential"].(map[string]interface{})
				if err := persistQQLoginCredential(credential); err != nil {
					updateQQLoginState(identifier, "error", "登录成功，但保存凭证失败: "+err.Error())
					return
				}
				updateQQLoginState(identifier, "done", "登录成功，凭证已安全保存")
				return
			case 1: // SCAN
				// 等待
			case 2: // CONF
				updateQQLoginState(identifier, "scanned", "已扫码，请在手机上确认")
			case 3: // TIMEOUT
				updateQQLoginState(identifier, "timeout", "二维码已过期")
				return
			case 4: // REFUSE
				updateQQLoginState(identifier, "refused", "已拒绝登录")
				return
			}

		}
	}
}

// updateQQLoginState 更新状态
func updateQQLoginState(identifier, status, msg string) {
	qqLoginMu.Lock()
	defer qqLoginMu.Unlock()
	if s, ok := qqLoginSessions[identifier]; ok {
		s.Status = status
		s.Message = msg
	}
}

func startQQSessionCleanup() {
	qqCleanupOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				cleanupQQSessions()
			}
		}()
	})
}

// 清理过期的 QQ 登录会话
func cleanupQQSessions() {
	qqLoginMu.Lock()
	cutoff := time.Now().Add(-10 * time.Minute)
	cancels := make([]context.CancelFunc, 0)
	for id, state := range qqLoginSessions {
		if state.CreatedAt.Before(cutoff) {
			delete(qqLoginSessions, id)
			if cancelPoll := qqLoginCancels[id]; cancelPoll != nil {
				cancels = append(cancels, cancelPoll)
				delete(qqLoginCancels, id)
			}
		}
	}
	qqLoginMu.Unlock()
	for _, cancelPoll := range cancels {
		cancelPoll()
	}
}
