// Package playlistcatalog keeps the tidy-playlist list in SQLite.
// Names come from Navidrome. Descriptions are edited in Telegram and are not
// overwritten by a later sync.
package playlistcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/storage"
)

const maxDescRunes = 200

// Playlist is one Navidrome playlist the organizer can offer.
type Playlist struct {
	ID      string
	Name    string
	Desc    string
	Comment string
	Active  bool
}

// Remote is a playlist row read from Navidrome.
type Remote struct {
	ID      string
	Name    string
	Comment string
}

// Result counts what a sync changed.
type Result struct {
	Added    int
	Updated  int
	Inactive int
}

// Store is the shared gymdl SQLite file.
type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := storage.Open(path)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS playlist_catalog (
id TEXT PRIMARY KEY,
name TEXT NOT NULL,
description TEXT NOT NULL DEFAULT '',
comment TEXT NOT NULL DEFAULT '',
active INTEGER NOT NULL DEFAULT 1,
synced_at INTEGER NOT NULL DEFAULT 0,
updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS playlist_catalog_state (
id INTEGER PRIMARY KEY CHECK (id = 1),
synced_at INTEGER NOT NULL
);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Synced reports whether a Navidrome sync has completed at least once.
func (s *Store) Synced(ctx context.Context) (bool, error) {
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT synced_at FROM playlist_catalog_state WHERE id=1`).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return at > 0, nil
}

// Active returns playlists currently present in Navidrome, empty descriptions first.
// Duplicate names keep the row that already has a description.
func (s *Store) Active(ctx context.Context) ([]Playlist, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,description,comment FROM playlist_catalog WHERE active=1 ORDER BY CASE WHEN description='' THEN 0 ELSE 1 END, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Playlist
	seen := map[string]int{}
	for rows.Next() {
		var item Playlist
		if err = rows.Scan(&item.ID, &item.Name, &item.Desc, &item.Comment); err != nil {
			return nil, err
		}
		item.Active = true
		if !usablePlaylistName(item.Name) {
			continue
		}
		if previous, ok := seen[item.Name]; ok {
			if out[previous].Desc == "" && item.Desc != "" {
				out[previous] = item
			}
			continue
		}
		seen[item.Name] = len(out)
		out = append(out, item)
	}
	return out, rows.Err()
}

// SetDesc stores a user-written description. An empty description is allowed.
func (s *Store) SetDesc(ctx context.Context, id, desc string) error {
	id = strings.TrimSpace(id)
	if !safePlaylistID(id) {
		return fmt.Errorf("歌单编号无效")
	}
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM playlist_catalog WHERE id=? AND active=1`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !usablePlaylistName(name)) {
		return fmt.Errorf("这张歌单不在当前列表里")
	}
	if err != nil {
		return err
	}
	cleaned, err := cleanDesc(desc)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE playlist_catalog SET description=?, updated_at=? WHERE id=? AND active=1`, cleaned, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("这张歌单不在当前列表里")
	}
	return nil
}

// Apply upserts the latest Navidrome list. Existing descriptions stay.
// A brand-new playlist can take its first description from the old yaml seed
// or from a human Navidrome comment.
func (s *Store) Apply(ctx context.Context, remotes []Remote, seeds []config.PlaylistInfo, now time.Time) (Result, error) {
	if now.IsZero() {
		now = time.Now()
	}
	seedByName := map[string]string{}
	for _, seed := range seeds {
		name := strings.TrimSpace(seed.Name)
		if name == "" {
			continue
		}
		if _, exists := seedByName[name]; !exists {
			seedByName[name] = strings.TrimSpace(seed.Desc)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	seen := map[string]struct{}{}
	var result Result
	stamp := now.Unix()
	for _, remote := range remotes {
		remote.Name = strings.TrimSpace(remote.Name)
		remote.Comment = strings.TrimSpace(remote.Comment)
		if !safePlaylistID(remote.ID) || !usablePlaylistName(remote.Name) {
			continue
		}
		if _, exists := seen[remote.ID]; exists {
			continue
		}
		seen[remote.ID] = struct{}{}
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT description FROM playlist_catalog WHERE id=?`, remote.ID).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			desc, descErr := cleanDesc(seedDesc(remote.Name, remote.Comment, seedByName))
			if descErr != nil {
				desc = ""
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO playlist_catalog(id,name,description,comment,active,synced_at,updated_at) VALUES(?,?,?,?,1,?,?)`, remote.ID, remote.Name, desc, remote.Comment, stamp, stamp); err != nil {
				return Result{}, err
			}
			result.Added++
			continue
		}
		if err != nil {
			return Result{}, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE playlist_catalog SET name=?, comment=?, active=1, synced_at=? WHERE id=?`, remote.Name, remote.Comment, stamp, remote.ID); err != nil {
			return Result{}, err
		}
		result.Updated++
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM playlist_catalog WHERE active=1`)
	if err != nil {
		return Result{}, err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return Result{}, err
		}
		if _, ok := seen[id]; !ok {
			stale = append(stale, id)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return Result{}, err
	}
	rows.Close()
	for _, id := range stale {
		if _, err = tx.ExecContext(ctx, `UPDATE playlist_catalog SET active=0, synced_at=? WHERE id=?`, stamp, id); err != nil {
			return Result{}, err
		}
		result.Inactive++
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO playlist_catalog_state(id, synced_at) VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET synced_at=excluded.synced_at`, stamp); err != nil {
		return Result{}, err
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	return result, nil
}

func seedDesc(name, comment string, seeds map[string]string) string {
	if desc := strings.TrimSpace(seeds[name]); desc != "" {
		return desc
	}
	if comment == "" || strings.HasPrefix(comment, "gymdl-") {
		return ""
	}
	return comment
}

func cleanDesc(desc string) (string, error) {
	desc = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return ' '
		}
		return r
	}, desc))
	if len([]rune(desc)) > maxDescRunes {
		return "", fmt.Errorf("描述最多 %d 字", maxDescRunes)
	}
	return desc, nil
}

// TidyName reports whether a playlist is offered for descriptions and assignment.
// Functional lists and generated AI lists stay out of that editor.
func TidyName(name string) bool {
	return usablePlaylistName(name)
}

func functionalPlaylistName(name string) bool {
	switch name {
	case "歌单", "回收站":
		return true
	default:
		return false
	}
}

func usablePlaylistName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, "[AI]") || functionalPlaylistName(name) {
		return false
	}
	for _, r := range name {
		if r == '\'' || r == ',' || r == '\n' || r == '\r' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func safePlaylistID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
