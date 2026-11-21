package audioquality

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInspectRealAudioFixtures(t *testing.T) {
	for _, tool := range []string{"ffprobe", "ffmpeg"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("requires " + tool)
		}
	}
	dir := t.TempDir()
	build := func(name string, args ...string) string {
		t.Helper()
		file := filepath.Join(dir, name)
		cmd := exec.CommandContext(t.Context(), "ffmpeg", append(append([]string{
			"-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=4",
			"-metadata", "title=Test Song", "-metadata", "artist=Test Artist",
		}, args...), file)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("encode %s: %v %s", file, err, output)
		}
		return file
	}
	low := build("low.mp3", "-ac", "2", "-c:a", "libmp3lame", "-b:a", "96k")
	high := build("high.mp3", "-ac", "2", "-c:a", "libmp3lame", "-b:a", "320k")
	opus := build("low.opus", "-c:a", "libopus", "-b:a", "48k", "-vbr", "off")
	aac := build("low.m4a", "-c:a", "aac", "-b:a", "48k")
	full := build("full.flac", "-c:a", "flac")
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(dir, "truncated.flac")
	if err := os.WriteFile(truncated, data[:len(data)*2/3], 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, file, codec string
		low, damaged      bool
	}{
		{"MP3 low", low, "mp3", true, false},
		{"MP3 healthy", high, "mp3", false, false},
		{"Opus packet bitrate", opus, "opus", true, false},
		{"AAC in MP4", aac, "aac", true, false},
		{"FLAC healthy", full, "flac", false, false},
		{"FLAC truncated", truncated, "flac", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Inspect(t.Context(), tc.file)
			if err != nil || result.Codec != tc.codec || result.Damaged != tc.damaged || LowBitrate(result.Codec, result.Bitrate) != tc.low {
				t.Fatalf("unexpected result: %#v err=%v", result, err)
			}
			if !tc.damaged && (result.Decoded < 3.9 || result.Title != "Test Song" || result.Artist != "Test Artist") {
				t.Fatalf("did not read full file or usable tags: %#v", result)
			}
		})
	}
	validShort, err := Inspect(t.Context(), full)
	if err != nil || validShort.NeedsReview() {
		t.Fatalf("a short playable file needs trustworthy comparison: %#v, %v", validShort, err)
	}
	validShort.Reference = 40
	if !validShort.ShorterThanSource() || validShort.Damaged {
		t.Fatalf("a cleanly encoded clip should be suspicious with a reference: %#v", validShort)
	}
}

func TestReviewIsSpecificToUserAndFileVersion(t *testing.T) {
	ctx := t.Context()
	db := filepath.Join(t.TempDir(), "gymdl.db")
	store, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	item := CachedResult{FileVersion: FileVersion{Path: "song.mp3", Size: 10, Mod: 1}, Result: Result{Codec: "mp3", Bitrate: 96}}
	if err := store.Save(ctx, "library", item); err != nil {
		t.Fatal(err)
	}
	marked, err := store.MarkNormal(ctx, "library", 7, item.FileVersion)
	if err != nil || !marked {
		t.Fatalf("could not record review: %t %v", marked, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, user := range []int64{7, 8} {
		cached, err := store.Results(ctx, "library", user)
		if err != nil || cached[item.Path].Reviewed != (user == 7) {
			t.Fatalf("review lost or shown to another user: %#v, %v", cached, err)
		}
	}
	old := item.FileVersion
	item.Mod++
	if err := store.Save(ctx, "library", item); err != nil {
		t.Fatal(err)
	}
	marked, err = store.MarkNormal(ctx, "library", 7, old)
	if err != nil || marked {
		t.Fatalf("obsolete file version accepted: %t %v", marked, err)
	}
	cached, err := store.Results(ctx, "library", 7)
	if err != nil || cached[item.Path].Reviewed {
		t.Fatalf("updated file inherited stale review: %#v %v", cached, err)
	}
}
