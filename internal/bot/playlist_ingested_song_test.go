package bot

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func TestFindIngestedSongDistinguishesVersions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		songs string
		want  string
		err   error
	}{
		{"matching file", `{"id":"low","title":"歌名","artist":"歌手","album":"专辑","suffix":"flac","size":1000},
			{"id":"high","title":"歌名","artist":"歌手","album":"专辑","suffix":"flac","size":2000}`, "high", nil},
		{"not scanned yet", `{"id":"low","title":"歌名","artist":"歌手","album":"专辑","suffix":"flac","size":1000}`, "", errIngestedSongNotFound},
		{"ambiguous versions", `{"id":"a","title":"歌名","artist":"歌手","album":"专辑","suffix":"flac","size":2000},
			{"id":"b","title":"歌名","artist":"歌手","album":"专辑","suffix":"flac","size":2000}`, "", errIngestedSongAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				hash := md5.Sum([]byte("password" + q.Get("s")))
				if r.URL.Path != "/navidrome/rest/search3.view" || q.Get("u") != "gymdl" ||
					q.Get("t") != fmt.Sprintf("%x", hash) || q.Get("query") != "歌名" {
					t.Errorf("unexpected Navidrome request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				fmt.Fprintf(w, `{"subsonic-response":{"status":"ok","searchResult3":{"song":[%s]}}}`, tc.songs)
			}))
			defer server.Close()
			service, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
				NavidromeURL: server.URL + "/navidrome", NavidromeUsername: "gymdl", NavidromePassword: "password", OliveTinURL: server.URL,
			})
			if err != nil {
				t.Fatal(err)
			}
			song, err := service.findIngestedSong(context.Background(), pendingMusicAction{
				title: "歌名", artist: "歌手", album: "专辑", extension: "flac", size: 2000,
			})
			if song.ID != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("got (%q, %v), want (%q, %v)", song.ID, err, tc.want, tc.err)
			}
		})
	}
}
