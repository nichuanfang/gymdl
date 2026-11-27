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
	// ReadFailed marks a file whose content could not be fetched at all
	// (WebDAV down, transfer aborted). It is not a verdict about the audio:
	// the next scan must retry it instead of trusting a cached "unreadable".
	ReadFailed bool
}

func Open(filename string) (*Store, error) {
	db, err := storage.Open(filename)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS audio_quality_results(
			library TEXT NOT NULL, path TEXT NOT NULL, size INTEGER NOT NULL, modified INTEGER NOT NULL,
			result TEXT NOT NULL, retry_at INTEGER NOT NULL, read_failed INTEGER NOT NULL DEFAULT 0,
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
	store := &Store{db: db}
	if err := store.ensureReadFailedColumn(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// ensureReadFailedColumn upgrades databases created before transfer failures
// were tracked separately from unreadable audio.
func (s *Store) ensureReadFailedColumn() error {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('audio_quality_results')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return err
		}
		if name == "read_failed" {
			return nil
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE audio_quality_results ADD COLUMN read_failed INTEGER NOT NULL DEFAULT 0`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Results(ctx context.Context, library string, user int64) (map[string]CachedResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.path,a.size,a.modified,a.result,a.retry_at,a.read_failed,r.user_id IS NOT NULL
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
		if err := rows.Scan(&item.Path, &item.Size, &item.Mod, &payload, &retryAt, &item.ReadFailed, &item.Reviewed); err != nil {
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
		INSERT INTO audio_quality_results(library,path,size,modified,result,retry_at,read_failed) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(library,path) DO UPDATE SET size=excluded.size,modified=excluded.modified,
		result=excluded.result,retry_at=excluded.retry_at,read_failed=excluded.read_failed`,
		library, item.Path, item.Size, item.Mod, string(payload), retryAt, item.ReadFailed)
	return err
}

// ForgetReadFailures drops cached rows whose audio could never be fetched, so
// a scan after the tunnel recovers starts from a clean slate instead of
// carrying last outage's verdicts. Rows with real findings are left alone.
func (s *Store) ForgetReadFailures(ctx context.Context, library string) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM audio_quality_results WHERE library=? AND read_failed=1`, library)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
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
