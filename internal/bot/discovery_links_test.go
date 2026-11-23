package bot

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/discovery"
	tb "gopkg.in/telebot.v4"
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

func TestDiscoveryCardPreviewUsesNetEaseSongPage(t *testing.T) {
	card := discovery.Card{Kind: discovery.TasteCardKind, Tracks: []discovery.Track{{
		Platform: "netease", ID: "186016", URL: "https://music.163.com/song?id=186016",
		Title: "晴天", Artist: "周杰伦", Album: "叶惠美", Duration: 269,
	}}}
	if got, want := discoveryCardPreviewURL(card, 0, ""), "https://music.163.com/song?id=186016"; got != want {
		t.Fatalf("NetEase card preview URL = %q, want %q", got, want)
	}
	for _, id := range []string{"", "0", "-1", "0186016", "186016&next=https://attacker.example"} {
		card.Tracks[0].ID = id
		if got := discoveryCardPreviewURL(card, 0, ""); got != "" {
			t.Errorf("invalid NetEase id %q produced preview URL %q", id, got)
		}
	}
	card.Kind = "rediscover"
	card.Tracks[0].ID = "186016"
	if got := discoveryCardPreviewURL(card, 0, ""); got != "" {
		t.Fatalf("non-personalized NetEase card preview URL = %q, want empty", got)
	}
}

func TestFreshFavoritePreviewUsesCanonicalNetEaseSongPage(t *testing.T) {
	app := &BotApp{cfg: &config.Config{}}
	tracks := []discovery.Track{
		{Platform: "netease", ID: "not-an-id", Title: "坏链接"},
		{Platform: "netease", ID: "186016", Title: "晴天", Artist: "周杰伦"},
	}
	if got, want := freshFavoritePreviewURL(app, tracks), "https://music.163.com/song?id=186016"; got != want {
		t.Fatalf("fresh favorite preview URL = %q, want %q", got, want)
	}
}

type freshNoticeAPIFake struct {
	tb.API
	method  string
	payload any
}

func (f *freshNoticeAPIFake) Raw(method string, payload interface{}) ([]byte, error) {
	f.method, f.payload = method, payload
	return []byte(`{"ok":true}`), nil
}

func TestFreshNoticeSetsTelegramPreviewURL(t *testing.T) {
	api := &freshNoticeAPIFake{}
	if err := sendDiscoveryFreshNotice(api, 42, "<b>收藏有新歌</b>", "https://music.163.com/song?id=186016"); err != nil {
		t.Fatal(err)
	}
	if api.method != "sendMessage" {
		t.Fatalf("Telegram method = %q", api.method)
	}
	payload, ok := api.payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", api.payload)
	}
	preview, ok := payload["link_preview_options"].(map[string]any)
	if !ok || preview["url"] != "https://music.163.com/song?id=186016" || preview["prefer_large_media"] != true {
		t.Fatalf("link preview options = %#v", payload["link_preview_options"])
	}
	if payload["chat_id"] != int64(42) || payload["parse_mode"] != string(tb.ModeHTML) {
		t.Fatalf("send payload = %#v", payload)
	}
	if _, err := json.Marshal(payload); err != nil {
		t.Fatalf("payload is not JSON encodable: %v", err)
	}
}
