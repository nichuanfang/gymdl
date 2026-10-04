package middleware

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
)

const (
	webAuthEnabledEnv  = "GYMDL_WEBUI_AUTH_ENABLED"
	webAuthUsernameEnv = "GYMDL_WEBUI_USERNAME"
	webAuthPasswordEnv = "GYMDL_WEBUI_PASSWORD"
	webAuthCookie      = "gymdl_web_session"
	webAuthSessionTTL  = 12 * time.Hour
)

type webSession struct {
	username  string
	expiresAt time.Time
}

type loginWindow struct {
	started time.Time
	count   int
}

// WebAuth provides optional cookie-based authentication for the WebUI API.
type WebAuth struct {
	enabled       bool
	configInvalid bool
	username      string
	password      string
	secure        bool
	mu            sync.Mutex
	sessions      map[string]webSession
	attempts      map[string]loginWindow
}

// resolveWebAuthSettings gives explicitly configured environment variables
// precedence over config.yaml while retaining file values for unset variables.
func resolveWebAuthSettings(fileConfig config.WebAuthConfig) (bool, string, string, error) {
	enabled := fileConfig.Enable
	if value := strings.TrimSpace(os.Getenv(webAuthEnabledEnv)); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, "", "", errors.New(webAuthEnabledEnv + " must be true or false")
		}
		enabled = parsed
	}

	username := strings.TrimSpace(fileConfig.Username)
	if value := strings.TrimSpace(os.Getenv(webAuthUsernameEnv)); value != "" {
		username = value
	}
	password := fileConfig.Password
	if value := os.Getenv(webAuthPasswordEnv); value != "" {
		password = value
	}
	if enabled && (username == "" || password == "") {
		return enabled, username, password, errors.New("启用 WebUI 管理登录时，必须配置 web_config.auth.username 和 password，或通过 GYMDL_WEBUI_USERNAME / GYMDL_WEBUI_PASSWORD 提供")
	}
	return enabled, username, password, nil
}

// ValidateWebAuthSettings prevents starting an enabled login gate without credentials.
func ValidateWebAuthSettings(fileConfig config.WebAuthConfig) error {
	_, _, _, err := resolveWebAuthSettings(fileConfig)
	return err
}

func NewWebAuth(secure bool, fileConfig config.WebAuthConfig) *WebAuth {
	enabled, username, password, err := resolveWebAuthSettings(fileConfig)
	if err != nil {
		// Startup validation reports the actionable error. Fail closed if this
		// constructor is ever used without that validation step.
		enabled = true
	}
	return &WebAuth{
		enabled:       enabled,
		configInvalid: err != nil,
		username:      username,
		password:      password,
		secure:        secure,
		sessions:      make(map[string]webSession),
		attempts:      make(map[string]loginWindow),
	}
}

// Middleware protects every route in /api/web except the three auth endpoints.
func (a *WebAuth) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !a.enabled || c.Request.URL.Path == "/api/web/auth/login" || c.Request.URL.Path == "/api/web/auth/session" || c.Request.URL.Path == "/api/web/auth/logout" {
			c.Next()
			return
		}
		cookie, err := c.Cookie(webAuthCookie)
		if err == nil {
			if _, ok := a.lookup(cookie); ok {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"code": http.StatusUnauthorized, "message": "需要登录",
		})
	}
}

func (a *WebAuth) Session(c *gin.Context) {
	if !a.enabled {
		c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": gin.H{"auth_enabled": false, "authenticated": true}})
		return
	}
	cookie, err := c.Cookie(webAuthCookie)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": gin.H{"auth_enabled": true, "authenticated": false}})
		return
	}
	session, ok := a.lookup(cookie)
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": gin.H{"auth_enabled": true, "authenticated": ok, "username": session.username}})
}

func (a *WebAuth) Login(c *gin.Context) {
	if !a.enabled {
		c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": gin.H{"auth_enabled": false, "authenticated": true}})
		return
	}
	if a.configInvalid {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": http.StatusServiceUnavailable, "message": "WebUI 管理登录配置无效"})
		return
	}
	if !a.allowLoginAttempt(c.ClientIP()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"code": http.StatusTooManyRequests, "message": "登录尝试过多，请稍后重试"})
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "message": "请输入用户名和密码"})
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(request.Username), []byte(a.username)) == 1
	passOK := constantTimeSecretEqual(request.Password, a.password)
	if !userOK || !passOK {
		c.JSON(http.StatusUnauthorized, gin.H{"code": http.StatusUnauthorized, "message": "用户名或密码错误"})
		return
	}
	a.mu.Lock()
	delete(a.attempts, c.ClientIP())
	a.mu.Unlock()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": "无法创建登录会话"})
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	a.mu.Lock()
	a.pruneExpiredLocked(time.Now())
	a.sessions[token] = webSession{username: a.username, expiresAt: time.Now().Add(webAuthSessionTTL)}
	a.mu.Unlock()
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(webAuthCookie, token, int(webAuthSessionTTL.Seconds()), "/api/web", "", a.cookieSecure(c), true)
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": gin.H{"auth_enabled": true, "authenticated": true, "username": a.username}})
}

func (a *WebAuth) Logout(c *gin.Context) {
	if cookie, err := c.Cookie(webAuthCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, cookie)
		a.mu.Unlock()
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(webAuthCookie, "", -1, "/api/web", "", a.cookieSecure(c), true)
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": gin.H{"logged_out": true}})
}

func (a *WebAuth) cookieSecure(c *gin.Context) bool {
	if a.secure || c.Request.TLS != nil {
		return true
	}
	forwardedProto := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0])
	return strings.EqualFold(forwardedProto, "https")
}

func (a *WebAuth) lookup(token string) (webSession, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.pruneExpiredLocked(now)
	s, ok := a.sessions[token]
	return s, ok && now.Before(s.expiresAt)
}

func (a *WebAuth) pruneExpiredLocked(now time.Time) {
	for token, session := range a.sessions {
		if !now.Before(session.expiresAt) {
			delete(a.sessions, token)
		}
	}
}

func constantTimeSecretEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func (a *WebAuth) allowLoginAttempt(ip string) bool {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, window := range a.attempts {
		if now.Sub(window.started) >= time.Minute {
			delete(a.attempts, key)
		}
	}
	window := a.attempts[ip]
	if now.Sub(window.started) >= time.Minute || window.started.IsZero() {
		window = loginWindow{started: now}
	}
	if window.count >= 10 {
		a.attempts[ip] = window
		return false
	}
	window.count++
	a.attempts[ip] = window
	return true
}
