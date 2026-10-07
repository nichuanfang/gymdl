package dispatch

import (
	"path/filepath"
	"testing"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/utils"
)

func TestSetStoredMusicPathsForLocalAndWebDAV(t *testing.T) {
	localSong := &music.SongInfo{MusicPath: filepath.Join("data", "temp", "song name.m4a")}
	local := &Session{Cfg: &config.Config{Tidy: &config.TidyConfig{Mode: 1, DistDir: "library"}}}
	local.setStoredMusicPaths([]*music.SongInfo{localSong})
	if want := filepath.Join("library", utils.SanitizeFileName("song name.m4a")); localSong.MusicPath != want {
		t.Fatalf("local MusicPath = %q, want %q", localSong.MusicPath, want)
	}

	webDAVSong := &music.SongInfo{
		SongArtists: "Test Artist",
		SongAlbum:   "Test Album",
		MusicPath:   filepath.Join("data", "temp", "song.m4a"),
	}
	webDAV := &Session{Cfg: &config.Config{Tidy: &config.TidyConfig{Mode: 2}}}
	webDAV.setStoredMusicPaths([]*music.SongInfo{webDAVSong})
	if webDAVSong.MusicPath != "Test Artist/Test Album/song.m4a" {
		t.Fatalf("WebDAV MusicPath = %q, want %q", webDAVSong.MusicPath, "Test Artist/Test Album/song.m4a")
	}
}
