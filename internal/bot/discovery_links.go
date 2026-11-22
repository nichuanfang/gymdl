package bot

import (
	"net/url"
	"strings"
)

const qqMusicLinkPath = "/api/playlist-assignment/music-link"

func discoveryPublicWebAppURL(app *BotApp) string {
	if app == nil || app.cfg == nil || app.cfg.PlaylistOrganizer == nil {
		return ""
	}
	return app.cfg.PlaylistOrganizer.WebAppURL
}

func discoveryListenURL(rawURL, webAppURL string) string {
	base, err := playlistWebAppBaseURL(webAppURL)
	if err != nil {
		return rawURL
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
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
		if !validSongMID(mid) || parsed.RawQuery != "" {
			return rawURL
		}
		link := *base
		link.Path = qqMusicLinkPath
		query := link.Query()
		query.Set("mid", mid)
		link.RawQuery = query.Encode()
		return link.String()
	default:
		// YouTube Music, Bilibili, and other platform HTTPS links are already
		// owned by those platforms and retain their native Universal Link rules.
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

func validSongMID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
