package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nichuanfang/gymdl/internal/aichat"
)

const maxPlaylistWebEvidenceRunes = 5000

func playlistWebSearchTool(tools []aichat.Tool) (aichat.Tool, string, bool) {
	for _, tool := range tools {
		if !strings.EqualFold(tool.Name, "mcp_webtool_websearch") || tool.Run == nil {
			continue
		}
		properties, _ := tool.Parameters["properties"].(map[string]any)
		if len(properties) == 0 {
			return aichat.Tool{}, "", false
		}
		for _, candidate := range []string{"query", "search_query", "searchquery", "search_text", "q", "keywords"} {
			for name := range properties {
				if strings.EqualFold(name, candidate) {
					return tool, name, true
				}
			}
		}
		if len(properties) == 1 {
			for name := range properties {
				return tool, name, true
			}
		}
		for name, raw := range properties {
			property, _ := raw.(map[string]any)
			description, _ := property["description"].(string)
			if strings.Contains(strings.ToLower(description), "search query") || strings.Contains(strings.ToLower(description), "搜索词") {
				return tool, name, true
			}
		}
		return aichat.Tool{}, "", false
	}
	return aichat.Tool{}, "", false
}

func runPlaylistWebSearch(ctx context.Context, tools []aichat.Tool, song unplaylistSong) (string, error) {
	tool, queryField, ok := playlistWebSearchTool(tools)
	if !ok {
		return "", errors.New("WebSearch MCP 工具未配置或参数不兼容")
	}
	query := playlistSongSearchQuery(song)
	args := map[string]any{queryField: query}
	for _, required := range requiredToolFields(tool.Parameters["required"]) {
		if required == queryField {
			continue
		}
		properties, _ := tool.Parameters["properties"].(map[string]any)
		property, _ := properties[required].(map[string]any)
		if value, exists := property["default"]; exists {
			args[required] = value
			continue
		}
		return "", fmt.Errorf("WebSearch MCP 工具还要求未支持的参数 %q", required)
	}
	result, err := tool.Run(ctx, args)
	if err != nil {
		return "", fmt.Errorf("调用 WebSearch MCP 工具失败: %w", err)
	}
	result = strings.TrimSpace(result)
	if result == "" {
		return "", errors.New("WebSearch MCP 没有返回搜索资料")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(result), &envelope) == nil {
		var isError bool
		_ = json.Unmarshal(envelope["is_error"], &isError)
		if isError || (len(envelope["error"]) > 0 && string(envelope["error"]) != "null") {
			return "", errors.New("WebSearch MCP 返回错误")
		}
		if content := envelope["content"]; len(content) > 0 {
			var text string
			if json.Unmarshal(content, &text) == nil && strings.TrimSpace(text) == "" {
				return "", errors.New("WebSearch MCP 没有返回搜索资料")
			}
		}
	}
	return result, nil
}

func requiredToolFields(raw any) []string {
	switch values := raw.(type) {
	case []string:
		return values
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if name, ok := value.(string); ok {
				result = append(result, name)
			}
		}
		return result
	default:
		return nil
	}
}

func playlistSongSearchQuery(song unplaylistSong) string {
	parts := []string{displaySongValue(song.Title), displaySongValue(song.Artist)}
	if strings.TrimSpace(song.Album) != "" {
		parts = append(parts, displaySongValue(song.Album))
	}
	return strings.Join(parts, " ") + " song genre style mood"
}

func truncatePlaylistWebEvidence(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	runes := []rune(value)
	if len(runes) <= maxPlaylistWebEvidenceRunes {
		return value
	}
	truncated := string(runes[:maxPlaylistWebEvidenceRunes])
	for !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "…"
}
