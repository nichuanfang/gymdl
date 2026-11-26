// Package lyricmeta defines the small, serializable lyric evidence vocabulary
// shared by download processors, library inspection, and duplicate detection.
package lyricmeta

import (
	"strings"

	"go.senan.xyz/taglib"
)

// Evidence records whether a file can support a lyric/instrumental distinction.
// It intentionally carries no lyric text.
type Evidence string

const (
	// Present means a non-empty, non-placeholder lyric was found.
	Present Evidence = "present"
	// ExplicitInstrumental means the source explicitly identified an instrumental.
	ExplicitInstrumental Evidence = "explicit_instrumental"
	// Unknown means lyric presence could not be established reliably.
	Unknown Evidence = "unknown"
)

// Normalize maps legacy or absent values to Unknown so old payloads stay readable.
func Normalize(value Evidence) Evidence {
	switch value {
	case Present, ExplicitInstrumental:
		return value
	default:
		return Unknown
	}
}

// FromLyricsText classifies an embedded lyric tag. Historical placeholders are
// not trustworthy instrumental evidence and therefore remain Unknown.
func FromLyricsText(text string) Evidence {
	text = strings.TrimSpace(text)
	if text == "" || IsLegacyInstrumentalPlaceholder(text) {
		return Unknown
	}
	return Present
}

// IsLegacyInstrumentalPlaceholder recognizes the old generated placeholder used
// when a lyric lookup returned nothing or failed.
func IsLegacyInstrumentalPlaceholder(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	const phrase = "此歌曲为没有填词的纯音乐"
	if !strings.Contains(text, phrase) {
		return false
	}
	// The legacy placeholder is a single short line. A real lyric that merely
	// mentions the phrase has more lines and must be kept as Present.
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if index := strings.Index(line, "]"); index+1 < len(line) {
				if line = strings.TrimSpace(line[index+1:]); line == "" {
					continue
				}
			}
		}
		lines = append(lines, line)
	}
	if len(lines) != 1 {
		return false
	}
	return strings.Contains(lines[0], phrase) && len([]rune(lines[0])) <= 40
}

// FromSource classifies provider output. Only an explicit provider flag can
// produce ExplicitInstrumental. Missing or failed lyrics remain Unknown.
func FromSource(lyrics string, explicitInstrumental bool) Evidence {
	if evidence := FromLyricsText(lyrics); evidence == Present {
		return Present
	}
	if explicitInstrumental {
		return ExplicitInstrumental
	}
	return Unknown
}

// ReadFile reads only embedded lyric tags. Missing lyrics are Unknown because
// absence alone cannot distinguish a failed lookup from an instrumental.
func ReadFile(path string) Evidence {
	tags, err := taglib.ReadTags(path)
	if err != nil {
		return Unknown
	}
	for _, value := range tags[taglib.Lyrics] {
		evidence := FromLyricsText(value)
		if evidence == Present {
			return Present
		}
	}
	return Unknown
}

// Conflict reports the one unambiguous mismatch that can bypass an audio match.
func Conflict(a, b Evidence) bool {
	a, b = Normalize(a), Normalize(b)
	return (a == Present && b == ExplicitInstrumental) || (a == ExplicitInstrumental && b == Present)
}
