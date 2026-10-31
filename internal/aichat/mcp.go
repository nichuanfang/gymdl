package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/nichuanfang/gymdl/config"
)

// MCPClient wraps the maintained mark3labs client so session handling,
// initialize/initialized ordering and streamable-HTTP framing stay in the
// library instead of being reimplemented here.
type MCPClient struct {
	cfg    config.MCPServerConfig
	mu     sync.Mutex
	client *client.Client
}

func NewMCPClients(servers []config.MCPServerConfig) []*MCPClient {
	out := make([]*MCPClient, 0, len(servers))
	for _, s := range servers {
		if strings.TrimSpace(s.URL) == "" || len(s.AllowedTools) == 0 {
			continue
		}
		out = append(out, &MCPClient{cfg: s})
	}
	return out
}

func (m *MCPClient) connect(ctx context.Context) (*client.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		return m.client, nil
	}
	options := []transport.StreamableHTTPCOption{}
	if m.cfg.Token != "" {
		options = append(options, transport.WithHTTPHeaders(map[string]string{
			"Authorization": "Bearer " + m.cfg.Token,
		}))
	}
	c, err := client.NewStreamableHttpClient(m.cfg.URL, options...)
	if err != nil {
		return nil, fmt.Errorf("mcp %s: create client: %w", m.cfg.Name, err)
	}
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp %s: start: %w", m.cfg.Name, err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "gymdl", Version: "1.0.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp %s: initialize: %w", m.cfg.Name, err)
	}
	m.client = c
	return c, nil
}

// Tools lists the server's tools and keeps only allow-listed names. An empty
// allow-list never exposes anything, which is enforced before connecting.
func (m *MCPClient) Tools(ctx context.Context) ([]Tool, error) {
	c, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(m.cfg.AllowedTools))
	for _, name := range m.cfg.AllowedTools {
		allowed[name] = true
	}
	listed, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("mcp %s: list tools: %w", m.cfg.Name, err)
	}
	out := make([]Tool, 0, len(listed.Tools))
	for _, item := range listed.Tools {
		if !allowed[item.Name] {
			continue
		}
		name := "mcp_" + sanitize(m.cfg.Name) + "_" + sanitize(item.Name)
		toolName := item.Name
		schema := map[string]any{}
		if item.InputSchema.Properties != nil {
			schema["properties"] = item.InputSchema.Properties
		}
		if len(item.InputSchema.Required) > 0 {
			schema["required"] = item.InputSchema.Required
		}
		schema["type"] = "object"
		out = append(out, Tool{
			Name:        name,
			Description: fmt.Sprintf("[MCP %s] %s", m.cfg.Name, item.Description),
			Parameters:  schema,
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				return m.call(ctx, toolName, args)
			},
		})
	}
	return out, nil
}

func (m *MCPClient) call(ctx context.Context, name string, args map[string]any) (string, error) {
	timeout := time.Duration(m.cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := m.callOnce(callCtx, name, args)
	if err == nil {
		return result, nil
	}
	// A broken session (server restart, dropped stream) keeps failing forever
	// because the client is cached. Close it and retry once with a fresh one so
	// MCP does not silently stay unavailable for the life of the process.
	if m.reset() {
		result, retryErr := m.callOnce(callCtx, name, args)
		if retryErr == nil {
			return result, nil
		}
		return "", fmt.Errorf("mcp %s: call %s: %w (retry: %v)", m.cfg.Name, name, err, retryErr)
	}
	return "", fmt.Errorf("mcp %s: call %s: %w", m.cfg.Name, name, err)
}

func (m *MCPClient) callOnce(ctx context.Context, name string, args map[string]any) (string, error) {
	c, err := m.connect(ctx)
	if err != nil {
		return "", err
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	result, err := c.CallTool(ctx, req)
	if err != nil {
		return "", err
	}
	return renderToolResult(result), nil
}

// reset drops the cached session so the next connect starts fresh. It reports
// whether a session was actually closed.
func (m *MCPClient) reset() bool {
	m.mu.Lock()
	c := m.client
	m.client = nil
	m.mu.Unlock()
	if c == nil {
		return false
	}
	_ = c.Close()
	return true
}

// renderToolResult keeps text content and structured content, and drops images
// because the model only receives text.
func renderToolResult(result *mcp.CallToolResult) string {
	if result == nil {
		return `{"error":"empty result"}`
	}
	parts := make([]string, 0, len(result.Content)+1)
	for _, item := range result.Content {
		if text, ok := item.(mcp.TextContent); ok && strings.TrimSpace(text.Text) != "" {
			parts = append(parts, text.Text)
		}
	}
	if len(parts) == 0 && result.StructuredContent != nil {
		if encoded, err := json.Marshal(result.StructuredContent); err == nil {
			parts = append(parts, string(encoded))
		}
	}
	if result.IsError {
		return fmt.Sprintf(`{"is_error":true,"content":%q}`, strings.Join(parts, "\n"))
	}
	if len(parts) == 0 {
		return `{"content":""}`
	}
	return strings.Join(parts, "\n")
}

func (m *MCPClient) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil {
		return nil
	}
	err := m.client.Close()
	m.client = nil
	return err
}

func sanitize(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else if r == '-' || r == ' ' || r == '.' {
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "server"
	}
	return b.String()
}
