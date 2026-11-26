package audiodupe

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/internal/lyricmeta"
	"github.com/nichuanfang/gymdl/internal/storage"
)

// Report is the last finished or in-progress scan the console can show.
type Report struct {
	Status             string    `json:"status"`
	Scanned            int       `json:"scanned"`
	Total              int       `json:"total"`
	Skipped            int       `json:"skipped"`
	Error              string    `json:"error,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
	Groups             []Group   `json:"groups"`
	LyricConflicts     []Group   `json:"lyric_conflicts,omitempty"`
	LyricConflictCount int       `json:"lyric_conflict_count,omitempty"`
}

// Store caches fingerprints and the latest grouping. It never removes audio files.
type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := storage.Open(path)
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS audio_fingerprints (
		path TEXT PRIMARY KEY,
		size INTEGER NOT NULL,
		mod_unix INTEGER NOT NULL,
		duration REAL NOT NULL,
		items BLOB NOT NULL,
		lyric_evidence TEXT,
		updated INTEGER NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = ensureFingerprintLyricColumn(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS audio_duplicate_report (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		payload TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audio_fingerprints_duration ON audio_fingerprints(duration)`); err != nil {
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

func (s *Store) HasAny(ctx context.Context) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("指纹库不可用")
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM audio_fingerprints LIMIT 1`).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) HasDuration(ctx context.Context, lo, hi float64) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("指纹库不可用")
	}
	if hi <= 0 {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM audio_fingerprints WHERE duration >= ? AND duration <= ? LIMIT 1`, lo, hi).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListDuration loads only fingerprints whose length can still be the same recording.
func (s *Store) ListDuration(ctx context.Context, lo, hi float64, excludePath string) ([]Track, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("指纹库不可用")
	}
	if hi <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT path, duration, items, lyric_evidence FROM audio_fingerprints WHERE duration >= ? AND duration <= ? AND path <> ?`, lo, hi, excludePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tracks []Track
	for rows.Next() {
		var track Track
		var blob []byte
		var evidence sql.NullString
		if err = rows.Scan(&track.Path, &track.Fingerprint.Duration, &blob, &evidence); err != nil {
			return nil, err
		}
		track.Fingerprint.Items = decodeItems(blob)
		track.LyricEvidence = lyricmeta.Normalize(lyricmeta.Evidence(evidence.String))
		tracks = append(tracks, track)
	}
	return tracks, rows.Err()
}

func (s *Store) LoadFingerprint(ctx context.Context, path string, size, modUnix int64) (Fingerprint, bool, error) {
	fp, _, ok, err := s.LoadFingerprintWithLyric(ctx, path, size, modUnix)
	return fp, ok, err
}

// LoadFingerprintWithLyric treats pre-lyric rows as cache misses so the next
// scan can repopulate both the fingerprint and its evidence.
func (s *Store) LoadFingerprintWithLyric(ctx context.Context, path string, size, modUnix int64) (Fingerprint, lyricmeta.Evidence, bool, error) {
	var duration float64
	var blob []byte
	var evidence sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT duration, items, lyric_evidence FROM audio_fingerprints WHERE path=? AND size=? AND mod_unix=?`, path, size, modUnix).Scan(&duration, &blob, &evidence)
	if err == sql.ErrNoRows {
		return Fingerprint{}, lyricmeta.Unknown, false, nil
	}
	if err != nil {
		return Fingerprint{}, lyricmeta.Unknown, false, err
	}
	if !evidence.Valid || strings.TrimSpace(evidence.String) == "" {
		return Fingerprint{}, lyricmeta.Unknown, false, nil
	}
	return Fingerprint{Duration: duration, Items: decodeItems(blob)}, lyricmeta.Normalize(lyricmeta.Evidence(evidence.String)), true, nil
}

func (s *Store) DeleteFingerprint(ctx context.Context, path string) error {
	if s == nil || s.db == nil {
		return errors.New("指纹库不可用")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM audio_fingerprints WHERE path=?`, path)
	return err
}

func (s *Store) SaveFingerprint(ctx context.Context, path string, size, modUnix int64, fp Fingerprint) error {
	return s.SaveFingerprintWithLyric(ctx, path, size, modUnix, fp, lyricmeta.Unknown)
}

func (s *Store) SaveFingerprintWithLyric(ctx context.Context, path string, size, modUnix int64, fp Fingerprint, evidence lyricmeta.Evidence) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audio_fingerprints(path,size,mod_unix,duration,items,lyric_evidence,updated) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET size=excluded.size, mod_unix=excluded.mod_unix, duration=excluded.duration,
		items=excluded.items, lyric_evidence=excluded.lyric_evidence, updated=excluded.updated`,
		path, size, modUnix, fp.Duration, encodeItems(fp.Items), lyricmeta.Normalize(evidence), time.Now().Unix())
	return err
}

func (s *Store) SaveReport(ctx context.Context, report Report) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audio_duplicate_report(id,payload) VALUES(1,?)
		ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, string(raw))
	return err
}

func (s *Store) LoadReport(ctx context.Context) (Report, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM audio_duplicate_report WHERE id=1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return Report{Status: "idle", Groups: []Group{}}, nil
	}
	if err != nil {
		return Report{}, err
	}
	var report Report
	if err = json.Unmarshal([]byte(raw), &report); err != nil {
		return Report{}, err
	}
	if report.Groups == nil {
		report.Groups = []Group{}
	}
	if report.LyricConflicts == nil {
		report.LyricConflicts = []Group{}
	}
	for i := range report.Groups {
		for j := range report.Groups[i].Tracks {
			report.Groups[i].Tracks[j].LyricEvidence = lyricmeta.Normalize(report.Groups[i].Tracks[j].LyricEvidence)
		}
	}
	for i := range report.LyricConflicts {
		for j := range report.LyricConflicts[i].Tracks {
			report.LyricConflicts[i].Tracks[j].LyricEvidence = lyricmeta.Normalize(report.LyricConflicts[i].Tracks[j].LyricEvidence)
		}
	}
	report.LyricConflictCount = len(report.LyricConflicts)
	return report, nil
}

func ensureFingerprintLyricColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(audio_fingerprints)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == "lyric_evidence" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE audio_fingerprints ADD COLUMN lyric_evidence TEXT`)
	return err
}

func encodeItems(items []uint32) []byte {
	buf := make([]byte, len(items)*4)
	for i, item := range items {
		binary.LittleEndian.PutUint32(buf[i*4:], item)
	}
	return buf
}

func decodeItems(buf []byte) []uint32 {
	items := make([]uint32, len(buf)/4)
	for i := range items {
		items[i] = binary.LittleEndian.Uint32(buf[i*4:])
	}
	return items
}
