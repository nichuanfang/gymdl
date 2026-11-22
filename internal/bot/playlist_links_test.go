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
