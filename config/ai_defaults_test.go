package config

import "testing"

func TestAISetDefaultsFillsAgentFields(t *testing.T) {
	c := &Config{}
	c.setDefaults()
	if c.AI == nil {
		t.Fatal("AI config was not initialized")
	}
	if c.AI.MaxToolRounds != 4 || c.AI.RequestTimeoutSeconds != 90 || c.AI.HistoryWindowDays != 30 || c.AI.MaxSuggestions != 10 {
		t.Fatalf("unexpected agent defaults: %#v", c.AI)
	}
	if c.AI.HistoryTable != "music_scrobble_records" {
		t.Fatalf("history table default = %q", c.AI.HistoryTable)
	}
	if c.AI.DefaultMode {
		t.Fatal("AI mode must not default on")
	}
	if c.PlaylistOrganizer == nil || c.PlaylistOrganizer.OliveTinURL != "" || c.PlaylistOrganizer.NavidromeURL != "" {
		t.Fatalf("unexpected playlist organizer defaults: %#v", c.PlaylistOrganizer)
	}
	if c.N8NConfig.APIKey != "" || c.N8NConfig.MusicRAGTableID != "" {
		t.Fatalf("MusicRAG bridge must remain opt-in: %#v", c.N8NConfig)
	}
	if c.AIPlaylist == nil || c.AIPlaylist.PineconeNamespace != "data-music" || c.AIPlaylist.EmbeddingModel != "text-embedding-3-small" || c.AIPlaylist.PlaylistSize != 80 || c.AIPlaylist.CandidateCount != 150 {
		t.Fatalf("unexpected AI playlist defaults: %#v", c.AIPlaylist)
	}
}

func TestAISetDefaultsPreservesExplicitValues(t *testing.T) {
	c := &Config{AI: &AIConfig{
		Enable:                true,
		DefaultMode:           true,
		MaxToolRounds:         2,
		RequestTimeoutSeconds: 30,
		HistoryWindowDays:     7,
		MaxSuggestions:        5,
		HistoryTable:          "recent_scrobbles",
		MCPServers:            []MCPServerConfig{{Name: "docs", URL: "https://mcp.test"}},
	}}
	c.setDefaults()
	if c.AI.MaxToolRounds != 2 || c.AI.RequestTimeoutSeconds != 30 || c.AI.HistoryWindowDays != 7 || c.AI.MaxSuggestions != 5 {
		t.Fatalf("explicit values overwritten: %#v", c.AI)
	}
	if !c.AI.DefaultMode || c.AI.HistoryTable != "recent_scrobbles" {
		t.Fatalf("explicit mode/table overwritten: %#v", c.AI)
	}
	if c.AI.MCPServers[0].TimeoutSeconds != 20 {
		t.Fatalf("MCP timeout default = %d, want 20", c.AI.MCPServers[0].TimeoutSeconds)
	}
}

func TestAIPlaylistSetDefaultsPreservesSecretsAndFillsLimits(t *testing.T) {
	c := &Config{AIPlaylist: &AIPlaylistConfig{
		PineconeHost:   "https://index.example",
		PineconeAPIKey: "configured-secret",
		PlaylistSize:   40,
	}}
	c.setDefaults()
	if c.AIPlaylist.PineconeHost != "https://index.example" || c.AIPlaylist.PineconeAPIKey != "configured-secret" || c.AIPlaylist.PlaylistSize != 40 {
		t.Fatalf("explicit AI playlist values overwritten: %#v", c.AIPlaylist)
	}
	if c.AIPlaylist.PineconeNamespace != "data-music" || c.AIPlaylist.EmbeddingModel != "text-embedding-3-small" || c.AIPlaylist.CandidateCount != 150 {
		t.Fatalf("missing AI playlist defaults not filled: %#v", c.AIPlaylist)
	}
}
