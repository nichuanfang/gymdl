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
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func withoutNavidromeRetryDelay(t *testing.T) {
	t.Helper()
	previous := navidromeRetryDelay
	navidromeRetryDelay = func(int) time.Duration { return 0 }
	t.Cleanup(func() { navidromeRetryDelay = previous })
}

func observedNavidromeLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.DebugLevel)
	return zap.New(core), logs
}

func observedEntries(logs *observer.ObservedLogs, level zapcore.Level) []observer.LoggedEntry {
	return logs.FilterLevelExact(level).All()
}

func observedStringField(entry observer.LoggedEntry, key string) string {
	for _, field := range entry.Context {
		if field.Key == key {
			if field.Type == zapcore.StringType {
				return field.String
			}
			return fmt.Sprint(field.Integer)
		}
	}
	return ""
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

func TestNavidromeLibraryLogsInitialLoadAndRetry(t *testing.T) {
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

	logger, logs := observedNavidromeLogger()
	library := &NavidromeLibrary{
		Config: &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p"},
		Client: server.Client(),
		Logger: logger,
	}
	if _, err := library.Contains(context.Background(), Track{Title: "Song", Artist: "Artist", Album: "Album"}); err != nil {
		t.Fatalf("retry load failed: %v", err)
	}
	entries := observedEntries(logs, zap.InfoLevel)
	if len(entries) != 1 || entries[0].Message != "Navidrome 曲库首次加载完成" {
		t.Fatalf("unexpected initial load log: %+v", entries)
	}
	if got := observedStringField(entries[0], "retries"); got != "1" {
		t.Fatalf("initial load retry count = %q, want 1", got)
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
	logger, logs := observedNavidromeLogger()
	library := &NavidromeLibrary{
		Config:  &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p"},
		Client:  server.Client(),
		Logger:  logger,
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
	warnings := observedEntries(logs, zap.WarnLevel)
	if len(warnings) != 1 || warnings[0].Message != "Navidrome 曲库刷新失败，继续使用最近快照" {
		t.Fatalf("stale snapshot warning was not deduplicated: %+v", warnings)
	}
	if got := observedStringField(warnings[0], "attempts"); got != "3" {
		t.Fatalf("warning attempts = %q, want 3", got)
	}
	if got := observedStringField(warnings[0], "last_retry_offset"); got != "0" {
		t.Fatalf("warning retry offset = %q, want 0", got)
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
	logger, logs := observedNavidromeLogger()
	library := &NavidromeLibrary{
		Config:  &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p"},
		Client:  server.Client(),
		Logger:  logger,
		byTitle: map[string][]Track{ownershipTitle(track.Title): {track}},
		expires: now.Add(-20 * time.Minute), snapshotAt: now.Add(-31 * time.Minute),
	}
	_, err := library.Contains(context.Background(), track)
	if err == nil || !strings.Contains(err.Error(), "曲库隧道暂不可用") {
		t.Fatalf("expired snapshot did not fail clearly: %v", err)
	}
	warnings := observedEntries(logs, zap.WarnLevel)
	if len(warnings) != 1 || warnings[0].Message != "Navidrome 最近快照已超过可用时限，暂时无法核对曲库" {
		t.Fatalf("expired snapshot warning missing: %+v", warnings)
	}
}

func TestNavidromeLibraryWarnsWithoutSnapshotAndClearsAfterRecovery(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	var offline atomic.Bool
	offline.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"subsonic-response":{"status":"ok","searchResult3":{"song":[{"id":"1","title":"Song","artist":"Artist","album":"Album"}]}}}`)
	}))
	defer server.Close()

	logger, logs := observedNavidromeLogger()
	library := &NavidromeLibrary{
		Config: &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p"},
		Client: server.Client(),
		Logger: logger,
	}
	track := Track{Title: "Song", Artist: "Artist", Album: "Album", Verified: true}
	if _, err := library.Contains(context.Background(), track); err == nil {
		t.Fatal("unavailable library without a snapshot did not fail")
	}
	library.Invalidate()
	if _, err := library.Contains(context.Background(), track); err == nil {
		t.Fatal("unavailable library without a snapshot did not fail again")
	}
	if warnings := observedEntries(logs, zap.WarnLevel); len(warnings) != 1 {
		t.Fatalf("repeated no-snapshot failure was not deduplicated: %+v", warnings)
	}

	offline.Store(false)
	library.Invalidate()
	if owned, err := library.Contains(context.Background(), track); err != nil || !owned {
		t.Fatalf("library did not recover: owned=%t err=%v", owned, err)
	}
	infos := observedEntries(logs, zap.InfoLevel)
	if len(infos) != 1 || infos[0].Message != "[Navidrome] 曲库连接已恢复，快照刷新成功" {
		t.Fatalf("recovery did not log a fresh snapshot: %+v", infos)
	}
}

func TestNavidromeLibraryLogsRecoveryAndWarnsAgainAfterNewOutage(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	var offline atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"subsonic-response":{"status":"ok","searchResult3":{"song":[{"id":"1","title":"Song","artist":"Artist","album":"Album"}]}}}`)
	}))
	defer server.Close()

	logger, logs := observedNavidromeLogger()
	library := &NavidromeLibrary{
		Config: &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p"},
		Client: server.Client(),
		Logger: logger,
	}
	track := Track{Title: "Song", Artist: "Artist", Album: "Album", Verified: true}
	for attempt := 1; attempt <= 2; attempt++ {
		offline.Store(false)
		library.Invalidate()
		if owned, err := library.Contains(context.Background(), track); err != nil || !owned {
			t.Fatalf("healthy load %d failed: owned=%t err=%v", attempt, owned, err)
		}
		offline.Store(true)
		library.Invalidate()
		if owned, err := library.Contains(context.Background(), track); err != nil || !owned {
			t.Fatalf("outage %d did not use its snapshot: owned=%t err=%v", attempt, owned, err)
		}
	}
	if warnings := observedEntries(logs, zap.WarnLevel); len(warnings) != 2 {
		t.Fatalf("warnings across separate outages = %d, want 2", len(warnings))
	}
	infos := observedEntries(logs, zap.InfoLevel)
	if len(infos) != 2 || infos[0].Message != "Navidrome 曲库首次加载完成" || infos[1].Message != "[Navidrome] 曲库连接已恢复，快照刷新成功" {
		t.Fatalf("unexpected load/recovery logs: %+v", infos)
	}
}

func TestNavidromeLibraryCancellationDoesNotWarn(t *testing.T) {
	previous := navidromeRetryDelay
	navidromeRetryDelay = func(int) time.Duration { return time.Hour }
	t.Cleanup(func() { navidromeRetryDelay = previous })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	logger, logs := observedNavidromeLogger()
	library := &NavidromeLibrary{
		Config: &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p"},
		Client: server.Client(),
		Logger: logger,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()
	_, err := library.Contains(ctx, Track{Title: "Song", Artist: "Artist", Album: "Album"})
	if err == nil || ctx.Err() == nil {
		t.Fatalf("cancellation did not stop the library load: ctx=%v err=%v", ctx.Err(), err)
	}
	if warnings := observedEntries(logs, zap.WarnLevel); len(warnings) != 0 {
		t.Fatalf("cancellation produced warnings: %+v", warnings)
	}
}

func TestNavidromeLibraryWarningsExcludeCredentials(t *testing.T) {
	withoutNavidromeRetryDelay(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	const (
		username = "private-user"
		password = "private-password"
	)
	logger, logs := observedNavidromeLogger()
	library := &NavidromeLibrary{
		Config: &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: username, NavidromePassword: password},
		Client: server.Client(),
		Logger: logger,
	}
	_, _ = library.Contains(context.Background(), Track{Title: "Song", Artist: "Artist", Album: "Album"})
	for _, entry := range logs.All() {
		for _, value := range []string{entry.Message, observedStringField(entry, "reason")} {
			if strings.Contains(value, username) || strings.Contains(value, password) {
				t.Fatalf("log leaked credentials: %+v", entry)
			}
		}
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
