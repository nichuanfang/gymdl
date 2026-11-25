package musiclink

import (
	"net/url"
	"strings"

	"github.com/nichuanfang/gymdl/internal/netease"
	"github.com/nichuanfang/gymdl/internal/qqmusic"
)

// DirectAppLink converts supported single-song pages to their platform's
// official app-aware links. Unknown and malformed URLs are returned unchanged.
func DirectAppLink(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	platform, id, _ := SourceSong(rawURL)
	switch platform {
	case "netease":
		return netease.SongAppLink(id)
	case "qq":
		link, _ := qqmusic.SongUniversalLink(id)
		return link
	}
	return rawURL
}

// SourceSong parses a provider song page with an optional validated cover.
func SourceSong(rawURL string) (platform, id, cover string) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || parsed.ForceQuery {
		return "", "", ""
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", "", ""
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", "", ""
	}
	if values, present := query["cover"]; present {
		if len(values) != 1 || values[0] == "" {
			return "", "", ""
		}
		cover = values[0]
		delete(query, "cover")
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "music.163.com", "m.music.163.com":
		if ids := query["id"]; parsed.Path == "/song" && len(query) == 1 && len(ids) == 1 && netease.ValidSongID(ids[0]) {
			if normalized := netease.CoverURL(cover); cover == "" || normalized != "" {
				return "netease", ids[0], normalized
			}
		}
	case "y.qq.com":
		if mid, ok := strings.CutPrefix(parsed.Path, qqmusic.SongPagePath); ok && len(query) == 0 && qqmusic.ValidSongMID(mid) {
			if cover == "" || qqmusic.ValidAlbumCoverID(cover) {
				return "qq", mid, qqmusic.AlbumCoverURL(cover, "")
			}
		}
	}
	return "", "", ""
}
