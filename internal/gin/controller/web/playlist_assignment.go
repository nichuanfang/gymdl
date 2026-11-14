package web

import (
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

type PlaylistAssignmentSelectionHandler func(selectionToken string, selectedPlaylists []string, initData string) error

var (
	playlistAssignmentSelectionMu      sync.RWMutex
	playlistAssignmentSelectionHandler PlaylistAssignmentSelectionHandler
)

func SetPlaylistAssignmentSelectionHandler(handler PlaylistAssignmentSelectionHandler) {
	playlistAssignmentSelectionMu.Lock()
	playlistAssignmentSelectionHandler = handler
	playlistAssignmentSelectionMu.Unlock()
}

func HandlePlaylistAssignmentWebAppSelection(c *gin.Context) {
	cfg := GetWebConfig()
	if cfg == nil || cfg.PlaylistOrganizer == nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	expectedOrigin, err := playlistWebAppOrigin(cfg.PlaylistOrganizer.WebAppURL)
	if err != nil || c.Request.Header.Get("Origin") != expectedOrigin {
		c.Status(http.StatusForbidden)
		return
	}
	selectionToken := c.Param("token")
	if len(selectionToken) != 32 {
		c.Status(http.StatusBadRequest)
		return
	}
	if _, err := hex.DecodeString(selectionToken); err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	var request struct {
		SelectedPlaylists []string `json:"selectedPlaylists"`
		InitData          string   `json:"initData"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) || errors.Is(err, io.EOF) {
			c.Status(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusBadRequest)
		return
	}
	if len(request.SelectedPlaylists) == 0 || len(request.SelectedPlaylists) > 100 || len(request.InitData) > 8192 {
		c.Status(http.StatusBadRequest)
		return
	}
	playlistAssignmentSelectionMu.RLock()
	handler := playlistAssignmentSelectionHandler
	playlistAssignmentSelectionMu.RUnlock()
	if handler == nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	if err := handler(selectionToken, request.SelectedPlaylists, request.InitData); err != nil {
		c.Status(http.StatusConflict)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func playlistWebAppOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("invalid playlist WebApp URL")
	}
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if strings.Contains(hostname, ":") {
		if port != "" && port != "443" {
			parsed.Host = net.JoinHostPort(hostname, port)
		} else {
			parsed.Host = "[" + hostname + "]"
		}
	} else if port != "" && port != "443" {
		parsed.Host = hostname + ":" + port
	} else {
		parsed.Host = hostname
	}
	return (&url.URL{Scheme: "https", Host: parsed.Host}).String(), nil
}
