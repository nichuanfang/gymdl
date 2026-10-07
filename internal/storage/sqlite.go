package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

const DefaultDatabasePath = "data/gymdl.sqlite3"

var openMu sync.Mutex

// Open returns a single-connection SQLite pool configured for Gymdl's local
// persistent store. Calls are serialized during initialization because the
// Bot and Web services may open the shared file concurrently at startup.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("SQLite database path is empty")
	}
	openMu.Lock()
	defer openMu.Unlock()

	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create SQLite database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect SQLite database: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure SQLite busy timeout: %w", err)
	}
	if path != ":memory:" {
		if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("enable SQLite WAL mode: %w", err)
		}
	}
	return db, nil
}
