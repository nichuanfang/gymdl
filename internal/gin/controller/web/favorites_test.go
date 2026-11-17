package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func TestQQFavoritePaginationAndIncompleteBaseline(t *testing.T) {
	for _, mode := range []string{"normalized", "legacy", "large_id", "partial", "changed_account", "expired"} {
		t.Run(mode, func(t *testing.T) {
			useQQMusicCredentialPath(t)
			pages := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "check_expired") {
					expired := mode == "expired"
					json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": expired})
					return
				}
				if strings.HasSuffix(r.URL.Path, "homepage") {
					fmt.Fprint(w, `{"code":0,"data":{"base_info":{"encrypted_uin":"encrypted"}}}`)
					return
				}
				if r.URL.Path != "/user/encrypted/fav/songs" {
					t.Error("did not use encrypted account", r.URL.Path)
				}
				pages++
				if r.Header.Get("Cookie") == "" {
					t.Error("missing active login")
				}
				id := pages
				more := 0
				if pages == 1 {
					more = 1
				}
				total := 2
				if mode == "partial" {
					total = 3
				}
				account := "123"
				if mode == "large_id" {
					account = "1152921504885411023"
				}
				if mode == "changed_account" {
					account = "456"
				}
				data := map[string]any{"code": 0, "total": total, "hasmore": more, "songs": []any{map[string]any{"id": id, "mid": fmt.Sprint(id), "title": "Song", "singer": []any{map[string]any{"name": "Artist"}}, "album": map[string]any{"name": "Album"}, "interval": 200}}, "info": map[string]any{"creator": map[string]any{"musicid": json.Number(account)}}}
				if mode == "legacy" {
					data["songlist"] = data["songs"]
					delete(data, "songs")
					data["dirinfo"] = data["info"]
					delete(data, "info")
					data["total_song_num"] = data["total"]
					delete(data, "total")
				}
				json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
			}))
			defer server.Close()
			accountWant := "123"
			if mode == "large_id" {
				accountWant = "1152921504885411023"
			}
			cfg := &config.Config{QQMusicApiConfig: &config.QQMusicApiConfig{Enable: true, Endpoint: server.URL, MusicId: accountWant, MusicKey: "test-key"}}
			account, tracks, err := QQFavorites(context.Background(), cfg)
			if mode == "normalized" || mode == "legacy" || mode == "large_id" {
				if err != nil || account != accountWant || len(tracks) != 2 || pages != 2 {
					t.Fatalf("account=%s tracks=%d pages=%d err=%v", account, len(tracks), pages, err)
				}
			} else if err == nil {
				t.Fatal("invalid snapshot accepted", mode)
			}
		})
	}
}
