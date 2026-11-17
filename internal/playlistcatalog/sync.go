package playlistcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

// Sync reads Navidrome and updates the shared SQLite catalog.
func Sync(ctx context.Context, organizer *config.PlaylistOrganizerConfig, dbPath string, seeds []config.PlaylistInfo) (Result, error) {
	store, err := Open(dbPath)
	if err != nil {
		return Result{}, err
	}
	defer store.Close()
	remotes, err := Fetch(ctx, organizer)
	if err != nil {
		return Result{}, err
	}
	return store.Apply(ctx, remotes, seeds, time.Now())
}

// Fetch lists non-AI playlists from Navidrome. It does not write the database.
func Fetch(ctx context.Context, organizer *config.PlaylistOrganizerConfig) ([]Remote, error) {
	if organizer == nil || strings.TrimSpace(organizer.NavidromeURL) == "" || strings.TrimSpace(organizer.NavidromeUsername) == "" || organizer.NavidromePassword == "" {
		return nil, fmt.Errorf("尚未配置 Navidrome")
	}
	base := strings.TrimRight(strings.TrimSpace(organizer.NavidromeURL), "/")
	token, err := login(ctx, base, organizer.NavidromeUsername, organizer.NavidromePassword)
	if err != nil {
		return nil, err
	}
	return list(ctx, base, token)
}

func login(ctx context.Context, base, username, password string) (string, error) {
	endpoint, err := url.JoinPath(base, "auth", "login")
	if err != nil {
		return "", fmt.Errorf("Navidrome 地址无效")
	}
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return "", err
	}
	response, err := doJSON(ctx, http.MethodPost, endpoint, body, "")
	if err != nil {
		return "", fmt.Errorf("Navidrome 登录失败: %w", err)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err = json.Unmarshal(response, &payload); err != nil || strings.TrimSpace(payload.Token) == "" {
		return "", fmt.Errorf("Navidrome 登录没有返回令牌")
	}
	return payload.Token, nil
}

func list(ctx context.Context, base, token string) ([]Remote, error) {
	endpoint, err := url.JoinPath(base, "api", "playlist")
	if err != nil {
		return nil, fmt.Errorf("Navidrome 地址无效")
	}
	response, err := doJSON(ctx, http.MethodGet, endpoint, nil, token)
	if err != nil {
		return nil, fmt.Errorf("读取 Navidrome 歌单失败: %w", err)
	}
	playlists, err := decodePlaylists(response)
	if err != nil {
		return nil, err
	}
	out := make([]Remote, 0, len(playlists))
	for _, playlist := range playlists {
		out = append(out, Remote{ID: playlist.ID, Name: playlist.Name, Comment: playlist.Comment})
	}
	return out, nil
}

type remotePlaylist struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Comment string `json:"comment"`
}

func decodePlaylists(raw []byte) ([]remotePlaylist, error) {
	var playlists []remotePlaylist
	if err := json.Unmarshal(raw, &playlists); err == nil {
		return playlists, nil
	}
	var wrapped struct {
		Data  []remotePlaylist `json:"data"`
		Items []remotePlaylist `json:"items"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("Navidrome 歌单列表无法解析")
	}
	if wrapped.Data != nil {
		return wrapped.Data, nil
	}
	return wrapped.Items, nil
}

func doJSON(ctx context.Context, method, endpoint string, body []byte, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gymdl/playlist-catalog")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Nd-Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return payload, nil
}
