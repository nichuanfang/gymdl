package aichat

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nichuanfang/gymdl/internal/storage"
)

// maxCandidatesPerChat bounds how many candidate rows one chat keeps. Buttons
// expire after candidateTTL anyway; the cap only limits table growth.
const maxCandidatesPerChat = 200

const (
	modeMigration    = "ai_chat_mode_v2"
	messageMigration = "ai_chat_messages_v1"
	candidateMigr    = "ai_chat_candidates_v1"
)

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := storage.Open(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) ensureSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	name TEXT PRIMARY KEY,
	applied_at_ns INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ai_chat_modes (
	user_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (user_id, chat_id)
);
CREATE TABLE IF NOT EXISTS ai_chat_messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ai_chat_messages_scope
	ON ai_chat_messages(user_id, chat_id, id);
CREATE TABLE IF NOT EXISTS ai_chat_candidates (
	token TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	title TEXT NOT NULL,
	artist TEXT NOT NULL,
	platform TEXT NOT NULL,
	url TEXT NOT NULL,
	name TEXT NOT NULL,
	created_at TEXT NOT NULL,
	owned INTEGER NOT NULL DEFAULT 0,
	is_vip INTEGER NOT NULL DEFAULT 0,
	grounded INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ai_chat_candidates_scope
	ON ai_chat_candidates(user_id, chat_id, created_at);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("create ai chat schema: %w", err)
	}
	// Older deployments created ai_chat_candidates without the flags below.
	// CREATE TABLE IF NOT EXISTS never adds columns, so migrate explicitly.
	for _, column := range []struct{ name, ddl string }{
		{"owned", `ALTER TABLE ai_chat_candidates ADD COLUMN owned INTEGER NOT NULL DEFAULT 0`},
		{"is_vip", `ALTER TABLE ai_chat_candidates ADD COLUMN is_vip INTEGER NOT NULL DEFAULT 0`},
		{"grounded", `ALTER TABLE ai_chat_candidates ADD COLUMN grounded INTEGER NOT NULL DEFAULT 0`},
	} {
		has, err := s.hasColumn("ai_chat_candidates", column.name)
		if err != nil {
			return fmt.Errorf("inspect ai_chat_candidates.%s: %w", column.name, err)
		}
		if has {
			continue
		}
		if _, err := s.db.Exec(column.ddl); err != nil {
			return fmt.Errorf("add ai_chat_candidates.%s: %w", column.name, err)
		}
	}
	for _, name := range []string{modeMigration, messageMigration, candidateMigr} {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(name, applied_at_ns) VALUES (?, ?)`, name, time.Now().UnixNano()); err != nil {
			return fmt.Errorf("record ai chat migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// expiredCandidateTokens lists candidate tokens whose created_at is before the
// cutoff. Callers must invoke it outside a transaction: the pool is limited to
// one connection, so a nested query would deadlock against the open tx.
func (s *Store) expiredCandidateTokens(userID, chatID int64, cutoff time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT token, created_at FROM ai_chat_candidates WHERE user_id = ? AND chat_id = ?`, userID, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var expired []string
	for rows.Next() {
		var token, createdAt string
		if err := rows.Scan(&token, &createdAt); err != nil {
			return nil, err
		}
		created, parseErr := time.Parse(time.RFC3339Nano, createdAt)
		if parseErr != nil {
			// Unparseable rows can never be trusted as fresh candidates.
			expired = append(expired, token)
			continue
		}
		if created.Before(cutoff) {
			expired = append(expired, token)
		}
	}
	return expired, rows.Err()
}

// ModeState returns the stored mode for a chat. found=false means the chat has
// never switched modes and the configured default should apply.
func (s *Store) ModeState(userID, chatID int64) (enabled bool, found bool, err error) {
	var value int
	err = s.db.QueryRow(`SELECT enabled FROM ai_chat_modes WHERE user_id = ? AND chat_id = ?`, userID, chatID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return value != 0, true, nil
}

func (s *Store) SetMode(userID, chatID int64, enabled bool) error {
	_, err := s.db.Exec(`INSERT INTO ai_chat_modes(user_id, chat_id, enabled, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT(user_id, chat_id) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
		userID, chatID, boolToInt(enabled), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) Reset(userID, chatID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM ai_chat_messages WHERE user_id = ? AND chat_id = ?`, userID, chatID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ai_chat_candidates WHERE user_id = ? AND chat_id = ?`, userID, chatID); err != nil {
		return err
	}
	return tx.Commit()
}

// AppendMessages trims old rows so a long-running chat cannot grow without
// bound; the newest maxRows messages are kept for replay.
func (s *Store) AppendMessages(userID, chatID int64, msgs []Message, maxRows int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, m := range msgs {
		if _, err := tx.Exec(`INSERT INTO ai_chat_messages(user_id, chat_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)`,
			userID, chatID, m.Role, m.Content, now); err != nil {
			return err
		}
	}
	if maxRows > 0 {
		if _, err := tx.Exec(`DELETE FROM ai_chat_messages WHERE user_id = ? AND chat_id = ? AND id NOT IN (
	SELECT id FROM ai_chat_messages WHERE user_id = ? AND chat_id = ? ORDER BY id DESC LIMIT ?
)`, userID, chatID, userID, chatID, maxRows); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) History(userID, chatID int64, limit int) ([]Message, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT role, content FROM (
	SELECT id, role, content FROM ai_chat_messages WHERE user_id = ? AND chat_id = ? ORDER BY id DESC LIMIT ?
) ORDER BY id ASC`, userID, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Message, 0, limit)
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkCandidateOwned flips the "已入库" badge for one candidate after a batch
// download, so a later press cannot offer the same track again.
func (s *Store) MarkCandidateOwned(userID, chatID int64, token string) error {
	_, err := s.db.Exec(`UPDATE ai_chat_candidates SET owned = 1 WHERE token = ? AND user_id = ? AND chat_id = ?`,
		token, userID, chatID)
	return err
}

// Candidates lists the still-valid candidates of one chat, newest first. It is
// used by the batch download button.
func (s *Store) Candidates(userID, chatID int64) ([]Candidate, error) {
	now := time.Now().UTC()
	expired, err := s.expiredCandidateTokens(userID, chatID, now.Add(-candidateTTL))
	if err != nil {
		return nil, err
	}
	stale := make(map[string]bool, len(expired))
	for _, token := range expired {
		stale[token] = true
	}
	rows, err := s.db.Query(`SELECT token, title, artist, platform, url, name, created_at, owned, is_vip, grounded
FROM ai_chat_candidates WHERE user_id = ? AND chat_id = ? ORDER BY rowid DESC`, userID, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Candidate, 0, 8)
	for rows.Next() {
		var c Candidate
		var createdAt string
		var owned, isVIP, grounded int
		if err := rows.Scan(&c.Token, &c.Title, &c.Artist, &c.Platform, &c.URL, &c.Name,
			&createdAt, &owned, &isVIP, &grounded); err != nil {
			return nil, err
		}
		if stale[c.Token] {
			continue
		}
		c.Owned, c.IsVIP, c.Grounded = owned != 0, isVIP != 0, grounded != 0
		if created, parseErr := time.Parse(time.RFC3339Nano, createdAt); parseErr == nil {
			c.ExpiresAt = created.Add(candidateTTL)
		}
		c.BatchID = createdAt
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SaveCandidates(userID, chatID int64, items []Candidate) error {
	now := time.Now().UTC()
	cutoff := now.Add(-candidateTTL)
	// Only drop candidate rows that are already past their TTL. Wiping the whole
	// chat used to invalidate the buttons of every earlier answer in the
	// conversation, so scrolling back and pressing one returned "已过期".
	// Expiry is evaluated in Go because RFC3339Nano strings are not
	// fixed-width and therefore not safely comparable in SQL. The scan runs
	// before the transaction because the SQLite pool holds a single connection.
	expired, err := s.expiredCandidateTokens(userID, chatID, cutoff)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, token := range expired {
		if _, err := tx.Exec(`DELETE FROM ai_chat_candidates WHERE token = ?`, token); err != nil {
			return err
		}
	}
	// Cap retained rows per chat so a long conversation cannot grow the table
	// without bound; the newest rows win.
	if _, err := tx.Exec(`DELETE FROM ai_chat_candidates WHERE user_id = ? AND chat_id = ? AND rowid NOT IN (
	SELECT rowid FROM ai_chat_candidates WHERE user_id = ? AND chat_id = ? ORDER BY rowid DESC LIMIT ?
)`, userID, chatID, userID, chatID, maxCandidatesPerChat); err != nil {
		return err
	}
	stamp := now.Format(time.RFC3339Nano)
	for _, c := range items {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO ai_chat_candidates(token, user_id, chat_id, title, artist, platform, url, name, created_at, owned, is_vip, grounded)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.Token, userID, chatID, c.Title, c.Artist, c.Platform, c.URL, c.Name, stamp,
			boolToInt(c.Owned), boolToInt(c.IsVIP), boolToInt(c.Grounded)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Candidate(token string, userID, chatID int64) (*Candidate, error) {
	c := &Candidate{}
	var createdAt string
	var owned, isVIP, grounded int
	err := s.db.QueryRow(`SELECT token, title, artist, platform, url, name, created_at, owned, is_vip, grounded
FROM ai_chat_candidates WHERE token = ? AND user_id = ? AND chat_id = ?`, token, userID, chatID).Scan(
		&c.Token, &c.Title, &c.Artist, &c.Platform, &c.URL, &c.Name, &createdAt, &owned, &isVIP, &grounded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Owned, c.IsVIP, c.Grounded = owned != 0, isVIP != 0, grounded != 0
	if created, parseErr := time.Parse(time.RFC3339Nano, createdAt); parseErr == nil {
		c.ExpiresAt = created.Add(candidateTTL)
	}
	c.BatchID = createdAt
	return c, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
