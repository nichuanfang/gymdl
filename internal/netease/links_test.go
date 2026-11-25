package netease

import "testing"

func TestCoverURL(t *testing.T) {
	const cover = "https://p2.music.126.net/ZGffiDQZrGj5s_hnR1CNbg==/109951165566379710.jpg"
	const resized = cover + "?param=500y500"
	for _, raw := range []string{cover, resized, "https://P2.music.126.net:443/ZGffiDQZrGj5s_hnR1CNbg==/109951165566379710.jpg"} {
		if got := CoverURL(raw); got != resized {
			t.Errorf("CoverURL(%q) = %q, want %q", raw, got, resized)
		}
	}
	for _, raw := range []string{
		"http://p2.music.126.net/cover.jpg",
		"https://user@evil.example/cover.jpg?param=500y500",
		"https://user@p2.music.126.net/cover.jpg",
		"https://p2.music.126.net.evil.example/cover.jpg",
		"https://p2.music.126.net:8443/cover.jpg",
		cover + "#fragment",
		cover + "?next=elsewhere",
		cover + "?param=800y800",
		resized + "&param=500y500",
	} {
		if got := CoverURL(raw); got != "" {
			t.Errorf("CoverURL(%q) did not reject invalid input: %q", raw, got)
		}
	}
	if got := CoverURLFromPictureID(109951165566379710); got != resized {
		t.Errorf("cover from album picture ID = %q, want %q", got, resized)
	}
}
