package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/musiccover"
	"github.com/nichuanfang/gymdl/internal/netease"
	"github.com/nichuanfang/gymdl/internal/qqmusic"
)

const musicPreviewTemplateSource = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="robots" content="noindex,nofollow">
  <title>{{.Title}} - {{.Artist}}</title>
  <meta property="og:type" content="music.song">
  <meta property="og:site_name" content="{{.Platform}}">
  <meta property="og:title" content="{{.Title}}">
  <meta property="og:description" content="{{.Description}}">
  <meta property="og:image" content="{{.CoverURL}}">
  <meta property="og:image:width" content="{{.CoverSize}}">
  <meta property="og:image:height" content="{{.CoverSize}}">
  <meta name="twitter:card" content="summary_large_image">
</head>
<body>
  <h1>{{.Title}}</h1>
  <p>{{.Artist}}{{if .Album}} · {{.Album}}{{end}}{{if .Duration}} · {{.Duration}}{{end}}</p>
  <p><a href="{{.AppURL}}">在 {{.Platform}} 中打开</a></p>
  <script>
    (function () {
      var appURL = {{.AppURL}};
      var webURL = {{.WebURL}};
      var timer = window.setTimeout(function () {
        if (!document.hidden) window.location.replace(webURL);
      }, 1800);
      window.addEventListener('pagehide', function () { window.clearTimeout(timer); }, { once: true });
      window.location.href = appURL;
    })();
  </script>
</body>
</html>`

var musicPreviewPage = template.Must(template.New("music-preview").Parse(musicPreviewTemplateSource))

type musicPreviewPageData struct {
	Platform    string
	Title       string
	Artist      string
	Album       string
	Duration    string
	Description string
	CoverURL    string
	CoverSize   int
	AppURL      string
	WebURL      string
}

func HandleMusicPreview(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Robots-Tag", "noindex, nofollow")
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	c.Header("Content-Type", "text/html; charset=utf-8")

	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	for key, values := range query {
		switch key {
		case "platform", "id", "mid", "cover", "title", "artist", "album", "duration":
			if len(values) == 1 {
				continue
			}
		}
		c.Status(http.StatusBadRequest)
		return
	}

	data := musicPreviewPageData{CoverSize: musiccover.Size}
	platform := query.Get("platform")
	// Links sent before platform selection was introduced omit platform.
	if !query.Has("platform") {
		platform = "qq"
	}
	switch platform {
	case "qq":
		if !query.Has("id") {
			mid := query.Get("mid")
			data.Platform = "QQ 音乐"
			data.CoverURL = qqmusic.AlbumCoverURL(query.Get("cover"), "")
			data.AppURL, _ = qqmusic.SongUniversalLink(mid)
			data.WebURL = qqmusic.SongPageURL(mid, "")
		}
	case "netease":
		if !query.Has("mid") {
			id := query.Get("id")
			data.Platform = "网易云音乐"
			data.CoverURL = netease.CoverURL(query.Get("cover"))
			data.AppURL = netease.SongAppLink(id)
			data.WebURL = netease.SongPageURL(id, "")
		}
	}
	title, titleOK := musicPreviewText(query.Get("title"), 720)
	artist, artistOK := musicPreviewText(query.Get("artist"), 960)
	album, albumOK := musicPreviewText(query.Get("album"), 960)
	if data.CoverURL == "" || data.AppURL == "" || !titleOK || !artistOK || !albumOK || title == "" || artist == "" {
		c.Status(http.StatusBadRequest)
		return
	}

	duration := ""
	if rawDuration := query.Get("duration"); rawDuration != "" {
		seconds, err := strconv.Atoi(rawDuration)
		if err != nil || seconds <= 0 || seconds > 24*60*60 {
			c.Status(http.StatusBadRequest)
			return
		}
		duration = musicPreviewDuration(seconds)
	}

	description := "歌手：" + artist
	if album != "" {
		description += " · 专辑：" + album
	}
	if duration != "" {
		description += " · 时长：" + duration
	}
	var body bytes.Buffer
	data.Title, data.Artist, data.Album = title, artist, album
	data.Duration, data.Description = duration, description
	if err := musicPreviewPage.Execute(&body, data); err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", body.Bytes())
}

func musicPreviewText(value string, maxBytes int) (string, bool) {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return "", true
	}
	if len(value) > maxBytes {
		return "", false
	}
	return value, true
}

func musicPreviewDuration(seconds int) string {
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
