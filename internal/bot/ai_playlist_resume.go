package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nichuanfang/gymdl/internal/storage"
)

type aiPlaylistResumePlan struct {
	PlaylistID      string
	Description     string
	SongIDs         []string
	Intro           string
	UsedFallback    bool
	Replace         bool
	Started         bool
	OriginalSongIDs []string
}

type aiPlaylistCreateIntent struct {
	OperationID  string
	Name         string
	Description  string
	Marker       string
	SongIDs      []string
	Intro        string
	UsedFallback bool
	PlaylistID   string
}

type aiPlaylistResumeStore interface {
	Load(context.Context, string) (*aiPlaylistResumePlan, error)
	Create(context.Context, aiPlaylistResumePlan) error
	Delete(context.Context, string) error
	MarkReplacementStarted(context.Context, string) error
	CreateIntent(context.Context, aiPlaylistCreateIntent) error
	LoadIntent(context.Context, string) (*aiPlaylistCreateIntent, error)
	ListIntents(context.Context) ([]aiPlaylistCreateIntent, error)
	SetIntentPlaylistID(context.Context, string, string) error
	DeleteIntent(context.Context, string) error
	Close() error
}

type sqliteAIPlaylistResumeStore struct {
	db *sql.DB
}

func openAIPlaylistResumeStore(path string) (*sqliteAIPlaylistResumeStore, error) {
	db, err := storage.Open(path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ai_playlist_pending (
		playlist_id TEXT PRIMARY KEY,
		description TEXT NOT NULL,
		song_ids_json TEXT NOT NULL,
		intro TEXT NOT NULL,
		used_fallback INTEGER NOT NULL CHECK (used_fallback IN (0, 1))
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize AI playlist resume table: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ai_playlist_pending_replace (
		playlist_id TEXT PRIMARY KEY,
		original_song_ids_json TEXT NOT NULL,
		started INTEGER NOT NULL DEFAULT 0 CHECK (started IN (0, 1))
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize AI playlist replacement snapshot table: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ai_playlist_create_intents (
		operation_id TEXT PRIMARY KEY,
		playlist_name TEXT NOT NULL,
		description TEXT NOT NULL,
		marker TEXT NOT NULL UNIQUE,
		song_ids_json TEXT NOT NULL,
		intro TEXT NOT NULL,
		used_fallback INTEGER NOT NULL CHECK (used_fallback IN (0, 1)),
		playlist_id TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize AI playlist creation intent table: %w", err)
	}
	return &sqliteAIPlaylistResumeStore{db: db}, nil
}

func (s *sqliteAIPlaylistResumeStore) CreateIntent(ctx context.Context, intent aiPlaylistCreateIntent) error {
	if s == nil || s.db == nil {
		return errors.New("AI playlist resume store is not initialized")
	}
	if err := validateAIPlaylistCreateIntent(intent); err != nil {
		return err
	}
	songIDsJSON, err := json.Marshal(intent.SongIDs)
	if err != nil {
		return fmt.Errorf("encode AI playlist creation song IDs: %w", err)
	}
	usedFallback := 0
	if intent.UsedFallback {
		usedFallback = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO ai_playlist_create_intents (
		operation_id, playlist_name, description, marker, song_ids_json, intro, used_fallback, playlist_id
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(operation_id) DO NOTHING`, intent.OperationID, intent.Name, intent.Description, intent.Marker, string(songIDsJSON), intent.Intro, usedFallback, intent.PlaylistID)
	if err != nil {
		return fmt.Errorf("save AI playlist creation intent: %w", err)
	}
	saved, err := s.LoadIntent(ctx, intent.OperationID)
	if err != nil {
		return err
	}
	if saved == nil || !sameAIPlaylistCreateIntent(*saved, intent) {
		return errors.New("AI playlist creation intent conflicts with an existing operation")
	}
	return nil
}

func (s *sqliteAIPlaylistResumeStore) LoadIntent(ctx context.Context, operationID string) (*aiPlaylistCreateIntent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("AI playlist resume store is not initialized")
	}
	var intent aiPlaylistCreateIntent
	var songIDsJSON string
	var usedFallback int
	err := s.db.QueryRowContext(ctx, `SELECT operation_id, playlist_name, description, marker, song_ids_json, intro, used_fallback, playlist_id
	FROM ai_playlist_create_intents WHERE operation_id = ?`, operationID).Scan(
		&intent.OperationID, &intent.Name, &intent.Description, &intent.Marker, &songIDsJSON, &intent.Intro, &usedFallback, &intent.PlaylistID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load AI playlist creation intent: %w", err)
	}
	if usedFallback != 0 && usedFallback != 1 {
		return nil, errors.New("saved AI playlist creation fallback flag is invalid")
	}
	if err := json.Unmarshal([]byte(songIDsJSON), &intent.SongIDs); err != nil {
		return nil, fmt.Errorf("decode AI playlist creation song IDs: %w", err)
	}
	intent.UsedFallback = usedFallback == 1
	if err := validateAIPlaylistCreateIntent(intent); err != nil {
		return nil, fmt.Errorf("invalid saved AI playlist creation intent: %w", err)
	}
	return &intent, nil
}

func (s *sqliteAIPlaylistResumeStore) ListIntents(ctx context.Context) ([]aiPlaylistCreateIntent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("AI playlist resume store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT operation_id, playlist_name, description, marker, song_ids_json, intro, used_fallback, playlist_id
	FROM ai_playlist_create_intents ORDER BY operation_id`)
	if err != nil {
		return nil, fmt.Errorf("list AI playlist creation intents: %w", err)
	}
	defer rows.Close()
	intents := make([]aiPlaylistCreateIntent, 0)
	for rows.Next() {
		var intent aiPlaylistCreateIntent
		var songIDsJSON string
		var usedFallback int
		if err := rows.Scan(&intent.OperationID, &intent.Name, &intent.Description, &intent.Marker, &songIDsJSON, &intent.Intro, &usedFallback, &intent.PlaylistID); err != nil {
			return nil, fmt.Errorf("read AI playlist creation intent: %w", err)
		}
		if usedFallback != 0 && usedFallback != 1 {
			return nil, errors.New("saved AI playlist creation fallback flag is invalid")
		}
		if err := json.Unmarshal([]byte(songIDsJSON), &intent.SongIDs); err != nil {
			return nil, fmt.Errorf("decode AI playlist creation song IDs: %w", err)
		}
		intent.UsedFallback = usedFallback == 1
		if err := validateAIPlaylistCreateIntent(intent); err != nil {
			return nil, fmt.Errorf("invalid saved AI playlist creation intent: %w", err)
		}
		intents = append(intents, intent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate AI playlist creation intents: %w", err)
	}
	return intents, nil
}

func (s *sqliteAIPlaylistResumeStore) SetIntentPlaylistID(ctx context.Context, operationID, playlistID string) error {
	if s == nil || s.db == nil {
		return errors.New("AI playlist resume store is not initialized")
	}
	if !safeNavidromeID(playlistID) {
		return errors.New("created AI playlist ID is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ai_playlist_create_intents SET playlist_id = ? WHERE operation_id = ? AND (playlist_id = '' OR playlist_id = ?)`, playlistID, operationID, playlistID)
	if err != nil {
		return fmt.Errorf("save created AI playlist ID: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check created AI playlist ID: %w", err)
	}
	if rows != 1 {
		return errors.New("AI playlist creation intent is missing or points to another playlist")
	}
	return nil
}

func (s *sqliteAIPlaylistResumeStore) DeleteIntent(ctx context.Context, operationID string) error {
	if s == nil || s.db == nil {
		return errors.New("AI playlist resume store is not initialized")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM ai_playlist_create_intents WHERE operation_id = ?`, operationID); err != nil {
		return fmt.Errorf("delete AI playlist creation intent: %w", err)
	}
	return nil
}

func validateAIPlaylistCreateIntent(intent aiPlaylistCreateIntent) error {
	if !safeAIPlaylistOperationID(intent.OperationID) || strings.TrimSpace(intent.Name) == "" || strings.TrimSpace(intent.Description) == "" || strings.TrimSpace(intent.Marker) == "" {
		return errors.New("AI playlist creation intent is incomplete")
	}
	if intent.Marker != "gymdl-ai-playlist-create:"+intent.OperationID {
		return errors.New("AI playlist creation marker does not match its operation")
	}
	if strings.TrimSpace(intent.Name) != intent.Name || !strings.HasPrefix(intent.Name, "[AI] ") || strings.TrimSpace(strings.TrimPrefix(intent.Name, "[AI] ")) == "" || len([]rune(intent.Name)) > 100 {
		return errors.New("AI playlist creation name is invalid")
	}
	if len(intent.SongIDs) == 0 {
		return errors.New("AI playlist creation has no song IDs")
	}
	seen := make(map[string]struct{}, len(intent.SongIDs))
	for _, id := range intent.SongIDs {
		if !safeNavidromeID(id) {
			return errors.New("AI playlist creation contains an invalid song ID")
		}
		if _, exists := seen[id]; exists {
			return errors.New("AI playlist creation contains duplicate song IDs")
		}
		seen[id] = struct{}{}
	}
	if intent.PlaylistID != "" && !safeNavidromeID(intent.PlaylistID) {
		return errors.New("AI playlist creation contains an invalid playlist ID")
	}
	return nil
}

func safeAIPlaylistOperationID(value string) bool {
	if len(value) < 8 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func sameAIPlaylistCreateIntent(first, second aiPlaylistCreateIntent) bool {
	if first.OperationID != second.OperationID || first.Name != second.Name || first.Description != second.Description || first.Marker != second.Marker || first.Intro != second.Intro || first.UsedFallback != second.UsedFallback || (first.PlaylistID != "" && second.PlaylistID != "" && first.PlaylistID != second.PlaylistID) || len(first.SongIDs) != len(second.SongIDs) {
		return false
	}
	for index := range first.SongIDs {
		if first.SongIDs[index] != second.SongIDs[index] {
			return false
		}
	}
	return true
}

func (s *sqliteAIPlaylistResumeStore) Load(ctx context.Context, playlistID string) (*aiPlaylistResumePlan, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("AI playlist resume store is not initialized")
	}
	var plan aiPlaylistResumePlan
	var songIDsJSON string
	var usedFallback int
	err := s.db.QueryRowContext(ctx, `SELECT playlist_id, description, song_ids_json, intro, used_fallback
	FROM ai_playlist_pending WHERE playlist_id = ?`, playlistID).Scan(
		&plan.PlaylistID, &plan.Description, &songIDsJSON, &plan.Intro, &usedFallback,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load AI playlist resume plan: %w", err)
	}
	if usedFallback != 0 && usedFallback != 1 {
		return nil, errors.New("saved AI playlist fallback flag is invalid")
	}
	if err := json.Unmarshal([]byte(songIDsJSON), &plan.SongIDs); err != nil {
		return nil, fmt.Errorf("decode AI playlist resume song IDs: %w", err)
	}
	var originalSongIDsJSON string
	var started int
	err = s.db.QueryRowContext(ctx, `SELECT original_song_ids_json, started FROM ai_playlist_pending_replace WHERE playlist_id = ?`, playlistID).Scan(&originalSongIDsJSON, &started)
	if err == nil {
		if started != 0 && started != 1 {
			return nil, errors.New("saved AI playlist replacement state is invalid")
		}
		if err := json.Unmarshal([]byte(originalSongIDsJSON), &plan.OriginalSongIDs); err != nil {
			return nil, fmt.Errorf("decode AI playlist replacement snapshot: %w", err)
		}
		plan.Replace = true
		plan.Started = started == 1
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load AI playlist replacement snapshot: %w", err)
	}
	plan.UsedFallback = usedFallback == 1
	if err := validateAIPlaylistResumePlan(plan); err != nil {
		return nil, fmt.Errorf("invalid saved AI playlist resume plan: %w", err)
	}
	return &plan, nil
}

func (s *sqliteAIPlaylistResumeStore) Create(ctx context.Context, plan aiPlaylistResumePlan) error {
	if s == nil || s.db == nil {
		return errors.New("AI playlist resume store is not initialized")
	}
	if err := validateAIPlaylistResumePlan(plan); err != nil {
		return err
	}
	songIDsJSON, err := json.Marshal(plan.SongIDs)
	if err != nil {
		return fmt.Errorf("encode AI playlist resume song IDs: %w", err)
	}
	usedFallback := 0
	if plan.UsedFallback {
		usedFallback = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin AI playlist resume transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_playlist_pending (
		playlist_id, description, song_ids_json, intro, used_fallback
	) VALUES (?, ?, ?, ?, ?)`, plan.PlaylistID, plan.Description, string(songIDsJSON), plan.Intro, usedFallback); err != nil {
		return fmt.Errorf("save AI playlist resume plan: %w", err)
	}
	if plan.Replace {
		originalSongIDsJSON, err := json.Marshal(plan.OriginalSongIDs)
		if err != nil {
			return fmt.Errorf("encode AI playlist replacement snapshot: %w", err)
		}
		started := 0
		if plan.Started {
			started = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_playlist_pending_replace (playlist_id, original_song_ids_json, started) VALUES (?, ?, ?)`, plan.PlaylistID, string(originalSongIDsJSON), started); err != nil {
			return fmt.Errorf("save AI playlist replacement snapshot: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx, `DELETE FROM ai_playlist_pending_replace WHERE playlist_id = ?`, plan.PlaylistID); err != nil {
		return fmt.Errorf("clear old AI playlist replacement snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit AI playlist resume plan: %w", err)
	}
	return nil
}

func (s *sqliteAIPlaylistResumeStore) MarkReplacementStarted(ctx context.Context, playlistID string) error {
	if s == nil || s.db == nil {
		return errors.New("AI playlist resume store is not initialized")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ai_playlist_pending_replace SET started = 1 WHERE playlist_id = ?`, playlistID)
	if err != nil {
		return fmt.Errorf("mark AI playlist replacement started: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check AI playlist replacement state: %w", err)
	}
	if rows != 1 {
		return errors.New("AI playlist replacement snapshot is missing")
	}
	return nil
}

func (s *sqliteAIPlaylistResumeStore) Delete(ctx context.Context, playlistID string) error {
	if s == nil || s.db == nil {
		return errors.New("AI playlist resume store is not initialized")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin AI playlist resume cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM ai_playlist_pending WHERE playlist_id = ?`, playlistID); err != nil {
		return fmt.Errorf("delete AI playlist resume plan: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ai_playlist_pending_replace WHERE playlist_id = ?`, playlistID); err != nil {
		return fmt.Errorf("delete AI playlist replacement snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit AI playlist resume cleanup: %w", err)
	}
	return nil
}

func (s *sqliteAIPlaylistResumeStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func validateAIPlaylistResumePlan(plan aiPlaylistResumePlan) error {
	if !safeNavidromeID(plan.PlaylistID) {
		return errors.New("saved playlist ID is invalid")
	}
	if len(plan.SongIDs) == 0 {
		return errors.New("saved playlist has no song IDs")
	}
	seen := make(map[string]struct{}, len(plan.SongIDs))
	for _, id := range plan.SongIDs {
		if !safeNavidromeID(id) {
			return errors.New("saved playlist contains an invalid song ID")
		}
		if _, exists := seen[id]; exists {
			return errors.New("saved playlist contains duplicate song IDs")
		}
		seen[id] = struct{}{}
	}
	if !plan.Replace && (plan.Started || len(plan.OriginalSongIDs) > 0) {
		return errors.New("append resume plan must not include a replacement snapshot")
	}
	if plan.Replace {
		for _, id := range plan.OriginalSongIDs {
			if !safeNavidromeID(id) {
				return errors.New("replacement snapshot contains an invalid song ID")
			}
		}
	}
	return nil
}
