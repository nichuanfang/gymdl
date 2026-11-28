package music

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/nichuanfang/gymdl/internal/qqmusic"
	"github.com/nichuanfang/gymdl/utils"
)

const (
	qqShareAttemptTimeout = 4 * time.Second
	qqShareRetryDelay     = 200 * time.Millisecond
	qqShareRedirectLimit  = 10
)

var (
	// ErrQQShareInvalid means the share did not identify a usable QQ Music song or playlist.
	ErrQQShareInvalid = errors.New("这个 QQ 音乐分享链接无法识别，请从 QQ 音乐重新分享")
	// ErrQQShareUnavailable means the provider could not be reached to resolve the share.
	ErrQQShareUnavailable = errors.New("QQ 音乐分享链接暂时打不开。可以稍后重发，或发送歌曲详情页链接")
)

// IsQQShareLinkError reports whether a link-resolution error is safe to show to users.
func IsQQShareLinkError(err error) bool {
	return errors.Is(err, ErrQQShareInvalid) || errors.Is(err, ErrQQShareUnavailable)
}

// ResolveShareURL returns a QQ song or playlist URL that requires no further redirects.
func (qm *QQMusicProcessor) ResolveShareURL(raw string) (string, error) {
	link, err := qm.parseQQMusicLink(raw)
	if err != nil {
		return "", err
	}
	if link.isSong {
		return qqmusic.SongPageURL(link.id, ""), nil
	}
	return "https://y.qq.com/n/ryqq/playlist/" + link.id, nil
}

func (qm *QQMusicProcessor) parseQQMusicLink(raw string) (QQMusicLink, error) {
	target, err := url.Parse(raw)
	if err != nil || !validQQShareURL(target) {
		return QQMusicLink{}, ErrQQShareInvalid
	}
	if link := parseQQShareTarget(target); link.id != "" {
		return link, nil
	}

	// RoundTrip keeps the configured transport and proxy and lets us inspect
	// Location before any follow-up request, including a malformed Location.
	transport := http.DefaultTransport
	if qm.client != nil && qm.client.Transport != nil {
		transport = qm.client.Transport
	}
	start := time.Now()
	link, failure := followQQShare(transport, target)
	if failure != nil && failure.retryable {
		utils.DebugWithFormat("[QQMusic] 分享链接解析重试 域名=%s 原因=%s 耗时=%v", failure.host, failure.kind, time.Since(start).Round(time.Millisecond))
		time.Sleep(qqShareRetryDelay)
		link, failure = followQQShare(transport, target)
	}
	if failure != nil {
		utils.WarnWithFormat("[QQMusic] 分享链接解析失败 域名=%s 原因=%s 耗时=%v", failure.host, failure.kind, time.Since(start).Round(time.Millisecond))
		return QQMusicLink{}, failure.publicError
	}
	return link, nil
}

type qqShareFailure struct {
	host        string
	kind        string
	publicError error
	retryable   bool
}

func followQQShare(transport http.RoundTripper, target *url.URL) (QQMusicLink, *qqShareFailure) {
	ctx, cancel := context.WithTimeout(context.Background(), qqShareAttemptTimeout)
	defer cancel()
	visited := make(map[string]bool)
	for hops := 0; ; hops++ {
		host := target.Hostname()
		requestURL := *target
		requestURL.Fragment = ""
		address := requestURL.String()
		if hops == qqShareRedirectLimit || visited[address] {
			return QQMusicLink{}, &qqShareFailure{host: host, kind: "redirect_loop_or_limit", publicError: ErrQQShareInvalid}
		}
		visited[address] = true

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return QQMusicLink{}, &qqShareFailure{host: host, kind: "invalid_request", publicError: ErrQQShareInvalid}
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Referer", "https://y.qq.com/")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
		resp, err := transport.RoundTrip(req)
		if err != nil {
			failure := &qqShareFailure{host: host, kind: "network_error", publicError: ErrQQShareUnavailable}
			var netErr net.Error
			switch {
			case errors.As(err, &netErr) && netErr.Timeout():
				failure.kind, failure.retryable = "timeout", true
			case errors.Is(err, syscall.ECONNRESET):
				failure.kind, failure.retryable = "connection_reset", true
			}
			return QQMusicLink{}, failure
		}
		_ = resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			location := resp.Header.Get("Location")
			next, err := target.Parse(location)
			if location == "" || err != nil || !validQQShareURL(next) {
				return QQMusicLink{}, &qqShareFailure{host: host, kind: "invalid_redirect_target", publicError: ErrQQShareInvalid}
			}
			if link := parseQQShareTarget(next); link.id != "" {
				return link, nil
			}
			target = next
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return QQMusicLink{}, &qqShareFailure{host: host, kind: "upstream_unavailable", publicError: ErrQQShareUnavailable, retryable: true}
		case http.StatusTooManyRequests, http.StatusInternalServerError:
			return QQMusicLink{}, &qqShareFailure{host: host, kind: "upstream_unavailable", publicError: ErrQQShareUnavailable}
		default:
			return QQMusicLink{}, &qqShareFailure{host: host, kind: "no_track_or_playlist", publicError: ErrQQShareInvalid}
		}
	}
}

func validQQShareURL(u *url.URL) bool {
	if u == nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if port := u.Port(); port != "" && !(u.Scheme == "http" && port == "80") && !(u.Scheme == "https" && port == "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "y.qq.com" || strings.HasSuffix(host, ".y.qq.com") || host == "music.qq.com"
}

func parseQQShareTarget(u *url.URL) QQMusicLink {
	path := strings.TrimSuffix(u.Path, "/")
	query := u.Query()
	switch {
	case strings.HasPrefix(path, qqmusic.SongPagePath):
		id := strings.TrimPrefix(path, qqmusic.SongPagePath)
		if qqmusic.ValidSongMID(id) {
			return QQMusicLink{id: id, isSong: true}
		}
	case path == "/base/fcgi-bin/u", path == "/n/ryqq/songDetail", path == "/v8/playsong.html",
		path == "/n3/other/pages/playsong/index.html", path == "/n2/m/share/details/song.html":
		if mid := query.Get("songmid"); qqmusic.ValidSongMID(mid) {
			return QQMusicLink{id: mid, isSong: true}
		}
		if id := query.Get("songid"); qqmusic.ValidSongMID(id) && strings.Trim(id, "0123456789") == "" {
			return QQMusicLink{id: id, isSong: true}
		}
	case strings.HasPrefix(path, "/n/ryqq/playlist/"):
		if id := strings.TrimPrefix(path, "/n/ryqq/playlist/"); qqmusic.ValidSongMID(id) {
			return QQMusicLink{id: id}
		}
	case path == "/n/ryqq/playlist", path == "/n2/m/share/details/taoge.html":
		for _, key := range []string{"id", "disstid"} {
			if id := query.Get(key); qqmusic.ValidSongMID(id) {
				return QQMusicLink{id: id}
			}
		}
	}
	return QQMusicLink{}
}

// IsQQMusicSingleLink identifies a direct or redirected QQ single-song link.
func IsQQMusicSingleLink(rawURL string) bool {
	_, ok := QQMusicTrackID(rawURL)
	return ok
}

// QQMusicTrackID returns the provider-owned ID for a single-song URL.
func QQMusicTrackID(rawURL string) (string, bool) {
	p := &QQMusicProcessor{}
	link, err := p.parseQQMusicLink(rawURL)
	if err != nil || !link.isSong {
		return "", false
	}
	return link.id, true
}
