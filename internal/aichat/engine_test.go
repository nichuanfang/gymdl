package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

var errTestTool = errors.New("tool exploded")

// fakeModel returns queued responses in order and records the requests.
type fakeModel struct {
	mu        sync.Mutex
	responses []string
	requests  []map[string]any
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.requests = append(f.requests, body)
	response := f.responses[0]
	f.responses = f.responses[1:]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(response))
}

func completion(content string, toolCalls string) string {
	calls := "null"
	if toolCalls != "" {
		calls = toolCalls
	}
	return `{"choices":[{"message":{"role":"assistant","content":` + strconvQuote(content) + `,"tool_calls":` + calls + `}}]}`
}

func strconvQuote(value string) string {
	if value == "" {
		return "null"
	}
	b, _ := json.Marshal(value)
	return string(b)
}

func newTestEngine(t *testing.T, modelURL string, tools []Tool) (*Engine, *Store) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	engine, err := NewEngine(&config.AIConfig{
		Enable: true, BaseUrl: modelURL, Model: "test-model", ApiKey: "test-key",
		MaxToolRounds: 3, RequestTimeoutSeconds: 10, MaxSuggestions: 5,
	}, store, HistorySource{}, nil, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	return engine, store
}

func TestEngineLetsModelChooseToolByIntent(t *testing.T) {
	model := &fakeModel{responses: []string{
		completion("", `[{"id":"c1","type":"function","function":{"name":"load_skill","arguments":"{\"name\":\"playlist\"}"}}]`),
		completion("按技能建议，先整理成三个主题。", ""),
	}}
	server := httptest.NewServer(model)
	defer server.Close()
	called := 0
	tools := []Tool{{
		Name: "load_skill", Parameters: map[string]any{"type": "object"},
		Run: func(_ context.Context, _ map[string]any) (string, error) {
			called++
			return "skill body", nil
		},
	}}
	engine, _ := newTestEngine(t, server.URL, tools)
	out, err := engine.Respond(context.Background(), TurnInput{UserID: 1, ChatID: 1, Text: "帮我把歌单分成主题"})
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("model-chosen tool was not executed: %d", called)
	}
	if !strings.Contains(out.Reply, "三个主题") || !strings.Contains(out.ToolNote, "load_skill") {
		t.Fatalf("unexpected output: %#v", out)
	}
	if len(out.Candidates) != 0 {
		t.Fatal("non-search intent should not produce download candidates")
	}
}

func TestEngineSecondTurnKeepsConversation(t *testing.T) {
	model := &fakeModel{responses: []string{
		completion("第一轮回答", ""),
		completion("第二轮回答", ""),
	}}
	server := httptest.NewServer(model)
	defer server.Close()
	engine, _ := newTestEngine(t, server.URL, nil)
	if _, err := engine.Respond(context.Background(), TurnInput{UserID: 1, ChatID: 1, Text: "第一次"}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Respond(context.Background(), TurnInput{UserID: 1, ChatID: 1, Text: "第二次"}); err != nil {
		t.Fatal(err)
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(model.requests))
	}
	second, _ := json.Marshal(model.requests[1]["messages"])
	if !strings.Contains(string(second), "第一次") {
		t.Fatalf("second turn lost conversation history: %s", second)
	}
}

func TestEngineToolFailureIsReportedBackToModel(t *testing.T) {
	model := &fakeModel{responses: []string{
		completion("", `[{"id":"c1","type":"function","function":{"name":"broken","arguments":"{}"}}]`),
		completion("工具失败了，我换个办法。", ""),
	}}
	server := httptest.NewServer(model)
	defer server.Close()
	tools := []Tool{{
		Name: "broken", Parameters: map[string]any{"type": "object"},
		Run: func(_ context.Context, _ map[string]any) (string, error) {
			return "", errTestTool
		},
	}}
	engine, _ := newTestEngine(t, server.URL, tools)
	out, err := engine.Respond(context.Background(), TurnInput{UserID: 1, ChatID: 1, Text: "试试"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Reply, "换个办法") {
		t.Fatalf("unexpected reply: %q", out.Reply)
	}
}

func TestEnginePromptListsOnlyAllowListedMusicSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "music-recommendation", "music-recommendation", "推荐歌曲")
	writeSkill(t, root, "docker-stack-deploy", "docker-stack-deploy", "重建容器")
	registry := NewSkillRegistry([]string{root}, []string{"music-recommendation"})
	engine := &Engine{cfg: &config.AIConfig{}, skills: registry}
	prompt := engine.systemPrompt(nil)
	if !strings.Contains(prompt, "music-recommendation") {
		t.Fatalf("allow-listed skill missing from prompt: %s", prompt)
	}
	if strings.Contains(prompt, "docker-stack-deploy") {
		t.Fatalf("deployment skill leaked into prompt: %s", prompt)
	}
}

func TestEnginePromptStatesNoHardToolPriority(t *testing.T) {
	engine := &Engine{cfg: &config.AIConfig{}}
	prompt := engine.systemPrompt(nil)
	if !strings.Contains(prompt, "没有固定先后顺序") {
		t.Fatalf("prompt must keep capabilities peer-level: %s", prompt)
	}
	if !strings.Contains(prompt, "不要默认先搜索") {
		t.Fatalf("prompt must forbid default searching: %s", prompt)
	}
}

// The agent does download via buttons; claiming otherwise was observed in
// production and directly contradicts the UI it ships with.
func TestEnginePromptAllowsDownloadViaButtons(t *testing.T) {
	engine := &Engine{cfg: &config.AIConfig{}}
	prompt := engine.systemPrompt(nil)
	if !strings.Contains(prompt, "你能帮用户下载") {
		t.Fatalf("prompt must state the download capability: %s", prompt)
	}
	if !strings.Contains(prompt, "不要说“我不能下载”") {
		t.Fatalf("prompt must forbid the false denial: %s", prompt)
	}
}

// TestTurnTimeoutBoundsWholeTurn pins the budget that stops a tool-heavy turn
// from running for several minutes across sequential 90-second requests.
func TestTurnTimeoutBoundsWholeTurn(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	engine, err := NewEngine(&config.AIConfig{ApiKey: "k", Model: "m", MaxToolRounds: 4, RequestTimeoutSeconds: 10},
		store, HistorySource{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Configured 4 rounds are lifted to the 6-round floor (one lookup per
	// recommended track plus the final answer), so the budget follows that.
	if got := engine.turnTimeout(); got != 105*time.Second {
		t.Fatalf("turn timeout = %v, want 105s for the effective 6 rounds", got)
	}
	if got := engine.effectiveRounds(); got != 6 {
		t.Fatalf("effective rounds = %d, want 6", got)
	}
	small, err := NewEngine(&config.AIConfig{ApiKey: "k", Model: "m", MaxToolRounds: 1, RequestTimeoutSeconds: 5},
		store, HistorySource{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 6 rounds x 5s x 1.5 = 45s, below the 60s floor.
	if got := small.turnTimeout(); got != 60*time.Second {
		t.Fatalf("short turn budget = %v, want the 60s floor", got)
	}
}

// TestRespondStopsWhenCallerCancels makes sure an aborted turn returns instead
// of finishing its remaining rounds.
func TestRespondStopsWhenCallerCancels(t *testing.T) {
	blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep rather than waiting on r.Context(): the server must still be
		// able to finish the handler so Close() is not called out as blocked.
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"late"}}]}`))
	}))
	defer blocking.Close()
	store := openTestStore(t)
	defer store.Close()
	engine, err := NewEngine(&config.AIConfig{BaseUrl: blocking.URL, ApiKey: "k", Model: "m", MaxToolRounds: 3, RequestTimeoutSeconds: 30},
		store, HistorySource{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := engine.Respond(ctx, TurnInput{UserID: 1, ChatID: 1, Text: "hi"}); err == nil {
		t.Fatal("a cancelled turn must return an error")
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("cancelled turn took %v; it must stop without waiting for the provider", elapsed)
	}
}

// TestProgressReportsToolStages covers the user-visible stage hints that keep
// the placeholder message from sitting still during provider searches.
func TestProgressReportsToolStages(t *testing.T) {
	var stages []string
	ctx := WithProgress(context.Background(), func(stage string) {
		stages = append(stages, stage)
	})
	ReportProgress(ctx, progressLabel("search_music"))
	ReportProgress(ctx, progressLabel("mcp_news_search"))
	ReportProgress(ctx, progressLabel("library_lookup"))
	if len(stages) != 3 {
		t.Fatalf("got %d stages: %v", len(stages), stages)
	}
	if !strings.Contains(stages[0], "检索") {
		t.Errorf("search stage is not descriptive: %q", stages[0])
	}
	// A context without a reporter must not panic.
	ReportProgress(context.Background(), "noop")
}
