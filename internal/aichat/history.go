package aichat

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// HistorySource reads the Last.fm scrobble table maintained by n8n. It is
// optional: an empty DSN or a failed query degrades to no profile.
type HistorySource struct {
	DSN       string
	Table     string
	WindowDay int
}

func (h HistorySource) Enabled() bool {
	return strings.TrimSpace(h.DSN) != "" && strings.TrimSpace(h.Table) != ""
}

// profileCacheTTL keeps one turn from opening a fresh Postgres connection and
// running four aggregations. Every message used to pay that cost.
const profileCacheTTL = 5 * time.Minute

type cachedProfile struct {
	profile *Profile
	expires time.Time
}

var (
	profileCacheMu sync.Mutex
	profileCache   = map[string]cachedProfile{}
)

func profileCacheKey(h HistorySource, days int) string {
	return strings.TrimSpace(h.DSN) + "\x00" + strings.TrimSpace(h.Table) + "\x00" + fmt.Sprint(days)
}

// Load aggregates the most recent window. When the primary window has no
// rows it retries with three times the window so an inactive month still
// yields a usable profile.
func (h HistorySource) Load(ctx context.Context) (*Profile, error) {
	if !h.Enabled() {
		return nil, nil
	}
	days := h.WindowDay
	if days <= 0 {
		days = 30
	}
	key := profileCacheKey(h, days)
	profileCacheMu.Lock()
	hit, ok := profileCache[key]
	profileCacheMu.Unlock()
	if ok && time.Now().Before(hit.expires) {
		return hit.profile, nil
	}
	profile, err := h.load(ctx)
	if err != nil {
		return nil, err
	}
	profileCacheMu.Lock()
	profileCache[key] = cachedProfile{profile: profile, expires: time.Now().Add(profileCacheTTL)}
	// Bound the map: it is keyed per configured DSN/table/window, which is a
	// small fixed set, but tests and config reloads can add entries.
	if len(profileCache) > 16 {
		for other := range profileCache {
			if other != key {
				delete(profileCache, other)
			}
		}
	}
	profileCacheMu.Unlock()
	return profile, nil
}

func (h HistorySource) load(ctx context.Context) (*Profile, error) {
	days := h.WindowDay
	if days <= 0 {
		days = 30
	}
	profile, err := h.loadWindow(ctx, days)
	if err != nil {
		return nil, err
	}
	if profile.Scrobbles == 0 && days < 90 {
		if wider, wideErr := h.loadWindow(ctx, 90); wideErr == nil {
			return wider, nil
		}
	}
	return profile, nil
}

func (h HistorySource) loadWindow(ctx context.Context, days int) (*Profile, error) {
	db, err := sql.Open("pgx", h.DSN)
	if err != nil {
		return nil, fmt.Errorf("open history database: %w", err)
	}
	defer db.Close()
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := db.PingContext(queryCtx); err != nil {
		return nil, fmt.Errorf("connect history database: %w", err)
	}
	table := strings.TrimSpace(h.Table)
	if !validTableName(table) {
		return nil, fmt.Errorf("history table name %q is not allowed", table)
	}
	profile := &Profile{WindowDays: days}
	countQuery := fmt.Sprintf(`SELECT count(*) FROM %s WHERE date::timestamptz > now() - ($1 || ' days')::interval`, table)
	if err := db.QueryRowContext(queryCtx, countQuery, days).Scan(&profile.Scrobbles); err != nil {
		return nil, fmt.Errorf("count history rows: %w", err)
	}
	if profile.Scrobbles == 0 {
		return profile, nil
	}
	artistQuery := fmt.Sprintf(`SELECT artist FROM %s WHERE date::timestamptz > now() - ($1 || ' days')::interval
AND coalesce(artist,'') <> '' GROUP BY artist ORDER BY count(*) DESC, artist LIMIT 15`, table)
	profile.Artists, err = scanStrings(ctx, db, artistQuery, days)
	if err != nil {
		return nil, fmt.Errorf("load top artists: %w", err)
	}
	trackQuery := fmt.Sprintf(`SELECT name || ' — ' || artist FROM %s WHERE date::timestamptz > now() - ($1 || ' days')::interval
AND coalesce(name,'') <> '' AND coalesce(artist,'') <> '' GROUP BY name, artist ORDER BY count(*) DESC LIMIT 15`, table)
	profile.Tracks, err = scanStrings(ctx, db, trackQuery, days)
	if err != nil {
		return nil, fmt.Errorf("load top tracks: %w", err)
	}
	albumQuery := fmt.Sprintf(`SELECT album FROM %s WHERE date::timestamptz > now() - ($1 || ' days')::interval
AND coalesce(album,'') <> '' GROUP BY album ORDER BY count(*) DESC, album LIMIT 10`, table)
	profile.Albums, err = scanStrings(ctx, db, albumQuery, days)
	if err != nil {
		return nil, fmt.Errorf("load top albums: %w", err)
	}
	return profile, nil
}

func scanStrings(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, 15)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSpace(value))
	}
	return out, rows.Err()
}

func validTableName(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for i, r := range name {
		digit := r >= '0' && r <= '9'
		letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		ok := r == '_' || letter || (digit && i > 0)
		if !ok {
			return false
		}
	}
	return true
}
