package web

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactSecretsHidesHistoryDSNAndMCPTokens(t *testing.T) {
	doc := map[string]any{
		"ai": map[string]any{
			"history_dsn": "postgres://reader:secret@postgres:5432/n8n",
			"mcp_servers": []any{
				map[string]any{"name": "docs", "url": "https://mcp.test", "token": "bearer-secret"},
			},
		},
	}
	redacted, ok := redactSecrets(doc).(map[string]any)
	if !ok {
		t.Fatal("redactSecrets returned an unexpected type")
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret@postgres") || strings.Contains(string(encoded), "bearer-secret") {
		t.Fatalf("credentials leaked: %s", encoded)
	}
	ai := redacted["ai"].(map[string]any)
	if ai["history_dsn_configured"] != true {
		t.Fatal("history DSN configured flag missing")
	}
	server := ai["mcp_servers"].([]any)[0].(map[string]any)
	if server["token"] != "" || server["token_configured"] != true {
		t.Fatalf("mcp token not redacted: %#v", server)
	}
}
