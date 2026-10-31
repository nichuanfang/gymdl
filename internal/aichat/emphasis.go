package aichat

import (
	"html"
	"sort"
	"strings"
)

// boldTokens are names worth emphasising in an answer. They are matched after
// HTML escaping, so the list must not contain characters that escaping rewrites.
var boldTokens = []string{
	"智能体模式", "普通模式",
	"网易云音乐", "网易云", "QQ 音乐", "QQ音乐", "B 站", "B站",
	"YouTube Music", "YouTube", "SoundCloud", "Spotify", "Apple Music",
}

// codeTokens render in a monospace chip, which reads better than bold for long
// identifiers such as skill names and commands.
var codeTokens = []string{
	"/mode", "/ai", "/clear", "/new",
	"music-recommendation", "music-discovery-by-artist", "music-library-query",
	"music-download-failure", "music-download-plan", "music-playlist-curation",
	"music-lyrics-search", "music-metadata-normalize",
	"search_music", "library_stats", "list_active_downloads", "load_skill",
}

func init() {
	// Escaped text is what the scanner sees, so escape the token tables once
	// using the same rules as the reply.
	for i, token := range boldTokens {
		boldTokens[i] = html.EscapeString(token)
	}
	for i, token := range codeTokens {
		codeTokens[i] = html.EscapeString(token)
	}
	// Longest first so "YouTube Music" wins over "YouTube" and "网易云音乐"
	// wins over "网易云"; the scanner skips matched spans, so nesting is moot.
	byLength := func(list []string) {
		sort.SliceStable(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	}
	byLength(boldTokens)
	byLength(codeTokens)
}

// emphasize adds Telegram-safe emphasis. Callers pass already-escaped text and
// the token tables are escaped with the same rules so an apostrophe such as
// "Can't" stays intact instead of being split into "Can&#39;t".
func emphasize(text string) string {
	var b strings.Builder
	bold := false
	code := false
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], "**") {
			if code {
				b.WriteString("**")
				i += 2
				continue
			}
			if bold {
				b.WriteString("</b>")
			} else {
				b.WriteString("<b>")
			}
			bold = !bold
			i += 2
			continue
		}
		if !bold && !code {
			if token := matchToken(text[i:], boldTokens); token != "" {
				b.WriteString("<b>" + token + "</b>")
				i += len(token)
				continue
			}
			if token := matchToken(text[i:], codeTokens); token != "" {
				b.WriteString("<code>" + token + "</code>")
				i += len(token)
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	if bold {
		b.WriteString("</b>")
	}
	_ = code
	return b.String()
}

func matchToken(text string, tokens []string) string {
	for _, token := range tokens {
		if strings.HasPrefix(text, token) {
			return token
		}
	}
	return ""
}
