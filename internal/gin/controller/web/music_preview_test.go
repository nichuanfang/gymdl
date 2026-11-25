package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHandleQQMusicPreviewRendersTelegramMetadataAndSafeAppLink(t *testing.T) {
	previousMode := gin.Mode()
	t.Cleanup(func() { gin.SetMode(previousMode) })
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.GET("/api/discovery/music-preview", HandleMusicPreview)
	request := httptest.NewRequest(http.MethodGet, "/api/discovery/music-preview?mid=001kG0Ys4JRmMa&cover=002ef2VB0Qfb30_2&title=Get+Up&artist=%E9%98%BF%E8%82%86&album=%E9%A2%84%E8%B0%8B%E9%82%82%E9%80%85&duration=200", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Errorf("X-Robots-Tag = %q", got)
	}
	body := response.Body.String()
	for _, want := range []string{
		`<meta property="og:title" content="Get Up">`,
		`<meta property="og:description" content="歌手：阿肆 · 专辑：预谋邂逅 · 时长：3:20">`,
		`https://y.gtimg.cn/music/photo_new/T002R500x500M000002ef2VB0Qfb30_2.jpg`,
		`<meta property="og:image:width" content="500">`,
		`<meta property="og:image:height" content="500">`,
		`https://music.qq.com/unilink/download.html?scheme=qqmusic%3A%2F%2F`,
		`https://y.qq.com/n/ryqq/songDetail/001kG0Ys4JRmMa`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview page missing %q", want)
		}
	}
}

func TestHandleQQMusicPreviewRejectsInvalidOrAmbiguousMetadata(t *testing.T) {
	previousMode := gin.Mode()
	t.Cleanup(func() { gin.SetMode(previousMode) })
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.GET("/api/discovery/music-preview", HandleMusicPreview)
	for _, target := range []string{
		"/api/discovery/music-preview?mid=001kG0Ys4JRmMa&cover=../../evil&title=Get+Up&artist=%E9%98%BF%E8%82%86",
		"/api/discovery/music-preview?mid=001kG0Ys4JRmMa&mid=0039MnYb0qxYhV&cover=002ef2VB0Qfb30_2&title=Get+Up&artist=%E9%98%BF%E8%82%86",
		"/api/discovery/music-preview?mid=001kG0Ys4JRmMa&cover=002ef2VB0Qfb30_2&title=+&artist=%E9%98%BF%E8%82%86",
		"/api/discovery/music-preview?mid=001kG0Ys4JRmMa&cover=002ef2VB0Qfb30_2&title=Get+Up&artist=%E9%98%BF%E8%82%86&duration=999999",
		"/api/discovery/music-preview?platform=netease&id=186016&cover=https%3A%2F%2Fuser%40evil.example%2Fcover.jpg&title=Song&artist=Artist",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want %d", target, response.Code, http.StatusBadRequest)
		}
	}
}

func TestHandleNeteaseMusicPreview(t *testing.T) {
	const cover = "https://p2.music.126.net/ZGffiDQZrGj5s_hnR1CNbg==/109951165566379710.jpg"
	query := url.Values{
		"platform": {"netease"}, "id": {"186016"}, "cover": {cover},
		"title": {"Song"}, "artist": {"Artist"},
	}
	router := gin.New()
	router.GET("/api/discovery/music-preview", HandleMusicPreview)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/discovery/music-preview?"+query.Encode(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", response.Code, response.Body.String())
	}
	for _, want := range []string{
		`<meta property="og:site_name" content="网易云音乐">`,
		cover + "?param=500y500",
		`<meta property="og:image:width" content="500">`,
		`<meta property="og:image:height" content="500">`,
		`https://m.music.163.com/m/applink/?scheme=orpheus%3A%2F%2Fsong%2F186016`,
		`https://music.163.com/song?id=186016`,
	} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("preview page missing %q", want)
		}
	}
}
