package bot

import (
	"net/url"
	"testing"
)

func TestDiscoveryListenURLUsesPlatformAppLinks(t *testing.T) {
	got := discoveryListenURL("https://music.163.com/song?id=186016", "https://webapp.example.com")
	if want := "https://m.music.163.com/m/applink/?scheme=orpheus%3A%2F%2Fsong%2F186016"; got != want {
		t.Fatalf("NetEase app link = %q, want %q", got, want)
	}

	got = discoveryListenURL("https://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV", "https://WEBAPP.example.com:443")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse QQ smart link: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "webapp.example.com" || parsed.Path != qqMusicLinkPath || parsed.Query().Get("mid") != "0039MnYb0qxYhV" {
		t.Fatalf("QQ smart link = %q", got)
	}
}

func TestDiscoveryListenURLLeavesUnsupportedOrUnsafeLinksAlone(t *testing.T) {
	for _, raw := range []string{
		"https://y.qq.com/n/ryqq/songDetail/invalid-mid?next=https://attacker.example",
		"https://y.qq.com/n/ryqq/songDetail/../settings",
		"http://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV",
		"https://music.163.com/song?id=not-a-number",
		"https://music.youtube.com/watch?v=abc123",
	} {
		if got := discoveryListenURL(raw, "https://webapp.example.com"); got != raw {
			t.Errorf("discoveryListenURL(%q) = %q, want original URL", raw, got)
		}
	}

	qq := "https://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV"
	if got := discoveryListenURL(qq, "http://webapp.example.com"); got != qq {
		t.Fatalf("QQ link with an unsafe public origin = %q, want original URL", got)
	}
}
