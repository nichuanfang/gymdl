package config

import (
	"os"
	"testing"

	"github.com/goccy/go-yaml"
)

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
	if len(main.PlaylistOrganizer.TidyPlaylist) != 0 {
		t.Fatalf("example config must not ship a playlist list: %#v", main.PlaylistOrganizer.TidyPlaylist)
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

func TestAutoAssignNeedsPlaylistAssist(t *testing.T) {
	withoutAssist := &Config{PlaylistOrganizer: &PlaylistOrganizerConfig{AutoAssign: true}}
	withoutAssist.SetDefaultsForTest()
	if withoutAssist.PlaylistOrganizer.AutoAssign {
		t.Fatal("auto_assign must stay off without playlist_assist")
	}
	withAssist := &Config{PlaylistOrganizer: &PlaylistOrganizerConfig{PlaylistAssist: true, AutoAssign: true}}
	withAssist.SetDefaultsForTest()
	if !withAssist.PlaylistOrganizer.AutoAssign {
		t.Fatal("auto_assign was dropped although playlist_assist is on")
	}
	shipped := &Config{}
	shipped.SetDefaultsForTest()
	if shipped.PlaylistOrganizer.AutoAssign || shipped.PlaylistOrganizer.PlaylistAssist {
		t.Fatalf("defaults must keep playlist archiving manual: %+v", shipped.PlaylistOrganizer)
	}
}

func TestAutoAssignFollowsLegacyPlaylistAssistMigration(t *testing.T) {
	c := &Config{
		PlaylistOrganizer: &PlaylistOrganizerConfig{AutoAssign: true},
		N8NConfig:         &N8NConfig{PlaylistAssist: true},
	}
	c.SetDefaultsForTest()
	if !c.PlaylistOrganizer.PlaylistAssist || !c.PlaylistOrganizer.AutoAssign {
		t.Fatalf("legacy assist migration should keep auto_assign: %+v", c.PlaylistOrganizer)
	}
}
