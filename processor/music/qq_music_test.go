package music

import (
	"encoding/json"
	"testing"
)

func TestQQSongDetailSupportsCurrentAndLegacyTrackFields(t *testing.T) {
	var current QQSongDetail
	if err := json.Unmarshal([]byte(`{"track":{"mid":"current"}}`), &current); err != nil {
		t.Fatal(err)
	}
	if current.TrackInfo.Mid != "current" {
		t.Fatalf("current track mid = %q", current.TrackInfo.Mid)
	}

	var legacy QQSongDetail
	if err := json.Unmarshal([]byte(`{"track_info":{"mid":"legacy"}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.LegacyTrackInfo.Mid != "legacy" {
		t.Fatalf("legacy track mid = %q", legacy.LegacyTrackInfo.Mid)
	}
}

func TestQQURLSupportsCurrentAndLegacyItems(t *testing.T) {
	var current QQURL
	if err := json.Unmarshal([]byte(`{"data":[{"purl":"/current"}]}`), &current); err != nil {
		t.Fatal(err)
	}
	if got := current.urlInfos()[0].Purl; got != "/current" {
		t.Fatalf("current purl = %q", got)
	}

	var legacy QQURL
	if err := json.Unmarshal([]byte(`{"midurlinfo":[{"purl":"/legacy"}]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if got := legacy.urlInfos()[0].Purl; got != "/legacy" {
		t.Fatalf("legacy purl = %q", got)
	}
}

func TestQQPlaylistSupportsCurrentAndLegacySongs(t *testing.T) {
	var current QQPlaylist
	if err := json.Unmarshal([]byte(`{"songs":[{"mid":"current"}]}`), &current); err != nil {
		t.Fatal(err)
	}
	if got := current.songs()[0].Mid; got != "current" {
		t.Fatalf("current song mid = %q", got)
	}

	var legacy QQPlaylist
	if err := json.Unmarshal([]byte(`{"songlist":[{"mid":"legacy"}]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if got := legacy.songs()[0].Mid; got != "legacy" {
		t.Fatalf("legacy song mid = %q", got)
	}
}

func TestQQApiResponseSupportsCurrentErrorMessage(t *testing.T) {
	var response QQApiResponse[struct{}]
	if err := json.Unmarshal([]byte(`{"code":-1,"msg":"请求失败"}`), &response); err != nil {
		t.Fatal(err)
	}
	if got := response.errorMessage(); got != "请求失败" {
		t.Fatalf("error message = %q", got)
	}
}

func TestQQDuetArtistsAgreeAcrossPreflightAndFilenames(t *testing.T) {
	var song QQSong
	if err := json.Unmarshal([]byte(`{"title":"Song","singer":[{"name":"甲"},{"name":"乙"}],"album":{"name":"Album"}}`), &song); err != nil {
		t.Fatal(err)
	}
	if got := qqSongArtists(song); got != "甲 / 乙" {
		t.Fatal("duet lost credited artist", got)
	}
	p := &QQMusicProcessor{}
	if p.safeFileName(song.Title, qqSongArtists(song), "flac") == p.safeFileName(song.Title, song.Singer[0].Name, "flac") {
		t.Fatal("duet filename collapsed into solo recording")
	}
}
