package aichat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Skill is a directory containing SKILL.md. Only the skill's declared
// instructions are surfaced; no arbitrary files are read.
type Skill struct {
	Name        string
	Description string
	Path        string
	content     string
}

type SkillRegistry struct {
	mu     sync.RWMutex
	skills map[string]Skill
}

// NewSkillRegistry loads every skill found under dirs. When allow is non-empty
// it acts as an allow-list, which is how Telegram avoids exposing deployment or
// host-control skills to chat input.
func NewSkillRegistry(dirs []string, allow []string) *SkillRegistry {
	r := &SkillRegistry{skills: make(map[string]Skill)}
	r.Reload(dirs, allow)
	return r
}

func (r *SkillRegistry) Reload(dirs []string, allow []string) {
	if len(allow) == 0 {
		r.mu.Lock()
		r.skills = make(map[string]Skill)
		r.mu.Unlock()
		return
	}
	allowed := make(map[string]bool, len(allow))
	for _, name := range allow {
		allowed[strings.TrimSpace(name)] = true
	}
	found := make(map[string]Skill)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name(), "SKILL.md")
			data, err := os.ReadFile(path)
			if err != nil || len(data) > 64<<10 {
				continue
			}
			skill := parseSkill(entry.Name(), path, string(data))
			if skill.Name == "" {
				continue
			}
			if !allowed[skill.Name] && !allowed[entry.Name()] {
				continue
			}
			found[skill.Name] = skill
		}
	}
	r.mu.Lock()
	r.skills = found
	r.mu.Unlock()
}

func parseSkill(fallback, path, content string) Skill {
	skill := Skill{Path: path, content: content}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for _, line := range lines[1:] {
			line = strings.TrimSpace(line)
			if line == "---" {
				break
			}
			if key, value, ok := strings.Cut(line, ":"); ok {
				switch strings.TrimSpace(key) {
				case "name":
					skill.Name = strings.Trim(strings.TrimSpace(value), `"'`)
				case "description":
					skill.Description = strings.Trim(strings.TrimSpace(value), `"'`)
				}
			}
		}
	}
	if skill.Name == "" {
		skill.Name = fallback
	}
	return skill
}

func (r *SkillRegistry) List() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Skill, 0, len(r.skills))
	names := make([]string, 0, len(r.skills))
	for name := range r.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := r.skills[name]
		out = append(out, Skill{Name: s.Name, Description: s.Description, Path: s.Path})
	}
	return out
}

// Instructions returns the SKILL.md body for a registered skill.
func (r *SkillRegistry) Instructions(name string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	skill, ok := r.skills[strings.TrimSpace(name)]
	if !ok {
		return "", false
	}
	return skill.content, true
}

// Names returns the allow-listed skill names, used by the Telegram UI so users
// can see which capabilities are actually mounted.
func (r *SkillRegistry) Names() []string {
	skills := r.List()
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.Name)
	}
	return out
}

// Tool exposes skill loading as a normal function tool so the model can pull
// instructions on demand instead of stuffing every skill into the prompt.
func (r *SkillRegistry) Tool() Tool {
	return Tool{
		Name:        "load_skill",
		Description: "载入一个本地技能说明。只允许已注册技能名，返回 SKILL.md 内容。",
		Parameters: map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "技能名"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			name = strings.TrimSpace(name)
			r.mu.RLock()
			skill, ok := r.skills[name]
			r.mu.RUnlock()
			if !ok {
				available := strings.Join(r.Names(), "、")
				return "", fmt.Errorf("unknown skill %q; available: %s", name, available)
			}
			if collector := CollectorFrom(ctx); collector != nil {
				collector.AddSkill(name)
			}
			return skill.content, nil
		},
	}
}
