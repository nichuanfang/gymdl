package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
)

func TestHandleQQMusicLinkRendersSmartLaunchAndFallback(t *testing.T) {
	previousConfig := GetWebConfig()
	previousMode := gin.Mode()
	t.Cleanup(func() {
		SetWebConfig(previousConfig)
		gin.SetMode(previousMode)
	})
	gin.SetMode(gin.TestMode)
	SetWebConfig(&config.Config{PlaylistOrganizer: &config.PlaylistOrganizerConfig{WebAppURL: "https://webapp.example.com"}})

	router := gin.New()
	router.POST("/api/playlist-assignment/:token", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/api/playlist-assignment/music-link", HandleQQMusicLink)
	request := httptest.NewRequest(http.MethodGet, "/api/playlist-assignment/music-link?mid=0039MnYb0qxYhV", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("smart link status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	body := response.Body.String()
	for _, want := range []string{
		"qqmusic://qq.com/media/playSonglist?p=",
		"https://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV",
		"window.location.replace(webURL)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("smart link page missing %q", want)
		}
	}
}

func TestHandleQQMusicLinkRejectsInvalidMID(t *testing.T) {
	previousConfig := GetWebConfig()
	SetWebConfig(&config.Config{PlaylistOrganizer: &config.PlaylistOrganizerConfig{WebAppURL: "https://webapp.example.com"}})
	t.Cleanup(func() { SetWebConfig(previousConfig) })

	router := gin.New()
	router.GET("/api/playlist-assignment/music-link", HandleQQMusicLink)
	for _, target := range []string{
		"/api/playlist-assignment/music-link",
		"/api/playlist-assignment/music-link?mid=abc%2Fdef",
		"/api/playlist-assignment/music-link?mid=abc%26redirect%3Dhttps%3A%2F%2Fevil.example",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want %d", target, response.Code, http.StatusBadRequest)
		}
	}
}
