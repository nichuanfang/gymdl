package web

import "testing"

func TestQQAlbumCoverURLKeepsPMIDVariantSuffix(t *testing.T) {
	got := qqAlbumCoverURL("002ef2VB0Qfb30_2", "002ef2VB0Qfb30")
	want := "https://y.gtimg.cn/music/photo_new/T002R300x300M000002ef2VB0Qfb30_2.jpg"
	if got != want {
		t.Fatalf("QQ album cover URL = %q, want %q", got, want)
	}
}

func TestQQAlbumCoverURLFallsBackToValidatedMID(t *testing.T) {
	got := qqAlbumCoverURL("../../invalid", "002ef2VB0Qfb30")
	want := "https://y.gtimg.cn/music/photo_new/T002R300x300M000002ef2VB0Qfb30.jpg"
	if got != want {
		t.Fatalf("QQ album cover URL fallback = %q, want %q", got, want)
	}
	if got := qqAlbumCoverURL("../../invalid", ""); got != "" {
		t.Fatalf("invalid QQ album cover URL = %q, want empty", got)
	}
}
