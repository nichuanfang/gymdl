package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func TestPlaylistReadsReuseLoginAndOliveTinLists(t *testing.T) {
	resetReadCache()
	t.Cleanup(resetReadCache)
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			calls["login"]++
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
		case "/api/StartActionAndWait":
			var action oliveTinActionRequest
			if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
				t.Fatal(err)
			}
			calls[action.ActionID]++
			_ = json.NewEncoder(w).Encode(map[string]any{"logEntry": map[string]any{"exitCode": 0, "output": "[]", "timedOut": false}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		NavidromeURL:      server.URL,
		NavidromeUsername: "user",
		NavidromePassword: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err = service.navidromeLogin(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = service.aiPlaylists(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = service.nextUnassignedSong(ctx, nil); err != nil && err != errNoPendingPlaylistSong {
			t.Fatal(err)
		}
	}
	if calls["login"] != 1 || calls["ai_playlists"] != 2 || calls["songs_unplaylist"] != 1 {
		t.Fatalf("cached reads still hit the network: %#v", calls)
	}
}

func resetReadCache() {
	readCacheMu.Lock()
	oliveTinReads = map[string]timedText{}
	navidromeTokens = map[string]timedText{}
	readCacheMu.Unlock()
}
