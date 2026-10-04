package task

import (
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/processor/music"
)

func TestGetFilteredHistoryFiltersBeforePagination(t *testing.T) {
	base := time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)
	manager := &TaskManager{
		history: []*Task{
			{ID: "1", Platform: "netease", Status: TaskStatusCompleted, URL: "https://music.163.com/1", CreatedAt: base, SongInfo: []*music.SongInfo{{SongName: "晨光", SongArtists: "甲"}}},
			{ID: "2", Platform: "qq", Status: TaskStatusFailed, URL: "https://y.qq.com/2", CreatedAt: base.Add(-time.Hour), Error: "timeout"},
			{ID: "3", Platform: "netease", Status: TaskStatusFailed, URL: "https://music.163.com/3", CreatedAt: base.Add(-48 * time.Hour), Error: "404"},
		},
	}
	items, total := manager.GetFilteredHistory(0, 1, HistoryFilter{
		Query: "晨光", Platform: "netease", Status: "completed", From: base.Add(-time.Minute), To: base.Add(time.Minute),
	})
	if total != 1 || len(items) != 1 || items[0].ID != "1" {
		t.Fatalf("expected matching item before pagination, total=%d items=%#v", total, items)
	}
	items, total = manager.GetFilteredHistory(0, 20, HistoryFilter{Query: "timeout"})
	if total != 1 || items[0].ID != "2" {
		t.Fatalf("expected error text to match, total=%d items=%#v", total, items)
	}
}

func TestDashboardMetricsCountsActiveAndRecentHistory(t *testing.T) {
	now := time.Now()
	manager := &TaskManager{
		tasks: map[string]*Task{
			"pending": {ID: "pending", Status: TaskStatusPending},
			"running": {ID: "running", Status: TaskStatusRunning},
		},
		history: []*Task{
			{ID: "done", Status: TaskStatusCompleted, CreatedAt: now.Add(-time.Hour)},
			{ID: "failed", Status: TaskStatusFailed, CreatedAt: now.Add(-2 * time.Hour)},
			{ID: "old", Status: TaskStatusCompleted, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-time.Hour)},
		},
	}
	got := manager.DashboardMetrics(now)
	if got.Pending != 1 || got.Running != 1 || got.Completed24h != 2 || got.Failed24h != 1 || got.TotalHistory != 3 {
		t.Fatalf("unexpected dashboard counts: %#v", got)
	}
}
