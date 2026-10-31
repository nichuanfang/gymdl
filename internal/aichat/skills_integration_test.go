package aichat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

const projectSkillsDir = "../../skills"

// projectSkillNames lists the shipped skills so a new directory cannot be added
// without also being reachable from the allow-list defaults.
func projectSkillNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(projectSkillsDir)
	if err != nil {
		t.Fatalf("read project skills: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

// TestEveryProjectSkillIsLoadable proves each shipped skill is parsed, listed,
// and readable through the tool the model actually calls.
func TestEveryProjectSkillIsLoadable(t *testing.T) {
	names := projectSkillNames(t)
	registry := NewSkillRegistry([]string{projectSkillsDir}, names)
	tool := registry.Tool()
	for _, name := range names {
		loaded, err := tool.Run(context.Background(), map[string]any{"name": name})
		if err != nil {
			t.Fatalf("skill %s not loadable: %v", name, err)
		}
		if strings.TrimSpace(loaded) == "" {
			t.Fatalf("skill %s loaded empty content", name)
		}
		if !strings.Contains(loaded, "name: "+name) {
			t.Fatalf("skill %s content does not declare its own name", name)
		}
	}
	if got := len(registry.List()); got != len(names) {
		t.Fatalf("listed %d skills, want %d", got, len(names))
	}
}

// TestDefaultAllowListCoversEveryShippedSkill keeps config defaults and the
// skills directory from drifting apart.
func TestDefaultAllowListCoversEveryShippedSkill(t *testing.T) {
	shipped := projectSkillNames(t)
	cfg := &config.Config{}
	cfg.SetDefaultsForTest()
	allowed := make(map[string]bool, len(cfg.AI.EnabledSkills))
	for _, name := range cfg.AI.EnabledSkills {
		allowed[name] = true
	}
	for _, name := range shipped {
		if !allowed[name] {
			t.Errorf("shipped skill %q missing from ai.enabled_skills defaults", name)
		}
	}
	for name := range allowed {
		found := false
		for _, shippedName := range shipped {
			if shippedName == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("allow-list entry %q has no skills/%s directory", name, name)
		}
	}
}

// TestEachSkillPromptLineIsDistinct guards the model's ability to choose: two
// skills with identical descriptions would be effectively unreachable.
func TestEachSkillPromptLineIsDistinct(t *testing.T) {
	names := projectSkillNames(t)
	registry := NewSkillRegistry([]string{projectSkillsDir}, names)
	seen := make(map[string]string, len(names))
	for _, skill := range registry.List() {
		desc := strings.TrimSpace(skill.Description)
		if desc == "" {
			t.Fatalf("skill %s has no description", skill.Name)
		}
		if previous, ok := seen[desc]; ok {
			t.Fatalf("skills %s and %s share a description", previous, skill.Name)
		}
		seen[desc] = skill.Name
	}
}

func TestSkillFilesAreInsideSkillsDir(t *testing.T) {
	names := projectSkillNames(t)
	for _, name := range names {
		path := filepath.Join(projectSkillsDir, name, "SKILL.md")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("skill %s missing SKILL.md: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("skill %s has an empty SKILL.md", name)
		}
	}
}
