package music

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"go.senan.xyz/taglib"
)

func TestWriteTagsEmbedsPublicSourceAndSquareCover(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is needed to generate the test audio file")
	}
	dir := t.TempDir()
	coverFile := filepath.Join(dir, "cover.png")
	var cover bytes.Buffer
	if err := png.Encode(&cover, image.NewRGBA(image.Rect(0, 0, 640, 320))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coverFile, cover.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		source string
	}{
		{"public-source", "https://music.163.com/song?id=186016"},
		{"no-source", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			audioFile := filepath.Join(dir, tc.name+".flac")
			if out, err := exec.Command(ffmpeg, "-loglevel", "error", "-f", "lavfi", "-i", "anullsrc", "-t", "0.1", audioFile).CombinedOutput(); err != nil {
				t.Fatalf("create test audio: %v: %s", err, out)
			}
			song := &SongInfo{SongName: "Test Song", SourceURL: tc.source, Url: "https://media.example.com/song.flac?token=private"}
			summary, err := json.Marshal(song)
			if err != nil || bytes.Contains(summary, []byte(song.Url)) {
				t.Fatalf("song summary exposed the media URL or could not be encoded: %v", err)
			}
			if err := WriteTagsWithCoverFile(song, audioFile, coverFile); err != nil {
				t.Fatal(err)
			}
			tags, err := taglib.ReadTags(audioFile)
			if err != nil {
				t.Fatal(err)
			}
			comment := tags[taglib.Comment]
			if tc.source == "" {
				if len(comment) != 0 {
					t.Fatalf("unexpected comment: %v", comment)
				}
			} else if len(comment) != 1 || comment[0] != tc.source {
				t.Fatalf("comment = %v, want %q", comment, tc.source)
			}
			info, err := ReadTags(audioFile)
			if err != nil || info.SourceURL != tc.source {
				t.Fatalf("ReadTags() = %+v, %v", info, err)
			}
			embedded, err := taglib.ReadImage(audioFile)
			if err != nil {
				t.Fatal(err)
			}
			size, _, err := image.DecodeConfig(bytes.NewReader(embedded))
			if err != nil || size.Width != 500 || size.Height != 500 {
				t.Fatalf("embedded cover = %dx%d, %v", size.Width, size.Height, err)
			}
		})
	}
}
