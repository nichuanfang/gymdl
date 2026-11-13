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

	c := &Config{PlaylistOrganizer: &PlaylistOrganizerConfig{TidyPlaylist: append([]PlaylistInfo(nil), inline...)}}
	if err := ApplyPlaylistFile(c, configPath); err != nil {
		t.Fatal(err)
	}
	if len(c.PlaylistOrganizer.TidyPlaylist) != 1 || c.PlaylistOrganizer.TidyPlaylist[0].Name != "inline" {
		t.Fatalf("missing sidecar should keep inline list: %#v", c.PlaylistOrganizer.TidyPlaylist)
	}

	write("tidy_playlist:\n  - name: sidecar\n    desc: new\n")
	c = &Config{PlaylistOrganizer: &PlaylistOrganizerConfig{TidyPlaylist: append([]PlaylistInfo(nil), inline...)}}
	if err := ApplyPlaylistFile(c, configPath); err != nil {
		t.Fatal(err)
	}
	if len(c.PlaylistOrganizer.TidyPlaylist) != 1 || c.PlaylistOrganizer.TidyPlaylist[0].Name != "sidecar" {
		t.Fatalf("sidecar should win: %#v", c.PlaylistOrganizer.TidyPlaylist)
	}

	write("tidy_playlist: []\n")
	c = &Config{PlaylistOrganizer: &PlaylistOrganizerConfig{TidyPlaylist: append([]PlaylistInfo(nil), inline...)}}
	if err := ApplyPlaylistFile(c, configPath); err != nil {
		t.Fatal(err)
	}
	if len(c.PlaylistOrganizer.TidyPlaylist) != 0 {
		t.Fatalf("explicit empty sidecar must override inline: %#v", c.PlaylistOrganizer.TidyPlaylist)
	}

	write("other_key: true\n")
	c = &Config{PlaylistOrganizer: &PlaylistOrganizerConfig{TidyPlaylist: append([]PlaylistInfo(nil), inline...)}}
	if err := ApplyPlaylistFile(c, configPath); err != nil {
		t.Fatal(err)
	}
	if len(c.PlaylistOrganizer.TidyPlaylist) != 1 || c.PlaylistOrganizer.TidyPlaylist[0].Name != "inline" {
		t.Fatalf("sidecar without tidy_playlist must not clear inline: %#v", c.PlaylistOrganizer.TidyPlaylist)
	}

	write("tidy_playlist: [\n")
	c = &Config{PlaylistOrganizer: &PlaylistOrganizerConfig{TidyPlaylist: append([]PlaylistInfo(nil), inline...)}}
	if err := ApplyPlaylistFile(c, configPath); err == nil {
		t.Fatal("broken sidecar should return an error")
	}
	if len(c.PlaylistOrganizer.TidyPlaylist) != 1 || c.PlaylistOrganizer.TidyPlaylist[0].Name != "inline" {
		t.Fatalf("broken sidecar must keep inline list: %#v", c.PlaylistOrganizer.TidyPlaylist)
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
	if main.N8NConfig == nil {
		t.Fatalf("missing n8n bridge settings: %#v", main.N8NConfig)
	}
	if main.PlaylistOrganizer == nil || main.PlaylistOrganizer.PlaylistAssist {
		t.Fatalf("unexpected playlist organizer defaults: %#v", main.PlaylistOrganizer)
	}
	if main.QQMusicApiConfig == nil || main.QQMusicApiConfig.ExpiredAt != 0 {
		t.Fatalf("unexpected qq music example defaults: %#v", main.QQMusicApiConfig)
	}

	entries, present, err := LoadPlaylistFile("../config.playlists.example.yaml")
	if err != nil || !present || len(entries) < 2 {
		t.Fatalf("playlist example invalid: entries=%d present=%v err=%v", len(entries), present, err)
	}
}

func TestSetDefaultsMovesLegacyPlaylistSettingsOutOfN8N(t *testing.T) {
	c := &Config{N8NConfig: &N8NConfig{
		PlaylistAssist: true,
		TidyPlaylist:   []PlaylistInfo{{Name: "legacy", Desc: "old config"}},
	}}
	c.SetDefaultsForTest()
	if c.PlaylistOrganizer == nil || !c.PlaylistOrganizer.PlaylistAssist || len(c.PlaylistOrganizer.TidyPlaylist) != 1 || c.PlaylistOrganizer.TidyPlaylist[0].Name != "legacy" {
		t.Fatalf("legacy playlist config was not migrated: %#v", c.PlaylistOrganizer)
	}
	if c.N8NConfig.PlaylistAssist || len(c.N8NConfig.TidyPlaylist) != 0 {
		t.Fatalf("legacy fields were not cleared from the n8n group: %#v", c.N8NConfig)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	n8n := doc["n8n_config"].(map[string]any)
	if _, exists := n8n["playlist_assist"]; exists {
		t.Fatal("marshaled n8n settings still contain playlist_assist")
	}
	if _, exists := n8n["tidy_playlist"]; exists {
		t.Fatal("marshaled n8n settings still contain tidy_playlist")
	}
}

func TestPreferExplicitPlaylistSettingsOverLegacyN8NValues(t *testing.T) {
	c := &Config{
		N8NConfig: &N8NConfig{
			PlaylistAssist: true,
			TidyPlaylist:   []PlaylistInfo{{Name: "legacy"}},
		},
		PlaylistOrganizer: &PlaylistOrganizerConfig{TidyPlaylist: []PlaylistInfo{}},
	}
	preferExplicitPlaylistConfig([]byte("playlist_organizer:\n  playlist_assist: false\n  tidy_playlist: []\n"), c)
	c.SetDefaultsForTest()
	if c.PlaylistOrganizer.PlaylistAssist || len(c.PlaylistOrganizer.TidyPlaylist) != 0 {
		t.Fatalf("explicit playlist settings were replaced by legacy values: %#v", c.PlaylistOrganizer)
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
	inline := &Config{PlaylistOrganizer: &PlaylistOrganizerConfig{TidyPlaylist: []PlaylistInfo{{Name: "inline"}}}}
	if err := ApplyPlaylistFile(inline, configPath); err != nil || len(inline.PlaylistOrganizer.TidyPlaylist) != 1 {
		t.Fatalf("placeholder must preserve inline fallback: %#v err=%v", inline.PlaylistOrganizer.TidyPlaylist, err)
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
