package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLocalSafePathPreventsTraversalAndSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "music", "track.mp3")
	if err := os.WriteFile(inside, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := localSafePath(root, "music/track.mp3"); err != nil {
		t.Fatalf("expected in-root file to resolve: %v", err)
	}
	if _, err := localSafePath(root, "../outside"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := localSafePath(root, "escape"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestRemoteSafePathAndRange(t *testing.T) {
	if got, err := remoteSafePath("/Music/Library", "/Artist/Song.flac"); err != nil || got != "/Music/Library/Artist/Song.flac" {
		t.Fatalf("remote path mismatch: got=%q err=%v", got, err)
	}
	if _, err := remoteSafePath("Music", "../outside"); err == nil {
		t.Fatal("expected WebDAV traversal to be rejected")
	}
	start, end, err := parseSingleRange("bytes=10-19", 100)
	if err != nil || start != 10 || end != 19 {
		t.Fatalf("range parse mismatch: %d-%d err=%v", start, end, err)
	}
	start, end, err = parseSingleRange("bytes=-10", 100)
	if err != nil || start != 90 || end != 99 {
		t.Fatalf("suffix range mismatch: %d-%d err=%v", start, end, err)
	}
	if _, _, err := parseSingleRange("bytes=200-", 100); err == nil {
		t.Fatal("expected out-of-range request to fail")
	}
}

func TestLocalStreamSupportsRangeAndDelete(t *testing.T) {
	root := t.TempDir()
	trackPath := filepath.Join(root, "track.mp3")
	track := []byte("0123456789")
	if err := os.WriteFile(trackPath, track, 0o600); err != nil {
		t.Fatal(err)
	}
	previous := GetWebConfig()
	SetWebConfig(&config.Config{Tidy: &config.TidyConfig{Mode: 1, DistDir: root}})
	defer SetWebConfig(previous)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/web/files/stream?path=track.mp3", nil)
	ctx.Request.Header.Set("Range", "bytes=2-5")
	HandleStreamFile(ctx)
	if recorder.Code != http.StatusPartialContent || recorder.Body.String() != "2345" {
		t.Fatalf("range response mismatch: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("unexpected Content-Range: %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("expected stable MP3 MIME type, got %q", got)
	}

	deleteRecorder := httptest.NewRecorder()
	deleteContext, _ := gin.CreateTestContext(deleteRecorder)
	deleteContext.Request = httptest.NewRequest(http.MethodDelete, "/api/web/files?path=track.mp3", nil)
	HandleDeleteFile(deleteContext)
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("delete failed: %d %s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if _, err := os.Stat(trackPath); !os.IsNotExist(err) {
		t.Fatalf("expected file to be deleted, got err=%v", err)
	}
}

func TestRelativeDistDirProducesUsableFilePaths(t *testing.T) {
	root := t.TempDir()
	trackPath := filepath.Join(root, "track.mp3")
	if err := os.WriteFile(trackPath, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relRoot, err := filepath.Rel(cwd, root)
	if err != nil {
		t.Fatal(err)
	}
	previous := GetWebConfig()
	SetWebConfig(&config.Config{Tidy: &config.TidyConfig{Mode: 1, DistDir: relRoot}})
	defer SetWebConfig(previous)

	gin.SetMode(gin.TestMode)
	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/api/web/files?path=%2F", nil)
	HandleListFiles(listContext)
	var result struct {
		Data struct {
			Entries []FileEntry `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data.Entries) != 1 || result.Data.Entries[0].Path != "track.mp3" {
		t.Fatalf("relative dist_dir returned unusable file paths: %#v", result.Data.Entries)
	}

	streamRecorder := httptest.NewRecorder()
	streamContext, _ := gin.CreateTestContext(streamRecorder)
	streamContext.Request = httptest.NewRequest(http.MethodGet, "/api/web/files/stream?path=track.mp3", nil)
	HandleStreamFile(streamContext)
	if streamRecorder.Code != http.StatusOK || streamRecorder.Body.String() != "0123456789" {
		t.Fatalf("listed path did not stream: status=%d body=%q", streamRecorder.Code, streamRecorder.Body.String())
	}

	deleteRecorder := httptest.NewRecorder()
	deleteContext, _ := gin.CreateTestContext(deleteRecorder)
	deleteContext.Request = httptest.NewRequest(http.MethodDelete, "/api/web/files?path=track.mp3", nil)
	HandleDeleteFile(deleteContext)
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("listed path did not delete: status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
}

type fakeLibraryDAV struct {
	dirs  map[string][]os.FileInfo
	err   map[string]error
	delay time.Duration

	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
}

func (f *fakeLibraryDAV) ReadDir(path string) ([]os.FileInfo, error) {
	f.mu.Lock()
	f.calls++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	err := f.err[path]
	items, ok := f.dirs[path]
	f.mu.Unlock()

	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, os.ErrNotExist
	}
	return items, nil
}

func (f *fakeLibraryDAV) stats() (calls, maxConcurrent int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.maxActive
}

func TestWebDAVFileSearchFiltersSongArtistAlbumAndPreservesRelativePaths(t *testing.T) {
	old := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	newer := old.Add(2 * time.Hour)
	remote := &fakeLibraryDAV{dirs: map[string][]os.FileInfo{
		"/library":                           {fakeInfo{name: "Narrow Artist", dir: true, mod: old}, fakeInfo{name: "Other Artist", dir: true, mod: newer}},
		"/library/Narrow Artist":             {fakeInfo{name: "Quiet Album", dir: true, mod: old}},
		"/library/Narrow Artist/Quiet Album": {fakeInfo{name: "Blue Song.flac", size: 100, mod: newer}, fakeInfo{name: "cover.jpg", size: 30, mod: newer}},
		"/library/Other Artist":              {fakeInfo{name: "Loud Album", dir: true, mod: newer}},
		"/library/Other Artist/Loud Album":   {fakeInfo{name: "Red Song.mp3", size: 200, mod: old}},
	}}

	artistResults, truncated, err := listWebDAVFiles("library", "/", "narrow", "artist", remote)
	if err != nil || truncated || len(artistResults) != 1 {
		t.Fatalf("artist search failed: entries=%#v truncated=%v err=%v", artistResults, truncated, err)
	}
	if artistResults[0].Path != "Narrow Artist/Quiet Album/Blue Song.flac" || artistResults[0].Artist != "Narrow Artist" || artistResults[0].Album != "Quiet Album" {
		t.Fatalf("WebDAV result metadata/path mismatch: %#v", artistResults[0])
	}

	albumResults, _, err := listWebDAVFiles("library", "/", "loud", "album", remote)
	if err != nil || len(albumResults) != 1 || albumResults[0].Name != "Red Song.mp3" {
		t.Fatalf("album search failed: %#v err=%v", albumResults, err)
	}
	songResults, _, err := listWebDAVFiles("library", "/Narrow Artist", "blue", "song", remote)
	if err != nil || len(songResults) != 1 || songResults[0].Path != "Narrow Artist/Quiet Album/Blue Song.flac" {
		t.Fatalf("song search under current subtree failed: %#v err=%v", songResults, err)
	}
}

func TestFileLibrarySortsNewestFirstAndReportsTruncationBoundary(t *testing.T) {
	now := time.Now()
	entries := []FileEntry{
		{Name: "older.mp3", ModTime: now.Add(-time.Hour)},
		{Name: "newer.mp3", ModTime: now},
		{Name: "same-dir", IsDir: true, ModTime: now},
	}
	sortFileEntries(entries)
	if entries[0].Name != "same-dir" || entries[1].Name != "newer.mp3" || entries[2].Name != "older.mp3" {
		t.Fatalf("expected newest-first order with directory tie-breaker: %#v", entries)
	}

	root := t.TempDir()
	for _, name := range []string{"Artist/Album/First.flac", "Artist/Album/Second.flac"} {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	results, truncated, err := listLocalFiles(root, "/", "artist", "artist")
	if err != nil || truncated || len(results) != 2 {
		t.Fatalf("local subtree search should return matching audio entries: %#v truncated=%v err=%v", results, truncated, err)
	}
}

func TestWebDAVFileSearchUsesBoundedConcurrencyAndReusesShortIndexCache(t *testing.T) {
	invalidateWebDAVFileIndexes()
	t.Cleanup(invalidateWebDAVFileIndexes)
	const base = "optimized-search-library"
	dirs := map[string][]os.FileInfo{
		"/" + base: make([]os.FileInfo, 12),
	}
	for i := 0; i < 12; i++ {
		artist := fmt.Sprintf("Artist %02d", i)
		dirs["/"+base][i] = fakeInfo{name: artist, dir: true}
		dirs["/"+base+"/"+artist] = []os.FileInfo{fakeInfo{name: "Album", dir: true}}
		dirs["/"+base+"/"+artist+"/Album"] = []os.FileInfo{
			fakeInfo{name: fmt.Sprintf("Song %02d.flac", i), size: int64(100 + i), mod: time.Now().Add(-time.Duration(i) * time.Minute)},
		}
	}
	remote := &fakeLibraryDAV{dirs: dirs, delay: 4 * time.Millisecond}

	type searchOutcome struct {
		query   string
		entries []FileEntry
		err     error
	}
	queries := []struct{ query, field string }{
		{"artist 11", "artist"},
		{"song 03", "song"},
		{"album", "album"},
		{"artist 05", "artist"},
	}
	outcomes := make(chan searchOutcome, len(queries))
	var searchWG sync.WaitGroup
	for _, query := range queries {
		searchWG.Add(1)
		go func(query, field string) {
			defer searchWG.Done()
			entries, _, err := listWebDAVFiles(base, "/", query, field, remote)
			outcomes <- searchOutcome{query: query, entries: entries, err: err}
		}(query.query, query.field)
	}
	searchWG.Wait()
	close(outcomes)
	for outcome := range outcomes {
		if outcome.err != nil || len(outcome.entries) == 0 {
			t.Fatalf("concurrent WebDAV search %q failed: entries=%#v err=%v", outcome.query, outcome.entries, outcome.err)
		}
		if outcome.query == "artist 11" && (len(outcome.entries) != 1 || outcome.entries[0].Artist != "Artist 11") {
			t.Fatalf("artist metadata did not filter precisely: %#v", outcome.entries)
		}
	}
	firstCalls, maxConcurrent := remote.stats()
	if firstCalls != 25 { // root + 12 artist dirs + 12 album dirs
		t.Fatalf("unexpected WebDAV directory request count: %d", firstCalls)
	}
	if maxConcurrent < 2 || maxConcurrent > webDAVFileSearchConcurrency {
		t.Fatalf("directory requests should use bounded concurrency, max=%d cap=%d", maxConcurrent, webDAVFileSearchConcurrency)
	}

	matches, truncated, err := listWebDAVFiles(base, "/", "song 03", "song", remote)
	if err != nil || truncated || len(matches) != 1 || matches[0].Name != "Song 03.flac" {
		t.Fatalf("cached WebDAV search failed: matches=%#v truncated=%v err=%v", matches, truncated, err)
	}
	secondCalls, _ := remote.stats()
	if secondCalls != firstCalls {
		t.Fatalf("changing a filter should reuse the scanned directory index: before=%d after=%d", firstCalls, secondCalls)
	}

	invalidateWebDAVFileIndexes()
	if _, _, err := listWebDAVFiles(base, "/", "song 03", "song", remote); err != nil {
		t.Fatal(err)
	}
	thirdCalls, _ := remote.stats()
	if thirdCalls <= secondCalls {
		t.Fatalf("cache invalidation should trigger a fresh WebDAV scan: before=%d after=%d", secondCalls, thirdCalls)
	}
}
