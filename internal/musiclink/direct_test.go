package musiclink

import (
	"net/url"
	"testing"
)

func TestDirectAppLinkWithCover(t *testing.T) {
	const qq = "https://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV"
	const netease = "https://music.163.com/song?id=186016"
	tests := []struct {
		page, withCover string
	}{
		{qq, qq + "?cover=Example_2"},
		{netease, netease + "&cover=" + url.QueryEscape("https://p2.music.126.net/example/123.jpg?param=500y500")},
	}
	for _, test := range tests {
		want := DirectAppLink(test.page)
		if got := DirectAppLink(test.withCover); want == test.page || got != want {
			t.Errorf("DirectAppLink(%q) = %q, want app link %q", test.withCover, got, want)
		}
	}
	for _, raw := range []string{
		qq + "?cover=Example_2&cover=Other", qq + "?cover=../invalid", qq + "?cover=Example&next=elsewhere",
		netease + "&id=123", netease + "&cover=" + url.QueryEscape("https://evil.example/123.jpg"),
	} {
		if got := DirectAppLink(raw); got != raw {
			t.Errorf("DirectAppLink(%q) changed an invalid song URL to %q", raw, got)
		}
	}
}
