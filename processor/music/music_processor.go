package music

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	browser "github.com/EDDYCJY/fake-useragent"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
	"go.senan.xyz/taglib"
)

/* ---------------------- 音乐接口定义 ---------------------- */
type Processor interface {
	processor.Processor
	// 歌曲元信息列表
	Songs() []*SongInfo
	// 下载音乐
	DownloadMusic(url string, callback func(string)) error
	// 构建下载命令
	DownloadCommand(url string) *exec.Cmd
	// 音乐整理之前的处理(如读取,嵌入元数据,刮削等)
	BeforeTidy() error
	// 是否需要移除DRM
	NeedRemoveDRM() bool
	// 移除DRM
	DRMRemove() error
	// 音乐整理
	TidyMusic() error
	// 加密后缀
	EncryptedExts() []string
	// 非加密后缀
	DecryptedExts() []string
}

/* ---------------------- 音乐结构体定义 ---------------------- */
// SongInfo 音乐信息
type SongInfo struct {
	SongName        string // 音乐名称
	SongArtists     string // 艺术家
	SongAlbum       string // 专辑
	SongAlbumArtist string // 专辑艺术家
	FileExt         string // 格式
	MusicSize       int64  // 音乐大小
	Bitrate         string // 码率
	Duration        int    // 时长
	Url             string // 下载地址
	MusicPath       string // 音乐文件路径
	PicUrl          string // 封面图url
	Lyric           string // 歌词
	Year            int    // 年份
	Genre           string // 流派
	Tidy            string // 入库方式(默认/webdav)
}

type imageResult struct {
	data []byte
	err  error
}

var UserAgent = browser.Chrome()

/* ---------------------- 常量 ---------------------- */

var BaseTempDir = filepath.Join("data", "temp", "music")

// 苹果音乐临时文件夹
var AppleMusicTempDir = filepath.Join(BaseTempDir, "AppleMusic")

// 网易云音乐临时文件夹
var NCMTempDir = filepath.Join(BaseTempDir, "NCM")

// QQ音乐临时文件夹
var QQTempDir = filepath.Join(BaseTempDir, "QQ")

// Youtube音乐临时文件夹
var YoutubeTempDir = filepath.Join(BaseTempDir, "Youtube")

// BiliBili音乐临时文件夹
var BiliBiliTempDir = filepath.Join(BaseTempDir, "BiliBili")

// SoundCloud临时文件夹
var SoundcloudTempDir = filepath.Join(BaseTempDir, "Soundcloud")

// Spotify临时文件夹
var SpotifyTempDir = filepath.Join(BaseTempDir, "Spotify")

// Forward临时文件夹
var ForwardTempDir = filepath.Join(BaseTempDir, "Forward")

/* ---------------------- 音乐下载相关业务函数 ---------------------- */

// 读取音乐目录 返回元信息列表
func ReadMusicDir(tempDir string, tidyType string, p Processor) ([]*SongInfo, error) {
	files, err := os.ReadDir(tempDir)
	if err != nil {
		return nil, fmt.Errorf("读取临时目录失败: %w", err)
	}

	songs := make([]*SongInfo, 0, len(files))
	for _, f := range files {
		// 跳过目录
		if f.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(f.Name()))
		if utils.Contains(p.DecryptedExts(), ext) {
			fullPath := filepath.Join(tempDir, f.Name())

			song, err := ReadTags(fullPath)
			FillDefaultTags(fullPath, song)

			if err != nil {
				return nil, fmt.Errorf("处理文件 %s 失败: %w", f.Name(), err)
			}

			// 重命名逻辑 - 如果已有同名文件，直接覆盖
			safeName := utils.MakeSafeFileName(song.SongName) + ext
			newPath := filepath.Join(tempDir, safeName)

			if f.Name() != safeName {
				if err := os.Rename(fullPath, newPath); err != nil {
					return nil, fmt.Errorf("重命名文件 %s 失败: %w", f.Name(), err)
				}
				fullPath = newPath
			}

			song.Tidy = tidyType
			songs = append(songs, song)
		}
	}
	return songs, nil
}

// ReadTags 读取音乐元数据
func ReadTags(path string) (*SongInfo, error) {
	tags, err := taglib.ReadTags(path)
	if err != nil {
		return nil, err
	}

	props, err := taglib.ReadProperties(path)
	if err != nil {
		return nil, err
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	songInfo := &SongInfo{
		FileExt:   strings.TrimPrefix(filepath.Ext(path), "."),
		MusicSize: fileInfo.Size(),
		Bitrate:   strconv.Itoa(int(props.Bitrate)),
		Duration:  int(props.Length),
	}

	if t, ok := tags[taglib.Title]; ok && len(t) > 0 {
		songInfo.SongName = t[0]
	} else {
		songInfo.SongName = filepath.Base(path)
	}

	if a, ok := tags[taglib.Artist]; ok && len(a) > 0 {
		songInfo.SongArtists = a[0]
	}

	if al, ok := tags[taglib.Album]; ok && len(al) > 0 {
		songInfo.SongAlbum = al[0]
	}

	if aa, ok := tags[taglib.AlbumArtist]; ok && len(aa) > 0 {
		songInfo.SongAlbumArtist = aa[0]
	}

	if d, ok := tags[taglib.Date]; ok && len(d) > 0 {
		songInfo.Year, _ = strconv.Atoi(d[0])
	}

	if l, ok := tags[taglib.Lyrics]; ok && len(l) > 0 {
		songInfo.Lyric = l[0]
	}

	if ge, ok := tags[taglib.Genre]; ok && len(ge) > 0 {
		songInfo.Genre = ge[0]
	}

	return songInfo, nil
}

// FillDefaultTags 标签写入默认值
func FillDefaultTags(path string, info *SongInfo) {
	updates := make(map[string][]string)

	// 默认专辑
	if info.SongAlbum == "" {
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if info.SongArtists != "" {
			info.SongAlbum = fmt.Sprintf("%s Singles", info.SongArtists)
		} else {
			info.SongAlbum = fmt.Sprintf("%s Collection", base)
		}
		updates[taglib.Album] = []string{info.SongAlbum}
	}

	// 默认专辑艺术家
	if info.SongAlbumArtist == "" {
		info.SongAlbumArtist = info.SongArtists
		updates[taglib.AlbumArtist] = []string{info.SongAlbumArtist}
	}

	// 默认年份
	if info.Year == 0 {
		info.Year = time.Now().Year()
		updates[taglib.Date] = []string{strconv.Itoa(info.Year)}
	}

	// 默认流派
	if info.Genre == "" {
		if info.Lyric == "" {
			info.Genre = "纯音乐"
		} else {
			info.Genre = "流行"
		}
		updates[taglib.Genre] = []string{info.Genre}
	}

	// 默认歌词
	if info.Lyric == "" {
		info.Lyric = "[00:00:00]此歌曲为没有填词的纯音乐，请您欣赏"
		updates[taglib.Lyrics] = []string{info.Lyric}
	}

	if len(updates) > 0 {
		if err := taglib.WriteTags(path, updates, 0); err != nil {
			utils.WarnWithFormat("write default tags failed: %w", err)
		}
	}
}

// WriteTags 嵌入标签
func WriteTags(song *SongInfo, filePath string) error {
	// 写入文本标签
	tags := map[string][]string{
		taglib.Title:       {song.SongName},
		taglib.Artist:      {song.SongArtists},
		taglib.Album:       {song.SongAlbum},
		taglib.AlbumArtist: {song.SongArtists},
		taglib.Date:        {strconv.Itoa(song.Year)},
		taglib.Lyrics:      {song.Lyric},
	}
	// 写入文本标签（opts传taglib.Clear则清除原标签，传0则不清除）
	if err := taglib.WriteTags(filePath, tags, 0); err != nil {
		return fmt.Errorf("write metadata failed for %s: %w", filePath, err)
	}
	return nil
}

// WriteTagsWithCoverFile 嵌入标签(封面通过文件嵌入)
func WriteTagsWithCoverFile(song *SongInfo, filePath string, coverFilePath string) error {
	// 写入文本标签
	tags := map[string][]string{
		taglib.Title:       {song.SongName},
		taglib.Artist:      {song.SongArtists},
		taglib.Album:       {song.SongAlbum},
		taglib.AlbumArtist: {song.SongArtists},
		taglib.Date:        {strconv.Itoa(song.Year)},
		taglib.Lyrics:      {song.Lyric},
	}
	var err error
	// 写入文本标签（opts传taglib.Clear则清除原标签，传0则不清除）
	if err = taglib.WriteTags(filePath, tags, 0); err != nil {
		return fmt.Errorf("write metadata failed for %s: %w", filePath, err)
	}
	data, err := os.ReadFile(coverFilePath)
	if err == nil && len(data) > 0 {
		if err = taglib.WriteImage(filePath, data); err != nil {
			return fmt.Errorf("write image failed for %s: %w", filePath, err)
		}
	}
	return nil
}

// WriteTagsWithCoverURL 嵌入标签(封面通过url嵌入)
func WriteTagsWithCoverURL(song *SongInfo, filePath string) error {
	imageCh := make(chan imageResult, 1)

	if song.PicUrl != "" {
		go func() {
			data, err := utils.FetchImage(song.PicUrl)
			imageCh <- imageResult{data, err}
		}()
	} else {
		// 没有图片时直接发送空结果
		imageCh <- imageResult{}
	}

	// 写入文本标签
	tags := map[string][]string{
		taglib.Title:       {song.SongName},
		taglib.Artist:      {song.SongArtists},
		taglib.Album:       {song.SongAlbum},
		taglib.AlbumArtist: {song.SongArtists},
		taglib.Date:        {strconv.Itoa(song.Year)},
		taglib.Lyrics:      {song.Lyric},
	}

	// opts传taglib.Clear则会清除原标签 传0则不清除
	if err := taglib.WriteTags(filePath, tags, 0); err != nil {
		return fmt.Errorf("write metadata failed for %s: %w", filePath, err)
	}

	// 等待图片下载完成
	res := <-imageCh
	if res.err != nil {
		return fmt.Errorf("fetch image failed for %s: %w", filePath, res.err)
	}

	// 写入封面图片
	if len(res.data) > 0 {
		if err := taglib.WriteImage(filePath, res.data); err != nil {
			return fmt.Errorf("write image failed for %s: %w", filePath, err)
		}
	}

	return nil
}
