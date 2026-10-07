package musicdedupe

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const legacyMusicRAGMigration = "import_music_rag_legacy_v1"
const downloadedTracksSchemaMigration = "downloaded_tracks_nullable_time_v2"

type LegacyTrack struct {
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Album     string `json:"album"`
	URL       string `json:"url"`
	SourceURL string `json:"source_url"`
}

type ImportStats struct {
	Total           int
	Imported        int
	Skipped         int
	AlreadyImported bool
}

func ensureSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at_ns INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create shared schema migration table: %w", err)
	}

	exists, err := tableExists(db, "downloaded_tracks")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := db.Exec(downloadedTracksTableSQL("downloaded_tracks")); err != nil {
			return fmt.Errorf("create downloaded track table: %w", err)
		}
	} else {
		columns, err := tableColumns(db, "downloaded_tracks")
		if err != nil {
			return err
		}
		if !columns["record_source"] || !columns["recorded_at"] {
			if err := migrateDownloadedTracks(db); err != nil {
				return err
			}
		}
	}

	const indexes = `
CREATE INDEX IF NOT EXISTS idx_downloaded_tracks_platform_id
	ON downloaded_tracks(platform, platform_track_id);
CREATE INDEX IF NOT EXISTS idx_downloaded_tracks_canonical_url
	ON downloaded_tracks(canonical_url);
CREATE INDEX IF NOT EXISTS idx_downloaded_tracks_metadata
	ON downloaded_tracks(title_key, artist_key, album_key);
`
	if _, err := db.Exec(indexes); err != nil {
		return fmt.Errorf("create downloaded track indexes: %w", err)
	}
	return nil
}

func downloadedTracksTableSQL(table string) string {
	return fmt.Sprintf(`CREATE TABLE %s (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	platform TEXT NOT NULL DEFAULT '',
	platform_track_id TEXT NOT NULL DEFAULT '',
	source_url TEXT NOT NULL DEFAULT '',
	canonical_url TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	artist TEXT NOT NULL DEFAULT '',
	album TEXT NOT NULL DEFAULT '',
	title_key TEXT NOT NULL DEFAULT '',
	artist_key TEXT NOT NULL DEFAULT '',
	album_key TEXT NOT NULL DEFAULT '',
	record_source TEXT NOT NULL DEFAULT 'telegram',
	downloaded_at TEXT NULL,
	recorded_at TEXT NOT NULL
)`, table)
}

func migrateDownloadedTracks(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin downloaded track schema migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DROP TABLE IF EXISTS downloaded_tracks_v2`); err != nil {
		return fmt.Errorf("clear interrupted downloaded track migration: %w", err)
	}
	if _, err := tx.Exec(downloadedTracksTableSQL("downloaded_tracks_v2")); err != nil {
		return fmt.Errorf("create migrated downloaded track table: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO downloaded_tracks_v2 (
	id, platform, platform_track_id, source_url, canonical_url,
	title, artist, album, title_key, artist_key, album_key,
	record_source, downloaded_at, recorded_at
)
SELECT id, platform, platform_track_id, source_url, canonical_url,
	title, artist, album, title_key, artist_key, album_key,
	'telegram', downloaded_at, downloaded_at
FROM downloaded_tracks`); err != nil {
		return fmt.Errorf("copy downloaded track rows to migrated table: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE downloaded_tracks`); err != nil {
		return fmt.Errorf("replace old downloaded track table: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE downloaded_tracks_v2 RENAME TO downloaded_tracks`); err != nil {
		return fmt.Errorf("activate migrated downloaded track table: %w", err)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO schema_migrations(name, applied_at_ns) VALUES (?, ?)`, downloadedTracksSchemaMigration, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record downloaded track schema migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit downloaded track schema migration: %w", err)
	}
	return nil
}

func tableExists(db *sql.DB, name string) (bool, error) {
	var found string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check SQLite table %q: %w", name, err)
	}
	return true, nil
}

func tableColumns(db *sql.DB, name string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + name + `)`)
	if err != nil {
		return nil, fmt.Errorf("inspect SQLite table %q: %w", name, err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var column, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &column, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("scan SQLite table %q schema: %w", name, err)
		}
		columns[column] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read SQLite table %q schema: %w", name, err)
	}
	return columns, nil
}

func DecodeLegacyExport(data []byte) ([]LegacyTrack, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, errors.New("legacy music export is empty")
	}
	if strings.HasPrefix(trimmed, "[") {
		var rows []LegacyTrack
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, fmt.Errorf("decode legacy music export array: %w", err)
		}
		return rows, nil
	}
	var export struct {
		Rows  []LegacyTrack `json:"rows"`
		Count *int          `json:"count"`
	}
	if err := json.Unmarshal(data, &export); err != nil {
		return nil, fmt.Errorf("decode legacy music export object: %w", err)
	}
	if export.Rows == nil {
		return nil, errors.New("legacy music export must be an array or contain a rows array")
	}
	if export.Count != nil && *export.Count != len(export.Rows) {
		return nil, fmt.Errorf("legacy music export is incomplete: rows=%d count=%d", len(export.Rows), *export.Count)
	}
	return export.Rows, nil
}

// ImportMusicRAGOnce seeds legacy metadata without inventing platform IDs or
// historical download timestamps. The migration marker and inserted rows are
// committed atomically, so a retry cannot duplicate a completed import.
func (s *Store) ImportMusicRAGOnce(ctx context.Context, rows []LegacyTrack) (ImportStats, error) {
	stats := ImportStats{Total: len(rows)}
	if s == nil || s.db == nil {
		return stats, errors.New("music dedupe store is not initialized")
	}
	if len(rows) == 0 {
		return stats, errors.New("legacy music export contains no rows; refusing to mark migration complete")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("begin legacy music import: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var marker string
	err = tx.QueryRowContext(ctx, `SELECT name FROM schema_migrations WHERE name = ?`, legacyMusicRAGMigration).Scan(&marker)
	if err == nil {
		stats.AlreadyImported = true
		return stats, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return stats, fmt.Errorf("check legacy music import marker: %w", err)
	}

	recordedAt := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range rows {
		row.Title = strings.TrimSpace(row.Title)
		row.Artist = strings.TrimSpace(row.Artist)
		row.Album = strings.TrimSpace(row.Album)
		sourceURL := strings.TrimSpace(row.SourceURL)
		if sourceURL == "" {
			sourceURL = strings.TrimSpace(row.URL)
		}
		canonicalURL := CanonicalURL(sourceURL)
		titleKey, artistKey, albumKey := normalizeText(row.Title), normalizeText(row.Artist), normalizeText(row.Album)
		completeMetadata := titleKey != "" && artistKey != "" && albumKey != ""
		if !completeMetadata && canonicalURL == "" {
			stats.Skipped++
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO downloaded_tracks (
	platform, platform_track_id, source_url, canonical_url,
	title, artist, album, title_key, artist_key, album_key,
	record_source, downloaded_at, recorded_at
) VALUES ('', '', ?, ?, ?, ?, ?, ?, ?, ?, 'legacy_import', NULL, ?)`,
			sourceURL, canonicalURL, row.Title, row.Artist, row.Album,
			titleKey, artistKey, albumKey, recordedAt)
		if err != nil {
			return stats, fmt.Errorf("import legacy music row %d: %w", stats.Imported+stats.Skipped+1, err)
		}
		stats.Imported++
	}
	if stats.Imported == 0 {
		return stats, errors.New("legacy music export has no rows with a complete metadata tuple or URL")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(name, applied_at_ns) VALUES (?, ?)`, legacyMusicRAGMigration, time.Now().UTC().UnixNano()); err != nil {
		return stats, fmt.Errorf("record legacy music import marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("commit legacy music import: %w", err)
	}
	return stats, nil
}
