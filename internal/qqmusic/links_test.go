package qqmusic

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestSongUniversalLinkEncodesQQMusicHandoff(t *testing.T) {
	link, ok := SongUniversalLink("001kG0Ys4JRmMa")
	if !ok {
		t.Fatal("valid song MID rejected")
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse Universal Link: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "music.qq.com" || parsed.Path != "/unilink/download.html" {
		t.Fatalf("Universal Link = %q", link)
	}
	schemeURL, err := url.Parse(parsed.Query().Get("scheme"))
	if err != nil {
		t.Fatalf("parse QQ scheme: %v", err)
	}
	if schemeURL.Scheme != "qqmusic" || schemeURL.Host != "qq.com" || schemeURL.Path != "/media/playSonglist" {
		t.Fatalf("QQ scheme = %q", schemeURL.String())
	}
	var payload songOpenPayload
	if err := json.Unmarshal([]byte(schemeURL.Query().Get("p")), &payload); err != nil {
		t.Fatalf("decode QQ payload: %v", err)
	}
	if payload.Action != "play" || payload.HideLoading != "1" || len(payload.Song) != 1 || payload.Song[0].SongMID != "001kG0Ys4JRmMa" || payload.Song[0].Type != 0 {
		t.Fatalf("QQ payload = %+v", payload)
	}

	if _, ok := SongUniversalLink("bad/mid"); ok {
		t.Fatal("invalid song MID accepted")
	}
}

func TestAlbumCoverURLAcceptsPMIDVariantAndFallsBackSafely(t *testing.T) {
	const prefix = "https://y.gtimg.cn/music/photo_new/T002R300x300M000"
	if got, want := AlbumCoverURL(" 002ef2VB0Qfb30_2 ", "002ef2VB0Qfb30"), prefix+"002ef2VB0Qfb30_2.jpg"; got != want {
		t.Fatalf("cover URL = %q, want %q", got, want)
	}
	if got, want := AlbumCoverURL("../../evil", "002ef2VB0Qfb30"), prefix+"002ef2VB0Qfb30.jpg"; got != want {
		t.Fatalf("fallback cover URL = %q, want %q", got, want)
	}
	if got := AlbumCoverURL("../../evil", ""); got != "" {
		t.Fatalf("invalid cover URL = %q, want empty", got)
	}
}

func TestValidAlbumCoverID(t *testing.T) {
	for _, value := range []string{"002ef2VB0Qfb30", "002ef2VB0Qfb30_2", "A9_123"} {
		if !ValidAlbumCoverID(value) {
			t.Errorf("ValidAlbumCoverID(%q) = false", value)
		}
	}
	for _, value := range []string{"", "_2", "abc_", "abc_2_3", "abc_2x", "../abc", "abc?x=y"} {
		if ValidAlbumCoverID(value) {
			t.Errorf("ValidAlbumCoverID(%q) = true", value)
		}
	}
}
