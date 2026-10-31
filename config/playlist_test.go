package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestPlaylistFilePath(t *testing.T) {
	if got := PlaylistFilePath(""); got != PlaylistFileName {
		t.Fatalf("empty path = %q, want %q", got, PlaylistFileName)
	}
	if got := PlaylistFilePath("/etc/gymdl/config.yaml"); got != "/etc/gymdl/config.playlists.yaml" {
		t.Fatalf("absolute path = %q", got)
	}
}

func TestApplyPlaylistFilePrecedenceAndFallback(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	inline := []PlaylistInfo{{Name: "inline", Desc: "old"}}

	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(PlaylistFilePath(configPath), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	c := &Config{N8NConfig: &N8NConfig{TidyPlaylist: append([]PlaylistInfo(nil), inline...)}}
	if err := ApplyPlaylistFile(c, configPath); err != nil {
		t.Fatal(err)
	}
	if len(c.N8NConfig.TidyPlaylist) != 1 || c.N8NConfig.TidyPlaylist[0].Name != "inline" {
		t.Fatalf("missing sidecar should keep inline list: %#v", c.N8NConfig.TidyPlaylist)
	}

	write("tidy_playlist:\n  - name: sidecar\n    desc: new\n")
	c = &Config{N8NConfig: &N8NConfig{TidyPlaylist: append([]PlaylistInfo(nil), inline...)}}
	if err := ApplyPlaylistFile(c, configPath); err != nil {
		t.Fatal(err)
	}
	if len(c.N8NConfig.TidyPlaylist) != 1 || c.N8NConfig.TidyPlaylist[0].Name != "sidecar" {
		t.Fatalf("sidecar should win: %#v", c.N8NConfig.TidyPlaylist)
	}

	write("tidy_playlist: []\n")
	c = &Config{N8NConfig: &N8NConfig{TidyPlaylist: append([]PlaylistInfo(nil), inline...)}}
	if err := ApplyPlaylistFile(c, configPath); err != nil {
		t.Fatal(err)
	}
	if len(c.N8NConfig.TidyPlaylist) != 0 {
		t.Fatalf("explicit empty sidecar must override inline: %#v", c.N8NConfig.TidyPlaylist)
	}

	write("other_key: true\n")
	c = &Config{N8NConfig: &N8NConfig{TidyPlaylist: append([]PlaylistInfo(nil), inline...)}}
	if err := ApplyPlaylistFile(c, configPath); err != nil {
		t.Fatal(err)
	}
	if len(c.N8NConfig.TidyPlaylist) != 1 || c.N8NConfig.TidyPlaylist[0].Name != "inline" {
		t.Fatalf("sidecar without tidy_playlist must not clear inline: %#v", c.N8NConfig.TidyPlaylist)
	}

	write("tidy_playlist: [\n")
	c = &Config{N8NConfig: &N8NConfig{TidyPlaylist: append([]PlaylistInfo(nil), inline...)}}
	if err := ApplyPlaylistFile(c, configPath); err == nil {
		t.Fatal("broken sidecar should return an error")
	}
	if len(c.N8NConfig.TidyPlaylist) != 1 || c.N8NConfig.TidyPlaylist[0].Name != "inline" {
		t.Fatalf("broken sidecar must keep inline list: %#v", c.N8NConfig.TidyPlaylist)
	}
}

func TestApplyPlaylistFileRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.Mkdir(PlaylistFilePath(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Config{}
	err := ApplyPlaylistFile(c, configPath)
	if err == nil || !strings.Contains(err.Error(), "是目录") {
		t.Fatalf("expected directory error, got %v", err)
	}
}

func TestWritePlaylistFilePermissionsAndBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.playlists.yaml")
	if err := os.WriteFile(path, []byte("tidy_playlist: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePlaylistFile(path, []PlaylistInfo{{Name: "中文", Desc: "desc"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("sidecar permissions are too open: %v", info.Mode().Perm())
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || !strings.Contains(string(backup), "tidy_playlist") {
		t.Fatalf("expected previous sidecar backup: %v %q", err, backup)
	}
}

func TestShippedConfigExamplesParse(t *testing.T) {
	var main Config
	if err := unmarshalFile("../config.yaml.example", &main); err != nil {
		t.Fatalf("config.yaml.example does not parse: %v", err)
	}
	if main.N8NConfig == nil || main.N8NConfig.PlaylistAssist {
		t.Fatalf("unexpected n8n example defaults: %#v", main.N8NConfig)
	}
	if main.QQMusicApiConfig == nil || main.QQMusicApiConfig.ExpiredAt != 0 {
		t.Fatalf("unexpected qq music example defaults: %#v", main.QQMusicApiConfig)
	}

	entries, present, err := LoadPlaylistFile("../config.playlists.example.yaml")
	if err != nil || !present || len(entries) < 2 {
		t.Fatalf("playlist example invalid: entries=%d present=%v err=%v", len(entries), present, err)
	}
}

func unmarshalFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, target)
}

func TestEnsurePlaylistFileCreatesPlaceholderWithoutOverwriting(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	path := PlaylistFilePath(configPath)
	EnsurePlaylistFile(configPath)
	entries, present, err := LoadPlaylistFile(path)
	if err != nil || present || len(entries) != 0 {
		t.Fatalf("placeholder must not contain tidy_playlist: %#v present=%v err=%v", entries, present, err)
	}
	inline := &Config{N8NConfig: &N8NConfig{TidyPlaylist: []PlaylistInfo{{Name: "inline"}}}}
	if err := ApplyPlaylistFile(inline, configPath); err != nil || len(inline.N8NConfig.TidyPlaylist) != 1 {
		t.Fatalf("placeholder must preserve inline fallback: %#v err=%v", inline.N8NConfig.TidyPlaylist, err)
	}
	if err := os.WriteFile(path, []byte("tidy_playlist:\n  - name: keep\n    desc: existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	EnsurePlaylistFile(configPath)
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "keep") {
		t.Fatalf("existing sidecar must not be overwritten: %v %q", err, data)
	}
}
