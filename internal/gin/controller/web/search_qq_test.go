package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func TestSearchQQRefreshesCredentialAndRetriesOnceAfterRiskControl(t *testing.T) {
	credentialPath := useQQMusicCredentialPath(t)
	if _, err := saveQQMusicCredential(credentialPath, map[string]interface{}{
		"musicid": "123456", "str_musicid": "123456", "musickey": "old-key",
		"refresh_key": "old-refresh-key", "refresh_token": "old-refresh-token",
		"access_token": "old-access-token", "openid": "old-open-id", "unionid": "old-union-id",
		"expired_at": "2000000000", "login_type": "0",
	}); err != nil {
		t.Fatal(err)
	}

	previousConfig := GetWebConfig()
	previousManager := manager
	manager = nil
	t.Cleanup(func() {
		SetWebConfig(previousConfig)
		manager = previousManager
	})

	searchCalls, refreshCalls := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/search_by_type":
			searchCalls++
			if r.URL.Query().Get("keyword") != "张三" || r.URL.Query().Get("num") != "10" || r.URL.Query().Get("page") != "1" {
				t.Errorf("unexpected QQ search query: %s", r.URL.RawQuery)
			}
			cookie := r.Header.Get("Cookie")
			if searchCalls == 1 {
				if !strings.Contains(cookie, "musickey=old-key") || !strings.Contains(cookie, "refresh_token=old-refresh-token") {
					t.Errorf("first search did not use the complete cached credential: %q", cookie)
				}
				_, _ = w.Write([]byte(`{"code":1001,"msg":"触发风控, 需登录或者安全验证","data":{}}`))
				return
			}
			if searchCalls > 2 {
				t.Errorf("QQ search retried more than once: %d", searchCalls)
			}
			if !strings.Contains(cookie, "musickey=new-key") || !strings.Contains(cookie, "refresh_token=new-refresh-token") {
				t.Errorf("retry did not use refreshed credential: %q", cookie)
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"song":[{"id":42,"mid":"song-mid","name":"Fallback title","title":"Result title","singer":[{"name":"Artist"}],"album":{"name":"Album"},"interval":205,"pay":{"pay_play":0}}]}}`))
		case "/login/refresh_credential":
			refreshCalls++
			if !strings.Contains(r.Header.Get("Cookie"), "refresh_token=old-refresh-token") {
				t.Errorf("credential refresh did not use saved refresh token: %q", r.Header.Get("Cookie"))
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"musicid":123456,"str_musicid":"123456","musickey":"new-key","refresh_key":"new-refresh-key","refresh_token":"new-refresh-token","access_token":"new-access-token","openid":"new-open-id","unionid":"new-union-id","expired_at":2000000000,"login_type":0}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	SetWebConfig(&config.Config{QQMusicApiConfig: &config.QQMusicApiConfig{
		Enable: true, Endpoint: api.URL, LoginType: 0,
	}})
	items, err := searchQQ("张三", 10, 0)
	if err != nil {
		t.Fatalf("QQ search should refresh once then succeed: %v", err)
	}
	if searchCalls != 2 || refreshCalls != 1 {
		t.Fatalf("expected exactly one refresh and one retry, search=%d refresh=%d", searchCalls, refreshCalls)
	}
	if len(items) != 1 || items[0].SongID != "42" || items[0].Name != "Result title" {
		t.Fatalf("unexpected refreshed QQ search results: %#v", items)
	}
	stored, _, err := currentQQMusicCredential(credentialPath, GetWebConfig().QQMusicApiConfig)
	if err != nil || stored == nil || stored.MusicKey != "new-key" || stored.RefreshToken != "new-refresh-token" {
		t.Fatalf("refreshed credential was not persisted: credential=%#v err=%v", stored, err)
	}
}

func TestSearchQQRetriesHTTP429AtMostThreeTimesWithoutRefreshing(t *testing.T) {
	useQQMusicCredentialPath(t)
	previousConfig := GetWebConfig()
	previousManager := manager
	manager = nil
	t.Cleanup(func() {
		SetWebConfig(previousConfig)
		manager = previousManager
	})

	searchCalls, refreshCalls := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/search_by_type":
			searchCalls++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":429,"msg":"too many requests","data":{}}`))
		case "/login/refresh_credential":
			refreshCalls++
			_, _ = w.Write([]byte(`{"code":0,"data":{"musicid":123456,"musickey":"unexpected"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	SetWebConfig(&config.Config{QQMusicApiConfig: &config.QQMusicApiConfig{
		Enable: true, Endpoint: api.URL, LoginType: 0,
	}})

	_, err := searchQQ("rate limited", 10, 0)
	if err == nil || !strings.Contains(err.Error(), "HTTP 状态码: 429") {
		t.Fatalf("HTTP 429 should be returned as an upstream rate limit, got %v", err)
	}
	if searchCalls != 4 || refreshCalls != 0 {
		t.Fatalf("HTTP 429 should make one initial request and at most three retries without refreshing credentials: search=%d refresh=%d", searchCalls, refreshCalls)
	}
}

func TestSearchQQRetriesHTTP429AndSucceeds(t *testing.T) {
	useQQMusicCredentialPath(t)
	previousConfig := GetWebConfig()
	previousManager := manager
	manager = nil
	t.Cleanup(func() {
		SetWebConfig(previousConfig)
		manager = previousManager
	})

	searchCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		searchCalls++
		if searchCalls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"song":[{"id":42,"mid":"song-mid","title":"Result title"}]}}`))
	}))
	defer api.Close()
	SetWebConfig(&config.Config{QQMusicApiConfig: &config.QQMusicApiConfig{
		Enable: true, Endpoint: api.URL,
	}})

	items, err := searchQQ("rate limited once", 10, 0)
	if err != nil {
		t.Fatalf("QQ search should succeed after retrying HTTP 429: %v", err)
	}
	if searchCalls != 2 {
		t.Fatalf("expected one retry after the first HTTP 429, got %d search calls", searchCalls)
	}
	if len(items) != 1 || items[0].SongID != "42" {
		t.Fatalf("unexpected retried QQ search results: %#v", items)
	}
}

func TestSearchQQDoesNotRefreshForNonRiskErrors(t *testing.T) {
	credentialPath := useQQMusicCredentialPath(t)
	if _, err := saveQQMusicCredential(credentialPath, map[string]interface{}{
		"musicid": "123456", "str_musicid": "123456", "musickey": "old-key",
		"refresh_key": "old-refresh-key", "refresh_token": "old-refresh-token", "expired_at": "2000000000",
	}); err != nil {
		t.Fatal(err)
	}
	previousConfig := GetWebConfig()
	previousManager := manager
	manager = nil
	t.Cleanup(func() {
		SetWebConfig(previousConfig)
		manager = previousManager
	})

	searchCalls, refreshCalls := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/login/refresh_credential" {
			refreshCalls++
			_, _ = w.Write([]byte(`{"code":0,"data":{"musicid":123456,"musickey":"unexpected"}}`))
			return
		}
		searchCalls++
		_, _ = w.Write([]byte(`{"code":999,"msg":"普通参数错误","data":{}}`))
	}))
	defer api.Close()
	SetWebConfig(&config.Config{QQMusicApiConfig: &config.QQMusicApiConfig{Enable: true, Endpoint: api.URL}})

	_, err := searchQQ("keyword", 10, 0)
	if err == nil || !strings.Contains(err.Error(), "普通参数错误") {
		t.Fatalf("expected the original QQ API error, got %v", err)
	}
	if searchCalls != 1 || refreshCalls != 0 {
		t.Fatalf("non-risk errors must not trigger refresh/retry: search=%d refresh=%d", searchCalls, refreshCalls)
	}
}

func TestQQRiskControlErrorClassification(t *testing.T) {
	for _, text := range []string{
		"QQ Music API 错误: 触发风控, 需登录或者安全验证",
		"安全校验失败",
		"captcha required",
		"risk control triggered",
	} {
		if !isQQRiskControlError(text) {
			t.Errorf("expected risk-control message to be recognized: %q", text)
		}
	}
	for _, text := range []string{"普通参数错误", "歌曲不存在", ""} {
		if isQQRiskControlError(text) {
			t.Errorf("ordinary API error must not trigger credential refresh: %q", text)
		}
	}
}

func TestSearchQQRetriesOnlyOnceIfRiskControlPersists(t *testing.T) {
	credentialPath := useQQMusicCredentialPath(t)
	if _, err := saveQQMusicCredential(credentialPath, map[string]interface{}{
		"musicid": "123456", "str_musicid": "123456", "musickey": "old-key",
		"refresh_key": "old-refresh-key", "refresh_token": "old-refresh-token", "expired_at": "2000000000",
	}); err != nil {
		t.Fatal(err)
	}
	previousConfig := GetWebConfig()
	previousManager := manager
	manager = nil
	t.Cleanup(func() {
		SetWebConfig(previousConfig)
		manager = previousManager
	})

	searchCalls, refreshCalls := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/search_by_type":
			searchCalls++
			_, _ = w.Write([]byte(`{"code":1001,"msg":"触发风控, 需登录或者安全验证","data":{}}`))
		case "/login/refresh_credential":
			refreshCalls++
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"musicid":123456,"str_musicid":"123456","musickey":"new-key","refresh_key":"new-refresh-key","refresh_token":"new-refresh-token","expired_at":2000000000,"login_type":0}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	SetWebConfig(&config.Config{QQMusicApiConfig: &config.QQMusicApiConfig{Enable: true, Endpoint: api.URL}})

	_, err := searchQQ("keyword", 10, 0)
	if err == nil || !isQQRiskControlError(err.Error()) {
		t.Fatalf("persistent upstream risk control should remain an explicit error, got %v", err)
	}
	if searchCalls != 2 || refreshCalls != 1 {
		t.Fatalf("risk recovery must refresh once and retry once only: search=%d refresh=%d", searchCalls, refreshCalls)
	}
}
