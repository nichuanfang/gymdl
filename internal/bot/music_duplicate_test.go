package bot

import (
	"testing"

	"github.com/nichuanfang/gymdl/internal/musicdedupe"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/processor/music"
)

func TestSuccessfulDownloadRecordDoesNotPersistSyntheticAlbumAsSourceMetadata(t *testing.T) {
	identity := musicdedupe.Track{
		Platform:         string(processor.LinkYoutubeMusic),
		PlatformTrackID:  "video-id",
		SourceURL:        "https://music.youtube.com/watch?v=video-id",
		Title:            "Song",
		Artist:           "Artist",
		MetadataResolved: true,
	}
	song := &music.SongInfo{SongName: "Song", SongArtists: "Artist", SongAlbum: "Artist Singles"}

	record := successfulDownloadRecord(processor.LinkYoutubeMusic, identity.SourceURL, identity, song, 1)
	if record.Album != "" {
		t.Fatalf("preflight album was unavailable, but synthesized file tag leaked into dedupe metadata: %q", record.Album)
	}
	if record.PlatformTrackID != "video-id" || record.SourceURL == "" {
		t.Fatalf("single-track identity not retained: %#v", record)
	}
}

func TestSuccessfulDownloadRecordUsesFileMetadataWithoutPreflight(t *testing.T) {
	identity := musicdedupe.Track{Platform: string(processor.LinkAppleMusic), PlatformTrackID: "123"}
	song := &music.SongInfo{SongName: "Song", SongArtists: "Artist", SongAlbum: "Album"}

	record := successfulDownloadRecord(processor.LinkAppleMusic, "https://music.apple.com/us/song/song/123", identity, song, 1)
	if record.Title != "Song" || record.Artist != "Artist" || record.Album != "Album" {
		t.Fatalf("expected post-download metadata fallback: %#v", record)
	}
}
