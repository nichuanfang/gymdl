package aichat

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"
)

// Render produces the Telegram message body. The design keeps the answer as the
// only visual focus: mode and tool details live in a single quiet footer, and
// candidate lists collapse to one line per track so a 10-song answer still fits
// a phone screen without scrolling past the buttons.
func Render(out *TurnOutput, defaultMode, enabled bool) string {
	var b strings.Builder
	// The model's reply is untrusted text: escape it so a stray "<" from a song
	// title cannot break Telegram's HTML parser, while keeping its own newlines.
	b.WriteString(emphasize(html.EscapeString(strings.TrimSpace(out.Reply))))
	if len(out.Candidates) > 0 {
		b.WriteString("\n\n")
		// No fixed cap here: the engine already limited the list by
		// ai.max_suggestions, and a second hard-coded limit made the numbered
		// text disagree with the number of buttons.
		b.WriteString(RenderCandidates(out.Candidates, 0))
	}
	// No per-message mode footer: the mode is already visible from the chat
	// itself and repeating it on every answer only adds noise.
	return strings.TrimRight(b.String(), "\n")
}

// RenderCandidates matches the existing ingestion feedback style: one compact
// block per track with an emoji anchor. Telegram renders a 10-item answer much
// faster to scan this way than as a bare list, and it stays consistent with the
// "入库成功" messages users already see.
func RenderCandidates(candidates []Candidate, limit int) string {
	if len(candidates) == 0 {
		return ""
	}
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	var b strings.Builder
	b.WriteString("🎧 <b>可下载曲目</b>\n")
	for i, c := range candidates {
		if strings.TrimSpace(c.Name) == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("%d. ", i+1))
		b.WriteString("🎵 <b>" + html.EscapeString(truncateRunes(c.Name, 40)) + "</b>")
		if artist := strings.TrimSpace(c.Artist); artist != "" {
			b.WriteString(" — " + html.EscapeString(truncateRunes(artist, 28)))
		}
		flags := make([]string, 0, 2)
		if strings.TrimSpace(c.Platform) != "" {
			flags = append(flags, PlatformLabel(c.Platform))
		}
		if c.IsVIP {
			flags = append(flags, "VIP")
		}
		if c.Owned {
			flags = append(flags, "已入库")
		}
		if len(flags) > 0 {
			b.WriteString("\n   " + html.EscapeString(strings.Join(flags, " · ")))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// telegramMessageLimit is Telegram's hard cap for one message body.
const telegramMessageLimit = 4096

// SplitMessage breaks a rendered reply into Telegram-sized chunks without
// cutting an HTML tag in half. It prefers newline boundaries so the candidate
// list stays readable.
func SplitMessage(body string) []string {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return []string{""}
	}
	if utf8.RuneCountInString(body) <= telegramMessageLimit {
		return []string{body}
	}
	const chunkRuneLimit = telegramMessageLimit - 64
	lines := strings.Split(body, "\n")
	var chunks []string
	var current strings.Builder
	count := 0
	openTags := make([]string, 0, 2)
	flush := func() {
		if current.Len() == 0 {
			return
		}
		for index := len(openTags) - 1; index >= 0; index-- {
			current.WriteString("</" + openTags[index] + ">")
		}
		chunks = append(chunks, strings.TrimRight(current.String(), "\n"))
		current.Reset()
		count = 0
		for _, tag := range openTags {
			reopen := "<" + tag + ">"
			current.WriteString(reopen)
			count += utf8.RuneCountInString(reopen)
		}
	}
	appendToken := func(token string) {
		current.WriteString(token)
		count += utf8.RuneCountInString(token)
		if name, opening, ok := splitMessageTag(token); ok {
			if opening {
				openTags = append(openTags, name)
			} else {
				for index := len(openTags) - 1; index >= 0; index-- {
					if openTags[index] == name {
						openTags = append(openTags[:index], openTags[index+1:]...)
						break
					}
				}
			}
		}
	}
	for lineIndex, line := range lines {
		tokens := splitMessageLineTokens(line)
		lineLength := 0
		for _, token := range tokens {
			lineLength += utf8.RuneCountInString(token)
		}
		if count+lineLength <= chunkRuneLimit {
			for _, token := range tokens {
				appendToken(token)
			}
		} else {
			flush()
			for _, token := range tokens {
				if count+utf8.RuneCountInString(token) > chunkRuneLimit {
					flush()
				}
				appendToken(token)
			}
		}
		if lineIndex < len(lines)-1 {
			if count+1 > chunkRuneLimit {
				flush()
			}
			appendToken("\n")
		}
	}
	flush()
	if len(chunks) == 0 {
		return []string{body}
	}
	return chunks
}

func splitMessageLineTokens(line string) []string {
	tokens := make([]string, 0, utf8.RuneCountInString(line))
	for len(line) > 0 {
		if line[0] == '<' {
			if end := strings.IndexByte(line, '>'); end >= 0 {
				tag := line[:end+1]
				if _, _, ok := splitMessageTag(tag); ok {
					tokens = append(tokens, tag)
					line = line[end+1:]
					continue
				}
			}
		}
		if line[0] == '&' {
			if end := strings.IndexByte(line, ';'); end > 1 && end <= 12 {
				tokens = append(tokens, line[:end+1])
				line = line[end+1:]
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(line)
		if size == 0 {
			break
		}
		tokens = append(tokens, line[:size])
		line = line[size:]
	}
	return tokens
}

func splitMessageTag(token string) (name string, opening bool, ok bool) {
	switch token {
	case "<b>":
		return "b", true, true
	case "</b>":
		return "b", false, true
	case "<code>":
		return "code", true, true
	case "</code>":
		return "code", false, true
	default:
		return "", false, false
	}
}

// PlatformLabel renders a provider name the way the candidate list shows it.
func PlatformLabel(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "netease":
		return "网易云"
	case "qq":
		return "QQ 音乐"
	case "youtube":
		return "YouTube"
	case "bilibili":
		return "B 站"
	}
	return platform
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

// ModeLabel is used by /mode and /ai confirmations.
func ModeLabel(enabled bool) string {
	if enabled {
		return "智能体模式"
	}
	return "普通模式"
}

func DefaultModeLabel(defaultMode bool) string {
	if defaultMode {
		return "智能体模式"
	}
	return "普通模式"
}

// ModeSummary is the body of /mode. On a phone the picker already shows both
// options, so this only states the current mode and the one rule that confuses
// people (links still download). Anything longer pushed the buttons off screen.
func ModeSummary(enabled bool, defaultMode bool) string {
	var b strings.Builder
	b.WriteString("当前：<b>" + ModeLabel(enabled) + "</b>\n")
	if enabled {
		b.WriteString("直接说话；发链接仍会下载。")
	} else {
		b.WriteString("发链接下载；对话用 /ai。")
	}
	if defaultMode != enabled {
		b.WriteString("\n<i>新聊天默认：" + DefaultModeLabel(defaultMode) + "</i>")
	}
	return b.String()
}

// CandidateLabel keeps button text predictable so Telegram does not wrap rows
// into an unreadable block on narrow screens.
func CandidateLabel(index int, c Candidate) string {
	title := truncateRunes(c.Name, 26)
	if c.Owned {
		return fmt.Sprintf("✓ %s", title)
	}
	return fmt.Sprintf("%d. %s", index+1, title)
}
