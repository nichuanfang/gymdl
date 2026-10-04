package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"path/filepath"
	"testing"
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
