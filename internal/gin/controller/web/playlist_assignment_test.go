package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
)

func TestHandlePlaylistAssignmentWebAppSelectionChecksOriginAndForwardsSelection(t *testing.T) {
	previousConfig := GetWebConfig()
	previousMode := gin.Mode()
	playlistAssignmentSelectionMu.Lock()
	previousHandler := playlistAssignmentSelectionHandler
	playlistAssignmentSelectionMu.Unlock()
	t.Cleanup(func() {
		SetWebConfig(previousConfig)
		gin.SetMode(previousMode)
		SetPlaylistAssignmentSelectionHandler(previousHandler)
	})
	gin.SetMode(gin.TestMode)
	SetWebConfig(&config.Config{PlaylistOrganizer: &config.PlaylistOrganizerConfig{WebAppURL: "https://WEBAPP.example.com:443"}})

	var gotToken, gotInitData string
	var gotPlaylists []string
	calls := 0
	SetPlaylistAssignmentSelectionHandler(func(token string, selected []string, initData string) error {
		calls++
		gotToken = token
		gotPlaylists = append([]string(nil), selected...)
		gotInitData = initData
		return nil
	})
	router := gin.New()
	router.POST("/api/playlist-assignment/:token", HandlePlaylistAssignmentWebAppSelection)
	const token = "0123456789abcdef0123456789abcdef"
	body, err := json.Marshal(map[string]any{"selectedPlaylists": []string{"华语", "摇滚"}, "initData": "signed-init-data"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/playlist-assignment/"+token, strings.NewReader(string(body)))
	request.Header.Set("Origin", "https://webapp.example.com")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || calls != 1 || gotToken != token || gotInitData != "signed-init-data" || strings.Join(gotPlaylists, ",") != "华语,摇滚" {
		t.Fatalf("selection callback response=%d body=%q calls=%d token=%q playlists=%#v initData=%q", response.Code, response.Body.String(), calls, gotToken, gotPlaylists, gotInitData)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/playlist-assignment/"+token, strings.NewReader(string(body)))
	request.Header.Set("Origin", "https://attacker.example.com")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || calls != 1 {
		t.Fatalf("cross-origin selection should be rejected: status=%d calls=%d", response.Code, calls)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/playlist-assignment/not-a-token", strings.NewReader(string(body)))
	request.Header.Set("Origin", "https://webapp.example.com")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || calls != 1 {
		t.Fatalf("malformed token should be rejected: status=%d calls=%d", response.Code, calls)
	}
}

func TestHandlePlaylistAssignmentWebAppSelectionRejectsOversizedBody(t *testing.T) {
	previousConfig := GetWebConfig()
	SetWebConfig(&config.Config{PlaylistOrganizer: &config.PlaylistOrganizerConfig{WebAppURL: "https://webapp.example.com"}})
	t.Cleanup(func() { SetWebConfig(previousConfig) })
	router := gin.New()
	router.POST("/api/playlist-assignment/:token", HandlePlaylistAssignmentWebAppSelection)
	request := httptest.NewRequest(http.MethodPost, "/api/playlist-assignment/0123456789abcdef0123456789abcdef", strings.NewReader(strings.Repeat("x", 17*1024)))
	request.Header.Set("Origin", "https://webapp.example.com")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized request status=%d, want %d", response.Code, http.StatusBadRequest)
	}
}
