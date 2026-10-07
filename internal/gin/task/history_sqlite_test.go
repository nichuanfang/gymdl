package task

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/processor/music"
)

func TestLegacyHistoryImportsOnceAndPreservesJSONBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gymdl.sqlite3")
	legacyPath := filepath.Join(dir, "history.json")
	legacy := []*Task{{
		ID: "old-task", URL: "https://example.test/song", Platform: "test",
		Status: TaskStatusCompleted, CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 1, 12, 1, 0, 0, time.UTC),
		SongInfo:  []*music.SongInfo{{SongName: "Legacy Song", SongArtists: "Legacy Artist", SongAlbum: "Legacy Album"}},
	}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	manager := NewTaskManagerWithDBPath(nil, dbPath, legacyPath)
	if manager.historyErr != nil {
		t.Fatal(manager.historyErr)
	}
	items, total := manager.GetHistory(0, 10)
	if total != 1 || len(items) != 1 || items[0].ID != "old-task" || items[0].SongInfo[0].SongAlbum != "Legacy Album" {
		t.Fatalf("legacy task history not imported: total=%d items=%#v", total, items)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy JSON should remain as a backup: %v", err)
	}

	manager = NewTaskManagerWithDBPath(nil, dbPath, legacyPath)
	defer manager.Close()
	items, total = manager.GetHistory(0, 10)
	if total != 1 || len(items) != 1 {
		t.Fatalf("restarting should not duplicate imported history: total=%d items=%#v", total, items)
	}
}

func TestEmptyTaskHistoryMetricsAreZero(t *testing.T) {
	manager := NewTaskManagerWithDBPath(nil, filepath.Join(t.TempDir(), "gymdl.sqlite3"), "")
	defer manager.Close()
	got := manager.DashboardMetrics(time.Now())
	if got.TotalHistory != 0 || got.Completed24h != 0 || got.Failed24h != 0 {
		t.Fatalf("empty history metrics should be zero: %#v", got)
	}
}
