package music

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadMusicDirReturnsReadTagsErrorWithoutPanicking(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.flac"), []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ReadMusicDir(dir, "WEBDAV", &QQMusicProcessor{})
	if err == nil {
		t.Fatal("expected an error for an unreadable music file")
	}
}
