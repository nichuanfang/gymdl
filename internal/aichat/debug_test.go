package aichat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func TestEngineRequestShapeWithToolCalls(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"t","arguments":"{}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	store, _ := OpenStore(t.TempDir() + "/x.sqlite3")
	defer store.Close()
	tools := []Tool{{Name: "t", Parameters: map[string]any{"type": "object"}, Run: func(context.Context, map[string]any) (string, error) { return "r", nil }}}
	engine, err := NewEngine(&config.AIConfig{BaseUrl: server.URL, Model: "m", ApiKey: "k", MaxToolRounds: 2, RequestTimeoutSeconds: 10}, store, HistorySource{}, nil, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Respond(context.Background(), TurnInput{UserID: 1, ChatID: 1, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(bodies[1]["messages"])
	t.Logf("second request messages: %s", raw)
}
