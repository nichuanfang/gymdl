package musicdedupe

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/utils"
)

var bilibiliTrackID = regexp.MustCompile(`(?i)(BV[0-9A-Za-z]+|av[0-9]+)`)
var appleSongID = regexp.MustCompile(`/song/[^/]+/([0-9]+)(?:$|/)`)

func IdentityFromLink(kind processor.LinkType, rawURL string) Track {
	track := Track{Platform: string(kind)}
	if !IsSingleTrackLink(kind, rawURL) {
		return track
	}
	track.SourceURL = rawURL

	switch kind {
	case processor.LinkNetEase:
		linkType, id := utils.ParseMusicID(rawURL)
		if linkType == 1 && id > 0 {
			track.PlatformTrackID = strconv.Itoa(id)
		}
	case processor.LinkQQMusic:
		if id, ok := music.QQMusicTrackID(rawURL); ok {
			track.PlatformTrackID = id
		}
	case processor.LinkYoutubeMusic:
		if parsed, err := url.Parse(rawURL); err == nil {
			track.PlatformTrackID = strings.TrimSpace(parsed.Query().Get("v"))
		}
	case processor.LinkBiliBiliMusic:
		if match := bilibiliTrackID.FindString(rawURL); match != "" {
			track.PlatformTrackID = match
		}
	case processor.LinkAppleMusic:
		if parsed, err := url.Parse(rawURL); err == nil {
			track.PlatformTrackID = strings.TrimSpace(parsed.Query().Get("i"))
			if track.PlatformTrackID == "" {
				if match := appleSongID.FindStringSubmatch(parsed.Path); len(match) == 2 {
					track.PlatformTrackID = match[1]
				}
			}
		}
	case processor.LinkSoundcloud:
		track.PlatformTrackID = CanonicalURL(rawURL)
	case processor.LinkSpotify:
		if parsed, err := url.Parse(rawURL); err == nil {
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(parts) == 2 && parts[0] == "track" {
				track.PlatformTrackID = parts[1]
			}
		}
	}
	return track
}

func IsSingleTrackLink(kind processor.LinkType, rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch kind {
	case processor.LinkNetEase:
		linkType, id := utils.ParseMusicID(rawURL)
		return linkType == 1 && id > 0
	case processor.LinkQQMusic:
		return music.IsQQMusicSingleLink(rawURL)
	case processor.LinkYoutubeMusic:
		return parsed.Query().Get("v") != ""
	case processor.LinkBiliBiliMusic:
		return strings.Contains(parsed.Path, "/video/") && parsed.Query().Get("p") == ""
	case processor.LinkAppleMusic:
		return strings.Contains(parsed.Path, "/song/") || parsed.Query().Get("i") != ""
	case processor.LinkSoundcloud:
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		return len(parts) == 2 && parts[0] != "sets"
	case processor.LinkSpotify:
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		return len(parts) == 2 && parts[0] == "track"
	default:
		return false
	}
}

// ReservationKey serializes concurrent submissions for the strongest identity
// available, preferring the cross-platform metadata tuple when it is complete.
func ReservationKey(track Track) string {
	title, artist, album := normalizeText(track.Title), normalizeText(track.Artist), normalizeText(track.Album)
	if title != "" && artist != "" && album != "" {
		return "metadata:" + title + "\x00" + artist + "\x00" + album
	}
	if track.Platform != "" && track.PlatformTrackID != "" {
		return "platform:" + normalizeText(track.Platform) + "\x00" + strings.TrimSpace(track.PlatformTrackID)
	}
	if canonical := CanonicalURL(track.SourceURL); canonical != "" {
		return "url:" + canonical
	}
	return ""
}
