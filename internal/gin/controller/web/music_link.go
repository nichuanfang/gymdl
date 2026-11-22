package web

import (
	"bytes"
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/qqmusic"
)

var musicLinkPage = template.Must(template.New("music-link").Parse(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="robots" content="noindex,nofollow">
  <title>正在打开 QQ 音乐</title>
  <style>
    body { font: 16px -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 12vh auto; max-width: 30rem; padding: 0 1.25rem; color: #202124; }
    a { display: block; margin: 1rem 0; padding: .9rem 1rem; border-radius: .75rem; text-align: center; text-decoration: none; background: #18a058; color: white; }
    a.secondary { background: #f1f3f4; color: #202124; }
    p { color: #5f6368; line-height: 1.6; }
  </style>
</head>
<body>
  <h1>正在打开 QQ 音乐</h1>
  <p id="status">如果没有自动打开，可以手动选择：</p>
  <a id="open-app" href="{{.AppURL}}">打开 QQ 音乐</a>
  <a id="open-web" class="secondary" href="{{.WebURL}}">在网页中打开歌曲</a>
  <script>
    (function () {
      var appURL = document.getElementById('open-app').href;
      var webURL = document.getElementById('open-web').href;
      var timer = window.setTimeout(function () {
        if (!document.hidden) {
          document.getElementById('status').textContent = '没有检测到 QQ 音乐，正在打开网页…';
          window.location.replace(webURL);
        }
      }, 1800);
      window.addEventListener('pagehide', function () { window.clearTimeout(timer); }, { once: true });
      window.location.href = appURL;
    })();
  </script>
</body>
</html>`))

type musicLinkPageData struct {
	AppURL template.URL
	WebURL string
}

func HandleQQMusicLink(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Robots-Tag", "noindex, nofollow")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")

	cfg := GetWebConfig()
	if cfg == nil || cfg.PlaylistOrganizer == nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	if _, err := playlistWebAppOrigin(cfg.PlaylistOrganizer.WebAppURL); err != nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	mid := strings.TrimSpace(c.Query("mid"))
	if !qqmusic.ValidSongMID(mid) {
		c.Status(http.StatusBadRequest)
		return
	}

	platform := strings.ToLower(strings.TrimSpace(c.Query("platform")))
	if platform != "" && platform != "ios" {
		c.Status(http.StatusBadRequest)
		return
	}
	appURL, _ := qqmusic.SongAppLink(mid)
	webURL := "https://y.qq.com/n/ryqq/songDetail/" + mid
	var body bytes.Buffer
	if err := musicLinkPage.Execute(&body, musicLinkPageData{AppURL: template.URL(appURL), WebURL: webURL}); err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", body.Bytes())
}
