package discovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/core/linkparser"
	"github.com/nichuanfang/gymdl/internal/audiodupe"
	"github.com/nichuanfang/gymdl/internal/musicdedupe"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/utils"
)

var errRecentDownload = errors.New("近期下载需等待曲库确认")
var errMetadataAmbiguous = errors.New("源链接歌曲元数据与收藏版本不一致，需确认")

func resolveSourceMetadata(cfg *config.Config, kind processor.LinkType, link string) (*music.SongInfo, error) {
	switch kind {
	case processor.LinkNetEase:
		return music.ResolveNeteaseSingleMetadata(cfg, link)
	case processor.LinkQQMusic:
		return music.ResolveQQSingleMetadata(cfg, link)
	case processor.LinkYoutubeMusic:
		return music.ResolveYTDLPMusicMetadata(cfg, link)
	}
	return nil, fmt.Errorf("未支持的音乐来源")
}

// Download keeps preflight, processing and successful recording under the same
// gate as Telegram downloads. Historical Bot records are not library inventory.
func Download(ctx context.Context, cfg *config.Config, store *musicdedupe.Store, t Track) (DownloadResult, error) {
	if store == nil {
		return DownloadResult{}, fmt.Errorf("查重数据库不可用")
	}
	taskCfg := config.WithLiveTaskOverrides(cfg)
	link, executor := linkparser.ParseLinkWithConfig(taskCfg, musicSourceURL(t))
	p, ok := executor.(music.Processor)
	if !ok || link == "" || !musicdedupe.IsSingleTrackLink(p.Name(), link) {
		return DownloadResult{}, fmt.Errorf("候选不是受支持的音乐单曲链接")
	}
	identity := musicdedupe.IdentityFromLink(p.Name(), link)
	p.Init(taskCfg)
	result := DownloadResult{}
	_, err := music.RunWorkflowWithHooks(ctx, p, link, nil, music.WorkflowHooks{Before: func() error {
		if core.GlobalWebDAV != nil && core.GlobalWebDAV.Outbox != nil {
			items, e := core.GlobalWebDAV.Outbox.List()
			if e != nil {
				return fmt.Errorf("无法核对待上传队列")
			}
			for _, item := range items {
				if Normalize(item.Title) == Normalize(t.Title) && Normalize(item.Artist) == Normalize(t.Artist) {
					return errRecentDownload
				}
			}
		}
		match, e := store.FindDuplicate(ctx, identity)
		if e != nil {
			return fmt.Errorf("无法核对近期下载")
		}
		if match != nil && match.Downloaded.After(time.Now().Add(-24*time.Hour)) {
			return errRecentDownload
		}
		if !t.Approved {
			metadata, e := resolveSourceMetadata(taskCfg, p.Name(), link)
			if e != nil {
				return fmt.Errorf("原平台元数据预检失败")
			}
			if metadata == nil || !SameRecording(t, Track{Title: metadata.SongName, Artist: metadata.SongArtists, Album: metadata.SongAlbum, Duration: metadata.Duration, Verified: true}) {
				return errMetadataAmbiguous
			}
		}
		return nil
	}, BeforeStore: func() error {
		songs := p.Songs()
		if len(songs) == 1 && songs[0] != nil {
			if file, err := os.Stat(songs[0].MusicPath); err == nil && file.Mode().IsRegular() {
				songs[0].MusicSize = file.Size()
			}
		}
		return reviewOneSong(ctx, songs, t.Approved)
	}, After: func(songs []*music.SongInfo) error {
		for _, song := range songs {
			actual := t
			actual.Title = song.SongName
			actual.Artist = song.SongArtists
			actual.Album = song.SongAlbum
			actual.Duration = song.Duration
			if song.SourceURL != "" {
				actual.URL = song.SourceURL
			}
			if song.PicUrl != "" {
				actual.CoverURL = song.PicUrl
			}
			actual.Verified = true
			result.Track = &actual
			if strings.EqualFold(song.Tidy, "WEBDAV_PENDING") {
				result.Pending = true
			}
			if len(songs) == 1 {
				storage := strings.ToUpper(song.Tidy)
				if result.Pending {
					storage = "WEBDAV"
				}
				if storedPath := music.StoredFilePath(song, taskCfg.Tidy); storedPath != "" && (storage == "LOCAL" || storage == "WEBDAV") {
					result.File = &IngestedFile{
						Path: storedPath, Storage: storage, Extension: song.FileExt, Size: song.MusicSize, Bitrate: song.Bitrate,
					}
				}
			}
			record := identity
			record.Title = song.SongName
			record.Artist = song.SongArtists
			record.Album = song.SongAlbum
			if err := store.RecordSuccessfulDownload(ctx, record); err != nil {
				return fmt.Errorf("保存成功记录失败，需核对实际曲库")
			}
		}
		return nil
	}})
	if errors.Is(err, errRecentDownload) || errors.Is(err, music.ErrWorkflowCommitted) {
		return DownloadResult{Pending: true}, nil
	}
	return result, err
}

func uploadedDiscoveryFile(file *IngestedFile) (bool, error) {
	webdav := core.GlobalWebDAV
	if webdav == nil || webdav.Outbox == nil || file.Storage != "WEBDAV" {
		return false, nil
	}
	items, err := webdav.Outbox.List()
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if path.Join(item.RemoteDir, item.FileName) == file.Path {
			return false, nil
		}
	}
	info, err := webdav.Stat(file.Path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !info.IsDir() && (file.Size <= 0 || file.Size == info.Size()), nil
}

// PendingTrack excludes staged uploads and very recent successful manual
// downloads while Navidrome is still scanning them.
func PendingTrack(ctx context.Context, store *musicdedupe.Store, t Track) (bool, error) {
	if store == nil {
		return false, fmt.Errorf("查重数据库不可用")
	}
	if t.Title != "" && t.Artist != "" {
		record, err := store.FindByTitle(ctx, utils.ToSimpleChinese(t.Title), utils.ToSimpleChinese(t.Artist))
		if err != nil {
			return false, err
		}
		if record != nil && recordingVersion(t) == recordingVersion(Track{Album: record.Album}) && record.Downloaded.After(time.Now().Add(-24*time.Hour)) {
			return true, nil
		}
	}
	if core.GlobalWebDAV != nil && core.GlobalWebDAV.Outbox != nil {
		items, err := core.GlobalWebDAV.Outbox.List()
		if err != nil {
			return false, fmt.Errorf("无法读取待上传队列")
		}
		for _, item := range items {
			if Normalize(item.Title) == Normalize(t.Title) && Normalize(item.Artist) == Normalize(t.Artist) {
				return true, nil
			}
		}
	}
	return false, nil
}

func reviewOneSong(ctx context.Context, songs []*music.SongInfo, bypass bool) error {
	var one *music.SongInfo
	count := 0
	for _, song := range songs {
		if song == nil {
			continue
		}
		count++
		one = song
	}
	if count != 1 || strings.TrimSpace(one.MusicPath) == "" {
		return nil
	}
	return audiodupe.ReviewFileWithLyric(ctx, one.MusicPath, float64(one.Duration), one.LyricEvidence, bypass)
}

func musicSourceURL(t Track) string {
	if t.Platform == "youtube" && t.ID != "" {
		return "https://music.youtube.com/watch?v=" + t.ID
	}
	return t.URL
}
