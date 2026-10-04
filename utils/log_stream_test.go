package utils

import "testing"

func TestLogStreamReadInitialPageReturnsNewestEntries(t *testing.T) {
	manager := newLogStreamManager(8)
	manager.append(LogEntry{Level: "INFO", Message: "one"})
	manager.append(LogEntry{Level: "DEBUG", Message: "debug"})
	manager.append(LogEntry{Level: "WARN", Message: "two"})
	manager.append(LogEntry{Level: "ERROR", Message: "three"})

	page := manager.read(0, "info", 2)
	if len(page.Items) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(page.Items))
	}
	if page.Items[0].Message != "two" || page.Items[1].Message != "three" {
		t.Fatalf("initial page should contain newest matching entries, got %#v", page.Items)
	}
	if page.Cursor != 4 {
		t.Fatalf("expected cursor 4, got %d", page.Cursor)
	}
}

func TestLogStreamReadIncrementalPagesDoNotSkipEntries(t *testing.T) {
	manager := newLogStreamManager(8)
	manager.append(LogEntry{Level: "INFO", Message: "one"})
	manager.append(LogEntry{Level: "WARN", Message: "two"})
	manager.append(LogEntry{Level: "ERROR", Message: "three"})

	first := manager.read(1, "info", 1)
	if len(first.Items) != 1 || first.Items[0].Message != "two" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	if first.Cursor != 2 {
		t.Fatalf("expected cursor to stop at returned entry 2, got %d", first.Cursor)
	}

	second := manager.read(first.Cursor, "info", 1)
	if len(second.Items) != 1 || second.Items[0].Message != "three" {
		t.Fatalf("unexpected second page: %#v", second)
	}
	if second.Cursor != 3 {
		t.Fatalf("expected cursor 3, got %d", second.Cursor)
	}
}

func TestLogStreamDetectsOverwrittenEntries(t *testing.T) {
	manager := newLogStreamManager(2)
	manager.append(LogEntry{Level: "INFO", Message: "one"})
	manager.append(LogEntry{Level: "INFO", Message: "two"})
	manager.append(LogEntry{Level: "INFO", Message: "three"})
	manager.append(LogEntry{Level: "INFO", Message: "four"})

	page := manager.read(1, "info", 10)
	if !page.Dropped {
		t.Fatal("expected overwritten entry to be reported")
	}
	if len(page.Items) != 2 || page.Items[0].Message != "three" || page.Items[1].Message != "four" {
		t.Fatalf("unexpected retained entries: %#v", page.Items)
	}
}
