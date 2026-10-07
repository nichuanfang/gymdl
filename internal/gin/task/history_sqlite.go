package task

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const taskHistoryMigration = "import_history_json_v1"

func initializeTaskHistory(db *sql.DB, legacyPath string) error {
	const schema = `
CREATE TABLE IF NOT EXISTS download_tasks (
	id TEXT PRIMARY KEY,
	url TEXT NOT NULL DEFAULT '',
	platform TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	progress TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	song_info_json TEXT NOT NULL DEFAULT 'null',
	created_at_ns INTEGER NOT NULL,
	updated_at_ns INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_download_tasks_created ON download_tasks(updated_at_ns DESC);
CREATE INDEX IF NOT EXISTS idx_download_tasks_platform_status ON download_tasks(platform, status, updated_at_ns DESC);
CREATE TABLE IF NOT EXISTS schema_migrations (
	name TEXT PRIMARY KEY,
	applied_at_ns INTEGER NOT NULL
);
`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("create download task history schema: %w", err)
	}
	if legacyPath == "" {
		return nil
	}
	return importLegacyTaskHistory(db, legacyPath)
}

func importLegacyTaskHistory(db *sql.DB, legacyPath string) error {
	var existing int
	err := db.QueryRow(`SELECT 1 FROM schema_migrations WHERE name = ?`, taskHistoryMigration).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check task history migration: %w", err)
	}

	data, readErr := os.ReadFile(legacyPath)
	var history []*Task
	if readErr == nil {
		if err := json.Unmarshal(data, &history); err != nil {
			return fmt.Errorf("parse legacy task history %q: %w", legacyPath, err)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read legacy task history %q: %w", legacyPath, readErr)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin task history migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, item := range history {
		if item == nil || strings.TrimSpace(item.ID) == "" {
			continue
		}
		if err := insertTaskHistory(tx, item); err != nil {
			return fmt.Errorf("import legacy task %s: %w", item.ID, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(name, applied_at_ns) VALUES (?, ?)`, taskHistoryMigration, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("mark task history migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task history migration: %w", err)
	}
	return nil
}

type taskHistoryExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertTaskHistory(execer taskHistoryExecer, item *Task) error {
	songInfo, err := json.Marshal(item.SongInfo)
	if err != nil {
		return fmt.Errorf("marshal song metadata: %w", err)
	}
	createdAt := item.CreatedAt.UTC()
	updatedAt := item.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	_, err = execer.Exec(`INSERT INTO download_tasks (
	id, url, platform, status, progress, error, song_info_json, created_at_ns, updated_at_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	url = excluded.url,
	platform = excluded.platform,
	status = excluded.status,
	progress = excluded.progress,
	error = excluded.error,
	song_info_json = excluded.song_info_json,
	created_at_ns = excluded.created_at_ns,
	updated_at_ns = excluded.updated_at_ns`,
		item.ID, item.URL, item.Platform, string(item.Status), item.Progress, item.Error,
		string(songInfo), createdAt.UnixNano(), updatedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("write task history row: %w", err)
	}
	return nil
}

func loadTaskHistory(db *sql.DB, offset, limit int, filter HistoryFilter) ([]*Task, int, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	where, args := taskHistoryWhere(filter)
	countQuery := `SELECT COUNT(*) FROM download_tasks` + where
	var total int
	if err := db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count task history: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), limit, offset)
	rows, err := db.Query(`SELECT id, url, platform, status, progress, error, song_info_json, created_at_ns, updated_at_ns
FROM download_tasks`+where+` ORDER BY updated_at_ns DESC, rowid DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query task history: %w", err)
	}
	defer rows.Close()
	items := make([]*Task, 0, limit)
	for rows.Next() {
		item := &Task{}
		var status, songInfo string
		var createdAt, updatedAt int64
		if err := rows.Scan(&item.ID, &item.URL, &item.Platform, &status, &item.Progress, &item.Error, &songInfo, &createdAt, &updatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan task history row: %w", err)
		}
		item.Status = TaskStatus(status)
		item.CreatedAt = time.Unix(0, createdAt).UTC()
		item.UpdatedAt = time.Unix(0, updatedAt).UTC()
		if songInfo != "" && songInfo != "null" {
			if err := json.Unmarshal([]byte(songInfo), &item.SongInfo); err != nil {
				return nil, 0, fmt.Errorf("decode task song metadata: %w", err)
			}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate task history rows: %w", err)
	}
	return items, total, nil
}

func loadTaskHistoryByID(db *sql.DB, id string) (*Task, error) {
	row := db.QueryRow(`SELECT id, url, platform, status, progress, error, song_info_json, created_at_ns, updated_at_ns
FROM download_tasks WHERE id = ? LIMIT 1`, id)
	item := &Task{}
	var status, songInfo string
	var createdAt, updatedAt int64
	err := row.Scan(&item.ID, &item.URL, &item.Platform, &status, &item.Progress, &item.Error, &songInfo, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	item.Status = TaskStatus(status)
	item.CreatedAt = time.Unix(0, createdAt).UTC()
	item.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if songInfo != "" && songInfo != "null" {
		if err := json.Unmarshal([]byte(songInfo), &item.SongInfo); err != nil {
			return nil, fmt.Errorf("decode task song metadata: %w", err)
		}
	}
	return item, nil
}

func taskHistoryWhere(filter HistoryFilter) (string, []any) {
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 9)
	if platform := strings.ToLower(strings.TrimSpace(filter.Platform)); platform != "" {
		clauses = append(clauses, `lower(platform) = ?`)
		args = append(args, platform)
	}
	if status := strings.ToLower(strings.TrimSpace(filter.Status)); status != "" {
		clauses = append(clauses, `lower(status) = ?`)
		args = append(args, status)
	}
	if !filter.From.IsZero() {
		clauses = append(clauses, `created_at_ns >= ?`)
		args = append(args, filter.From.UnixNano())
	}
	if !filter.To.IsZero() {
		clauses = append(clauses, `created_at_ns <= ?`)
		args = append(args, filter.To.UnixNano())
	}
	if query := strings.ToLower(strings.TrimSpace(filter.Query)); query != "" {
		pattern := "%" + escapeLike(query) + "%"
		clauses = append(clauses, `(lower(url) LIKE ? ESCAPE '\' OR lower(platform) LIKE ? ESCAPE '\' OR lower(status) LIKE ? ESCAPE '\' OR lower(error) LIKE ? ESCAPE '\' OR lower(song_info_json) LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern, pattern, pattern, pattern)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

func taskHistoryMetrics(db *sql.DB, now time.Time) (DashboardMetrics, error) {
	var metrics DashboardMetrics
	cutoff := now.Add(-24 * time.Hour).UnixNano()
	err := db.QueryRow(`SELECT
	COUNT(*),
	COALESCE(SUM(CASE WHEN status = ? AND updated_at_ns >= ? THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN status = ? AND updated_at_ns >= ? THEN 1 ELSE 0 END), 0)
FROM download_tasks`, TaskStatusCompleted, cutoff, TaskStatusFailed, cutoff).Scan(
		&metrics.TotalHistory, &metrics.Completed24h, &metrics.Failed24h,
	)
	if err != nil {
		return DashboardMetrics{}, fmt.Errorf("query task history metrics: %w", err)
	}
	return metrics, nil
}
