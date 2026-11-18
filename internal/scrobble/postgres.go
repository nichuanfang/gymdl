package scrobble

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var tableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// PostgresStore writes incremental listens into the table rediscover and the
// listening report already read.
type PostgresStore struct {
	DB    *sql.DB
	Table string
}

func OpenPostgres(dsn, table string) (*PostgresStore, error) {
	if !tableName.MatchString(table) {
		return nil, fmt.Errorf("收听历史表名无效")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("无法打开收听历史写入连接")
	}
	db.SetMaxOpenConns(1)
	return &PostgresStore{DB: db, Table: table}, nil
}

func (s *PostgresStore) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func (s *PostgresStore) Latest(ctx context.Context) (time.Time, bool, error) {
	var raw sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT max(date) FROM `+s.Table).Scan(&raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("读取最近收听时间失败: %w", err)
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, false, nil
	}
	parsed, err := ParseStored(raw.String)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("最近收听时间无法解析")
	}
	return parsed, true, nil
}

func (s *PostgresStore) InsertIfNew(ctx context.Context, track Track) (bool, error) {
	urlSeen, tupleSeen, err := s.seen(ctx, track)
	if err != nil {
		return false, err
	}
	if !ShouldInsert(urlSeen, tupleSeen, track.URL) {
		return false, nil
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO `+s.Table+` (name, artist, album, image, mbid, url, date) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		track.Name, track.Artist, track.Album, track.Image, track.MBID, track.URL, track.Date)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

func (s *PostgresStore) seen(ctx context.Context, track Track) (bool, bool, error) {
	var urlSeen bool
	if track.URL != "" {
		if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+s.Table+` WHERE url = $1)`, track.URL).Scan(&urlSeen); err != nil {
			return false, false, err
		}
	}
	var tupleSeen bool
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+s.Table+` WHERE name = $1 AND artist = $2 AND date = $3)`, track.Name, track.Artist, track.Date).Scan(&tupleSeen); err != nil {
		return false, false, err
	}
	return urlSeen, tupleSeen, nil
}
