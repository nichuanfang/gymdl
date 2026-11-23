package bot

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/internal/discovery"
)

func TestDiscoveryListenURLUsesPlatformAppLinks(t *testing.T) {
	got := discoveryListenURL("https://music.163.com/song?id=186016")
	if want := "https://m.music.163.com/m/applink/?scheme=orpheus%3A%2F%2Fsong%2F186016"; got != want {
		t.Fatalf("NetEase app link = %q, want %q", got, want)
	}

	got = discoveryListenURL("https://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse QQ Universal Link: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "music.qq.com" || parsed.Path != "/unilink/download.html" {
		t.Fatalf("QQ Universal Link = %q", got)
	}
	appScheme := parsed.Query().Get("scheme")
	if !strings.HasPrefix(appScheme, "qqmusic://qq.com/media/playSonglist?") {
		t.Fatalf("QQ app scheme = %q", appScheme)
	}
	schemeURL, err := url.Parse(appScheme)
	if err != nil {
		t.Fatalf("parse QQ app scheme: %v", err)
	}
	var payload struct {
		Action      string `json:"action"`
		HideLoading string `json:"hideLoading"`
		Song        []struct {
			SongMID string `json:"songmid"`
			Type    int    `json:"type"`
		} `json:"song"`
	}
	if err := json.Unmarshal([]byte(schemeURL.Query().Get("p")), &payload); err != nil {
		t.Fatalf("decode QQ song payload: %v", err)
	}
	if payload.Action != "play" || payload.HideLoading != "1" || len(payload.Song) != 1 || payload.Song[0].SongMID != "0039MnYb0qxYhV" || payload.Song[0].Type != 0 {
		t.Fatalf("QQ song payload = %+v", payload)
	}
}

func TestDiscoveryListenURLLeavesUnsupportedOrUnsafeLinksAlone(t *testing.T) {
	for _, raw := range []string{
		"https://y.qq.com/n/ryqq/songDetail/invalid-mid?next=https://attacker.example",
		"https://y.qq.com/n/ryqq/songDetail/../settings",
		"https://y.qq.com:8443/n/ryqq/songDetail/0039MnYb0qxYhV",
		"https://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV?",
		"http://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV",
		"https://music.163.com/song?id=not-a-number",
		"https://music.youtube.com/watch?v=abc123",
	} {
		if got := discoveryListenURL(raw); got != raw {
			t.Errorf("discoveryListenURL(%q) = %q, want original URL", raw, got)
		}
	}
}

func TestDiscoveryCardPreviewUsesYouTubeWatchPage(t *testing.T) {
	card := discovery.Card{Tracks: []discovery.Track{{
		Platform: "youtube", ID: "dQw4w9WgXcQ", Title: "示例", Artist: "艺人",
		URL: "https://music.youtube.com/watch?v=dQw4w9WgXcQ",
	}}}
	if got, want := discoveryCardPreviewURL(card, 0, "https://webapp.example.com"), "https://music.youtube.com/watch?v=dQw4w9WgXcQ"; got != want {
		t.Fatalf("YouTube preview = %q, want %q", got, want)
	}
	card.Tracks[0].ID = "短"
	if got := discoveryCardPreviewURL(card, 0, "https://webapp.example.com"); got != "" {
		t.Fatalf("short YouTube id preview = %q", got)
	}
}

func TestDiscoveryCardPreviewUsesNeteaseSongPage(t *testing.T) {
	card := discovery.Card{Tracks: []discovery.Track{{
		Platform: "netease", ID: "186016", Title: "我的回忆不是我的", Artist: "张悬",
		URL: "https://music.163.com/song?id=186016",
	}}}
	if got, want := discoveryCardPreviewURL(card, 0, ""), "https://music.163.com/song?id=186016"; got != want {
		t.Fatalf("NetEase preview = %q, want %q", got, want)
	}
	card.Tracks[0].ID = "186016 "
	if got := discoveryCardPreviewURL(card, 0, ""); got != "" {
		t.Fatalf("unsafe NetEase id preview = %q", got)
	}
	if got := discoveryLinkPreview(""); got["is_disabled"] != true {
		t.Fatalf("empty preview options = %#v", got)
	}
	options := discoveryLinkPreview("https://music.163.com/song?id=186016")
	if options["is_disabled"] == true || options["url"] != "https://music.163.com/song?id=186016" {
		t.Fatalf("NetEase preview was disabled: %#v", options)
	}
}

func TestDiscoveryCardPreviewAcceptsQQAlbumPMIDVariant(t *testing.T) {
	const coverURL = "https://y.gtimg.cn/music/photo_new/T002R300x300M000002ef2VB0Qfb30_2.jpg"
	card := discovery.Card{Tracks: []discovery.Track{{
		Platform: "qq", ID: "001kG0Ys4JRmMa", CoverURL: coverURL,
		Title: "Get Up", Artist: "阿肆", Album: "预谋邂逅", Duration: 200,
	}}}
	got := discoveryCardPreviewURL(card, 0, "https://WEBAPP.example.com:443")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse QQ card preview URL: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "webapp.example.com" || parsed.Path != "/api/discovery/music-preview" {
		t.Fatalf("QQ card preview URL = %q", got)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"mid": "001kG0Ys4JRmMa", "cover": "002ef2VB0Qfb30_2", "title": "Get Up",
		"artist": "阿肆", "album": "预谋邂逅", "duration": "200",
	} {
		if query.Get(key) != want {
			t.Errorf("preview query %s = %q, want %q", key, query.Get(key), want)
		}
	}

	card.Tracks[0].CoverURL = "https://y.gtimg.cn/music/photo_new/T002R300x300M000abc_2_bad.jpg"
	if got := discoveryCardPreviewURL(card, 0, "https://webapp.example.com"); got != "" {
		t.Fatalf("malformed QQ cover preview = %q, want empty", got)
	}
	card.Tracks[0].CoverURL = coverURL
	card.Tracks[0].Title = "   "
	if got := discoveryCardPreviewURL(card, 0, "https://webapp.example.com"); got != "" {
		t.Fatalf("preview without a title = %q, want empty", got)
	}
	card.Tracks[0].Title = "Get Up"
	if got := discoveryCardPreviewURL(card, 0, "http://webapp.example.com"); got != "" {
		t.Fatalf("insecure preview origin = %q, want empty", got)
	}
}
