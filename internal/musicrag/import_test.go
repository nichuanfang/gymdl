package musicrag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestImportFromN8NCopiesRowsOnceAndSkipsUnsafeIDs(t *testing.T) {
	store := openTestStore(t)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-N8N-API-KEY") != "secret" || r.URL.Query().Get("limit") != "100" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":[{"music_id":"song-1","title":"夜曲","artist":"周杰伦","year":2005,"status":"成功","genre":"流行","mood":"忧伤","language":"国语","description":"简介"},{"music_id":"bad id"}],"nextCursor":"page-2"}`))
			return
		}
		if r.URL.Query().Get("cursor") != "page-2" {
			t.Errorf("cursor = %q", r.URL.Query().Get("cursor"))
		}
		_, _ = w.Write([]byte(`{"data":[{"music_id":"song-2","title":"晴天","status":"待处理"}],"nextCursor":""}`))
	}))
	defer server.Close()

	inserted, err := ImportFromN8N(context.Background(), store, N8NSource{BaseURL: server.URL, APIKey: "secret", TableID: "table-1", Client: server.Client()})
	if err != nil || inserted != 2 || store.Count(context.Background()) != 2 {
		t.Fatalf("inserted=%d count=%d err=%v", inserted, store.Count(context.Background()), err)
	}
	again, err := ImportFromN8N(context.Background(), store, N8NSource{BaseURL: server.URL, APIKey: "secret", TableID: "table-1", Client: server.Client()})
	if err != nil || again != 0 || calls != 2 {
		t.Fatalf("second import = %d calls=%d err=%v", again, calls, err)
	}
}

func TestImportFromN8NDoesNotMarkAnEmptyTable(t *testing.T) {
	store := openTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[],"nextCursor":null}`))
	}))
	defer server.Close()
	_, err := ImportFromN8N(context.Background(), store, N8NSource{BaseURL: server.URL, APIKey: "secret", TableID: "table-1", Client: server.Client()})
	if err == nil {
		t.Fatal("empty remote table was marked imported")
	}
	done, statusErr := store.Imported(context.Background())
	if statusErr != nil || done {
		t.Fatalf("imported=%v err=%v", done, statusErr)
	}
}
