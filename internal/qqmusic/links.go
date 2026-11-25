package qqmusic

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/nichuanfang/gymdl/internal/musiccover"
)

const (
	songUniversalLink = "https://music.qq.com/unilink/download.html"
	coverCDNPrefix    = "https://y.gtimg.cn/music/photo_new/T002R"
	legacyCoverPrefix = coverCDNPrefix + "300x300M000"
	SongPagePath      = "/n/ryqq/songDetail/"
)

var coverURLPrefix = coverCDNPrefix + strconv.Itoa(musiccover.Size) + "x" + strconv.Itoa(musiccover.Size) + "M000"

type songOpenPayload struct {
	Action      string          `json:"action"`
	Song        []songOpenTrack `json:"song"`
	HideLoading string          `json:"hideLoading"`
}

type songOpenTrack struct {
	SongMID string `json:"songmid"`
	Type    int    `json:"type"`
}

// SongUniversalLink creates the Universal Link used by QQ Music's own H5 player.
func SongUniversalLink(songMID string) (string, bool) {
	appURL, ok := SongAppLink(songMID)
	if !ok {
		return "", false
	}
	return songUniversalLink + "?scheme=" + url.QueryEscape(appURL), true
}

// SongAppLink builds QQ Music's iOS protocol URL.
func SongAppLink(songMID string) (string, bool) {
	if !ValidSongMID(songMID) {
		return "", false
	}
	payload, err := json.Marshal(songOpenPayload{
		Action:      "play",
		Song:        []songOpenTrack{{SongMID: songMID, Type: 0}},
		HideLoading: "1",
	})
	if err != nil {
		return "", false
	}

	return "qqmusic://qq.com/media/playSonglist?p=" + url.QueryEscape(string(payload)), true
}

// ValidSongMID accepts the alphanumeric song MIDs used in QQ Music links.
func ValidSongMID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !isASCIIAlphaNumeric(r) {
			return false
		}
	}
	return true
}

// ValidAlbumCoverID accepts QQ album PMIDs, which may append a numeric variant
// such as "_2" to the alphanumeric album MID.
func ValidAlbumCoverID(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}

	base, variant, hasVariant := strings.Cut(value, "_")
	if base == "" {
		return false
	}
	for _, r := range base {
		if !isASCIIAlphaNumeric(r) {
			return false
		}
	}
	if !hasVariant {
		return true
	}
	if variant == "" {
		return false
	}
	for _, r := range variant {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// AlbumCoverURL returns a validated QQ album-cover URL, falling back to the
// album MID if the optional PMID is malformed.
func AlbumCoverURL(pmid, mid string) string {
	coverID := strings.TrimSpace(pmid)
	if !ValidAlbumCoverID(coverID) {
		coverID = strings.TrimSpace(mid)
	}
	if !ValidAlbumCoverID(coverID) {
		return ""
	}
	return coverURLPrefix + coverID + ".jpg"
}

// AlbumCoverID accepts cached legacy URLs as well as current cover URLs.
func AlbumCoverID(raw string) string {
	for _, prefix := range []string{coverURLPrefix, legacyCoverPrefix} {
		id, ok := strings.CutPrefix(raw, prefix)
		if ok {
			id, hasSuffix := strings.CutSuffix(id, ".jpg")
			if hasSuffix && ValidAlbumCoverID(id) {
				return id
			}
		}
	}
	return ""
}

func SongPageURL(mid, coverID string) string {
	if !ValidSongMID(mid) {
		return ""
	}
	page := "https://y.qq.com" + SongPagePath + mid
	if ValidAlbumCoverID(coverID) {
		page += "?cover=" + coverID
	}
	return page
}

func isASCIIAlphaNumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
