package musicrag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// LibrarySong is one track returned by the OliveTin songs_all action.
// That action replaces the NavidromSQLExecutor query used by MusicRAG.
type LibrarySong struct {
	ID        string
	Title     string
	Artist    string
	Album     string
	Year      string
	URL       string
	CreatedAt string
}

// SyncLibrary inserts songs that are not tracked yet and reports rows whose
// music_id is no longer in the library. An empty library is refused so a
// failed listing cannot wipe the local table.
func SyncLibrary(ctx context.Context, store *Store, songs []LibrarySong) (int, []string, error) {
	if len(songs) == 0 {
		return 0, nil, fmt.Errorf("曲库列表为空，已跳过清理")
	}
	enqueued := 0
	present := make(map[string]struct{}, len(songs))
	for _, song := range songs {
		if !SafeID(song.ID) {
			return enqueued, nil, fmt.Errorf("曲库歌曲 ID 无效")
		}
		present[song.ID] = struct{}{}
		before := store.Count(ctx)
		if err := store.Enqueue(ctx, Song{
			MusicID: song.ID, Title: song.Title, Artist: song.Artist, Album: song.Album,
			Year: song.Year, URL: song.URL, CreatedAt: song.CreatedAt, Status: StatusPending,
		}); err != nil {
			return enqueued, nil, err
		}
		if store.Count(ctx) > before {
			enqueued++
		}
	}
	known, err := store.AllIDs(ctx)
	if err != nil {
		return enqueued, nil, err
	}
	var stale []string
	for id := range known {
		if _, ok := present[id]; !ok {
			stale = append(stale, id)
		}
	}
	return enqueued, stale, nil
}

// OliveTinLibrary reads songs_all. It is the library half of the old
// NavidromSQLExecutor call and is not a new user-facing entry.
type OliveTinLibrary struct {
	BaseURL  string
	Username string
	Password string
	Client   *http.Client
}

func (l OliveTinLibrary) Songs(ctx context.Context) ([]LibrarySong, error) {
	if (strings.TrimSpace(l.Username) == "") != (strings.TrimSpace(l.Password) == "") {
		return nil, fmt.Errorf("OliveTin 用户名和密码必须同时配置")
	}
	base := strings.TrimRight(strings.TrimSpace(l.BaseURL), "/")
	endpoint, err := url.JoinPath(base, "api", "StartActionAndWait")
	if err != nil {
		return nil, err
	}
	body := []byte(`{"actionId":"songs_all","arguments":[{"name":"limit","value":"-1"}]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if l.Username != "" {
		req.SetBasicAuth(l.Username, l.Password)
	}
	client := l.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取曲库失败: %w", err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("读取曲库返回 HTTP %d", resp.StatusCode)
	}
	return parseOliveTinSongs(payload)
}

func parseOliveTinSongs(payload []byte) ([]LibrarySong, error) {
	var envelope struct {
		LogEntry struct {
			ExitCode float64 `json:"exitCode"`
			Stdout   string  `json:"stdout"`
			Output   string  `json:"output"`
			TimedOut bool    `json:"timedOut"`
		} `json:"logEntry"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("解析曲库响应失败: %w", err)
	}
	if envelope.LogEntry.TimedOut || envelope.LogEntry.ExitCode != 0 {
		return nil, fmt.Errorf("曲库查询失败")
	}
	raw := strings.TrimSpace(envelope.LogEntry.Stdout)
	if raw == "" {
		raw = strings.TrimSpace(envelope.LogEntry.Output)
	}
	if raw == "" || raw == "[]" || raw == "null" {
		return nil, fmt.Errorf("曲库列表为空，已跳过清理")
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		var one map[string]any
		if err := json.Unmarshal([]byte(raw), &one); err != nil {
			return nil, fmt.Errorf("解析曲库歌曲失败: %w", err)
		}
		rows = []map[string]any{one}
	}
	songs := make([]LibrarySong, 0, len(rows))
	for _, row := range rows {
		id := cell(row["id"])
		if id == "" {
			id = cell(row["music_id"])
		}
		urlText := cell(row["url"])
		if urlText == "" {
			urlText = cell(row["comment"])
		}
		songs = append(songs, LibrarySong{
			ID: id, Title: cell(row["title"]), Artist: cell(row["artist"]), Album: cell(row["album"]),
			Year: cell(row["year"]), URL: urlText, CreatedAt: cell(row["created_at"]),
		})
	}
	return songs, nil
}
