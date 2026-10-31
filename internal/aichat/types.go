package aichat

import (
	"context"
	"sync"
	"time"
)

// Message is the persisted conversation shape shared with SQLite.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Candidate is a grounded suggestion: Platform/URL are always produced by a
// real provider search, never by the model.
type Candidate struct {
	Token     string    `json:"token"`
	Title     string    `json:"title"`
	Artist    string    `json:"artist"`
	Platform  string    `json:"platform"`
	URL       string    `json:"url"`
	Name      string    `json:"name"`
	Reason    string    `json:"reason,omitempty"`
	Grounded  bool      `json:"grounded"`
	Owned     bool      `json:"owned"`
	IsVIP     bool      `json:"is_vip,omitempty"`
	ExpiresAt time.Time `json:"-"`
	// BatchID groups the candidates produced by one answer so the bot can offer
	// "下载前 3 首" against that turn only.
	BatchID string `json:"-"`
}

// SearchResult mirrors the WebUI search item without importing the controller.
type SearchResult struct {
	Platform    string
	SongID      string
	SongMID     string
	Name        string
	Artists     string
	Album       string
	DurationSec int
	URL         string
	IsVIP       bool
}

// OwnedFunc reports whether a track has already been downloaded.
type OwnedFunc func(ctx context.Context, title, artist, url string) (bool, error)

// Profile is the aggregated listening preference injected into the prompt.
type Profile struct {
	WindowDays int
	Scrobbles  int
	Artists    []string
	Tracks     []string
	Albums     []string
}

// Tool is an executable capability exposed to the model.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Run         func(ctx context.Context, args map[string]any) (string, error)
}

// SkillLister is the small surface the engine needs to describe local
// capabilities in the system prompt.
type SkillLister interface {
	List() []Skill
}

type collectorKey struct{}

type progressKey struct{}

// ProgressFunc receives short, user-safe stage descriptions while a turn runs.
// It must be fast and non-blocking; the bot usually edits the placeholder
// message.
type ProgressFunc func(stage string)

// WithProgress attaches a progress reporter to the turn context.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, fn)
}

// ReportProgress is a no-op when no reporter is attached.
func ReportProgress(ctx context.Context, stage string) {
	if fn, ok := ctx.Value(progressKey{}).(ProgressFunc); ok && fn != nil {
		fn(stage)
	}
}

// Collector gathers grounded search hits for the current turn so the bot can
// render buttons for exactly the tracks the model actually looked up.
type Collector struct {
	mu      sync.Mutex
	results []SearchResult
	reasons map[string]string
	skills  []string
}

func NewCollector() *Collector {
	return &Collector{reasons: make(map[string]string)}
}

func WithCollector(ctx context.Context, c *Collector) context.Context {
	return context.WithValue(ctx, collectorKey{}, c)
}

func CollectorFrom(ctx context.Context) *Collector {
	c, _ := ctx.Value(collectorKey{}).(*Collector)
	return c
}

func (c *Collector) Add(items []SearchResult) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, item := range items {
		key := item.Platform + ":" + item.SongID
		duplicate := false
		for _, existing := range c.results {
			if existing.Platform+":"+existing.SongID == key {
				duplicate = true
				break
			}
		}
		if !duplicate {
			c.results = append(c.results, item)
		}
	}
}

func (c *Collector) Results() []SearchResult {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]SearchResult(nil), c.results...)
}

// AddSkill records a loaded skill so the caller can verify the model actually
// pulled the instruction it claims to follow.
func (c *Collector) AddSkill(name string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, existing := range c.skills {
		if existing == name {
			return
		}
	}
	c.skills = append(c.skills, name)
}

func (c *Collector) Skills() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.skills...)
}
