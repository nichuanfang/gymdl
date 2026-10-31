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
