package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nichuanfang/gymdl/utils"
)

const (
	tastePoolTarget   = 30
	tasteSearchGap    = 5 * time.Minute
	tasteLimitFloor   = 30 * time.Minute
	tasteLimitCeiling = 6 * time.Hour
	tastePoolIdle     = 10 * time.Minute
)

func (s *Store) ensureTastePool() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS discovery_taste_pool(
user_id INTEGER NOT NULL, identity TEXT NOT NULL, platform TEXT NOT NULL,
track_json TEXT NOT NULL, created INTEGER NOT NULL, PRIMARY KEY(user_id, identity));
CREATE TABLE IF NOT EXISTS discovery_search_gate(
platform TEXT PRIMARY KEY, next_unix INTEGER NOT NULL, backoff_seconds INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS discovery_taste_cursor(
user_id INTEGER PRIMARY KEY, next_index INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS discovery_daily_cache(
cache_key TEXT PRIMARY KEY, day TEXT NOT NULL, fetched INTEGER NOT NULL,
tracks_json TEXT NOT NULL, last_error TEXT NOT NULL DEFAULT '');`)
	if err != nil {
		return fmt.Errorf("prepare taste recommendation pool: %w", err)
	}
	return nil
}

func (s *Store) SearchBlocked(ctx context.Context, platform string, now time.Time) (time.Duration, bool, error) {
	var next int64
	err := s.db.QueryRowContext(ctx, `SELECT next_unix FROM discovery_search_gate WHERE platform=?`, platform).Scan(&next)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	until := time.Unix(next, 0)
	if !now.Before(until) {
		return 0, false, nil
	}
	return until.Sub(now), true, nil
}

func (s *Store) NoteSearchSuccess(ctx context.Context, platform string, now time.Time, wait time.Duration) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO discovery_search_gate(platform, next_unix, backoff_seconds) VALUES(?,?,0)
ON CONFLICT(platform) DO UPDATE SET next_unix=excluded.next_unix, backoff_seconds=0`,
		platform, now.Add(wait).Unix())
	return err
}

func (s *Store) NoteSearchLimit(ctx context.Context, platform string, now time.Time) (time.Duration, error) {
	var backoff int64
	err := s.db.QueryRowContext(ctx, `SELECT backoff_seconds FROM discovery_search_gate WHERE platform=?`, platform).Scan(&backoff)
	if err == sql.ErrNoRows {
		err = nil
	}
	if err != nil {
		return 0, err
	}
	if backoff < int64(tasteLimitFloor/time.Second) {
		backoff = int64(tasteLimitFloor / time.Second)
	} else {
		backoff *= 2
	}
	if backoff > int64(tasteLimitCeiling/time.Second) {
		backoff = int64(tasteLimitCeiling / time.Second)
	}
	wait := time.Duration(backoff) * time.Second
	_, err = s.db.ExecContext(ctx, `INSERT INTO discovery_search_gate(platform, next_unix, backoff_seconds) VALUES(?,?,?)
ON CONFLICT(platform) DO UPDATE SET next_unix=excluded.next_unix, backoff_seconds=excluded.backoff_seconds`,
		platform, now.Add(wait).Unix(), backoff)
	return wait, err
}

func (s *Store) LoadDailyCache(ctx context.Context, key string) ([]Track, string, int64, string, bool) {
	var day, raw, lastError string
	var fetched int64
	err := s.db.QueryRowContext(ctx, `SELECT day, fetched, tracks_json, last_error FROM discovery_daily_cache WHERE cache_key=?`, key).Scan(&day, &fetched, &raw, &lastError)
	if err != nil {
		return nil, "", 0, "", false
	}
	var tracks []Track
	if raw != "" && raw != "null" {
		if json.Unmarshal([]byte(raw), &tracks) != nil {
			return nil, "", 0, "", false
		}
	}
	return tracks, day, fetched, lastError, true
}

func (s *Store) SaveDailyCache(ctx context.Context, key, day string, tracks []Track, lastError string) error {
	raw, err := json.Marshal(tracks)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO discovery_daily_cache(cache_key, day, fetched, tracks_json, last_error) VALUES(?,?,?,?,?)
ON CONFLICT(cache_key) DO UPDATE SET day=excluded.day, fetched=excluded.fetched, tracks_json=excluded.tracks_json, last_error=excluded.last_error`,
		key, day, time.Now().Unix(), string(raw), lastError)
	return err
}

func (s *Store) CountTastePool(ctx context.Context, user int64, platform string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM discovery_taste_pool WHERE user_id=? AND platform=?`, user, platform).Scan(&count)
	return count, err
}

func (s *Store) SaveTastePool(ctx context.Context, user int64, tracks []Track) error {
	now := time.Now().Unix()
	for _, track := range tracks {
		raw, err := json.Marshal(track)
		if err != nil {
			return err
		}
		identity := Identity(track)
		if _, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO discovery_taste_pool(user_id, identity, platform, track_json, created)
SELECT ?,?,?,?,? WHERE NOT EXISTS (
SELECT 1 FROM discovery_preferences WHERE user_id=? AND identity=? AND (shown=1 OR dismissed=1))`,
			user, identity, track.Platform, string(raw), now, user, identity); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListTastePool(ctx context.Context, user int64, platform string, limit int) ([]Track, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT track_json FROM discovery_taste_pool WHERE user_id=? AND platform=? ORDER BY created, identity LIMIT ?`, user, platform, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tracks []Track
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var track Track
		if err = json.Unmarshal([]byte(raw), &track); err != nil {
			return nil, err
		}
		tracks = append(tracks, track)
	}
	return tracks, rows.Err()
}

func (s *Store) DeleteTasteCandidate(ctx context.Context, user int64, track Track) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM discovery_taste_pool WHERE user_id=? AND identity=?`, user, Identity(track))
	return err
}

func (s *Store) TakeTasteCandidate(ctx context.Context, user int64, platform string) (Track, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Track{}, false, err
	}
	defer tx.Rollback()
	var identity, raw string
	err = tx.QueryRowContext(ctx, `SELECT identity, track_json FROM discovery_taste_pool WHERE user_id=? AND platform=? ORDER BY created, identity LIMIT 1`, user, platform).Scan(&identity, &raw)
	if err == sql.ErrNoRows {
		return Track{}, false, nil
	}
	if err != nil {
		return Track{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM discovery_taste_pool WHERE user_id=? AND identity=?`, user, identity); err != nil {
		return Track{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Track{}, false, err
	}
	var track Track
	if err = json.Unmarshal([]byte(raw), &track); err != nil {
		return Track{}, false, err
	}
	return track, true, nil
}

func (s *Store) TasteUserIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT user_id FROM discovery_bindings ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) NextTasteArtist(ctx context.Context, user int64, count int) (int, error) {
	if count <= 0 {
		return 0, nil
	}
	var next int
	err := s.db.QueryRowContext(ctx, `SELECT next_index FROM discovery_taste_cursor WHERE user_id=?`, user).Scan(&next)
	if err == sql.ErrNoRows {
		next = 0
		err = nil
	}
	if err != nil {
		return 0, err
	}
	current := next % count
	if _, err = s.db.ExecContext(ctx, `INSERT INTO discovery_taste_cursor(user_id, next_index) VALUES(?,?)
ON CONFLICT(user_id) DO UPDATE SET next_index=excluded.next_index`, user, current+1); err != nil {
		return 0, err
	}
	return current, nil
}

func (s *Service) runTasteRefill(ctx context.Context) {
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			wait := s.refillTaste(ctx)
			if wait < time.Minute {
				wait = time.Minute
			}
			timer.Reset(wait)
		}
	}
}

func (s *Service) refillTaste(ctx context.Context) time.Duration {
	if s == nil || s.Store == nil || s.Taste == nil {
		return tastePoolIdle
	}
	now := time.Now()
	if wait, blocked, err := s.Store.SearchBlocked(ctx, "netease", now); err != nil {
		utils.WarnWithFormat("[Discovery] 读取网易云搜索冷却失败")
		return tastePoolIdle
	} else if blocked {
		return wait
	}
	users, err := s.Store.TasteUserIDs(ctx)
	if err != nil {
		utils.WarnWithFormat("[Discovery] 读取推荐用户失败")
		return tastePoolIdle
	}
	if s.Taste.HistoryOwner != 0 {
		found := false
		for _, id := range users {
			if id == s.Taste.HistoryOwner {
				found = true
				break
			}
		}
		if !found {
			users = append(users, s.Taste.HistoryOwner)
		}
	}
	if len(users) == 0 {
		return tastePoolIdle
	}
	user := users[s.tasteRefillUser%len(users)]
	s.tasteRefillUser++
	count, err := s.Store.CountTastePool(ctx, user, "netease")
	if err != nil {
		return tastePoolIdle
	}
	if count >= tastePoolTarget {
		return tastePoolIdle
	}
	artists := s.tasteRefillArtists(ctx, user)
	if len(artists) == 0 {
		return tastePoolIdle
	}
	index, err := s.Store.NextTasteArtist(ctx, user, len(artists))
	if err != nil {
		return tastePoolIdle
	}
	query, cancel := context.WithTimeout(ctx, 8*time.Second)
	tracks, err := s.searchTaste(query, "netease", artists[index].name, 0)
	cancel()
	if err != nil {
		if tasteSearchRejected(err) {
			wait, noteErr := s.Store.NoteSearchLimit(ctx, "netease", time.Now())
			if noteErr != nil {
				wait = tasteLimitFloor
			}
			utils.WarnWithFormat("[Discovery] 网易云搜索被限流，暂停实时推荐，%s 后再试一位艺人", wait.Round(time.Minute))
			return wait
		}
		utils.WarnWithFormat("[Discovery] 网易云后台补歌失败，10 分钟后再试")
		_ = s.Store.NoteSearchSuccess(ctx, "netease", time.Now(), tastePoolIdle)
		return tastePoolIdle
	}
	favorites, favErr := s.Store.Favorites(ctx, user)
	if favErr != nil {
		return tastePoolIdle
	}
	var history []Track
	if s.Taste.HistoryOwner != 0 && user == s.Taste.HistoryOwner && s.History != nil {
		historyCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		history, _ = s.cachedHistory(historyCtx)
		cancel()
	}
	known := tasteKnownTracks(history, favorites)
	exclusions, _ := s.Store.exclusions(ctx, user, true)
	kept := make([]Track, 0, len(tracks))
	for _, track := range tracks {
		if !verifiedTasteTrack(track, "netease", artists[index].name) || exclusions[Identity(track)] != "" ||
			tasteHasSong(track, known[ownershipTitle(track.Title)]) {
			continue
		}
		kept = append(kept, track)
	}
	if room := tastePoolTarget - count; room < len(kept) {
		if room < 0 {
			room = 0
		}
		kept = kept[:room]
	}
	if err = s.Store.SaveTastePool(ctx, user, kept); err != nil {
		utils.WarnWithFormat("[Discovery] 保存网易云推荐池失败")
		return tastePoolIdle
	}
	if err = s.Store.NoteSearchSuccess(ctx, "netease", time.Now(), tasteSearchGap); err != nil {
		return tasteSearchGap
	}
	return tasteSearchGap
}

func (s *Service) tasteRefillArtists(ctx context.Context, user int64) []tasteArtist {
	favorites, err := s.Store.Favorites(ctx, user)
	if err != nil {
		return nil
	}
	var history []Track
	if s.Taste != nil && s.Taste.HistoryOwner != 0 && user == s.Taste.HistoryOwner && s.History != nil {
		historyCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		history, _ = s.cachedHistory(historyCtx)
		cancel()
	}
	artists, _ := rankTasteArtists(history, favorites, time.Now())
	return artists
}

func (s *Service) recommendPooledTaste(ctx context.Context, user int64, req TasteRequest) (TasteResult, error) {
	if s.Library == nil {
		return TasteResult{}, fmt.Errorf("暂时无法核对曲库，请稍后重试")
	}
	exclusions, err := s.Store.exclusions(ctx, user, true)
	if err != nil {
		return TasteResult{}, fmt.Errorf("暂时无法核对入库队列，请稍后重试")
	}
	queued, err := s.Store.ListTastePool(ctx, user, "netease", tastePoolTarget)
	if err != nil {
		return TasteResult{}, fmt.Errorf("暂时读不到备好的推荐")
	}
	queued = s.hydrateNeteaseCovers(ctx, queued, tastePoolTarget)
	for _, track := range queued {
		if exclusions[Identity(track)] != "" || !verifiedTasteTrack(track, "netease", track.Artist) {
			_ = s.Store.DeleteTasteCandidate(ctx, user, track)
			continue
		}
		owned, err := s.Library.Contains(ctx, track)
		if err != nil {
			return TasteResult{}, fmt.Errorf("暂时无法核对曲库，请稍后重试")
		}
		if owned {
			_ = s.Store.DeleteTasteCandidate(ctx, user, track)
			continue
		}
		if s.Pending != nil {
			pending, err := s.Pending(ctx, track)
			if err != nil {
				return TasteResult{}, fmt.Errorf("暂时无法核对入库队列，请稍后重试")
			}
			if pending {
				_ = s.Store.DeleteTasteCandidate(ctx, user, track)
				continue
			}
		}
		if err = s.Store.DeleteTasteCandidate(ctx, user, track); err != nil {
			return TasteResult{}, fmt.Errorf("暂时读不到备好的推荐")
		}
		notice := ""
		if req.Filter.Label() != "" {
			notice = "网易云正在冷却，这首先按备好的口味给你，没有按场景现搜。"
		}
		return TasteResult{Track: &track, Reason: "来自你常听或收藏的艺人", Notice: notice, Source: "netease_pool"}, nil
	}
	if track, ok, notice, err := s.pickDailyTaste(ctx, user, exclusions); err != nil {
		return TasteResult{}, err
	} else if ok {
		return TasteResult{Track: &track, Reason: "来自网易云每日推荐", Notice: notice, Source: "netease_daily"}, nil
	} else if notice != "" {
		return TasteResult{Notice: notice}, nil
	}
	return TasteResult{Notice: s.pooledTasteNotice(ctx)}, nil
}

func (s *Service) pickDailyTaste(ctx context.Context, user int64, exclusions map[string]string) (Track, bool, string, error) {
	tracks, err := s.dailyTracks(ctx)
	if err != nil {
		if tasteSearchRejected(err) {
			return Track{}, false, "网易云每日推荐暂时也受限。艺人池子还在冷却，过一会儿再来。", nil
		}
		return Track{}, false, "", nil
	}
	tracks = s.hydrateNeteaseCovers(ctx, tracks, 30)
	favorites, err := s.Store.Favorites(ctx, user)
	if err != nil {
		return Track{}, false, "", err
	}
	known := tasteKnownTracks(nil, favorites)
	for _, track := range tracks {
		if exclusions[Identity(track)] != "" || !verifiedTasteTrack(track, "netease", track.Artist) ||
			tasteHasSong(track, known[ownershipTitle(track.Title)]) {
			continue
		}
		owned, err := s.Library.Contains(ctx, track)
		if err != nil {
			return Track{}, false, "", fmt.Errorf("暂时无法核对曲库，请稍后重试")
		}
		if owned {
			continue
		}
		return track, true, "艺人池子暂时空了，先从今天的每日推荐里拿。", nil
	}
	return Track{}, false, "艺人池子和今天的每日推荐都没有新歌了。后台还在慢慢补。", nil
}

func (s *Service) dailyTracks(ctx context.Context) ([]Track, error) {
	if s.Taste == nil || s.Taste.Daily == nil {
		return nil, errors.New("每日推荐不可用")
	}
	today := time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	if tracks, day, fetched, cachedErr, ok := s.Store.LoadDailyCache(ctx, "netease"); ok && day == today {
		if cachedErr == "" {
			return tracks, nil
		}
		if time.Since(time.Unix(fetched, 0)) < 6*time.Hour {
			return nil, errors.New(cachedErr)
		}
	}
	tracks, err := s.Taste.Daily(ctx)
	if err != nil {
		_ = s.Store.SaveDailyCache(ctx, "netease", today, nil, err.Error())
		return nil, err
	}
	if err = s.Store.SaveDailyCache(ctx, "netease", today, tracks, ""); err != nil {
		return tracks, nil
	}
	return tracks, nil
}

func (s *Service) pooledTasteNotice(ctx context.Context) string {
	wait, blocked, err := s.Store.SearchBlocked(ctx, "netease", time.Now())
	if err == nil && blocked {
		minutes := int((wait + time.Minute - 1) / time.Minute)
		if minutes < 1 {
			minutes = 1
		}
		return fmt.Sprintf("网易云说操作太频繁。实时连搜已经停了，后台大约 %d 分钟后才会再找一位艺人。", minutes)
	}
	return "新歌由后台一次只找一位艺人，现在还没备好。过一会儿再打开就行。"
}
