package music

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/XiaoMengXinX/Music163Api-Go/api"
	"github.com/XiaoMengXinX/Music163Api-Go/types"
	ncmutils "github.com/XiaoMengXinX/Music163Api-Go/utils"
	downloader "github.com/XiaoMengXinX/SimpleDownloader"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/utils"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor"
)

/* ---------------------- 结构体与构造方法 ---------------------- */

type NetEaseProcessor struct {
	cfg     *config.Config //配置文件
	songs   []*SongInfo    //歌曲元信息列表
	tempDir string         //临时目录
	musicU  string         //会员cookie
}

// Init  初始化
func (ncm *NetEaseProcessor) Init(cfg *config.Config) {
	ncm.cfg = cfg
	ncm.songs = make([]*SongInfo, 0)
	ncm.tempDir = processor.BuildOutputDir(NCMTempDir)
	cookiePath := filepath.Join(cfg.CookieCloud.CookieFilePath, cfg.CookieCloud.CookieFile)
	ncm.musicU = utils.GetCookieValue(cookiePath, ".music.163.com", "MUSIC_U")
}

/* ---------------------- 基础接口实现 ---------------------- */

func (ncm *NetEaseProcessor) Name() processor.LinkType {
	return processor.LinkNetEase
}

func (ncm *NetEaseProcessor) Songs() []*SongInfo {
	return ncm.songs
}

/* ------------------------ 下载逻辑 ------------------------ */

func (ncm *NetEaseProcessor) DownloadMusic(url string, callback func(string)) error {
	start := time.Now()
	utils.InfoWithFormat("[NCM] 🎵 开始下载: %s", url)
	ncmType, musicID := utils.ParseMusicID(url)
	switch ncmType {
	case 1:
		//单曲下载
		return ncm.downloadSingle(musicID, start, callback)
	case 2:
		//列表下载
		return ncm.downloadPlaylist(musicID, start, callback)
	}
	return errors.New("不支持的下载类型")
}

func (ncm *NetEaseProcessor) DownloadCommand(url string) *exec.Cmd {
	return nil
}

func (ncm *NetEaseProcessor) BeforeTidy() error {
	var fileName string
	var coverFileName string
	for _, song := range ncm.songs {
		fileName = filepath.Join(ncm.tempDir, ncm.safeFileName(song))
		coverFileName = filepath.Join(ncm.tempDir, ncm.safeCoverFileName(song))
		err := WriteTagsWithCoverFile(song, fileName, coverFileName)
		if err != nil {
			return err
		}
	}
	return nil
}

func (ncm *NetEaseProcessor) NeedRemoveDRM() bool {
	return false
}

func (ncm *NetEaseProcessor) DRMRemove() error {
	return nil
}

func (ncm *NetEaseProcessor) TidyMusic() error {
	files, err := os.ReadDir(ncm.tempDir)
	if err != nil {
		return fmt.Errorf("读取临时目录失败: %w", err)
	}
	if len(files) == 0 {
		utils.WarnWithFormat("[AppleMusic] ⚠️ 未找到待整理的音乐文件")
		return errors.New("未找到待整理的音乐文件")
	}

	switch ncm.cfg.Tidy.Mode {
	case 1:
		return ncm.tidyToLocal(files)
	case 2:
		return ncm.tidyToWebDAV(files, core.GlobalWebDAV)
	default:
		return fmt.Errorf("未知整理模式: %d", ncm.cfg.Tidy.Mode)
	}
}

func (ncm *NetEaseProcessor) EncryptedExts() []string {
	return []string{".ncm"}
}

func (ncm *NetEaseProcessor) DecryptedExts() []string {
	return []string{".flac", ".mp3", ".aac", ".m4a", ".ogg"}
}

/* ------------------------ 拓展方法 ------------------------ */

// downloadSingle 单曲下载
func (ncm *NetEaseProcessor) downloadSingle(musicID int, start time.Time, callback func(string)) error {
	var err error

	utils.DebugWithFormat("[NCM] 获取单曲数据: ID=%d", musicID)
	detail, songURL, songLyric, err := ncm.FetchSongData(musicID, ncm.cfg)
	if err != nil {
		utils.ErrorWithFormat("[NCM] ❌ 获取歌曲数据失败: %v", err)
		return err
	}

	if len(detail.Songs) == 0 || len(songURL.Data) == 0 || songURL.Data[0].Url == "" {
		errMsg := "未获取到有效歌曲信息或歌曲无下载地址"
		utils.ErrorWithFormat("[NCM] ❌ %s: ID=%d", errMsg, musicID)
		return errors.New(errMsg)
	}

	// 构建歌曲元信息
	songInfo := ncm.buildSongInfo(ncm.cfg, detail, songURL, songLyric)
	fileName := ncm.safeFileName(songInfo)
	coverFileName := ncm.safeCoverFileName(songInfo)

	// 创建临时目录
	if err := processor.CreateOutputDir(ncm.tempDir); err != nil {
		utils.ErrorWithFormat("[NCM] ❌ 创建临时目录失败: %v", err)
		return err
	}

	// 下载文件
	utils.InfoWithFormat("[NCM] ⬇️ 开始下载: %s", fileName)
	if err := ncm.downloadFile(songURL.Data[0].Url, fileName, songInfo.PicUrl, coverFileName, ncm.tempDir); err != nil {
		_ = processor.RemoveTempDir(ncm.tempDir)
		utils.ErrorWithFormat("[NCM] ❌ 下载失败: %v", err)
		return fmt.Errorf("下载失败: %w", err)
	}
	// 更新元信息列表
	ncm.songs = append(ncm.songs, songInfo)
	utils.InfoWithFormat("[NCM] ✅ 下载完成: %s （耗时 %v）", fileName, time.Since(start).Truncate(time.Millisecond))
	callback(fmt.Sprintf("下载完成: %s （耗时 %v）", fileName, time.Since(start).Truncate(time.Millisecond)))
	return nil
}

// downloadPlaylist 列表下载
func (ncm *NetEaseProcessor) downloadPlaylist(musicID int, start time.Time, callback func(string)) error {
	utils.DebugWithFormat("[NCM] 获取歌单数据: ID=%d", musicID)
	detail, err := ncm.FetchPlaylistData(musicID, ncm.cfg)
	if err != nil {
		utils.ErrorWithFormat("[NCM] ❌ 获取歌单数据失败: %v", err)
		return err
	}

	if detail.Playlist.TrackCount == 0 {
		errMsg := "未获取到有效歌曲信息或歌曲无下载地址"
		utils.ErrorWithFormat("[NCM] ❌ %s: 歌单ID=%d", errMsg, musicID)
		return errors.New(errMsg)
	}

	// 批量获取歌曲信息（包含歌词）
	trackIDs := make([]int, len(detail.Playlist.TrackIds))
	for i, track := range detail.Playlist.TrackIds {
		trackIDs[i] = track.Id
	}

	songMap, err := ncm.FetchPlaylistSongData(trackIDs, ncm.cfg)
	if err != nil {
		return err
	}

	utils.InfoWithFormat("[NCM] 开始下载歌单: %s (%d首)", detail.Playlist.Name, detail.Playlist.TrackCount)
	callback(fmt.Sprintf("开始下载歌单: %s (%d首)", detail.Playlist.Name, detail.Playlist.TrackCount))

	//创建下载目录
	if err = processor.CreateOutputDir(ncm.tempDir); err != nil {
		return err
	}
	var fileName string
	var coverFileName string
	for index, track := range detail.Playlist.TrackIds {
		songInfo, ok := songMap[track.Id]
		if !ok {
			utils.WarnWithFormat("[NCM] ⚠️ 歌曲信息缺失，跳过: ID=%d", track.Id)
			continue
		}
		callback(fmt.Sprintf("开始下载第%d首...", index+1))
		utils.InfoWithFormat("[NCM] 正在下载第%d首: %s", index+1, songInfo.SongName)
		fileName = ncm.safeFileName(songInfo)
		coverFileName = ncm.safeCoverFileName(songInfo)
		if err = ncm.downloadFile(songInfo.Url, fileName, songInfo.PicUrl, coverFileName, ncm.tempDir); err != nil {
			utils.ErrorWithFormat("[NCM] ❌ 歌单下载中断，第%d首下载失败: %v", index+1, err)
			return err
		}

		ncm.songs = append(ncm.songs, songInfo)
		utils.InfoWithFormat("[NCM] ✅ 下载完成: %s （耗时 %v）", fileName, time.Since(start).Truncate(time.Millisecond))
		callback(fmt.Sprintf("下载完成: %s （耗时 %v）", fileName, time.Since(start).Truncate(time.Millisecond)))
	}

	utils.InfoWithFormat("[NCM] ✅ 歌单下载完成: %s （耗时 %v）", detail.Playlist.Name, time.Since(start).Truncate(time.Millisecond))
	callback(fmt.Sprintf("歌单下载完成: %s （耗时 %v）", detail.Playlist.Name, time.Since(start).Truncate(time.Millisecond)))
	return nil
}

// FetchSongData 获取单曲信息
func (ncm *NetEaseProcessor) FetchSongData(musicID int, cfg *config.Config) (*types.SongsDetailData, *types.SongsURLData, *types.SongLyricData, error) {
	utils.DebugWithFormat("[NCM] 请求歌曲信息中... ID=%d", musicID)

	batch := api.NewBatch(
		api.BatchAPI{Key: api.SongDetailAPI, Json: api.CreateSongDetailReqJson([]int{musicID})},
		api.BatchAPI{Key: api.SongUrlAPI, Json: api.CreateSongURLJson(api.SongURLConfig{Ids: []int{musicID}})},
		api.BatchAPI{Key: api.SongLyricAPI, Json: api.CreateSongLyricReqJson(musicID)},
	)

	req := ncmutils.RequestData{}
	if ncm.musicU != "" {
		req.Cookies = []*http.Cookie{{Name: "MUSIC_U", Value: ncm.musicU}}
	}

	result := batch.Do(req)
	if result.Error != nil {
		return nil, nil, nil, fmt.Errorf("网易云API请求失败: %w", result.Error)
	}

	_, parsed := batch.Parse()

	var detail types.SongsDetailData
	var urls types.SongsURLData
	var lyrics types.SongLyricData

	if err := json.Unmarshal([]byte(parsed[api.SongDetailAPI]), &detail); err != nil {
		return nil, nil, nil, fmt.Errorf("解析歌曲详情失败: %w", err)
	}
	if err := json.Unmarshal([]byte(parsed[api.SongUrlAPI]), &urls); err != nil {
		return nil, nil, nil, fmt.Errorf("解析歌曲URL失败: %w", err)
	}
	if err := json.Unmarshal([]byte(parsed[api.SongLyricAPI]), &lyrics); err != nil {
		return nil, nil, nil, fmt.Errorf("解析歌曲歌词失败: %w", err)
	}

	utils.DebugWithFormat("[NCM] 歌曲信息获取成功: %s", detail.Songs[0].Name)
	return &detail, &urls, &lyrics, nil
}

// FetchPlaylistData 获取播放列表信息
func (ncm *NetEaseProcessor) FetchPlaylistData(musicID int, cfg *config.Config) (*types.PlaylistDetailData, error) {
	utils.DebugWithFormat("[NCM] 请求歌单信息中... ID=%d", musicID)

	batch := api.NewBatch(
		api.BatchAPI{Key: api.PlaylistDetailAPI, Json: api.CreatePlaylistDetailReqJson(musicID)},
	)
	req := ncmutils.RequestData{}
	if ncm.musicU != "" {
		req.Cookies = []*http.Cookie{{Name: "MUSIC_U", Value: ncm.musicU}}
	}
	result := batch.Do(req)
	if result.Error != nil {
		return nil, fmt.Errorf("网易云API请求失败: %w", result.Error)
	}

	_, parsed := batch.Parse()

	var detail types.PlaylistDetailData

	if err := json.Unmarshal([]byte(parsed[api.PlaylistDetailAPI]), &detail); err != nil {
		return nil, fmt.Errorf("解析歌单详情失败: %w", err)
	}
	utils.DebugWithFormat("[NCM] 歌单信息获取成功: %s", detail.Playlist.Name)
	return &detail, nil
}

// FetchSongLyric 获取歌词
func (ncm *NetEaseProcessor) FetchSongLyric(musicID int, cfg *config.Config) string {
	utils.DebugWithFormat("[NCM] 请求歌词信息中... ID=%d", musicID)

	batch := api.NewBatch(
		api.BatchAPI{Key: api.SongLyricAPI, Json: api.CreateSongLyricReqJson(musicID)},
	)

	cookiePath := filepath.Join(cfg.CookieCloud.CookieFilePath, cfg.CookieCloud.CookieFile)
	musicU := utils.GetCookieValue(cookiePath, ".music.163.com", "MUSIC_U")

	req := ncmutils.RequestData{}
	if musicU != "" {
		req.Cookies = []*http.Cookie{{Name: "MUSIC_U", Value: musicU}}
	}

	result := batch.Do(req)
	if result.Error != nil {
		return ""
	}

	_, parsed := batch.Parse()

	var lyrics types.SongLyricData

	if err := json.Unmarshal([]byte(parsed[api.SongLyricAPI]), &lyrics); err != nil {
		return ""
	}

	utils.DebugWithFormat("[NCM] 歌词信息获取成功: %s", musicID)
	return utils.ParseNCMLyric(&lyrics)
}

// downloadFile 下载文件和封面
func (ncm *NetEaseProcessor) downloadFile(url string, fileName string, coverUrl string, coverFileName string, saveDir string) error {
	if coverUrl != "" {
		utils.DebugWithFormat("[NCM] 开始下载文件: %s 和封面: %s", fileName, coverFileName)
	} else {
		utils.DebugWithFormat("[NCM] 开始下载文件: %s", fileName)
	}

	d := downloader.NewDownloader().
		SetSavePath(saveDir).
		SetBreakPoint(true).
		SetTimeOut(300 * time.Second)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	download := func(url, fileName string) {
		defer wg.Done()
		task, err := d.NewDownloadTask(url)
		if err != nil {
			errCh <- err
			return
		}

		task.CleanTempFiles()
		task.ReplaceHostName(ncm.fixHost(task.GetHostName())).
			ForceHttps().
			ForceMultiThread()

		if err := task.SetFileName(fileName).Download(); err != nil {
			errCh <- err
		}
	}

	// 下载主文件
	wg.Add(1)
	go download(url, fileName)

	// 下载封面（如果 URL 非空）
	if coverUrl != "" {
		wg.Add(1)
		go download(coverUrl, coverFileName)
	}

	wg.Wait()
	close(errCh)

	if len(errCh) > 0 {
		return <-errCh
	}
	return nil
}

// FetchPlaylistSongData 批量获取歌单歌曲信息
func (ncm *NetEaseProcessor) FetchPlaylistSongData(musicIDs []int, cfg *config.Config) (map[int]*SongInfo, error) {
	utils.DebugWithFormat("[NCM] 批量请求歌曲信息: IDs=%v", musicIDs)

	// 1. 批量请求detail和url
	batch := api.NewBatch(
		api.BatchAPI{Key: api.SongDetailAPI, Json: api.CreateSongDetailReqJson(musicIDs)},
		api.BatchAPI{Key: api.SongUrlAPI, Json: api.CreateSongURLJson(api.SongURLConfig{Ids: musicIDs})},
	)
	req := ncmutils.RequestData{}
	if ncm.musicU != "" {
		req.Cookies = []*http.Cookie{{Name: "MUSIC_U", Value: ncm.musicU}}
	}

	result := batch.Do(req)
	if result.Error != nil {
		return nil, fmt.Errorf("网易云API请求失败: %w", result.Error)
	}

	_, parsed := batch.Parse()

	var details types.SongsDetailData
	var urls types.SongsURLData

	if err := json.Unmarshal([]byte(parsed[api.SongDetailAPI]), &details); err != nil {
		return nil, fmt.Errorf("解析歌曲详情失败: %w", err)
	}
	if err := json.Unmarshal([]byte(parsed[api.SongUrlAPI]), &urls); err != nil {
		return nil, fmt.Errorf("解析歌曲URL失败: %w", err)
	}

	// 2. 歌词
	songLyricMap := make(map[int]string)
	for _, id := range musicIDs {
		lyric := ncm.FetchSongLyric(id, cfg)
		songLyricMap[id] = lyric
	}

	// 3. 构建 SongInfo map
	songMap := make(map[int]*SongInfo)
	for i, s := range details.Songs {
		u := urls.Data[i]
		lyric := songLyricMap[s.Id]
		if lyric == "" {
			lyric = "[00:00:00]此歌曲为没有填词的纯音乐，请您欣赏"
		}
		year := utils.ParseNCMYear(&details)
		tidy := processor.DetermineTidyType(cfg)
		songMap[s.Id] = &SongInfo{
			SongName:    s.Name,
			SongArtists: utils.ParseArtist(s),
			SongAlbum:   s.Al.Name,
			FileExt:     ncm.detectExt(u.Url),
			MusicSize:   int64(u.Size),
			Bitrate:     strconv.Itoa((8 * u.Size / (s.Dt / 1000)) / 1000),
			Duration:    s.Dt / 1000,
			Url:         u.Url,
			PicUrl:      s.Al.PicUrl,
			Tidy:        tidy,
			Lyric:       lyric,
			Year:        year,
		}
	}

	return songMap, nil
}

// fixHost 主机名修正
func (ncm *NetEaseProcessor) fixHost(host string) string {
	replacer := strings.NewReplacer("m8.", "m7.", "m801.", "m701.", "m804.", "m701.", "m704.", "m701.")
	return replacer.Replace(host)
}

// buildSongInfo 构建歌曲信息
func (ncm *NetEaseProcessor) buildSongInfo(cfg *config.Config, detail *types.SongsDetailData, urls *types.SongsURLData, lyric *types.SongLyricData) *SongInfo {
	s := detail.Songs[0]
	u := urls.Data[0]
	// 整理方式
	tidy := processor.DetermineTidyType(cfg)

	ncmLyric := utils.ParseNCMLyric(lyric)
	if ncmLyric == "" {
		ncmLyric = "[00:00:00]此歌曲为没有填词的纯音乐，请您欣赏"
	}
	year := utils.ParseNCMYear(detail)

	return &SongInfo{
		SongName:    s.Name,
		SongArtists: utils.ParseArtist(s),
		SongAlbum:   s.Al.Name,
		FileExt:     ncm.detectExt(u.Url),
		MusicSize:   int64(u.Size),
		Bitrate:     strconv.Itoa((8 * u.Size / (s.Dt / 1000)) / 1000),
		Duration:    s.Dt / 1000,
		Url:         u.Url,
		PicUrl:      s.Al.PicUrl,
		Tidy:        tidy,
		Lyric:       ncmLyric,
		Year:        year,
	}
}

// detectExt 检测扩展名
func (ncm *NetEaseProcessor) detectExt(url string) string {
	if idx := strings.Index(url, "?"); idx != -1 {
		url = url[:idx]
	}
	ext := strings.ToLower(path.Ext(url))
	switch ext {
	case ".mp3", ".aac", ".m4a", ".ogg", ".flac":
		return strings.TrimPrefix(ext, ".")
	default:
		return "mp3"
	}
}

// safeFileName 合法的文件名
func (ncm *NetEaseProcessor) safeFileName(info *SongInfo) string {
	replacer := strings.NewReplacer("/", " ", "?", " ", "*", " ", ":", " ",
		"|", " ", "\\", " ", "<", " ", ">", " ", "\"", " ")
	return replacer.Replace(fmt.Sprintf("%s - %s.%s",
		strings.ReplaceAll(info.SongArtists, "/", ","),
		info.SongName,
		info.FileExt))
}

// safeCoverFileName 合法的封面文件名
func (ncm *NetEaseProcessor) safeCoverFileName(info *SongInfo) string {
	replacer := strings.NewReplacer("/", " ", "?", " ", "*", " ", ":", " ",
		"|", " ", "\\", " ", "<", " ", ">", " ", "\"", " ")
	return replacer.Replace(fmt.Sprintf("%s - %s.%s",
		strings.ReplaceAll(info.SongArtists, "/", ","),
		info.SongName+"_cover", "jpg"))
}

// safeTempFileName 合法临时文件路径
func (ncm *NetEaseProcessor) safeTempFileName(info *SongInfo) string {
	replacer := strings.NewReplacer("/", " ", "?", " ", "*", " ", ":", " ",
		"|", " ", "\\", " ", "<", " ", ">", " ", "\"", " ")
	return replacer.Replace(fmt.Sprintf("%s - %s.%s",
		strings.ReplaceAll(info.SongArtists, "/", ","),
		fmt.Sprintf("%s_temp", info.SongName),
		info.FileExt))
}

// 整理到本地
func (ncm *NetEaseProcessor) tidyToLocal(files []os.DirEntry) error {
	dstDir := ncm.cfg.Tidy.DistDir
	if dstDir == "" {
		_ = processor.RemoveTempDir(ncm.tempDir)
		return errors.New("未配置输出目录")
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		_ = processor.RemoveTempDir(ncm.tempDir)
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	for _, f := range files {
		if !utils.FilterMusicFile(f, ncm.EncryptedExts(), ncm.DecryptedExts()) {
			utils.DebugWithFormat("[AppleMusic] 跳过非音乐文件: %s", f.Name())
			continue
		}
		src := filepath.Join(ncm.tempDir, f.Name())
		dst := filepath.Join(dstDir, utils.SanitizeFileName(f.Name()))
		err := processor.ToLocal(src, dst)
		if err != nil {
			return err
		}
		utils.InfoWithFormat("[AppleMusic] 📦 已整理: %s", dst)
	}
	// 清除临时目录
	err := processor.RemoveTempDir(ncm.tempDir)
	if err != nil {
		return err
	}
	return nil
}

// 整理到webdav
func (ncm *NetEaseProcessor) tidyToWebDAV(files []os.DirEntry, webdav *core.WebDAV) error {
	if webdav == nil {
		_ = processor.RemoveTempDir(ncm.tempDir)
		return errors.New("WebDAV 未初始化")
	}

	for _, f := range files {
		if !utils.FilterMusicFile(f, ncm.EncryptedExts(), ncm.DecryptedExts()) {
			utils.DebugWithFormat("[AppleMusic] 跳过非音乐文件: %s", f.Name())
			continue
		}

		filePath := filepath.Join(ncm.tempDir, f.Name())
		if err := webdav.Upload(filePath); err != nil {
			utils.WarnWithFormat("[AppleMusic] ☁️ 上传失败 %s: %v", f.Name(), err)
			continue
		}
		utils.InfoWithFormat("[AppleMusic] ☁️ 已上传: %s", f.Name())
	}
	// 清除临时目录
	err := processor.RemoveTempDir(ncm.tempDir)
	if err != nil {
		return err
	}
	return nil
}
