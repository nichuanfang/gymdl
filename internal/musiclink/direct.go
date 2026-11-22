package musiclink

import (
	"net/url"
	"strings"

	"github.com/nichuanfang/gymdl/internal/qqmusic"
)

// DirectAppLink converts supported single-song pages to their platform's
// official app-aware links. Unknown and malformed URLs are returned unchanged.
func DirectAppLink(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || parsed.RawFragment != "" {
		return rawURL
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return rawURL
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "music.163.com", "m.music.163.com":
		if parsed.Path != "/song" {
			return rawURL
		}
		ids := parsed.Query()["id"]
		if len(ids) != 1 || !validNumericSongID(ids[0]) {
			return rawURL
		}
		appLink := "orpheus://song/" + ids[0]
		return "https://m.music.163.com/m/applink/?scheme=" + url.QueryEscape(appLink)
	case "y.qq.com":
		const prefix = "/n/ryqq/songDetail/"
		if !strings.HasPrefix(parsed.Path, prefix) {
			return rawURL
		}
		mid := strings.TrimPrefix(parsed.Path, prefix)
		if !qqmusic.ValidSongMID(mid) || parsed.RawQuery != "" || parsed.ForceQuery {
			return rawURL
		}
		link, ok := qqmusic.SongUniversalLink(mid)
		if !ok {
			return rawURL
		}
		return link
	default:
		// YouTube Music, Bilibili, and other platform links retain their
		// existing platform URL and association behavior.
		return rawURL
	}
}

func validNumericSongID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
