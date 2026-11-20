package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/internal/storage"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := storage.Open(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS discovery_bindings(user_id INTEGER NOT NULL,chat_id INTEGER NOT NULL,platform TEXT NOT NULL,account TEXT NOT NULL,paused INTEGER NOT NULL DEFAULT 0,paused_by_user INTEGER NOT NULL DEFAULT 0,last_sync INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(user_id,platform));
 CREATE TABLE IF NOT EXISTS discovery_favorites(user_id INTEGER NOT NULL,platform TEXT NOT NULL,account TEXT NOT NULL,track_id TEXT NOT NULL,track_json TEXT NOT NULL,active INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(user_id,platform,account,track_id));
 CREATE TABLE IF NOT EXISTS discovery_jobs(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL,chat_id INTEGER NOT NULL,identity TEXT NOT NULL,track_json TEXT NOT NULL,state TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,next_run INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',updated INTEGER NOT NULL,result TEXT NOT NULL DEFAULT '',source_platform TEXT NOT NULL DEFAULT '',source_account TEXT NOT NULL DEFAULT '',notified INTEGER NOT NULL DEFAULT 0,UNIQUE(user_id,identity));
 CREATE INDEX IF NOT EXISTS discovery_job_ready ON discovery_jobs(state,next_run);
 CREATE TABLE IF NOT EXISTS discovery_preferences(user_id INTEGER NOT NULL,identity TEXT NOT NULL,shown INTEGER NOT NULL DEFAULT 0,dismissed INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(user_id,identity));
 CREATE INDEX IF NOT EXISTS discovery_preference_user ON discovery_preferences(user_id,dismissed,shown);
 CREATE TABLE IF NOT EXISTS discovery_cards(token TEXT PRIMARY KEY,user_id INTEGER NOT NULL,chat_id INTEGER NOT NULL,payload TEXT NOT NULL,expires INTEGER NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = s.ensureBindingPauseColumn(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// ensureBindingPauseColumn upgrades older databases in place. It is idempotent
// and repairs rows from the older boolean-only pause state.
func (s *Store) ensureBindingPauseColumn() error {
	exists, err := s.hasColumn("discovery_bindings", "paused_by_user")
	if err != nil {
		return fmt.Errorf("inspect discovery binding pause state: %w", err)
	}
	if !exists {
		if _, err = s.db.Exec(`ALTER TABLE discovery_bindings ADD COLUMN paused_by_user INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add discovery binding pause state: %w", err)
		}
	}
	if _, err = s.db.Exec(`UPDATE discovery_bindings
SET paused_by_user=CASE WHEN paused=1 AND paused_by_user=0 AND trim(last_error)='' THEN 1 ELSE paused_by_user END,
    paused=CASE WHEN paused=1 AND paused_by_user=0 AND trim(last_error)<>'' THEN 0 ELSE paused END
WHERE paused=1 AND paused_by_user=0`); err != nil {
		return fmt.Errorf("migrate discovery binding pause state: %w", err)
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
		if err = rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }
func token() string           { return uuid.NewString()[:8] + uuid.NewString()[:8] }
func (s *Store) Bindings(ctx context.Context, user int64) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id,chat_id,platform,account,paused,paused_by_user,last_sync,last_error FROM discovery_bindings WHERE (?=0 OR user_id=?) ORDER BY platform`, user, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Binding
	for rows.Next() {
		var b Binding
		if err = rows.Scan(&b.User, &b.Chat, &b.Platform, &b.Account, &b.Paused, &b.PausedByUser, &b.LastSync, &b.Error); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func validSnapshot(s Snapshot) error {
	if !s.Complete || s.Account == "" || (s.Platform != "netease" && s.Platform != "qq" && s.Platform != "youtube") {
		return fmt.Errorf("收藏快照不完整，未更新基线")
	}
	ids := map[string]bool{}
	for _, t := range s.Tracks {
		if t.ID == "" || t.Platform != s.Platform || ids[t.ID] {
			return fmt.Errorf("收藏分页重复或歌曲身份缺失，未更新基线")
		}
		ids[t.ID] = true
	}
	return nil
}

// Apply atomically records the whole snapshot and queues only new favourites.
// Rebinding is explicit and makes a new baseline; it never imports the backlog.
// The returned tracks are only the favorites newly queued by this call.
func (s *Store) Apply(ctx context.Context, user, chat int64, snap Snapshot, bind bool) (int, []Track, error) {
	if err := validSnapshot(snap); err != nil {
		return 0, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	var account string
	var paused, pausedByUser bool
	err = tx.QueryRowContext(ctx, `SELECT account,paused,paused_by_user FROM discovery_bindings WHERE user_id=? AND platform=?`, user, snap.Platform).Scan(&account, &paused, &pausedByUser)
	if !bind {
		if err != nil {
			return 0, nil, fmt.Errorf("请先绑定收藏账号")
		}
		if paused || pausedByUser {
			return 0, nil, fmt.Errorf("该来源已暂停")
		}
		if account != snap.Account {
			return 0, nil, fmt.Errorf("收藏账号已变化，请重新预览并绑定；原基线保留")
		}
	}
	known := map[string]bool{}
	if !bind {
		rows, e := tx.QueryContext(ctx, `SELECT track_id FROM discovery_favorites WHERE user_id=? AND platform=? AND account=? AND active=1`, user, snap.Platform, snap.Account)
		if e != nil {
			return 0, nil, e
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return 0, nil, e
			}
			known[id] = true
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return 0, nil, e
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE discovery_favorites SET active=0 WHERE user_id=? AND platform=? AND account=?`, user, snap.Platform, snap.Account); err != nil {
		return 0, nil, err
	}
	count := 0
	var fresh []Track
	now := time.Now().Unix()
	for _, t := range snap.Tracks {
		if !t.Verified {
			var previous string
			if e := tx.QueryRowContext(ctx, `SELECT track_json FROM discovery_favorites WHERE user_id=? AND platform=? AND account=? AND track_id=?`, user, snap.Platform, snap.Account, t.ID).Scan(&previous); e == nil {
				var old Track
				if json.Unmarshal([]byte(previous), &old) == nil && (old.Verified || old.NonMusic) {
					t = old
				}
			}
		}
		t.Favorite = true
		t.CardAction = ""
		t.IngestID = ""
		t.IngestNote = ""
		data, e := json.Marshal(t)
		if e != nil {
			return 0, nil, e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO discovery_favorites(user_id,platform,account,track_id,track_json,active) VALUES(?,?,?,?,?,1) ON CONFLICT(user_id,platform,account,track_id) DO UPDATE SET track_json=excluded.track_json,active=1`, user, snap.Platform, snap.Account, t.ID, string(data)); err != nil {
			return 0, nil, err
		}
		if !bind && !known[t.ID] {
			res, e := enqueueTx(ctx, tx, user, chat, t, now, snap.Account, true)
			if e != nil {
				return 0, nil, e
			}
			n, _ := res.RowsAffected()
			count += int(n)
			if n > 0 {
				fresh = append(fresh, t)
			}
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO discovery_bindings(user_id,chat_id,platform,account,paused,paused_by_user,last_sync,last_error) VALUES(?,?,?,?,0,0,?,'') ON CONFLICT(user_id,platform) DO UPDATE SET chat_id=excluded.chat_id,account=excluded.account,paused=0,paused_by_user=0,last_sync=excluded.last_sync,last_error=''`, user, chat, snap.Platform, snap.Account, now)
	if err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(); err != nil {
		return 0, nil, err
	}
	return count, fresh, nil
}

func enqueueTx(ctx context.Context, tx *sql.Tx, user, chat int64, t Track, now int64, account string, requeueCompleted bool) (sql.Result, error) {
	data, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	if !requeueCompleted {
		return tx.ExecContext(ctx, `INSERT INTO discovery_jobs(id,user_id,chat_id,identity,track_json,state,updated,source_platform,source_account) VALUES(?,?,?,?,?,'queued',?,?,?) ON CONFLICT(user_id,identity) DO NOTHING`, token(), user, chat, Identity(t), string(data), now, t.Platform, account)
	}
	// A song that already finished can be liked again. Queued, running, failed,
	// ignored, and owned rows stay as they are, and a bind never reaches here.
	return tx.ExecContext(ctx, `INSERT INTO discovery_jobs(id,user_id,chat_id,identity,track_json,state,updated,source_platform,source_account) VALUES(?,?,?,?,?,'queued',?,?,?) ON CONFLICT(user_id,identity) DO UPDATE SET chat_id=excluded.chat_id,track_json=excluded.track_json,state='queued',attempts=0,next_run=0,last_error='',updated=excluded.updated,result='',source_platform=excluded.source_platform,source_account=excluded.source_account,notified=0 WHERE discovery_jobs.state='completed'`, token(), user, chat, Identity(t), string(data), now, t.Platform, account)
}

func (s *Store) Enqueue(ctx context.Context, user, chat int64, t Track) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = enqueueTx(ctx, tx, user, chat, t, time.Now().Unix(), "", false); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE discovery_jobs SET source_account='',state=CASE WHEN state IN ('failed','ignored') THEN 'queued' ELSE state END,next_run=0 WHERE user_id=? AND identity=?`, user, Identity(t)); err != nil {
		return "", err
	}
	var id string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM discovery_jobs WHERE user_id=? AND identity=?`, user, Identity(t)).Scan(&id); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func (s *Store) Pause(ctx context.Context, user int64, platform string, pause bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE discovery_bindings SET paused=?,paused_by_user=?,last_error='' WHERE user_id=? AND platform=?`, pause, pause, user, platform)
	return err
}

// SourceError records a read failure without changing a user's pause choice.
// The bool return reports whether the message changed.
func (s *Store) SourceError(ctx context.Context, b Binding, message string, pause bool) (bool, error) {
	var old string
	if err := s.db.QueryRowContext(ctx, `SELECT last_error FROM discovery_bindings WHERE user_id=? AND platform=?`, b.User, b.Platform).Scan(&old); err != nil {
		return false, err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE discovery_bindings SET last_error=? WHERE user_id=? AND platform=?`, message, b.User, b.Platform)
	return old != message, err
}

func (s *Store) ActiveFavoriteTracks(ctx context.Context, user int64, platform, account string) (map[string]Track, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT track_id, track_json FROM discovery_favorites WHERE user_id=? AND platform=? AND account=? AND active=1`, user, platform, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Track{}
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var track Track
		if err = json.Unmarshal([]byte(raw), &track); err != nil {
			return nil, err
		}
		if track.ID == "" {
			track.ID = id
		}
		out[id] = track
	}
	return out, rows.Err()
}

func (s *Store) Favorites(ctx context.Context, user int64) ([]Track, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.track_json,f.active FROM discovery_favorites f JOIN discovery_bindings b ON f.user_id=b.user_id AND f.platform=b.platform AND f.account=b.account WHERE f.user_id=?`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tracks []Track
	for rows.Next() {
		var raw string
		var active bool
		if err = rows.Scan(&raw, &active); err != nil {
			return nil, err
		}
		var t Track
		if err = json.Unmarshal([]byte(raw), &t); err != nil {
			return nil, err
		}
		t.Favorite = active
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

const (
	ExcludedDismissed = "dismissed"
	ExcludedShown     = "shown"
	ExcludedPending   = "pending"
	ExcludedQueued    = "queued"
	ExcludedOther     = "other"
)

// Exclusions keeps the reason a song is hidden. The old boolean-only view made
// a short batch look like a failed search instead of "already seen".
func (s *Store) Exclusions(ctx context.Context, user int64) (map[string]string, error) {
	out := map[string]string{}
	rows, err := s.db.QueryContext(ctx, `SELECT identity,dismissed,shown FROM discovery_preferences WHERE user_id=?`, user)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-30 * 24 * time.Hour).Unix()
	for rows.Next() {
		var identity string
		var dismissed bool
		var shown int64
		if err = rows.Scan(&identity, &dismissed, &shown); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case dismissed:
			out[identity] = ExcludedDismissed
		case shown > cutoff:
			out[identity] = ExcludedShown
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	// Pending uploads and failures stay visible in the favorites/queue views,
	// but they are not permanent rediscover exclusions. NAS outages can leave a
	// song pending for days, and hiding it made the next rediscover batch look
	// empty even though it had never reached the library.
	jobs, err := s.db.QueryContext(ctx, `SELECT identity,state FROM discovery_jobs WHERE user_id=? AND state IN ('queued','running','completed','owned')`, user)
	if err != nil {
		return nil, err
	}
	defer jobs.Close()
	for jobs.Next() {
		var identity, state string
		if err = jobs.Scan(&identity, &state); err != nil {
			return nil, err
		}
		reason := ExcludedQueued
		switch state {
		case PendingUpload:
			reason = ExcludedPending
		case Ignored:
			reason = ExcludedDismissed
		}
		// A service row can map to the same preferred title as a user's
		// dismissal. Keep the more specific permanent decision in that case.
		if existing, exists := out[identity]; !exists || reason == ExcludedPending || (reason == ExcludedDismissed && existing != ExcludedDismissed) {
			out[identity] = reason
		}
	}
	return out, jobs.Err()
}

func (s *Store) Excluded(ctx context.Context, user int64) (map[string]bool, error) {
	reasons, err := s.Exclusions(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(reasons))
	for key := range reasons {
		out[key] = true
	}
	return out, nil
}

func (s *Store) Preference(ctx context.Context, user int64, t Track, dismiss bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO discovery_preferences(user_id,identity,shown,dismissed) VALUES(?,?,?,?) ON CONFLICT(user_id,identity) DO UPDATE SET shown=excluded.shown,dismissed=MAX(dismissed,excluded.dismissed)`, user, Identity(t), time.Now().Unix(), dismiss)
	return err
}

// RestorePreference clears a permanent dismissal and the short display window
// so a song can be offered again. It intentionally does not delete queue jobs.
func (s *Store) RestorePreference(ctx context.Context, user int64, t Track) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM discovery_preferences WHERE user_id=? AND identity=?`, user, Identity(t))
	return err
}

// JobCounts returns state counts without the 100-row display limit.
func (s *Store) JobCounts(ctx context.Context, user int64) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state,COUNT(*) FROM discovery_jobs WHERE (?=0 OR user_id=?) GROUP BY state`, user, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var state string
		var count int
		if err = rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		out[state] = count
	}
	return out, rows.Err()
}

func (s *Store) SaveCard(ctx context.Context, c Card) (Card, error) {
	c.Token = token()
	c.Expires = time.Now().Add(30 * time.Minute).Unix()
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO discovery_cards VALUES(?,?,?,?,?)`, c.Token, c.User, c.Chat, string(raw), c.Expires)
	if err != nil {
		return c, err
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM discovery_cards WHERE expires<?`, time.Now().Add(-24*time.Hour).Unix())
	return c, nil
}

func (s *Store) ReplaceCard(ctx context.Context, c Card) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE discovery_cards SET payload=? WHERE token=? AND user_id=? AND chat_id=? AND expires>?`, string(raw), c.Token, c.User, c.Chat, time.Now().Unix())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("按钮已过期或不属于当前会话，请重新发送指令")
	}
	return nil
}

func (s *Store) Card(ctx context.Context, tok string, user, chat int64) (Card, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM discovery_cards WHERE token=? AND user_id=? AND chat_id=? AND expires>?`, tok, user, chat, time.Now().Unix()).Scan(&raw)
	if err != nil {
		return Card{}, fmt.Errorf("按钮已过期或不属于当前会话，请重新发送指令")
	}
	var c Card
	err = json.Unmarshal([]byte(raw), &c)
	return c, err
}

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var raw string
	err := row.Scan(&j.ID, &j.User, &j.Chat, &raw, &j.State, &j.Attempts, &j.Next, &j.Error, &j.Updated, &j.Result)
	if err != nil {
		return j, err
	}
	err = json.Unmarshal([]byte(raw), &j.Track)
	return j, err
}

const jobColumns = `id,user_id,chat_id,track_json,state,attempts,next_run,last_error,updated,result`

func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM discovery_jobs WHERE id=?`, id))
}

func (s *Store) Jobs(ctx context.Context, user int64, unnotified bool) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM discovery_jobs WHERE (?=0 OR user_id=?) AND (?=0 OR (notified=0 AND state NOT IN ('queued','running'))) ORDER BY updated DESC LIMIT 100`, user, user, unnotified)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FailedJobs reads failure rows directly, so the failure detail view is not
// limited to the newest 100 jobs that the card overview happens to load.
func (s *Store) FailedJobs(ctx context.Context, user int64, offset, limit int) ([]Job, int, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM discovery_jobs WHERE user_id=? AND state='failed'`, user).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM discovery_jobs WHERE user_id=? AND state='failed' ORDER BY updated DESC,id LIMIT ? OFFSET ?`, user, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]Job, 0, limit)
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

func (s *Store) Claim(ctx context.Context, allowAuto bool) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM discovery_jobs j WHERE state='queued' AND next_run<=? AND (source_account='' OR (? AND EXISTS(SELECT 1 FROM discovery_bindings b WHERE b.user_id=j.user_id AND b.platform=j.source_platform AND b.account=j.source_account AND b.paused=0))) ORDER BY updated LIMIT 1`, time.Now().Unix(), allowAuto))
	if err != nil {
		return j, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE discovery_jobs SET state='running',attempts=attempts+1,updated=?,notified=0 WHERE id=?`, time.Now().Unix(), j.ID)
	if err != nil {
		return j, err
	}
	j.Attempts++
	j.State = Running
	return j, tx.Commit()
}

func (s *Store) Finish(ctx context.Context, j Job, state, message, result string, next int64) error {
	data, err := json.Marshal(j.Track)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE discovery_jobs SET track_json=?,state=?,last_error=?,result=?,next_run=?,updated=?,notified=0 WHERE id=?`, string(data), state, message, result, next, time.Now().Unix(), j.ID)
	return err
}

func (s *Store) Recover(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE discovery_jobs SET state='queued',next_run=0 WHERE state='running'`)
	return err
}

func (s *Store) Retry(ctx context.Context, user int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE discovery_jobs SET state='queued',attempts=0,next_run=0,last_error='',notified=0,source_account='' WHERE user_id=? AND state='failed'`, user)
	return err
}

func (s *Store) NotifyDone(ctx context.Context, id string, updated int64, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE discovery_jobs SET notified=1 WHERE id=? AND updated=? AND state=?`, id, updated, state)
	return err
}

func (s *Store) ResolveJob(ctx context.Context, id string, user int64, accept bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT track_json FROM discovery_jobs WHERE id=? AND user_id=? AND state='needs_confirmation'`, id, user).Scan(&raw); err != nil {
		return errors.New("此待确认任务已处理或不存在")
	}
	var t Track
	if err = json.Unmarshal([]byte(raw), &t); err != nil {
		return err
	}
	t.Approved = accept
	data, _ := json.Marshal(t)
	state := Ignored
	if accept {
		state = Queued
	}
	_, err = tx.ExecContext(ctx, `UPDATE discovery_jobs SET track_json=?,state=?,attempts=0,next_run=0,last_error='',notified=0,source_account='' WHERE id=? AND user_id=?`, string(data), state, id, user)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// TakeCard makes a baseline confirmation single-use, including across restarts.
func (s *Store) TakeCard(ctx context.Context, tok string, user, chat int64) (Card, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `DELETE FROM discovery_cards WHERE token=? AND user_id=? AND chat_id=? AND expires>? RETURNING payload`, tok, user, chat, time.Now().Unix()).Scan(&raw)
	if err != nil {
		return Card{}, fmt.Errorf("确认已处理或过期，请重新预览")
	}
	var c Card
	err = json.Unmarshal([]byte(raw), &c)
	return c, err
}

func (s *Store) AdoptIdentity(ctx context.Context, j Job) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var other string
	err = tx.QueryRowContext(ctx, `SELECT id FROM discovery_jobs WHERE user_id=? AND identity=?`, j.User, Identity(j.Track)).Scan(&other)
	if err == nil && other != j.ID {
		return false, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE discovery_jobs SET identity=? WHERE id=?`, Identity(j.Track), j.ID)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) RememberFavorite(ctx context.Context, user int64, t Track) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE discovery_favorites SET track_json=? WHERE user_id=? AND platform=? AND track_id=?`, string(data), user, t.Platform, t.ID)
	return err
}

func (s *Store) PendingJobs(ctx context.Context, user int64) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM discovery_jobs WHERE (?=0 OR user_id=?) AND state='pending_upload' ORDER BY updated`, user, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			return nil, e
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ConfirmationAt returns one needs_confirmation job in oldest-first order.
// index is clamped into range. total is zero when nothing is waiting.
func (s *Store) ConfirmationAt(ctx context.Context, user int64, index int) (Job, int, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM discovery_jobs WHERE user_id=? AND state='needs_confirmation'`, user).Scan(&total); err != nil {
		return Job{}, 0, 0, err
	}
	if total == 0 {
		return Job{}, 0, 0, nil
	}
	if index < 0 {
		index = 0
	}
	if index >= total {
		index = total - 1
	}
	job, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM discovery_jobs WHERE user_id=? AND state='needs_confirmation' ORDER BY updated,id LIMIT 1 OFFSET ?`, user, index))
	if err != nil {
		return Job{}, 0, total, err
	}
	return job, index, total, nil
}
