package audioquality

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/nichuanfang/gymdl/internal/storage"
)

type Store struct{ db *sql.DB }

type FileVersion struct {
	Path string
	Size int64
	Mod  int64
}

type CachedResult struct {
	FileVersion
	Result
	Reviewed bool
	RetryAt  time.Time
}

func Open(filename string) (*Store, error) {
	db, err := storage.Open(filename)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS audio_quality_results(
			library TEXT NOT NULL, path TEXT NOT NULL, size INTEGER NOT NULL, modified INTEGER NOT NULL,
			result TEXT NOT NULL, retry_at INTEGER NOT NULL,
			PRIMARY KEY(library,path)
		);
		CREATE TABLE IF NOT EXISTS audio_quality_reviews(
			library TEXT NOT NULL, path TEXT NOT NULL, user_id INTEGER NOT NULL,
			size INTEGER NOT NULL, modified INTEGER NOT NULL,
			PRIMARY KEY(library,path,user_id)
		);
		CREATE TABLE IF NOT EXISTS audio_quality_duplicate_reviews(
			library TEXT NOT NULL, user_id INTEGER NOT NULL, first_path TEXT NOT NULL, second_path TEXT NOT NULL,
			payload BLOB NOT NULL, PRIMARY KEY(library,user_id,first_path,second_path)
		);
		CREATE TABLE IF NOT EXISTS audio_quality_sessions(
			chat_id INTEGER PRIMARY KEY, token TEXT NOT NULL, phase TEXT NOT NULL,
			expires_at INTEGER NOT NULL, payload BLOB NOT NULL
		);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Results(ctx context.Context, library string, user int64) (map[string]CachedResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.path,a.size,a.modified,a.result,a.retry_at,r.user_id IS NOT NULL
		FROM audio_quality_results a LEFT JOIN audio_quality_reviews r
		ON r.library=a.library AND r.path=a.path AND r.size=a.size AND r.modified=a.modified AND r.user_id=?
		WHERE a.library=?`, user, library)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]CachedResult)
	for rows.Next() {
		var item CachedResult
		var payload string
		var retryAt int64
		if err := rows.Scan(&item.Path, &item.Size, &item.Mod, &payload, &retryAt, &item.Reviewed); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &item.Result); err != nil {
			return nil, err
		}
		if retryAt > 0 {
			item.RetryAt = time.Unix(retryAt, 0)
		}
		out[item.Path] = item
	}
	return out, rows.Err()
}

func (s *Store) Save(ctx context.Context, library string, item CachedResult) error {
	payload, err := json.Marshal(item.Result)
	if err != nil {
		return err
	}
	retryAt := int64(0)
	if !item.RetryAt.IsZero() {
		retryAt = item.RetryAt.Unix()
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audio_quality_results(library,path,size,modified,result,retry_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(library,path) DO UPDATE SET size=excluded.size,modified=excluded.modified,
		result=excluded.result,retry_at=excluded.retry_at`,
		library, item.Path, item.Size, item.Mod, string(payload), retryAt)
	return err
}

func (s *Store) MarkNormal(ctx context.Context, library string, user int64, file FileVersion) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO audio_quality_reviews(library,path,user_id,size,modified)
		SELECT library,path,?,size,modified FROM audio_quality_results WHERE library=? AND path=? AND size=? AND modified=?
		ON CONFLICT(library,path,user_id) DO UPDATE SET size=excluded.size,modified=excluded.modified`,
		user, library, file.Path, file.Size, file.Mod)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}
