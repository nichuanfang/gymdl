package musicdedupe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/internal/storage"
)

// Track is the dedupe identity and the metadata persisted after a successful
// download. Historical records are intentionally not imported into this store.
type Track struct {
	Platform         string
	PlatformTrackID  string
	SourceURL        string
	Title            string
	Artist           string
	Album            string
	MetadataResolved bool
}

type Match struct {
	Track
	MatchedBy    string
	RecordSource string
	Downloaded   time.Time
	Recorded     time.Time
}

type Store struct {
	db *sql.DB
}

// Stats summarises the persisted download records so the agent can answer
// "how many songs do I have" from real data instead of guessing.
type Stats struct {
	Tracks    int
	Artists   int
	Platforms string
}

const DefaultDatabasePath = storage.DefaultDatabasePath

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("music dedupe database path is empty")
	}
	db, err := storage.Open(path)
	if err != nil {
		return nil, err
	}
	if err := ensureSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize music dedupe schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) FindDuplicate(ctx context.Context, track Track) (*Match, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("music dedupe store is not initialized")
	}
	track.Platform = normalizeText(track.Platform)
	track.PlatformTrackID = strings.TrimSpace(track.PlatformTrackID)
	canonicalURL := CanonicalURL(track.SourceURL)
	titleKey, artistKey, albumKey := normalizeText(track.Title), normalizeText(track.Artist), normalizeText(track.Album)

	queries := []struct {
		by   string
		sql  string
		args []any
	}{
		{
			by:   "platform_id",
			sql:  `SELECT platform, platform_track_id, source_url, title, artist, album, record_source, downloaded_at, recorded_at FROM downloaded_tracks WHERE platform = ? AND platform_track_id = ? ORDER BY id DESC LIMIT 1`,
			args: []any{track.Platform, track.PlatformTrackID},
		},
		{
			by:   "url",
			sql:  `SELECT platform, platform_track_id, source_url, title, artist, album, record_source, downloaded_at, recorded_at FROM downloaded_tracks WHERE canonical_url = ? AND canonical_url <> '' ORDER BY id DESC LIMIT 1`,
			args: []any{canonicalURL},
		},
		{
			by:   "metadata",
			sql:  `SELECT platform, platform_track_id, source_url, title, artist, album, record_source, downloaded_at, recorded_at FROM downloaded_tracks WHERE title_key = ? AND artist_key = ? AND album_key = ? AND title_key <> '' AND artist_key <> '' AND album_key <> '' ORDER BY id DESC LIMIT 1`,
			args: []any{titleKey, artistKey, albumKey},
		},
	}

	for _, query := range queries {
		if (query.by == "platform_id" && (track.Platform == "" || track.PlatformTrackID == "")) ||
			(query.by == "url" && canonicalURL == "") ||
			(query.by == "metadata" && (titleKey == "" || artistKey == "" || albumKey == "")) {
			continue
		}
		match := &Match{MatchedBy: query.by}
		var downloadedAt, recordedAt sql.NullString
		err := s.db.QueryRowContext(ctx, query.sql, query.args...).Scan(
			&match.Platform, &match.PlatformTrackID, &match.SourceURL,
			&match.Title, &match.Artist, &match.Album, &match.RecordSource, &downloadedAt, &recordedAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("query downloaded music by %s: %w", query.by, err)
		}
		if downloadedAt.Valid {
			match.Downloaded, _ = time.Parse(time.RFC3339Nano, downloadedAt.String)
		}
		if recordedAt.Valid {
			match.Recorded, _ = time.Parse(time.RFC3339Nano, recordedAt.String)
		}
		return match, nil
	}
	return nil, nil
}

func (s *Store) RecordSuccessfulDownload(ctx context.Context, track Track) error {
	if s == nil || s.db == nil {
		return errors.New("music dedupe store is not initialized")
	}
	track.Platform = normalizeText(track.Platform)
	track.PlatformTrackID = strings.TrimSpace(track.PlatformTrackID)
	track.SourceURL = strings.TrimSpace(track.SourceURL)
	track.Title = strings.TrimSpace(track.Title)
	track.Artist = strings.TrimSpace(track.Artist)
	track.Album = strings.TrimSpace(track.Album)
	canonicalURL := CanonicalURL(track.SourceURL)
	if track.Title == "" && track.Artist == "" && track.Album == "" && track.PlatformTrackID == "" && canonicalURL == "" {
		return errors.New("refusing to record a track without a usable identity")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO downloaded_tracks (
	platform, platform_track_id, source_url, canonical_url,
	title, artist, album, title_key, artist_key, album_key,
	record_source, downloaded_at, recorded_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'telegram', ?, ?)`,
		track.Platform, track.PlatformTrackID, track.SourceURL, canonicalURL,
		track.Title, track.Artist, track.Album,
		normalizeText(track.Title), normalizeText(track.Artist), normalizeText(track.Album), now, now)
	if err != nil {
		return fmt.Errorf("record successful music download: %w", err)
	}
	return nil
}

// Stats aggregates the download store. Empty artist or platform values are
// ignored so partial records do not inflate the counts.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	if s == nil || s.db == nil {
		return Stats{}, errors.New("music dedupe store is not initialized")
	}
	var stats Stats
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM downloaded_tracks`).Scan(&stats.Tracks); err != nil {
		return Stats{}, fmt.Errorf("count downloaded tracks: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(DISTINCT artist_key) FROM downloaded_tracks WHERE artist_key <> ''`).Scan(&stats.Artists); err != nil {
		return Stats{}, fmt.Errorf("count artists: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT platform, count(*) FROM downloaded_tracks WHERE platform <> '' GROUP BY platform ORDER BY count(*) DESC`)
	if err != nil {
		return Stats{}, fmt.Errorf("count platforms: %w", err)
	}
	defer rows.Close()
	parts := make([]string, 0, 4)
	for rows.Next() {
		var platform string
		var count int
		if err := rows.Scan(&platform, &count); err != nil {
			return Stats{}, err
		}
		parts = append(parts, fmt.Sprintf(`%q:%d`, platform, count))
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}
	stats.Platforms = "{" + strings.Join(parts, ",") + "}"
	return stats, nil
}

// FindByTitle looks a track up by normalised title, optionally narrowed by
// artist. It is the read-only lookup the agent uses for "do I already own this".
func (s *Store) FindByTitle(ctx context.Context, title, artist string) (*Match, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("music dedupe store is not initialized")
	}
	titleKey := normalizeText(title)
	if titleKey == "" {
		return nil, errors.New("title is empty")
	}
	query := `SELECT platform, platform_track_id, source_url, title, artist, album, record_source, downloaded_at, recorded_at
FROM downloaded_tracks WHERE title_key = ?`
	args := []any{titleKey}
	if artistKey := normalizeText(artist); artistKey != "" {
		query += ` AND artist_key = ?`
		args = append(args, artistKey)
	}
	query += ` ORDER BY id DESC LIMIT 1`

	match := &Match{MatchedBy: "metadata"}
	var downloadedAt, recordedAt sql.NullString
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&match.Platform, &match.PlatformTrackID, &match.SourceURL,
		&match.Title, &match.Artist, &match.Album, &match.RecordSource, &downloadedAt, &recordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup track by title: %w", err)
	}
	if downloadedAt.Valid {
		match.Downloaded, _ = time.Parse(time.RFC3339Nano, downloadedAt.String)
	}
	if recordedAt.Valid {
		match.Recorded, _ = time.Parse(time.RFC3339Nano, recordedAt.String)
	}
	return match, nil
}

func CanonicalURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return normalizeText(raw)
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	if port := u.Port(); port != "" && !((u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443")) {
		host += ":" + port
	}
	if host == "youtu.be" || host == "youtube.com" || host == "music.youtube.com" {
		id := u.Query().Get("v")
		if id == "" && host == "youtu.be" {
			id = strings.Trim(strings.TrimPrefix(u.Path, "/"), "/")
			if slash := strings.IndexByte(id, '/'); slash >= 0 {
				id = id[:slash]
			}
		}
		if id == "" {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed") {
				id = parts[1]
			}
		}
		if id != "" {
			return "youtube:" + id
		}
	}
	if host == "music.apple.com" {
		if id := u.Query().Get("i"); id != "" {
			return "apple-music:" + id
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 4 && parts[1] == "song" {
			return "apple-music:" + parts[len(parts)-1]
		}
	}

	query := u.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if isTrackingParam(lower) || ((host == "youtube.com" || host == "music.youtube.com") && (lower == "list" || lower == "index" || lower == "start_radio")) {
			query.Del(key)
		}
	}
	for key := range query {
		values := query[key]
		sort.Strings(values)
		query[key] = values
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path == "" {
		path = "/"
	}
	result := strings.ToLower(u.Scheme) + "://" + host + path
	if encoded := query.Encode(); encoded != "" {
		result += "?" + encoded
	}
	if host == "music.163.com" && u.Fragment != "" {
		fragment := strings.TrimPrefix(u.Fragment, "#")
		fragment = strings.TrimPrefix(fragment, "/")
		if fragment != "" {
			result += "#" + fragment
		}
	}
	return result
}

func isTrackingParam(key string) bool {
	return key == "si" || key == "feature" || key == "fbclid" || key == "gclid" ||
		key == "ref" || key == "source" || key == "spm" || key == "adtag" || strings.HasPrefix(key, "utm_")
}

func normalizeText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}
