package web

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	webtask "github.com/nichuanfang/gymdl/internal/gin/task"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/studio-b12/gowebdav"
)

func duplicateTestSong() *music.SongInfo {
	return &music.SongInfo{SongName: " Song ", SongArtists: " Artist ", SongAlbum: " Album ", MusicSize: 5, FileExt: ".MP3"}
}

func duplicateTestTags(name, artist, album, ext string, size int64) *music.SongInfo {
	return &music.SongInfo{SongName: name, SongArtists: artist, SongAlbum: album, FileExt: ext, MusicSize: size}
}

func TestDuplicateIdentityUsesOnlyFourExactFields(t *testing.T) {
	expected := normalizedSongInfo(duplicateTestSong())
	matching := duplicateTestTags("Song", "Artist", "Album", "mp3", 99_999_999)
	if !expected.matches(matching) {
		t.Fatal("size must not affect a match when the four requested fields match")
	}
	cases := []struct {
		name string
		info *music.SongInfo
	}{
		{"title", duplicateTestTags("Different", "Artist", "Album", "mp3", 5)},
		{"artist", duplicateTestTags("Song", "Different", "Album", "mp3", 5)},
		{"album", duplicateTestTags("Song", "Artist", "Different", "mp3", 5)},
		{"format", duplicateTestTags("Song", "Artist", "Album", "flac", 5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if expected.matches(tc.info) {
				t.Fatalf("%s mismatch must not be considered duplicate", tc.name)
			}
		})
	}
	if normalizeAudioExt(" .FLAC ") != "flac" || normalizeAudioExt("MP3") != "mp3" {
		t.Fatal("extension normalization must trim whitespace, dot prefix, and case")
	}
}

func TestLocalDuplicateTargetAndTagReadFailures(t *testing.T) {
	target := duplicateTestSong()
	if _, status, _ := findLocalDuplicate(filepath.Join(t.TempDir(), "not-created"), target, music.ReadTags); status != "clear" {
		t.Fatalf("missing local tidy directory should be clear, got %q", status)
	}
	if _, status, _ := findLocalDuplicate("", target, music.ReadTags); status != "unknown" {
		t.Fatalf("missing local target should be unknown, got %q", status)
	}

	root := t.TempDir()
	file := filepath.Join(root, "old-name.mp3")
	if err := os.WriteFile(file, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := func(path string) (*music.SongInfo, error) {
		resolved, err := filepath.EvalSymlinks(file)
		if err != nil || path != resolved {
			t.Fatalf("unsafe/unexpected local file path: %s (expected %s)", path, resolved)
		}
		return duplicateTestTags("Song", "Artist", "Album", "mp3", 5), nil
	}
	match, status, _ := findLocalDuplicate(root, target, reader)
	if status != "duplicate" || match == nil || match.Name != "old-name.mp3" {
		t.Fatalf("expected strict local duplicate, got status=%s match=%#v", status, match)
	}

	_, status, _ = findLocalDuplicate(root, target, func(string) (*music.SongInfo, error) {
		return nil, errors.New("taglib failed")
	})
	if status != "unknown" {
		t.Fatalf("tag read errors must remain unknown, got %q", status)
	}
	_, status, _ = findLocalDuplicate(root, target, func(string) (*music.SongInfo, error) {
		return duplicateTestTags("Song", "", "", "mp3", 5), nil
	})
	if status != "unknown" {
		t.Fatalf("incomplete tags must remain unknown, got %q", status)
	}
}

func TestLocalDuplicateIgnoresFileSize(t *testing.T) {
	target := duplicateTestTags("Human World", "Faye Wong", "Fable", "flac", 100_000)
	root := t.TempDir()
	file := filepath.Join(root, "Faye Wong - Human World.flac")
	if err := os.WriteFile(file, bytes.Repeat([]byte("x"), 128), 0o600); err != nil {
		t.Fatal(err)
	}

	match, status, _ := findLocalDuplicate(root, target, func(string) (*music.SongInfo, error) {
		return duplicateTestTags("Human World", "Faye Wong", "Fable", "flac", 128), nil
	})
	if status != "duplicate" || match == nil || match.Path != filepath.Base(file) {
		t.Fatalf("matching four fields must be duplicate regardless of file size: status=%s match=%#v", status, match)
	}
}

func TestLocalDuplicateMismatchAndSymlinkSafety(t *testing.T) {
	target := duplicateTestSong()
	root := t.TempDir()
	cases := []struct {
		name string
		file string
		data []byte
		tags *music.SongInfo
	}{
		{"title", "a.mp3", []byte("12345"), duplicateTestTags("Other", "Artist", "Album", "mp3", 5)},
		{"artist", "b.mp3", []byte("12345"), duplicateTestTags("Song", "Other", "Album", "mp3", 5)},
		{"album", "c.mp3", []byte("12345"), duplicateTestTags("Song", "Artist", "Other", "mp3", 5)},
		{"format", "e.flac", []byte("12345"), duplicateTestTags("Song", "Artist", "Album", "flac", 5)},
	}
	for _, tc := range cases {
		if err := os.WriteFile(filepath.Join(root, tc.file), tc.data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, status, _ := findLocalDuplicate(root, target, func(file string) (*music.SongInfo, error) {
		for _, tc := range cases {
			if filepath.Base(file) == tc.file {
				return tc.tags, nil
			}
		}
		return nil, errors.New("unexpected candidate")
	})
	if status != "clear" {
		t.Fatalf("title/artist/album/format mismatch should not be duplicate, got %q", status)
	}

	sizeRoot := t.TempDir()
	sizeFile := filepath.Join(sizeRoot, "same-tags-different-size.mp3")
	if err := os.WriteFile(sizeFile, []byte("123456"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, status, _ = findLocalDuplicate(sizeRoot, target, func(string) (*music.SongInfo, error) {
		return duplicateTestTags("Song", "Artist", "Album", "mp3", 6), nil
	})
	if status != "duplicate" {
		t.Fatalf("matching four fields must be duplicate despite the size delta, got %q", status)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "escape.mp3"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, status, reason := findLocalDuplicate(root, target, func(path string) (*music.SongInfo, error) {
		if strings.Contains(path, "outside") || strings.Contains(path, "escape") {
			t.Fatalf("tag reader was passed an escaped path: %s", path)
		}
		return nil, errors.New("unreadable candidate")
	})
	if status != "unknown" || reason == "" {
		t.Fatalf("unsafe or unreadable candidate should not be reported clear: status=%s reason=%s", status, reason)
	}
}

type fakeInfo struct {
	name string
	size int64
	dir  bool
	mod  time.Time
}

func (f fakeInfo) Name() string { return f.name }
func (f fakeInfo) Size() int64  { return f.size }
func (f fakeInfo) Mode() os.FileMode {
	if f.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (f fakeInfo) ModTime() time.Time { return f.mod }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

type fakeDuplicateRemote struct {
	entries   []os.FileInfo
	readErr   error
	streamErr error
	streams   map[string][]byte
	paths     []string
}

func (f *fakeDuplicateRemote) ReadDir(path string) ([]os.FileInfo, error) {
	f.paths = append(f.paths, path)
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.entries, nil
}
func (f *fakeDuplicateRemote) ReadStream(path string) (io.ReadCloser, error) {
	f.paths = append(f.paths, path)
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	data, ok := f.streams[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func TestWebDAVDuplicateCandidatePathMissingAndReadFailures(t *testing.T) {
	target := duplicateTestSong()
	missing := &fakeDuplicateRemote{readErr: gowebdav.NewPathError("ReadDir", "/library/Artist/Album", http.StatusNotFound)}
	if _, status, _ := findWebDAVDuplicate("library", target, missing, music.ReadTags); status != "clear" {
		t.Fatalf("missing artist/album directory should be clear, got %q", status)
	}
	if len(missing.paths) != 1 || missing.paths[0] != "/library/Artist/Album" {
		t.Fatalf("unexpected safe candidate path: %v", missing.paths)
	}

	failed := &fakeDuplicateRemote{readErr: errors.New("permission denied")}
	if _, status, _ := findWebDAVDuplicate("library", target, failed, music.ReadTags); status != "unknown" {
		t.Fatalf("WebDAV read failure should be unknown, got %q", status)
	}

	info := fakeInfo{name: "file.mp3", size: 5}
	remotePath := "/library/Artist/Album/file.mp3"
	streamFailed := &fakeDuplicateRemote{entries: []os.FileInfo{info}, streamErr: errors.New("stream failed")}
	if _, status, _ := findWebDAVDuplicate("library", target, streamFailed, music.ReadTags); status != "unknown" {
		t.Fatalf("WebDAV audio stream failure should be unknown, got %q", status)
	}

	matching := &fakeDuplicateRemote{entries: []os.FileInfo{info}, streams: map[string][]byte{remotePath: []byte("12345")}}
	match, status, _ := findWebDAVDuplicate("library", target, matching, func(path string) (*music.SongInfo, error) {
		if filepath.Ext(path) != ".mp3" {
			t.Fatalf("temporary file must retain the media extension: %s", path)
		}
		return duplicateTestTags("Song", "Artist", "Album", "mp3", 5), nil
	})
	if status != "duplicate" || match == nil || match.Path != "library/Artist/Album/file.mp3" {
		t.Fatalf("expected WebDAV duplicate at candidate path, got status=%s match=%#v", status, match)
	}

	legacyTarget := duplicateTestTags("Song", "Artist", "Album", "flac", 5)
	legacy := &fakeDuplicateRemote{
		entries: []os.FileInfo{fakeInfo{name: "Song - legacy.flac", size: 10_000_005}},
		streams: map[string][]byte{"/library/Artist/Album/Song - legacy.flac": []byte("audio")},
	}
	legacyMatch, legacyStatus, legacyReason := findWebDAVDuplicate("library", legacyTarget, legacy, func(string) (*music.SongInfo, error) {
		return duplicateTestTags("Song", "Artist", "Album", "flac", 10_000_005), nil
	})
	if legacyStatus != "duplicate" || legacyMatch == nil {
		t.Fatalf("WebDAV files with matching four fields should be duplicate regardless of size: status=%s match=%#v reason=%q", legacyStatus, legacyMatch, legacyReason)
	}

	unknown := &fakeDuplicateRemote{entries: []os.FileInfo{info}, streams: map[string][]byte{remotePath: []byte("12345")}}
	if _, status, _ := findWebDAVDuplicate("library", target, unknown, func(string) (*music.SongInfo, error) {
		return nil, errors.New("tag read error")
	}); status != "unknown" {
		t.Fatalf("WebDAV tag errors must be unknown, got %q", status)
	}
}

func TestDuplicateSubmitConfirmationAndForceSubmission(t *testing.T) {
	previousPreflight := duplicatePreflight
	previousManager := manager
	previousConfig := GetWebConfig()
	t.Cleanup(func() {
		duplicatePreflight = previousPreflight
		manager = previousManager
		SetWebConfig(previousConfig)
	})
	SetWebConfig(&config.Config{Tidy: &config.TidyConfig{Mode: 1, DistDir: t.TempDir()}})
	taskManager := webtask.NewTaskManagerWithDBPath(&config.Config{AdditionalConfig: &config.AdditionalConfig{}}, filepath.Join(t.TempDir(), "tasks.sqlite3"), "")
	if err := taskManager.InitializationError(); err != nil {
		t.Fatal(err)
	}
	manager = taskManager
	t.Cleanup(func() { _ = taskManager.Close() })
	calls := 0
	duplicatePreflight = func(*config.Config, string) duplicateCheckResult {
		calls++
		return duplicateCheckResult{Status: "duplicate", Match: &duplicateMatch{Path: "Artist/Album/song.mp3"}}
	}

	gin.SetMode(gin.TestMode)
	blocked := submitTaskRequest(t, `{"url":"https://example.test/track"}`)
	if blocked.Code != http.StatusConflict || calls != 1 || !strings.Contains(blocked.Body.String(), `"confirmation_required":true`) {
		t.Fatalf("duplicate submit should require confirmation: code=%d calls=%d body=%s", blocked.Code, calls, blocked.Body.String())
	}

	calls = 0
	forced := submitTaskRequest(t, `{"url":"https://example.test/track","force_download":true}`)
	if forced.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("confirmed force submit should skip only the second preflight, then validate link submission: code=%d calls=%d body=%s", forced.Code, calls, forced.Body.String())
	}
}

func TestUnknownDuplicatePreflightRequiresConfirmation(t *testing.T) {
	previousPreflight := duplicatePreflight
	previousManager := manager
	previousConfig := GetWebConfig()
	t.Cleanup(func() {
		duplicatePreflight = previousPreflight
		manager = previousManager
		SetWebConfig(previousConfig)
	})
	SetWebConfig(&config.Config{Tidy: &config.TidyConfig{Mode: 1, DistDir: t.TempDir()}})
	taskManager := webtask.NewTaskManagerWithDBPath(&config.Config{AdditionalConfig: &config.AdditionalConfig{}}, filepath.Join(t.TempDir(), "tasks.sqlite3"), "")
	if err := taskManager.InitializationError(); err != nil {
		t.Fatal(err)
	}
	manager = taskManager
	t.Cleanup(func() { _ = taskManager.Close() })
	duplicatePreflight = func(*config.Config, string) duplicateCheckResult {
		return duplicateCheckResult{Status: "unknown", Reason: "暂时无法读取整理目录"}
	}

	recorder := submitTaskRequest(t, `{"url":"https://example.test/track"}`)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "暂时无法读取整理目录") {
		t.Fatalf("unknown duplicate state should ask for confirmation: code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPlaylistLinksAreNotSingleTrackPreflightTargets(t *testing.T) {
	if isSingleDownloadLink(processor.LinkNetEase, "https://music.163.com/playlist?id=123") {
		t.Fatal("playlist link must not be treated as a single track")
	}
	if !isSingleDownloadLink(processor.LinkNetEase, "https://music.163.com/song?id=123") {
		t.Fatal("single song link should receive download-time duplicate preflight")
	}
}

func submitTaskRequest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/web/task/submit", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	HandleSubmitTask(ctx)
	return recorder
}

func TestDuplicateSourceMetadataCacheAndSingleflight(t *testing.T) {
	key := "test-duplicate-source-metadata-cache"
	duplicateMetadataCache.Lock()
	delete(duplicateMetadataCache.items, key)
	duplicateMetadataCache.Unlock()
	t.Cleanup(func() {
		duplicateMetadataCache.Lock()
		delete(duplicateMetadataCache.items, key)
		duplicateMetadataCache.Unlock()
	})

	var calls atomic.Int32
	resolver := func() (*music.SongInfo, error) {
		calls.Add(1)
		time.Sleep(15 * time.Millisecond)
		return duplicateTestTags("Song", "Artist", "Album", "flac", 42), nil
	}
	const concurrentRequests = 12
	results := make(chan *music.SongInfo, concurrentRequests)
	var wg sync.WaitGroup
	for i := 0; i < concurrentRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := loadDuplicateSourceMetadata(key, resolver)
			if err != nil {
				t.Errorf("metadata lookup failed: %v", err)
				return
			}
			results <- info
		}()
	}
	wg.Wait()
	close(results)
	for info := range results {
		if info.SongName != "Song" || info.MusicSize != 42 {
			t.Fatalf("unexpected cached metadata: %#v", info)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent checks should collapse into one provider lookup, got %d", calls.Load())
	}

	if _, err := loadDuplicateSourceMetadata(key, resolver); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("valid metadata should be served from the short cache, got %d provider lookups", calls.Load())
	}
}

func TestDuplicateSourceMetadataDoesNotCacheFailures(t *testing.T) {
	key := "test-duplicate-source-metadata-failure"
	duplicateMetadataCache.Lock()
	delete(duplicateMetadataCache.items, key)
	duplicateMetadataCache.Unlock()
	t.Cleanup(func() {
		duplicateMetadataCache.Lock()
		delete(duplicateMetadataCache.items, key)
		duplicateMetadataCache.Unlock()
	})

	var calls atomic.Int32
	resolver := func() (*music.SongInfo, error) {
		calls.Add(1)
		return nil, errors.New("provider unavailable")
	}
	for i := 0; i < 2; i++ {
		if _, err := loadDuplicateSourceMetadata(key, resolver); err == nil {
			t.Fatal("provider failure must be returned")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("failed lookups must not be cached; calls=%d", calls.Load())
	}
}
