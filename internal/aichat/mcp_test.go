package aichat

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/nichuanfang/gymdl/config"
)

func newMCPTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := server.NewMCPServer("test", "1.0.0")
	s.AddTool(
		mcp.NewTool("lookup", mcp.WithDescription("look up a track")),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("found: " + req.GetString("title", "")), nil
		},
	)
	s.AddTool(
		mcp.NewTool("dangerous_write", mcp.WithDescription("write something")),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("should not be exposed"), nil
		},
	)
	httpServer := server.NewStreamableHTTPServer(s)
	ts := httptest.NewServer(httpServer)
	t.Cleanup(ts.Close)
	return ts
}

func TestMCPClientListsOnlyAllowListedTools(t *testing.T) {
	ts := newMCPTestServer(t)
	clients := NewMCPClients([]config.MCPServerConfig{{
		Name: "music", URL: ts.URL, AllowedTools: []string{"lookup"},
	}})
	if len(clients) != 1 {
		t.Fatalf("clients = %d, want 1", len(clients))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, err := clients[0].Tools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "mcp_music_lookup" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	result, err := tools[0].Run(ctx, map[string]any{"title": "Luv(sic)"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if result != "found: Luv(sic)" {
		t.Fatalf("tool result = %q", result)
	}
}

func TestMCPClientWithoutAllowListNeverConnects(t *testing.T) {
	ts := newMCPTestServer(t)
	clients := NewMCPClients([]config.MCPServerConfig{{Name: "music", URL: ts.URL}})
	if len(clients) != 0 {
		t.Fatalf("empty allow-list created %d clients, want 0", len(clients))
	}
}

func TestLoadMCPToolsSkipsBrokenServer(t *testing.T) {
	ts := newMCPTestServer(t)
	good := NewMCPClients([]config.MCPServerConfig{{Name: "good", URL: ts.URL, AllowedTools: []string{"lookup"}}})
	broken := NewMCPClients([]config.MCPServerConfig{{Name: "broken", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"x"}}})
	tools := LoadMCPTools(context.Background(), append(good, broken...))
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1 (broken server must be skipped)", len(tools))
	}
}
