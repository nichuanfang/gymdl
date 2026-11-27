package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

func withoutNavidromeRetryDelay(t *testing.T) {
	t.Helper()
	previous := navidromeRetryDelay
	navidromeRetryDelay = func(int) time.Duration { return 0 }
	t.Cleanup(func() { navidromeRetryDelay = previous })
}

func TestReadNavidromeAudioRetriesTransientNotFound(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.NotFound(w, nil)
			return
		}
		fmt.Fprint(w, `{"subsonic-response":{"status":"ok","searchResult3":{"song":[{"id":"1","title":"Song","artist":"Artist","album":"Album"}]}}}`)
	}))
	defer server.Close()

	songs, err := ReadNavidromeAudio(context.Background(), &config.PlaylistOrganizerConfig{
		NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p",
	}, server.Client())
	if err != nil || len(songs) != 1 || calls.Load() != 2 {
		t.Fatalf("transient 404 was not retried: calls=%d songs=%d err=%v", calls.Load(), len(songs), err)
	}
}

func TestReadNavidromeAudioDoesNotRetryAuthenticationFailure(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := ReadNavidromeAudio(context.Background(), &config.PlaylistOrganizerConfig{
		NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p",
	}, server.Client())
	if err == nil || !strings.Contains(err.Error(), "登录失效") || calls.Load() != 1 {
		t.Fatalf("authentication failure was retried or misclassified: calls=%d err=%v", calls.Load(), err)
	}
}

func TestReadNavidromeAudioStopsRetryWaitWhenContextEnds(t *testing.T) {
	previous := navidromeRetryDelay
	navidromeRetryDelay = func(int) time.Duration { return time.Hour }
	t.Cleanup(func() { navidromeRetryDelay = previous })
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := ReadNavidromeAudio(ctx, &config.PlaylistOrganizerConfig{
		NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p",
	}, server.Client())
	if err == nil || ctx.Err() == nil || calls.Load() != 1 {
		t.Fatalf("retry ignored context cancellation: calls=%d ctx=%v err=%v", calls.Load(), ctx.Err(), err)
	}
}

func TestNavidromeLibraryUsesRecentSnapshotDuringOutage(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	now := time.Now()
	track := Track{Title: "Song", Artist: "Artist", Album: "Album", Verified: true}
	library := &NavidromeLibrary{
		Config:  &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p"},
		Client:  server.Client(),
		byTitle: map[string][]Track{ownershipTitle(track.Title): {track}},
		expires: now.Add(-time.Minute), snapshotAt: now.Add(-15 * time.Minute),
	}
	owned, err := library.Contains(context.Background(), track)
	if err != nil || !owned {
		t.Fatalf("recent snapshot was not used: owned=%t err=%v", owned, err)
	}
	state := library.SnapshotState()
	if !state.Stale || calls.Load() != navidromeAttempts {
		t.Fatalf("stale state or retry count mismatch: state=%+v calls=%d", state, calls.Load())
	}
	if _, err = library.Contains(context.Background(), track); err != nil || calls.Load() != navidromeAttempts {
		t.Fatalf("retry cooldown triggered another refresh: calls=%d err=%v", calls.Load(), err)
	}
}

func TestNavidromeLibraryRejectsSnapshotOlderThanMaxAge(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	now := time.Now()
	track := Track{Title: "Song", Artist: "Artist", Album: "Album", Verified: true}
	library := &NavidromeLibrary{
		Config:  &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p"},
		Client:  server.Client(),
		byTitle: map[string][]Track{ownershipTitle(track.Title): {track}},
		expires: now.Add(-20 * time.Minute), snapshotAt: now.Add(-31 * time.Minute),
	}
	_, err := library.Contains(context.Background(), track)
	if err == nil || !strings.Contains(err.Error(), "曲库隧道暂不可用") {
		t.Fatalf("expired snapshot did not fail clearly: %v", err)
	}
}

func TestNavidromeLibraryKeepsLastCompleteSnapshotWhenLaterPageFails(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("songOffset") == "0" {
			var songs strings.Builder
			for i := 0; i < 500; i++ {
				if i > 0 {
					songs.WriteByte(',')
				}
				fmt.Fprintf(&songs, `{"id":"new-%d","title":"New %d","artist":"Artist","album":"Album"}`, i, i)
			}
			fmt.Fprintf(w, `{"subsonic-response":{"status":"ok","searchResult3":{"song":[%s]}}}`, songs.String())
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	now := time.Now()
	old := Track{Title: "Old", Artist: "Artist", Album: "Album", Verified: true}
	library := &NavidromeLibrary{
		Config:  &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p"},
		Client:  server.Client(),
		byTitle: map[string][]Track{ownershipTitle(old.Title): {old}},
		expires: now.Add(-time.Minute), snapshotAt: now.Add(-15 * time.Minute),
	}
	owned, err := library.Contains(context.Background(), old)
	if err != nil || !owned {
		t.Fatalf("partial refresh replaced the previous snapshot: owned=%t err=%v", owned, err)
	}
	if _, ok := library.byTitle[ownershipTitle("New 1")]; ok {
		t.Fatal("partial page data was committed")
	}
	if calls.Load() < 2 {
		t.Fatalf("pagination failure was not exercised: calls=%d", calls.Load())
	}
}

func TestReadNavidromeAudioRetriesServerError(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"subsonic-response":{"status":"ok","searchResult3":{"song":[{"id":"1","title":"Song","artist":"Artist","album":"Album"}]}}}`)
	}))
	defer server.Close()

	songs, err := ReadNavidromeAudio(context.Background(), &config.PlaylistOrganizerConfig{
		NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p",
	}, server.Client())
	if err != nil || len(songs) != 1 || calls.Load() != 2 {
		t.Fatalf("transient 5xx was not retried: calls=%d songs=%d err=%v", calls.Load(), len(songs), err)
	}
}

func TestReadNavidromeAudioDoesNotRetryMalformedJSON(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"subsonic-response":`)
	}))
	defer server.Close()

	_, err := ReadNavidromeAudio(context.Background(), &config.PlaylistOrganizerConfig{
		NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p",
	}, server.Client())
	if err == nil || !strings.Contains(err.Error(), "响应异常") || calls.Load() != 1 {
		t.Fatalf("malformed JSON was retried or misclassified: calls=%d err=%v", calls.Load(), err)
	}
}

func TestReadNavidromeAudioClassifiesSubsonicAuthenticationError(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"subsonic-response":{"status":"failed","error":{"code":40,"message":"Wrong username or password"}}}`)
	}))
	defer server.Close()

	_, err := ReadNavidromeAudio(context.Background(), &config.PlaylistOrganizerConfig{
		NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p",
	}, server.Client())
	if err == nil || !strings.Contains(err.Error(), "登录失效") || calls.Load() != 1 {
		t.Fatalf("Subsonic authentication failure was retried or misclassified: calls=%d err=%v", calls.Load(), err)
	}
}

func TestNavidromeLibraryKeepsEmptySnapshotDuringOutage(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	var offline atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"subsonic-response":{"status":"ok","searchResult3":{"song":[]}}}`)
	}))
	defer server.Close()

	track := Track{Title: "Song", Artist: "Artist", Album: "Album", Verified: true}
	library := &NavidromeLibrary{
		Config: &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p"},
		Client: server.Client(),
	}
	owned, err := library.Contains(context.Background(), track)
	if err != nil || owned {
		t.Fatalf("empty library failed to load: owned=%t err=%v", owned, err)
	}
	state := library.SnapshotState()
	if state.LoadedAt.IsZero() {
		t.Fatal("successfully loaded empty library did not record a snapshot")
	}
	offline.Store(true)
	library.Invalidate()
	owned, err = library.Contains(context.Background(), track)
	if err != nil || owned || !library.SnapshotState().Stale {
		t.Fatalf("empty snapshot was not retained during outage: owned=%t err=%v state=%+v", owned, err, library.SnapshotState())
	}
}

func TestRediscoverBatchZeroSnapshotUnix(t *testing.T) {
	if got := (RediscoverBatch{}).LibrarySnapshotUnix(); got != 0 {
		t.Fatalf("zero snapshot converted to %d", got)
	}
	now := time.Now().Truncate(time.Second)
	batch := RediscoverBatch{LibrarySnapshotAt: now}
	if got := batch.LibrarySnapshotUnix(); got != now.Unix() {
		t.Fatalf("snapshot converted to %d, want %d", got, now.Unix())
	}
}
