package bot

import (
	"net/url"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/internal/netease"
	"github.com/nichuanfang/gymdl/internal/qqmusic"
)

const playlistPreviewBase = "https://webapp.example.com"
const playlistNeteaseCover = "https://p2.music.126.net/example/123.jpg?param=500y500"

func TestPlaylistAssignmentSongLinkUsesPlatformAppLinks(t *testing.T) {
	tests := []struct {
		name    string
		comment string
		want    string
	}{
		{
			name:    "QQ Music",
			comment: "来源 " + qqmusic.SongPageURL("0039MnYb0qxYhV", "Example_2"),
			want:    "https://music.qq.com/unilink/download.html?scheme=qqmusic%3A%2F%2F",
		},
		{
			name:    "NetEase Cloud Music",
			comment: netease.SongPageURL("186016", playlistNeteaseCover),
			want:    "https://m.music.163.com/m/applink/?scheme=orpheus%3A%2F%2Fsong%2F186016",
		},
		{
			name:    "unrelated source stays unchanged",
			comment: "https://example.com/music/source",
			want:    `href="https://example.com/music/source"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := playlistAssignmentTitle(unplaylistSong{Title: "试听歌曲", Comment: test.comment})
			if !strings.Contains(got, test.want) {
				t.Fatalf("playlist title = %q, want to contain %q", got, test.want)
			}
		})
	}
}

func TestPlaylistAssignmentPreviewUsesControlledPages(t *testing.T) {
	const previewPage = playlistPreviewBase + "/api/discovery/music-preview"
	tests := []struct {
		comment string
		want    string
	}{
		{netease.SongPageURL("186016", playlistNeteaseCover), previewPage + "?album=&artist=Enya&cover=" + url.QueryEscape(playlistNeteaseCover) + "&id=186016&platform=netease&title=Only+Time"},
		{qqmusic.SongPageURL("0039MnYb0qxYhV", "Example_2"), previewPage + "?album=&artist=Enya&cover=Example_2&mid=0039MnYb0qxYhV&platform=qq&title=Only+Time"},
		{"https://music.163.com/#/song?id=186016", ""},
		{"https://music.163.com/song?id=186016", ""},
		{"https://music.youtube.com/watch?v=dQw4w9WgXcQ", "https://music.youtube.com/watch?v=dQw4w9WgXcQ"},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "https://music.youtube.com/watch?v=dQw4w9WgXcQ"},
		{"来源 https://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV", ""},
		{"https://m.music.163.com/m/applink/?scheme=orpheus%3A%2F%2Fsong%2F186016", ""},
		{"", ""},
		{"not a link", ""},
	}
	for _, test := range tests {
		got := playlistAssignmentPreviewURL(unplaylistSong{Title: "Only Time", Artist: "Enya", Comment: test.comment}, playlistPreviewBase)
		if got != test.want {
			t.Fatalf("comment %q preview = %q, want %q", test.comment, got, test.want)
		}
	}
}

func TestPlaylistAssignmentPreviewWaitsForTheSongCard(t *testing.T) {
	song := unplaylistSong{ID: "song-1", Title: "Only Time", Artist: "Enya", Comment: netease.SongPageURL("186016", playlistNeteaseCover)}
	loading := pendingPlaylistAssignment{song: song, aiSuggestionLoading: true, webAppBaseURL: playlistPreviewBase}
	if got := playlistAssignmentCardPreviewURL(loading); got != "" {
		t.Fatalf("loading preview = %q, want none until the song is shown", got)
	}
	if text := formatPlaylistAssignment(loading); !strings.Contains(text, "正在生成建议") || strings.Contains(text, "Only Time") {
		t.Fatalf("loading copy = %q", text)
	}
	ready := loading
	ready.aiSuggestionLoading = false
	if got := playlistAssignmentCardPreviewURL(ready); !strings.HasPrefix(got, playlistPreviewBase+"/api/discovery/music-preview?") {
		t.Fatalf("ready preview = %q, want controlled preview page", got)
	}
	if text := formatPlaylistAssignment(ready); !strings.Contains(text, "Only Time") {
		t.Fatalf("ready card hid the song: %q", text)
	}
}
