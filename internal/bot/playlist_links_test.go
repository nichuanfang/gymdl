package bot

import (
	"strings"
	"testing"
)

func TestPlaylistAssignmentSongLinkUsesPlatformAppLinks(t *testing.T) {
	tests := []struct {
		name    string
		comment string
		want    string
	}{
		{
			name:    "QQ Music",
			comment: "来源 https://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV",
			want:    "https://music.qq.com/unilink/download.html?scheme=qqmusic%3A%2F%2F",
		},
		{
			name:    "NetEase Cloud Music",
			comment: "https://music.163.com/song?id=186016",
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

func TestPlaylistAssignmentPreviewUsesThePublicSongPage(t *testing.T) {
	tests := []struct {
		comment string
		want    string
	}{
		{"https://music.163.com/#/song?id=186016", "https://music.163.com/song?id=186016"},
		{"https://music.163.com/song?id=186016", "https://music.163.com/song?id=186016"},
		{"https://music.youtube.com/watch?v=dQw4w9WgXcQ", "https://music.youtube.com/watch?v=dQw4w9WgXcQ"},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "https://music.youtube.com/watch?v=dQw4w9WgXcQ"},
		{"来源 https://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV", "https://y.qq.com/n/ryqq/songDetail/0039MnYb0qxYhV"},
		{"https://m.music.163.com/m/applink/?scheme=orpheus%3A%2F%2Fsong%2F186016", ""},
		{"", ""},
		{"not a link", ""},
	}
	for _, test := range tests {
		got := playlistAssignmentPreviewURL(unplaylistSong{Title: "Only Time", Comment: test.comment})
		if got != test.want {
			t.Fatalf("comment %q preview = %q, want %q", test.comment, got, test.want)
		}
	}
}

func TestPlaylistAssignmentPreviewWaitsForTheSongCard(t *testing.T) {
	song := unplaylistSong{ID: "song-1", Title: "Only Time", Artist: "Enya", Comment: "https://music.163.com/song?id=186016"}
	loading := pendingPlaylistAssignment{song: song, aiSuggestionLoading: true}
	if got := playlistAssignmentCardPreviewURL(loading); got != "" {
		t.Fatalf("loading preview = %q, want none until the song is shown", got)
	}
	if text := formatPlaylistAssignment(loading); !strings.Contains(text, "正在生成建议") || strings.Contains(text, "Only Time") {
		t.Fatalf("loading copy = %q", text)
	}
	ready := loading
	ready.aiSuggestionLoading = false
	if got, want := playlistAssignmentCardPreviewURL(ready), "https://music.163.com/song?id=186016"; got != want {
		t.Fatalf("ready preview = %q, want %q", got, want)
	}
	if text := formatPlaylistAssignment(ready); !strings.Contains(text, "Only Time") {
		t.Fatalf("ready card hid the song: %q", text)
	}
}
