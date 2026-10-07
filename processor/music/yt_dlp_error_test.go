package music

import (
	"errors"
	"strings"
	"testing"
)

func TestNewYTDLPFailureFormatsUnavailableMessage(t *testing.T) {
	cause := errors.New("exit status 1")
	err := newYTDLPFailure("yt-dlp 下载失败", cause, "ERROR: [youtube] Requested format is not available. Use --list-formats")

	if !strings.Contains(err.Error(), "当前下载设置下没有可用的音频格式") {
		t.Fatalf("expected a user-friendly unavailable-format message, got %q", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("expected the original process error to remain unwrap-able")
	}
}

func TestNewYTDLPFailureIncludesUsefulDownloaderDetail(t *testing.T) {
	err := newYTDLPFailure("yt-dlp 下载失败", errors.New("exit status 1"), "[download] 100%\nERROR: [youtube] Video unavailable")

	if !strings.Contains(err.Error(), "Video unavailable") {
		t.Fatalf("expected yt-dlp error detail, got %q", err)
	}
	if strings.Contains(err.Error(), "[download] 100%") {
		t.Fatalf("progress output should not replace the error detail, got %q", err)
	}
}
