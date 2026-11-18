package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func ytProvider(t *testing.T, fixture any) *Providers {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	cookie := []byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t4102444800\tSAPISID\ttest-cookie\n")
	os.WriteFile(filepath.Join(dir, "cookies.txt"), cookie, 0600)
	os.WriteFile(filepath.Join(dir, "fixture.json"), data, 0600)
	stub := `#!/bin/sh
next_cookie=0
for arg in "$@"; do
 if [ "$next_cookie" = 1 ]; then printf '%s' 'modified cookie copy' > "$arg"; next_cookie=0; fi
 if [ "$arg" = '--cookies' ]; then next_cookie=1; fi
done
if [ "$GYMDL_YT_TEST_ERROR" = '1' ]; then
 printf '%s\n' 'ERROR: [youtube:tab] LL: YouTube said: The playlist does not exist.' >&2
 exit 1
fi
cat "$GYMDL_YT_TEST_FIXTURE"
`
	os.WriteFile(filepath.Join(dir, "yt-dlp"), []byte(stub), 0700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GYMDL_YT_TEST_FIXTURE", filepath.Join(dir, "fixture.json"))
	t.Cleanup(func() {
		got, err := os.ReadFile(filepath.Join(dir, "cookies.txt"))
		if err != nil || string(got) != string(cookie) {
			t.Error("yt-dlp modified shared CookieCloud file")
		}
	})
	return &Providers{Config: &config.Config{CookieCloud: &config.CookieCloudConfig{CookieFilePath: dir, CookieFile: "cookies.txt"}}}
}

func TestYouTubeFavoriteCompletenessAndAccountFallback(t *testing.T) {
	for _, mode := range []string{"valid", "partial", "duplicate", "account_fallback", "logged_out", "inaccessible"} {
		t.Run(mode, func(t *testing.T) {
			list := map[string]any{"channel_id": "UCstable-account", "playlist_count": 1, "entries": []any{map[string]any{"id": "abc", "title": "Video title", "duration": 200}}}
			switch mode {
			case "partial":
				list["playlist_count"] = 2
			case "duplicate":
				list["playlist_count"] = 2
				list["entries"] = []any{map[string]any{"id": "abc"}, map[string]any{"id": "abc"}}
			case "account_fallback", "logged_out":
				delete(list, "channel_id")
			}
			p := ytProvider(t, list)
			if mode == "inaccessible" {
				t.Setenv("GYMDL_YT_TEST_ERROR", "1")
			}
			if mode == "account_fallback" || mode == "logged_out" {
				p.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != "www.youtube.com" {
						t.Error("unexpected account probe host")
					}
					cookie, err := r.Cookie("SAPISID")
					if err != nil || cookie.Value != "test-cookie" {
						t.Error("account probe lost auth")
					}
					body := `{"LOGGED_IN":true,"DATASYNC_ID":"private-stable-account"}`
					if mode == "logged_out" {
						body = `{"LOGGED_IN":false}`
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
			}
			snap, err := p.Fetch(context.Background(), "youtube")
			if mode == "valid" || mode == "account_fallback" {
				if err != nil || len(snap.Tracks) != 1 || !snap.Complete || snap.Account == "" {
					t.Fatal(snap, err)
				}
				if strings.Contains(snap.Account, "private-stable-account") {
					t.Fatal("raw account context persisted")
				}
			} else if err == nil {
				t.Fatal("unsafe snapshot accepted", mode)
			}
			if mode == "inaccessible" && !strings.Contains(err.Error(), "未建立基线") {
				t.Fatal("unhelpful access error", err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestYouTubeOnlyTrustsMusicMetadata(t *testing.T) {
	for _, mode := range []string{"song", "video", "unknown", "live"} {
		t.Run(mode, func(t *testing.T) {
			m := map[string]any{"id": "abc", "title": "Uploader title", "track": "Song", "artist": "Artist", "duration": 200, "categories": []string{"Music"}}
			switch mode {
			case "video":
				m["categories"] = []string{"Education"}
			case "unknown":
				delete(m, "track")
				delete(m, "artist")
			case "live":
				m["is_live"] = true
			}
			p := ytProvider(t, m)
			tr, skip, err := p.Enrich(context.Background(), Track{Platform: "youtube", ID: "abc", URL: "https://www.youtube.com/watch?v=abc"})
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "song":
				if skip || !tr.Verified || tr.Title != "Song" || tr.Artist != "Artist" {
					t.Fatal(tr, skip)
				}
			case "unknown":
				if skip || tr.Verified {
					t.Fatal("uploader/title guessed as music")
				}
			default:
				if !skip {
					t.Fatal("non-single autoimported")
				}
			}
		})
	}
}

func TestSourceSyncFailurePreservesSnapshotAndPausesExpiredLogin(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := testTrack("1", "Song", "Artist")
	s.Apply(ctx, 1, 1, snapshot("account", a), true)
	notices := 0
	svc := Service{Store: s, Enabled: true, Provider: stubProvider{fetchErr: fmt.Errorf("登录失效，请重登")}, Notify: func(int64, string) error { notices++; return nil }}
	if _, err := svc.Sync(ctx, 1); err == nil {
		t.Fatal("source failure hidden")
	}
	bindings, _ := s.Bindings(ctx, 1)
	if !bindings[0].Paused || bindings[0].Error == "" {
		t.Fatal("expired source not paused")
	}
	favorites, _ := s.Favorites(ctx, 1)
	if len(favorites) != 1 {
		t.Fatal("failed source cleared baseline")
	}
	svc.Sync(ctx, 1)
	if notices != 1 {
		t.Fatal("repeated login alerts")
	}
}

func TestYoutubeDependencyFailureDoesNotBlameCookie(t *testing.T) {
	err := youtubeReadError(exec.ErrNotFound, "")
	if !strings.Contains(err.Error(), "yt-dlp") || strings.Contains(err.Error(), "登录失效") {
		t.Fatal("dependency failure misreported as login failure", err)
	}
	err = youtubeReadError(errors.New("exit 1"), "Cookies are no longer valid")
	if !strings.Contains(err.Error(), "登录失效") {
		t.Fatal("expired cookie not distinguished", err)
	}
}
