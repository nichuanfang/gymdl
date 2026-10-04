package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
)

func TestWebAuthProtectsAPIAndSupportsSessionLifecycle(t *testing.T) {
	t.Setenv(webAuthEnabledEnv, "true")
	t.Setenv(webAuthUsernameEnv, "admin")
	t.Setenv(webAuthPasswordEnv, "strong-password")
	fileConfig := config.WebAuthConfig{}
	if err := ValidateWebAuthSettings(fileConfig); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	auth := NewWebAuth(false, fileConfig)
	router := gin.New()
	group := router.Group("/api/web")
	group.Use(auth.Middleware())
	group.POST("/auth/login", auth.Login)
	group.GET("/auth/session", auth.Session)
	group.POST("/auth/logout", auth.Logout)
	group.GET("/files", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/web/files", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 before login, got %d", unauthorized.Code)
	}

	payload, _ := json.Marshal(map[string]string{"username": "admin", "password": "strong-password"})
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/web/auth/login", bytes.NewReader(payload))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	router.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("expected successful login, got %d: %s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := loginResponse.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie flags are not secure: %#v", cookie)
	}

	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/web/files", nil)
	request.AddCookie(cookie)
	router.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("expected authenticated request to pass, got %d", authorized.Code)
	}

	logout := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/web/auth/logout", nil)
	logoutRequest.AddCookie(cookie)
	router.ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusOK {
		t.Fatalf("expected logout success, got %d", logout.Code)
	}
	blocked := httptest.NewRecorder()
	blockedRequest := httptest.NewRequest(http.MethodGet, "/api/web/files", nil)
	blockedRequest.AddCookie(cookie)
	router.ServeHTTP(blocked, blockedRequest)
	if blocked.Code != http.StatusUnauthorized {
		t.Fatalf("expected revoked session to fail, got %d", blocked.Code)
	}
}

func TestWebAuthSettingsRequiresCredentials(t *testing.T) {
	t.Setenv(webAuthEnabledEnv, "true")
	t.Setenv(webAuthUsernameEnv, "")
	t.Setenv(webAuthPasswordEnv, "")
	if ValidateWebAuthSettings(config.WebAuthConfig{}) == nil {
		t.Fatal("expected missing credentials to be rejected")
	}
	t.Setenv(webAuthEnabledEnv, "false")
	if err := ValidateWebAuthSettings(config.WebAuthConfig{Enable: true}); err != nil {
		t.Fatalf("disabled auth should not require credentials: %v", err)
	}
}

func TestWebAuthUsesConfigAndEnvironmentOverrides(t *testing.T) {
	t.Setenv(webAuthEnabledEnv, "")
	t.Setenv(webAuthUsernameEnv, "")
	t.Setenv(webAuthPasswordEnv, "")
	fileConfig := config.WebAuthConfig{Enable: true, Username: "file-admin", Password: "file-password"}
	if err := ValidateWebAuthSettings(fileConfig); err != nil {
		t.Fatalf("valid file configuration should be accepted: %v", err)
	}
	auth := NewWebAuth(false, fileConfig)
	if !auth.enabled || auth.username != "file-admin" || auth.password != "file-password" {
		t.Fatalf("expected auth to use config.yaml credentials, got %#v", auth)
	}

	t.Setenv(webAuthUsernameEnv, "env-admin")
	t.Setenv(webAuthPasswordEnv, "env-password")
	auth = NewWebAuth(false, fileConfig)
	if auth.username != "env-admin" || auth.password != "env-password" {
		t.Fatalf("expected non-empty environment variables to override config.yaml, got %#v", auth)
	}

	t.Setenv(webAuthEnabledEnv, "false")
	auth = NewWebAuth(false, fileConfig)
	if auth.enabled {
		t.Fatal("explicit environment false should override config.yaml enable=true")
	}
}

func TestWebAuthRejectsInvalidEnvironmentOverride(t *testing.T) {
	t.Setenv(webAuthEnabledEnv, "sometimes")
	if err := ValidateWebAuthSettings(config.WebAuthConfig{}); err == nil {
		t.Fatal("expected invalid environment boolean to be rejected")
	}
}

func TestInvalidWebAuthConstructorFailsClosed(t *testing.T) {
	t.Setenv(webAuthEnabledEnv, "true")
	t.Setenv(webAuthUsernameEnv, "")
	t.Setenv(webAuthPasswordEnv, "")
	auth := NewWebAuth(false, config.WebAuthConfig{})
	router := gin.New()
	router.POST("/api/web/auth/login", auth.Login)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/web/auth/login", bytes.NewBufferString(`{"username":"","password":""}`)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid auth configuration should fail closed, got %d: %s", recorder.Code, recorder.Body.String())
	}
}
