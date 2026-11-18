package musicrag

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const n8nImportMarker = "import_n8n_music_rag_v1"

// N8NSource is the still-live music_rag data table. Import copies it once.
// The remote table is left in place.
type N8NSource struct {
	BaseURL string
	APIKey  string
	TableID string
	Client  *http.Client
}

func (s *Store) Imported(ctx context.Context) (bool, error) {
	if err := s.ensureMigrations(ctx); err != nil {
		return false, err
	}
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM schema_migrations WHERE name = ?`, n8nImportMarker).Scan(&name)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func (s *Store) MarkImported(ctx context.Context) error {
	if err := s.ensureMigrations(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(name, applied_at_ns) VALUES (?, ?)`, n8nImportMarker, time.Now().UTC().UnixNano())
	return err
}

func (s *Store) ensureMigrations(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at_ns INTEGER NOT NULL
	)`)
	return err
}

// ImportFromN8N copies every remote row that is not already stored.
// Existing local rows keep their status. The remote table is not modified.
func ImportFromN8N(ctx context.Context, store *Store, source N8NSource) (int, error) {
	done, err := store.Imported(ctx)
	if err != nil {
		return 0, err
	}
	if done {
		return 0, nil
	}
	rows, err := source.Fetch(ctx)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("n8n music_rag 没有返回任何行，未标记导入完成")
	}
	inserted, err := store.Import(ctx, rows)
	if err != nil {
		return inserted, err
	}
	if err := store.MarkImported(ctx); err != nil {
		return inserted, err
	}
	return inserted, nil
}

// Fetch reads the data table without deleting or updating it.
func (s N8NSource) Fetch(ctx context.Context) ([]Song, error) {
	if strings.TrimSpace(s.BaseURL) == "" || strings.TrimSpace(s.APIKey) == "" || strings.TrimSpace(s.TableID) == "" {
		return nil, fmt.Errorf("n8n music_rag 导入配置不完整")
	}
	if !SafeID(s.TableID) {
		return nil, fmt.Errorf("n8n music_rag 表 ID 无效")
	}
	base, err := url.Parse(strings.TrimSpace(s.BaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("n8n 地址无效")
	}
	endpoint, err := url.JoinPath(base.String(), "api", "v1", "data-tables", s.TableID, "rows")
	if err != nil {
		return nil, err
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	var songs []Song
	cursor := ""
	for page := 0; page < 100; page++ {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		query := parsed.Query()
		query.Set("limit", "100")
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		parsed.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-N8N-API-KEY", s.APIKey)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("读取 n8n music_rag 失败: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("读取 n8n music_rag 返回 HTTP %d", resp.StatusCode)
		}
		batch, next, err := parseN8NPage(body)
		if err != nil {
			return nil, err
		}
		songs = append(songs, batch...)
		if next == "" || next == cursor {
			return songs, nil
		}
		cursor = next
	}
	return nil, fmt.Errorf("n8n music_rag 分页没有结束")
}

func parseN8NPage(body []byte) ([]Song, string, error) {
	trimmed := strings.TrimSpace(string(body))
	var rawRows []map[string]any
	var next string
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &rawRows); err != nil {
			return nil, "", fmt.Errorf("解析 n8n music_rag 失败: %w", err)
		}
	} else {
		var envelope struct {
			Data       json.RawMessage `json:"data"`
			NextCursor string          `json:"nextCursor"`
		}
		if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
			return nil, "", fmt.Errorf("解析 n8n music_rag 失败: %w", err)
		}
		data := strings.TrimSpace(string(envelope.Data))
		if data == "" || data == "null" {
			return nil, "", fmt.Errorf("n8n music_rag 响应缺少 data")
		}
		if err := json.Unmarshal(envelope.Data, &rawRows); err != nil {
			return nil, "", fmt.Errorf("解析 n8n music_rag 失败: %w", err)
		}
		next = strings.TrimSpace(envelope.NextCursor)
	}
	songs := make([]Song, 0, len(rawRows))
	for _, row := range rawRows {
		song, ok := songFromRow(row)
		if !ok {
			continue
		}
		songs = append(songs, song)
	}
	return songs, next, nil
}

func songFromRow(row map[string]any) (Song, bool) {
	song := Song{
		MusicID:     cell(row["music_id"]),
		Title:       cell(row["title"]),
		Artist:      cell(row["artist"]),
		Album:       cell(row["album"]),
		Year:        cell(row["year"]),
		URL:         cell(row["url"]),
		CreatedAt:   cell(row["music_created_at"]),
		Genre:       cell(row["genre"]),
		Mood:        cell(row["mood"]),
		Language:    cell(row["language"]),
		Description: cell(row["description"]),
		Status:      cell(row["status"]),
		Error:       cell(row["error_message"]),
	}
	if !SafeID(song.MusicID) {
		return Song{}, false
	}
	if song.Status == "" {
		song.Status = StatusPending
	}
	return song, true
}

func cell(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}
