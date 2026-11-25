package netease

import (
	"crypto/md5"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"

	"github.com/nichuanfang/gymdl/internal/musiccover"
)

var coverDimension = strconv.Itoa(musiccover.Size) + "y" + strconv.Itoa(musiccover.Size)

// CoverURLFromPictureID converts the numeric album image ID used by search to
// the same CDN URL returned by the song detail endpoint.
func CoverURLFromPictureID(id int64) string {
	if id <= 0 {
		return ""
	}
	pictureID := strconv.FormatInt(id, 10)
	const key = "3go8&$8*3*3h0k(2)2"
	mixed := []byte(pictureID)
	for i := range mixed {
		mixed[i] ^= key[i%len(key)]
	}
	hash := md5.Sum(mixed)
	return CoverURL("https://p2.music.126.net/" + base64.URLEncoding.EncodeToString(hash[:]) + "/" + pictureID + ".jpg")
}

// CoverURL accepts only HTTPS cover URLs on the NetEase image CDN.
func CoverURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.ForceQuery ||
		!strings.HasSuffix(strings.ToLower(u.Hostname()), ".music.126.net") ||
		(u.Port() != "" && u.Port() != "443") || u.Path == "" || u.Path == "/" {
		return ""
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || (u.RawQuery != "" && (len(query) != 1 || len(query["param"]) != 1 || query.Get("param") != coverDimension)) {
		return ""
	}
	u.Host = strings.ToLower(u.Hostname())
	u.RawQuery = "param=" + coverDimension
	return u.String()
}

func ValidSongID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == id
}

func SongPageURL(id, cover string) string {
	if !ValidSongID(id) {
		return ""
	}
	page := "https://music.163.com/song?id=" + id
	if cover = CoverURL(cover); cover != "" {
		page += "&cover=" + url.QueryEscape(cover)
	}
	return page
}

func SongAppLink(id string) string {
	if !ValidSongID(id) {
		return ""
	}
	return "https://m.music.163.com/m/applink/?scheme=" + url.QueryEscape("orpheus://song/"+id)
}
