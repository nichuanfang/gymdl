package musicdedupe

import (
	"testing"

	"github.com/nichuanfang/gymdl/processor"
)

func TestIdentityFromSingleTrackLinks(t *testing.T) {
	track := IdentityFromLink(processor.LinkYoutubeMusic, "https://music.youtube.com/watch?v=abc123&list=PL1")
	if track.PlatformTrackID != "abc123" || track.SourceURL != "https://music.youtube.com/watch?v=abc123&list=PL1" {
		t.Fatalf("unexpected YouTube track identity: %#v", track)
	}
	if !IsSingleTrackLink(processor.LinkYoutubeMusic, "https://music.youtube.com/watch?v=abc123&list=PL1") {
		t.Fatal("watch URL with a video ID should be treated as a single track")
	}
}

func TestPlaylistLinksDoNotBecomeSingleTrackIdentity(t *testing.T) {
	if IsSingleTrackLink(processor.LinkAppleMusic, "https://music.apple.com/us/album/example/12345") {
		t.Fatal("album URL must not be treated as a single track")
	}
	track := IdentityFromLink(processor.LinkAppleMusic, "https://music.apple.com/us/album/example/12345")
	if track.PlatformTrackID != "" || track.SourceURL != "" {
		t.Fatalf("playlist/album identity should not be persisted as one track: %#v", track)
	}
}
