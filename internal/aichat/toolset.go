package aichat

import (
	"context"
	"strings"
	"time"
)

// LoadMCPTools initializes every configured MCP server and converts its
// allow-listed tools into ordinary agent tools. A failed server is skipped so
// one broken endpoint cannot disable the whole agent.
func LoadMCPTools(ctx context.Context, clients []*MCPClient) []Tool {
	out := make([]Tool, 0)
	for _, client := range clients {
		if client == nil {
			continue
		}
		initCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		tools, err := client.Tools(initCtx)
		cancel()
		if err != nil {
			continue
		}
		out = append(out, tools...)
	}
	return out
}

// MergeTools appends sources while dropping duplicate names; the first
// definition wins so built-ins cannot be shadowed by a remote server.
func MergeTools(sources ...[]Tool) []Tool {
	seen := make(map[string]bool)
	out := make([]Tool, 0)
	for _, source := range sources {
		for _, tool := range source {
			name := strings.TrimSpace(tool.Name)
			if name == "" || seen[name] || tool.Run == nil {
				continue
			}
			seen[name] = true
			out = append(out, tool)
		}
	}
	return out
}
