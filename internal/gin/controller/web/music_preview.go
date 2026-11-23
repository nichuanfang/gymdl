package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/qqmusic"
)

const qqMusicPreviewTemplateSource = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="robots" content="noindex,nofollow">
  <title>{{.Title}} - {{.Artist}}</title>
  <meta property="og:type" content="music.song">
  <meta property="og:site_name" content="QQ音乐">
  <meta property="og:title" content="{{.Title}}">
  <meta property="og:description" content="{{.Description}}">
  <meta property="og:image" content="{{.CoverURL}}">
  <meta property="og:image:width" content="300">
  <meta property="og:image:height" content="300">
  <meta name="twitter:card" content="summary_large_image">
</head>
<body>
  <h1>{{.Title}}</h1>
  <p>{{.Artist}}{{if .Album}} · {{.Album}}{{end}}{{if .Duration}} · {{.Duration}}{{end}}</p>
  <p><a href="{{.AppURL}}">在 QQ 音乐中打开</a></p>
  <script>
    (function () {
      var appURL = {{.AppURLJSON}};
      var webURL = {{.WebURLJSON}};
      var timer = window.setTimeout(function () {
        if (!document.hidden) window.location.replace(webURL);
      }, 1800);
      window.addEventListener('pagehide', function () { window.clearTimeout(timer); }, { once: true });
      window.location.href = appURL;
    })();
  </script>
</body>
</html>`

var qqMusicPreviewPage = template.Must(template.New("qq-music-preview").Parse(qqMusicPreviewTemplateSource))

type qqMusicPreviewPageData struct {
	Title       string
	Artist      string
	Album       string
	Duration    string
	Description string
	CoverURL    string
	AppURL      string
	AppURLJSON  template.JS
	WebURLJSON  template.JS
}

func HandleQQMusicPreview(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Robots-Tag", "noindex, nofollow")
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	c.Header("Content-Type", "text/html; charset=utf-8")

	query := c.Request.URL.Query()
	for _, key := range []string{"mid", "cover", "title", "artist"} {
		if len(query[key]) != 1 {
			c.Status(http.StatusBadRequest)
			return
		}
	}
	for _, key := range []string{"album", "duration"} {
		if len(query[key]) > 1 {
			c.Status(http.StatusBadRequest)
			return
		}
	}

	mid := query.Get("mid")
	coverID := query.Get("cover")
	title, titleOK := musicPreviewText(query.Get("title"), 720)
	artist, artistOK := musicPreviewText(query.Get("artist"), 960)
	album, albumOK := musicPreviewText(query.Get("album"), 960)
	if !qqmusic.ValidSongMID(mid) || !qqmusic.ValidAlbumCoverID(coverID) || !titleOK || !artistOK || !albumOK || title == "" || artist == "" {
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

	appURL, ok := qqmusic.SongUniversalLink(mid)
	if !ok {
		c.Status(http.StatusBadRequest)
		return
	}
	webURL := "https://y.qq.com/n/ryqq/songDetail/" + mid
	description := "歌手：" + artist
	if album != "" {
		description += " · 专辑：" + album
	}
	if duration != "" {
		description += " · 时长：" + duration
	}
	var body bytes.Buffer
	if err := qqMusicPreviewPage.Execute(&body, qqMusicPreviewPageData{
		Title: title, Artist: artist, Album: album, Duration: duration,
		Description: description, CoverURL: qqmusic.AlbumCoverURL(coverID, ""),
		AppURL: appURL, AppURLJSON: template.JS(strconv.Quote(appURL)),
		WebURLJSON: template.JS(strconv.Quote(webURL)),
	}); err != nil {
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
