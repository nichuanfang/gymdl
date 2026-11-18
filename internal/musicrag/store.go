// Package musicrag keeps the music library index state that used to live in
// the n8n music_rag data table.
package musicrag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/nichuanfang/gymdl/internal/storage"
)

const (
	StatusPending = "待处理"
	StatusReady   = "成功"
	StatusFailed  = "失败"
)

// Song is one library track tracked for embedding and stale-vector cleanup.
type Song struct {
	MusicID     string
	Title       string
	Artist      string
	Album       string
	Year        string
	URL         string
	CreatedAt   string
	Genre       string
	Mood        string
	Language    string
	Description string
	Status      string
	Error       string
}

// Store is the local replacement for the n8n music_rag data table.
type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := storage.Open(path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS music_rag (
		music_id TEXT PRIMARY KEY,
		title TEXT NOT NULL DEFAULT '',
		artist TEXT NOT NULL DEFAULT '',
		album TEXT NOT NULL DEFAULT '',
		year TEXT NOT NULL DEFAULT '',
		url TEXT NOT NULL DEFAULT '',
		music_created_at TEXT NOT NULL DEFAULT '',
		genre TEXT NOT NULL DEFAULT '',
		mood TEXT NOT NULL DEFAULT '',
		language TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT '',
		error_message TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("创建 music_rag 表失败: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Count(ctx context.Context) int {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM music_rag`).Scan(&n)
	return n
}

// Enqueue inserts a pending row once. An existing row is left unchanged so a
// repeated playlist assignment cannot reset a finished embedding.
func (s *Store) Enqueue(ctx context.Context, song Song) error {
	if !SafeID(song.MusicID) {
		return errors.New("歌曲 ID 无效")
	}
	if strings.TrimSpace(song.Status) == "" {
		song.Status = StatusPending
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO music_rag(music_id,title,artist,album,year,url,music_created_at,genre,mood,language,description,status,error_message)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(music_id) DO NOTHING`,
		song.MusicID, song.Title, song.Artist, song.Album, song.Year, song.URL, song.CreatedAt, song.Genre, song.Mood, song.Language, song.Description, song.Status, song.Error)
	if err != nil {
		return fmt.Errorf("写入曲库状态失败: %w", err)
	}
	return nil
}

// Import adds rows that are not already stored and preserves their status.
func (s *Store) Import(ctx context.Context, rows []Song) (int, error) {
	inserted := 0
	for _, song := range rows {
		if !SafeID(song.MusicID) {
			return inserted, errors.New("歌曲 ID 无效")
		}
		if strings.TrimSpace(song.Status) == "" {
			song.Status = StatusPending
		}
		result, err := s.db.ExecContext(ctx, `INSERT INTO music_rag(music_id,title,artist,album,year,url,music_created_at,genre,mood,language,description,status,error_message)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(music_id) DO NOTHING`,
			song.MusicID, song.Title, song.Artist, song.Album, song.Year, song.URL, song.CreatedAt, song.Genre, song.Mood, song.Language, song.Description, song.Status, song.Error)
		if err != nil {
			return inserted, fmt.Errorf("导入曲库状态失败: %w", err)
		}
		n, _ := result.RowsAffected()
		inserted += int(n)
	}
	return inserted, nil
}

// Indexable returns songs still waiting for an embedding, including rows that
// failed earlier. A failed row is retried on a later run and does not block
// the songs after it.
func (s *Store) Indexable(ctx context.Context, limit int) ([]Song, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT music_id,title,artist,album,year,url,music_created_at,genre,mood,language,description,status,error_message
		FROM music_rag WHERE status IN (?, ?)
		ORDER BY CASE status WHEN ? THEN 0 ELSE 1 END, music_id LIMIT ?`, StatusPending, StatusFailed, StatusPending, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSongs(rows)
}

func (s *Store) Pending(ctx context.Context, limit int) ([]Song, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT music_id,title,artist,album,year,url,music_created_at,genre,mood,language,description,status,error_message
		FROM music_rag WHERE status=? ORDER BY music_id LIMIT ?`, StatusPending, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSongs(rows)
}

func scanSongs(rows *sql.Rows) ([]Song, error) {
	var out []Song
	for rows.Next() {
		var song Song
		if err := rows.Scan(&song.MusicID, &song.Title, &song.Artist, &song.Album, &song.Year, &song.URL, &song.CreatedAt, &song.Genre, &song.Mood, &song.Language, &song.Description, &song.Status, &song.Error); err != nil {
			return nil, err
		}
		out = append(out, song)
	}
	return out, rows.Err()
}

func (s *Store) Mark(ctx context.Context, song Song) error {
	if !SafeID(song.MusicID) {
		return errors.New("歌曲 ID 无效")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE music_rag SET genre=?,mood=?,language=?,description=?,status=?,error_message=? WHERE music_id=?`,
		song.Genre, song.Mood, song.Language, song.Description, song.Status, song.Error, song.MusicID)
	return err
}

func (s *Store) Delete(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if !SafeID(id) {
			return errors.New("歌曲 ID 无效")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM music_rag WHERE music_id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AllIDs(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT music_id FROM music_rag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

func (s *Store) SetError(ctx context.Context, id, message string) error {
	if !SafeID(id) {
		return errors.New("歌曲 ID 无效")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE music_rag SET error_message=? WHERE music_id=?`, message, id)
	return err
}

func (s *Store) IDs(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT music_id FROM music_rag WHERE status=?`, StatusReady)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

func SafeID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}
