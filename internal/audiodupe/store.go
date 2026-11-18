package audiodupe

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"github.com/nichuanfang/gymdl/internal/storage"
)

// Report is the last finished or in-progress scan the console can show.
type Report struct {
	Status    string    `json:"status"`
	Scanned   int       `json:"scanned"`
	Total     int       `json:"total"`
	Skipped   int       `json:"skipped"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	Groups    []Group   `json:"groups"`
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
		updated INTEGER NOT NULL
	)`); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT path, duration, items FROM audio_fingerprints WHERE duration >= ? AND duration <= ? AND path <> ?`, lo, hi, excludePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tracks []Track
	for rows.Next() {
		var track Track
		var blob []byte
		if err = rows.Scan(&track.Path, &track.Fingerprint.Duration, &blob); err != nil {
			return nil, err
		}
		track.Fingerprint.Items = decodeItems(blob)
		tracks = append(tracks, track)
	}
	return tracks, rows.Err()
}

func (s *Store) LoadFingerprint(ctx context.Context, path string, size, modUnix int64) (Fingerprint, bool, error) {
	var duration float64
	var blob []byte
	err := s.db.QueryRowContext(ctx, `SELECT duration, items FROM audio_fingerprints WHERE path=? AND size=? AND mod_unix=?`, path, size, modUnix).Scan(&duration, &blob)
	if err == sql.ErrNoRows {
		return Fingerprint{}, false, nil
	}
	if err != nil {
		return Fingerprint{}, false, err
	}
	return Fingerprint{Duration: duration, Items: decodeItems(blob)}, true, nil
}

func (s *Store) DeleteFingerprint(ctx context.Context, path string) error {
	if s == nil || s.db == nil {
		return errors.New("指纹库不可用")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM audio_fingerprints WHERE path=?`, path)
	return err
}

func (s *Store) SaveFingerprint(ctx context.Context, path string, size, modUnix int64, fp Fingerprint) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audio_fingerprints(path,size,mod_unix,duration,items,updated) VALUES(?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET size=excluded.size, mod_unix=excluded.mod_unix, duration=excluded.duration, items=excluded.items, updated=excluded.updated`,
		path, size, modUnix, fp.Duration, encodeItems(fp.Items), time.Now().Unix())
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
	return report, nil
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
