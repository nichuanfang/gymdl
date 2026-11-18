package musicrag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSyncLibraryEnqueuesMissingAndReportsStale(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.Enqueue(ctx, Song{MusicID: "keep", Title: "留下", Status: StatusReady}); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(ctx, Song{MusicID: "gone", Title: "过期", Status: StatusReady}); err != nil {
		t.Fatal(err)
	}
	enqueued, stale, err := SyncLibrary(ctx, store, []LibrarySong{{ID: "keep", Title: "新标题"}, {ID: "fresh", Title: "新歌"}})
	if err != nil || enqueued != 1 || len(stale) != 1 || stale[0] != "gone" {
		t.Fatalf("enqueued=%d stale=%v err=%v", enqueued, stale, err)
	}
	pending, err := store.Pending(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].MusicID != "fresh" {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	if _, _, err = SyncLibrary(ctx, store, nil); err == nil {
		t.Fatal("empty library was accepted")
	}
	if store.Count(ctx) != 3 {
		t.Fatalf("empty library changed the table: %d", store.Count(ctx))
	}
}

func TestOliveTinLibraryReadsSongsAll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "olive" || pass != "secret" || r.URL.Path != "/api/StartActionAndWait" {
			t.Errorf("unexpected request")
		}
		var body struct {
			ActionID string `json:"actionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ActionID != "songs_all" {
			t.Errorf("action = %#v err=%v", body, err)
		}
		_, _ = w.Write([]byte(`{"logEntry":{"exitCode":0,"stdout":"[{\"id\":\"song-1\",\"title\":\"夜曲\",\"artist\":\"周杰伦\",\"year\":2005,\"comment\":\"https://music.example/1\"}]","timedOut":false}}`))
	}))
	defer server.Close()
	songs, err := (OliveTinLibrary{BaseURL: server.URL, Username: "olive", Password: "secret", Client: server.Client()}).Songs(context.Background())
	if err != nil || len(songs) != 1 || songs[0].ID != "song-1" || songs[0].Year != "2005" || songs[0].URL != "https://music.example/1" {
		t.Fatalf("songs=%#v err=%v", songs, err)
	}
}
