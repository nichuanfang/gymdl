package musicrag

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStoreEnqueueIsIdempotentAndDeleteRemovesRows(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	song := Song{MusicID: "song-1", Title: "夜曲", Artist: "周杰伦", Album: "十一月的萧邦", Year: "2005", URL: "https://music.example/1", CreatedAt: "2024-01-01", Status: StatusPending}
	if err := store.Enqueue(ctx, song); err != nil {
		t.Fatal(err)
	}
	song.Title = "另一首"
	if err := store.Enqueue(ctx, song); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Title != "夜曲" || pending[0].Status != StatusPending {
		t.Fatalf("enqueue duplicated or overwrote the row: %#v", pending)
	}
	if err := store.Delete(ctx, []string{"song-1", "song-1", "missing"}); err != nil {
		t.Fatal(err)
	}
	pending, err = store.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("deleted row still pending: %#v", pending)
	}
}

func TestStoreImportKeepsExistingStatusAndListsOnlyPending(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	rows := []Song{
		{MusicID: "done", Title: "A", Artist: "B", Status: StatusReady, Genre: "流行", Mood: "温暖", Language: "国语", Description: "一段简介"},
		{MusicID: "wait", Title: "C", Artist: "D", Status: StatusPending},
	}
	n, err := store.Import(ctx, rows)
	if err != nil || n != 2 {
		t.Fatalf("import = %d, %v", n, err)
	}
	if n, err = store.Import(ctx, rows); err != nil || n != 0 {
		t.Fatalf("second import = %d, %v", n, err)
	}
	pending, err := store.Pending(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].MusicID != "wait" {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	if store.Count(ctx) != 2 {
		t.Fatalf("count = %d", store.Count(ctx))
	}
}

func TestStoreRejectsUnsafeIDs(t *testing.T) {
	store := openTestStore(t)
	if err := store.Enqueue(context.Background(), Song{MusicID: "bad id"}); err == nil {
		t.Fatal("unsafe music id accepted")
	}
	if err := store.Delete(context.Background(), []string{"ok", "bad id"}); err == nil {
		t.Fatal("unsafe delete id accepted")
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
