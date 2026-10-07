package musicdedupe

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSuccessfulDownloadStoreStartsEmptyAndMatchesAllIdentities(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "music.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	track := Track{
		Platform:        "QQ音乐",
		PlatformTrackID: "MUSIC-MID-1",
		SourceURL:       "https://y.qq.com/n/ryqq/songDetail/MUSIC-MID-1?ADTAG=share",
		Title:           "Human World",
		Artist:          "Faye Wong",
		Album:           "Fable",
	}
	if match, err := store.FindDuplicate(ctx, track); err != nil || match != nil {
		t.Fatalf("new store should not contain historical data: match=%#v err=%v", match, err)
	}
	if err := store.RecordSuccessfulDownload(ctx, track); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		in   Track
		by   string
	}{
		{
			name: "platform id",
			in:   Track{Platform: "QQ音乐", PlatformTrackID: "MUSIC-MID-1"},
			by:   "platform_id",
		},
		{
			name: "url",
			in:   Track{SourceURL: "https://y.qq.com/n/ryqq/songDetail/MUSIC-MID-1?ADTAG=other"},
			by:   "url",
		},
		{
			name: "normalized metadata",
			in:   Track{Title: " human   world ", Artist: "FAYE WONG", Album: "Fable"},
			by:   "metadata",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			match, err := store.FindDuplicate(ctx, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if match == nil || match.MatchedBy != tc.by {
				t.Fatalf("expected match by %q, got %#v", tc.by, match)
			}
		})
	}
}

func TestMetadataRequiresCompleteSongArtistAlbumTuple(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "music.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.RecordSuccessfulDownload(ctx, Track{Title: "Same", Artist: "Artist", Album: "Album"}); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []Track{
		{Title: "Same", Artist: "Artist"},
		{Title: "Same", Artist: "Artist", Album: "Different"},
		{Title: "Different", Artist: "Artist", Album: "Album"},
	} {
		match, err := store.FindDuplicate(ctx, candidate)
		if err != nil {
			t.Fatal(err)
		}
		if match != nil {
			t.Fatalf("incomplete or mismatched metadata should not match: %#v", match)
		}
	}
}

func TestCanonicalURLNormalizesYouTubeAndTrackingParameters(t *testing.T) {
	first := CanonicalURL("https://music.youtube.com/watch?v=abc123&list=PL1&si=tracking")
	second := CanonicalURL("https://youtu.be/abc123?feature=share")
	if first != second {
		t.Fatalf("equivalent YouTube track URLs differ: %q != %q", first, second)
	}
}

func TestImportMusicRAGOnceImportsLegacyMetadataAndURLWithoutPlatformIDs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	legacy := []LegacyTrack{
		{Title: "Legacy Song", Artist: "Legacy Artist", Album: "Legacy Album"},
		{Title: "URL Only", URL: "https://youtu.be/legacy123?feature=share"},
		{Title: "", Artist: "", Album: ""},
	}

	stats, err := store.ImportMusicRAGOnce(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 3 || stats.Imported != 2 || stats.Skipped != 1 || stats.AlreadyImported {
		t.Fatalf("unexpected first import stats: %#v", stats)
	}

	metadataMatch, err := store.FindDuplicate(ctx, Track{Title: "Legacy Song", Artist: "Legacy Artist", Album: "Legacy Album"})
	if err != nil {
		t.Fatal(err)
	}
	if metadataMatch == nil || metadataMatch.MatchedBy != "metadata" {
		t.Fatalf("legacy metadata tuple should match: %#v", metadataMatch)
	}
	if metadataMatch.Platform != "" || metadataMatch.PlatformTrackID != "" {
		t.Fatalf("legacy import must not invent platform identity: %#v", metadataMatch.Track)
	}
	if metadataMatch.RecordSource != "legacy_import" || !metadataMatch.Downloaded.IsZero() || metadataMatch.Recorded.IsZero() {
		t.Fatalf("legacy timestamps/source should distinguish unknown download time: %#v", metadataMatch)
	}

	urlMatch, err := store.FindDuplicate(ctx, Track{SourceURL: "https://www.youtube.com/watch?v=legacy123&list=PL1"})
	if err != nil {
		t.Fatal(err)
	}
	if urlMatch == nil || urlMatch.MatchedBy != "url" || urlMatch.PlatformTrackID != "" {
		t.Fatalf("legacy URL should match without a platform ID: %#v", urlMatch)
	}

	retry, err := store.ImportMusicRAGOnce(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.AlreadyImported || retry.Imported != 0 {
		t.Fatalf("repeat import should be a no-op: %#v", retry)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM downloaded_tracks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("repeat import duplicated rows: count=%d", count)
	}
}

func TestDecodeLegacyExportArrayAndN8NEnvelope(t *testing.T) {
	for _, input := range []string{
		`[{"title":"Song","artist":"Artist","album":"Album","music_id":"not-a-platform-id"}]`,
		`{"rows":[{"title":"Song","artist":"Artist","album":"Album","music_id":"not-a-platform-id"}],"count":1}`,
	} {
		rows, err := DecodeLegacyExport([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Title != "Song" || rows[0].Artist != "Artist" || rows[0].Album != "Album" {
			t.Fatalf("unexpected decoded export: %#v", rows)
		}
	}
	if _, err := DecodeLegacyExport([]byte(`{"rows":[{"title":"Song","artist":"Artist","album":"Album"}],"count":1078}`)); err == nil {
		t.Fatal("partial n8n page must not be accepted as a complete export")
	}
}

func TestOpenMigratesPreviousDownloadTableAndKeepsDownloadTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gymdl.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE downloaded_tracks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	platform TEXT NOT NULL DEFAULT '',
	platform_track_id TEXT NOT NULL DEFAULT '',
	source_url TEXT NOT NULL DEFAULT '',
	canonical_url TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	artist TEXT NOT NULL DEFAULT '',
	album TEXT NOT NULL DEFAULT '',
	title_key TEXT NOT NULL DEFAULT '',
	artist_key TEXT NOT NULL DEFAULT '',
	album_key TEXT NOT NULL DEFAULT '',
	downloaded_at TEXT NOT NULL
);
INSERT INTO downloaded_tracks (platform, platform_track_id, title, artist, album, title_key, artist_key, album_key, downloaded_at)
VALUES ('qq音乐', 'old-id', 'Song', 'Artist', 'Album', 'song', 'artist', 'album', '2026-10-01T00:00:00Z');`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	match, err := store.FindDuplicate(context.Background(), Track{Platform: "QQ音乐", PlatformTrackID: "old-id"})
	if err != nil {
		t.Fatal(err)
	}
	if match == nil || match.RecordSource != "telegram" || match.Downloaded.IsZero() || match.Recorded.IsZero() {
		t.Fatalf("old successful row was not preserved as a Telegram record: %#v", match)
	}
}
