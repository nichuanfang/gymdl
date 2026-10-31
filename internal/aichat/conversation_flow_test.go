package aichat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

// scriptedModel replays a fixed conversation script and captures every request
// so we can assert on the exact wire shape sent to the provider.
type scriptedModel struct {
	mu       sync.Mutex
	script   []string
	requests []map[string]any
}

func (s *scriptedModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	next := `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`
	if len(s.script) > 0 {
		next = s.script[0]
		s.script = s.script[1:]
	}
	s.requests = append(s.requests, body)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(next))
}

func (s *scriptedModel) messagesAt(i int) []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.requests) {
		return nil
	}
	msgs, _ := s.requests[i]["messages"].([]any)
	return msgs
}

// TestAssistantToolCallMessageHasNoNullContent guards the 400 seen in
// production: the provider rejects a replayed assistant message whose content
// is null. go-openai omits empty content, so this asserts the wire shape.
func TestAssistantToolCallMessageHasNoNullContent(t *testing.T) {
	model := &scriptedModel{script: []string{
		`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"noop","arguments":"{}"}}]}}]}`,
		`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`,
	}}
	server := httptest.NewServer(model)
	defer server.Close()
	store, err := OpenStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	noop := Tool{Name: "noop", Description: "noop", Parameters: map[string]any{"type": "object"}, Run: func(context.Context, map[string]any) (string, error) { return `{"ok":true}`, nil }}
	engine, err := NewEngine(&config.AIConfig{BaseUrl: server.URL, Model: "m", ApiKey: "k", MaxToolRounds: 2, RequestTimeoutSeconds: 10}, store, HistorySource{}, nil, []Tool{noop}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Respond(context.Background(), TurnInput{UserID: 1, ChatID: 1, Text: "hi"}); err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	msgs := model.messagesAt(1)
	if len(msgs) == 0 {
		t.Fatal("second request was not sent")
	}
	found := false
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok || msg["role"] != "assistant" {
			continue
		}
		if _, hasCalls := msg["tool_calls"]; !hasCalls {
			continue
		}
		found = true
		if content, present := msg["content"]; present && content == nil {
			t.Fatalf("assistant tool-call message sent null content: %#v", msg)
		}
	}
	if !found {
		t.Fatalf("assistant tool-call message missing: %#v", msgs)
	}
}

// TestFollowUpTurnsNeverReplayEmptyAssistantContent covers the multi-turn case:
// stored history must never contain blank assistant rows that would be replayed.
func TestFollowUpTurnsNeverReplayEmptyAssistantContent(t *testing.T) {
	model := &scriptedModel{script: []string{
		`{"choices":[{"message":{"role":"assistant","content":"第一轮"}}]}`,
		`{"choices":[{"message":{"role":"assistant","content":"第二轮"}}]}`,
	}}
	server := httptest.NewServer(model)
	defer server.Close()
	store, err := OpenStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine, err := NewEngine(&config.AIConfig{BaseUrl: server.URL, Model: "m", ApiKey: "k", MaxToolRounds: 2, RequestTimeoutSeconds: 10}, store, HistorySource{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"第一条", "第二条"} {
		if _, err := engine.Respond(context.Background(), TurnInput{UserID: 1, ChatID: 1, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.History(1, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range history {
		if m.Role == "assistant" && strings.TrimSpace(m.Content) == "" {
			t.Fatalf("empty assistant row stored: %#v", history)
		}
	}
	if len(history) != 4 {
		t.Fatalf("history = %d rows, want 4", len(history))
	}
}

// TestResetThenContinueStartsCleanConversation mirrors /clear in Telegram.
func TestResetThenContinueStartsCleanConversation(t *testing.T) {
	model := &scriptedModel{script: []string{
		`{"choices":[{"message":{"role":"assistant","content":"旧话题"}}]}`,
		`{"choices":[{"message":{"role":"assistant","content":"新话题"}}]}`,
	}}
	server := httptest.NewServer(model)
	defer server.Close()
	store, err := OpenStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine, err := NewEngine(&config.AIConfig{BaseUrl: server.URL, Model: "m", ApiKey: "k", MaxToolRounds: 2, RequestTimeoutSeconds: 10}, store, HistorySource{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Respond(context.Background(), TurnInput{UserID: 1, ChatID: 1, Text: "旧话题"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Respond(context.Background(), TurnInput{UserID: 1, ChatID: 1, Text: "新话题"}); err != nil {
		t.Fatal(err)
	}
	msgs := model.messagesAt(1)
	encoded, _ := json.Marshal(msgs)
	if strings.Contains(string(encoded), "旧话题") {
		t.Fatalf("cleared context leaked into next request: %s", encoded)
	}
}
