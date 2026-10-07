package music

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nichuanfang/gymdl/config"
)

type ytDLPMusicMetadata struct {
	Title        string `json:"title"`
	Track        string `json:"track"`
	Artist       string `json:"artist"`
	Creator      string `json:"creator"`
	Uploader     string `json:"uploader"`
	Album        string `json:"album"`
	Ext          string `json:"ext"`
	ExtractorKey string `json:"extractor_key"`
	ID           string `json:"id"`
}

// ResolveYTDLPMusicMetadata reads single-track metadata without downloading
// media. It is used only for pre-download duplicate checks.
func ResolveYTDLPMusicMetadata(cfg *config.Config, rawURL string) (*SongInfo, error) {
	if cfg == nil || cfg.CookieCloud == nil {
		return nil, errors.New("CookieCloud 配置不可用")
	}
	cookiePath := filepath.Join(cfg.CookieCloud.CookieFilePath, cfg.CookieCloud.CookieFile)
	if _, err := os.Stat(cookiePath); err != nil {
		return nil, fmt.Errorf("读取 Cookie 文件失败: %w", err)
	}
	cmd := exec.Command("yt-dlp",
		"--no-playlist",
		"--skip-download",
		"--no-warnings",
		"--no-progress",
		"--cookies", cookiePath,
		"--dump-single-json",
		rawURL,
	)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, newYTDLPFailure("yt-dlp 元数据预检失败", err, string(exitErr.Stderr))
		}
		return nil, newYTDLPFailure("yt-dlp 元数据预检失败", err, "")
	}
	var metadata ytDLPMusicMetadata
	if err := json.Unmarshal(output, &metadata); err != nil {
		return nil, fmt.Errorf("解析 yt-dlp 元数据失败: %w", err)
	}
	artist := firstNonBlank(metadata.Artist, metadata.Creator, metadata.Uploader)
	title := firstNonBlank(metadata.Track, metadata.Title)
	return &SongInfo{
		SongName:    strings.TrimSpace(title),
		SongArtists: strings.TrimSpace(artist),
		SongAlbum:   strings.TrimSpace(metadata.Album),
		FileExt:     strings.TrimPrefix(strings.TrimSpace(metadata.Ext), "."),
	}, nil
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
