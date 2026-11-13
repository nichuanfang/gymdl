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
	PlaylistID   string
	Description  string
	SongIDs      []string
	Intro        string
	UsedFallback bool
}

type aiPlaylistResumeStore interface {
	Load(context.Context, string) (*aiPlaylistResumePlan, error)
	Create(context.Context, aiPlaylistResumePlan) error
	Delete(context.Context, string) error
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
	return &sqliteAIPlaylistResumeStore{db: db}, nil
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
	if _, err := s.db.ExecContext(ctx, `INSERT INTO ai_playlist_pending (
		playlist_id, description, song_ids_json, intro, used_fallback
	) VALUES (?, ?, ?, ?, ?)`, plan.PlaylistID, plan.Description, string(songIDsJSON), plan.Intro, usedFallback); err != nil {
		return fmt.Errorf("save AI playlist resume plan: %w", err)
	}
	return nil
}

func (s *sqliteAIPlaylistResumeStore) Delete(ctx context.Context, playlistID string) error {
	if s == nil || s.db == nil {
		return errors.New("AI playlist resume store is not initialized")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM ai_playlist_pending WHERE playlist_id = ?`, playlistID); err != nil {
		return fmt.Errorf("delete AI playlist resume plan: %w", err)
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
	if strings.TrimSpace(plan.Description) == "" {
		return errors.New("saved playlist description is empty")
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
	return nil
}
