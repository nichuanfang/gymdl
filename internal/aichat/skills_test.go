package aichat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func writeSkill(t *testing.T, root, dir, name, description string) {
	t.Helper()
	path := filepath.Join(root, dir)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# body\n"
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillAllowListExcludesUnlistedSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "music-recommendation", "music-recommendation", "推荐歌曲")
	writeSkill(t, root, "docker-stack-deploy", "docker-stack-deploy", "重建容器")
	registry := NewSkillRegistry([]string{root}, []string{"music-recommendation"})
	names := registry.Names()
	if len(names) != 1 || names[0] != "music-recommendation" {
		t.Fatalf("allow-list leaked skills: %#v", names)
	}
}

func TestEmptyAllowListMountsNothing(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "music-recommendation", "music-recommendation", "推荐歌曲")
	registry := NewSkillRegistry([]string{root}, nil)
	if got := len(registry.Names()); got != 0 {
		t.Fatalf("empty allow-list mounted %d skills, want 0", got)
	}
}

func TestSkillToolOnlyReturnsAllowListedContent(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "music-playlist-curation", "music-playlist-curation", "整理歌单")
	writeSkill(t, root, "vps-checkup", "vps-checkup", "体检主机")
	registry := NewSkillRegistry([]string{root}, []string{"music-playlist-curation"})
	tool := registry.Tool()
	if _, err := tool.Run(t.Context(), map[string]any{"name": "music-playlist-curation"}); err != nil {
		t.Fatalf("allow-listed skill failed: %v", err)
	}
	if _, err := tool.Run(t.Context(), map[string]any{"name": "vps-checkup"}); err == nil {
		t.Fatal("unlisted skill must not be readable through the tool")
	}
}

func TestProjectMusicSkillsParse(t *testing.T) {
	root := "../../skills"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("project skills dir missing: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	registry := NewSkillRegistry([]string{root}, names)
	skills := registry.List()
	if len(skills) != len(names) {
		t.Fatalf("project ships %d music skills, parsed %d", len(names), len(skills))
	}
	for _, skill := range skills {
		if skill.Description == "" {
			t.Fatalf("skill %s has no description", skill.Name)
		}
	}
}

// TestRoutedSkillsAreInlinedWithoutToolCall pins that a trigger match reaches
// the model as instructions, not as a request to call load_skill (which cost an
// extra model round trip and was not guaranteed to happen).
func TestRoutedSkillsAreInlinedWithoutToolCall(t *testing.T) {
	var system string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if msgs, ok := body["messages"].([]any); ok && len(msgs) > 0 {
			if first, ok := msgs[0].(map[string]any); ok {
				system, _ = first["content"].(string)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()
	store, err := OpenStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry := NewSkillRegistry([]string{projectSkillsDir}, []string{"music-recommendation"})
	engine, err := NewEngine(&config.AIConfig{BaseUrl: srv.URL, Model: "m", ApiKey: "k", MaxToolRounds: 2, RequestTimeoutSeconds: 10},
		store, HistorySource{}, registry, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Respond(context.Background(), TurnInput{UserID: 1, ChatID: 1, Text: "推荐几首歌"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(system, "### 技能：music-recommendation") {
		t.Fatalf("routed skill was not inlined; prompt starts with %q", firstLines(system, 3))
	}
	if strings.Contains(system, "本轮相关技能") {
		t.Fatal("prompt still asks the model to call load_skill for a routed skill")
	}
}

func firstLines(value string, n int) string {
	lines := strings.Split(value, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
