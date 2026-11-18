package musicrag

import (
	"context"
	"errors"
	"testing"
)

type deletingVectors struct {
	fakeVectors
	fail string
}

func (d *deletingVectors) Delete(_ context.Context, id string) error {
	if id == d.fail {
		return errors.New("pinecone down")
	}
	d.ids = append(d.ids, "deleted:"+id)
	return nil
}

func TestIndexLibraryRemovesStaleRowsWithoutBlockingTheNextSong(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"keep", "stuck", "gone"} {
		if err := store.Enqueue(ctx, Song{MusicID: id, Title: id, Status: StatusReady}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Enqueue(ctx, Song{MusicID: "wait", Title: "待处理", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	vectors := &deletingVectors{fail: "stuck"}
	stats, err := IndexLibrary(ctx, store, []LibrarySong{{ID: "keep"}, {ID: "wait"}, {ID: "fresh"}}, fakeClassify{}, &fakeEmbed{}, vectors, 10)
	if err == nil || stats.Enqueued != 1 || stats.Removed != 1 || stats.Indexed != 2 {
		t.Fatalf("stats=%#v err=%v", stats, err)
	}
	ids, err := store.AllIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ids["gone"]; ok {
		t.Fatal("removed song is still stored")
	}
	if _, ok := ids["stuck"]; !ok {
		t.Fatal("failed vector delete dropped the row")
	}
	if _, ok := ids["fresh"]; !ok {
		t.Fatal("new song was not stored")
	}
}
