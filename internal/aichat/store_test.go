package aichat

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestModeDefaultsToNotFoundUntilUserSwitches(t *testing.T) {
	store := openTestStore(t)
	enabled, found, err := store.ModeState(1, 2)
	if err != nil || found || enabled {
		t.Fatalf("unset mode = (%v,%v,%v), want (false,false,nil)", enabled, found, err)
	}
	if err := store.SetMode(1, 2, true); err != nil {
		t.Fatal(err)
	}
	enabled, found, err = store.ModeState(1, 2)
	if err != nil || !found || !enabled {
		t.Fatalf("after enabling = (%v,%v,%v)", enabled, found, err)
	}
}

func TestConversationIsScopedByUserAndChat(t *testing.T) {
	store := openTestStore(t)
	if err := store.AppendMessages(1, 10, []Message{{Role: "user", Content: "想听爵士"}}, 40); err != nil {
		t.Fatal(err)
	}
	sameChat, _ := store.History(1, 10, 10)
	if len(sameChat) != 1 {
		t.Fatalf("same chat history = %d, want 1", len(sameChat))
	}
	otherChat, _ := store.History(1, 11, 10)
	if len(otherChat) != 0 {
		t.Fatalf("history leaked into another chat: %#v", otherChat)
	}
	otherUser, _ := store.History(2, 10, 10)
	if len(otherUser) != 0 {
		t.Fatalf("history leaked across users: %#v", otherUser)
	}
}

func TestAppendMessagesKeepsOnlyNewestRows(t *testing.T) {
	store := openTestStore(t)
	for _, text := range []string{"one", "two", "three", "four"} {
		if err := store.AppendMessages(1, 1, []Message{{Role: "user", Content: text}}, 3); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.History(1, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 || history[0].Content != "two" || history[2].Content != "four" {
		t.Fatalf("trimmed history = %#v", history)
	}
}

func TestResetClearsMessagesButKeepsMode(t *testing.T) {
	store := openTestStore(t)
	if err := store.SetMode(1, 1, true); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessages(1, 1, []Message{{Role: "user", Content: "hi"}}, 40); err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(1, 1); err != nil {
		t.Fatal(err)
	}
	history, _ := store.History(1, 1, 10)
	if len(history) != 0 {
		t.Fatalf("history not cleared: %#v", history)
	}
	enabled, found, err := store.ModeState(1, 1)
	if err != nil || !found || !enabled {
		t.Fatalf("reset must keep AI mode, got (%v,%v,%v)", enabled, found, err)
	}
}

func TestCandidateLookupIsScopedToOwner(t *testing.T) {
	store := openTestStore(t)
	if err := store.SaveCandidates(1, 1, []Candidate{{Token: "t1", Name: "Song", URL: "https://example.test/song"}}); err != nil {
		t.Fatal(err)
	}
	found, err := store.Candidate("t1", 1, 1)
	if err != nil || found == nil || found.URL == "" {
		t.Fatalf("owner lookup failed: %#v %v", found, err)
	}
	foreign, err := store.Candidate("t1", 2, 2)
	if err != nil || foreign != nil {
		t.Fatalf("candidate leaked to another chat: %#v %v", foreign, err)
	}
}

func TestCandidateCarriesExpiry(t *testing.T) {
	store := openTestStore(t)
	if err := store.SaveCandidates(1, 1, []Candidate{{Token: "t1", Name: "Song", URL: "https://example.test/song"}}); err != nil {
		t.Fatal(err)
	}
	found, err := store.Candidate("t1", 1, 1)
	if err != nil || found == nil {
		t.Fatalf("lookup failed: %#v %v", found, err)
	}
	if found.ExpiresAt.IsZero() {
		t.Fatal("candidate expiry was not parsed")
	}
}

// TestSaveCandidatesKeepsPreviousTurns pins the fix for "old buttons say
// 候选已过期". Each new answer used to delete every candidate of the chat.
func TestSaveCandidatesKeepsPreviousTurns(t *testing.T) {
	store := openTestStore(t)
	if err := store.SaveCandidates(1, 1, []Candidate{{Token: "turn1", Name: "A", URL: "https://example.test/a"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCandidates(1, 1, []Candidate{{Token: "turn2", Name: "B", URL: "https://example.test/b"}}); err != nil {
		t.Fatal(err)
	}
	previous, err := store.Candidate("turn1", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if previous == nil {
		t.Fatal("candidate from the previous turn was deleted")
	}
	latest, err := store.Candidate("turn2", 1, 1)
	if err != nil || latest == nil {
		t.Fatalf("latest candidate missing: %v", err)
	}
}

// TestSaveCandidatesPrunesExpiredRows keeps the retention window honest.
func TestSaveCandidatesPrunesExpiredRows(t *testing.T) {
	store := openTestStore(t)
	if err := store.SaveCandidates(1, 1, []Candidate{{Token: "stale", Name: "A", URL: "https://example.test/a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE ai_chat_candidates SET created_at = ? WHERE token = 'stale'`, time.Now().Add(-2*candidateTTL).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCandidates(1, 1, nil); err != nil {
		t.Fatal(err)
	}
	stale, err := store.Candidate("stale", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stale != nil {
		t.Fatal("expired candidate was not pruned")
	}
}

// TestCandidateFlagsRoundTrip pins the persistence of the badge flags: the
// message said "已入库" while the click handler read owned=false.
func TestCandidateFlagsRoundTrip(t *testing.T) {
	store := openTestStore(t)
	if err := store.SaveCandidates(1, 1, []Candidate{{
		Token: "flags", Name: "Song", URL: "https://example.test/song",
		Owned: true, IsVIP: true, Grounded: true,
	}}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Candidate("flags", 1, 1)
	if err != nil || got == nil {
		t.Fatalf("lookup failed: %v", err)
	}
	if !got.Owned || !got.IsVIP || !got.Grounded {
		t.Fatalf("flags lost: owned=%v vip=%v grounded=%v", got.Owned, got.IsVIP, got.Grounded)
	}
}

// TestStoreMigratesLegacyCandidateTable covers deployments whose table was
// created before the badge columns existed.
func TestStoreMigratesLegacyCandidateTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gymdl.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ai_chat_candidates (
	token TEXT PRIMARY KEY, user_id INTEGER NOT NULL, chat_id INTEGER NOT NULL,
	title TEXT NOT NULL, artist TEXT NOT NULL, platform TEXT NOT NULL,
	url TEXT NOT NULL, name TEXT NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("opening a legacy database must migrate it: %v", err)
	}
	defer store.Close()
	if err := store.SaveCandidates(1, 1, []Candidate{{Token: "t", Name: "N", URL: "u", Owned: true}}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Candidate("t", 1, 1)
	if err != nil || got == nil || !got.Owned {
		t.Fatalf("migrated table did not store flags: %#v %v", got, err)
	}
}

// TestCandidatesListsOnlyCurrentBatchAndSkipsExpired covers the data behind the
// "下载前 3 首" button: it must see one answer's tracks in order and never
// resurrect an expired one.
func TestCandidatesListsOnlyCurrentBatchAndSkipsExpired(t *testing.T) {
	store := openTestStore(t)
	if err := store.SaveCandidates(1, 1, []Candidate{
		{Token: "a", Name: "A", URL: "https://example.test/a"},
		{Token: "b", Name: "B", URL: "https://example.test/b"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE ai_chat_candidates SET created_at = ? WHERE token = 'b'`,
		time.Now().Add(-2*candidateTTL).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCandidates(1, 1, []Candidate{{Token: "c", Name: "C", URL: "https://example.test/c"}}); err != nil {
		t.Fatal(err)
	}
	items, err := store.Candidates(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d candidates, want the unexpired two: %#v", len(items), items)
	}
	if items[0].Token != "c" || items[1].Token != "a" {
		t.Fatalf("expected newest-first order, got %s,%s", items[0].Token, items[1].Token)
	}
	if items[0].BatchID == items[1].BatchID {
		t.Fatal("separate answers must have distinct batch ids")
	}
}

// TestMarkCandidateOwnedUpdatesBadge keeps a batch run from re-offering a track.
func TestMarkCandidateOwnedUpdatesBadge(t *testing.T) {
	store := openTestStore(t)
	if err := store.SaveCandidates(1, 1, []Candidate{{Token: "t", Name: "A", URL: "https://example.test/a"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCandidateOwned(1, 1, "t"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Candidate("t", 1, 1)
	if err != nil || got == nil || !got.Owned {
		t.Fatalf("owned flag not set: %#v %v", got, err)
	}
}
