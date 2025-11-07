package music

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
)

/* ---------------------- 结构体与构造方法 ---------------------- */

type QQMusicProcessor struct {
	cfg     *config.Config
	tempDir string
	songs   []*SongInfo
	qmApi   *QQMusicAPI
	client  *http.Client // http请求池
}

type QQMusicLink struct {
	id     string // 歌曲或者歌单id
	isSong bool   // 是否为歌曲
}

type QQApiResponse[T any] struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      T      `json:"data"`
	Timestamp int64  `json:"timestamp"`
}

type QQSong struct {
	Mid    string `json:"mid"`
	Name   string `json:"name"`
	Title  string `json:"title"`
	Singer []struct {
		Name string `json:"name"`
	} `json:"singer"`
	Album struct {
		Name string `json:"name"`
		Mid  string `json:"mid"`
	} `json:"album"`
	Interval   int              `json:"interval"`
	IsOnly     int              `json:"isonly"`
	Language   int              `json:"language"`
	Genre      int              `json:"genre"`
	IndexCD    int              `json:"index_cd"`
	IndexAlbum int              `json:"index_album"`
	TimePublic string           `json:"time_public"`
	File       utils.QQFileInfo `json:"file"`
}

type QQPlaylist struct {
	TotalSongNum int      `json:"total_song_num"`
	SonglistSize int      `json:"songlist_size"`
	Songlist     []QQSong `json:"songlist"`
}

// QQMusicAPI 自建qq-music-api
type QQMusicAPI struct {
	baseUrl string            // 服务端点
	headers map[string]string // 请求头
	client  *http.Client
}

// 已知需要重定向的域名列表
var needRedirectHosts = map[string]bool{
	"c6.y.qq.com": true,
}

// Init 初始化（只使用自建 API）
func (qm *QQMusicProcessor) Init(cfg *config.Config) {
	qm.cfg = cfg
	qm.songs = make([]*SongInfo, 0)
	qm.tempDir = processor.BuildOutputDir(QQTempDir)
	qm.client = &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	qmApi := &QQMusicAPI{
		baseUrl: cfg.QQMusicApiConfig.Endpoint,
		client:  qm.client,
	}
	qmApi.initHeaders(cfg)
	qm.qmApi = qmApi
}

/* ---------------------- 基础接口实现 ---------------------- */

func (qm *QQMusicProcessor) Name() processor.LinkType {
	return processor.LinkQQMusic
}

func (qm *QQMusicProcessor) Songs() []*SongInfo {
	return qm.songs
}

/* ------------------------ 下载逻辑 ------------------------ */

func (qm *QQMusicProcessor) DownloadMusic(url string, callback func(string)) error {
	var err error
	var qqMusicLink QQMusicLink
	if !qm.cfg.QQMusicApiConfig.Enable {
		return errors.New("QQMusicApiConfig is disabled")
	}
	qqMusicLink, err = qm.parseQQMusicLink(url)
	if err != nil {
		return err
	}
	err = qm.qmApi.download(qm, qqMusicLink, callback)
	if err != nil {
		return err
	}
	return nil
}

func (qm *QQMusicProcessor) DownloadCommand(url string) *exec.Cmd {
	return nil
}

func (qm *QQMusicProcessor) BeforeTidy() error {
	//写入封面 歌词 专辑艺术家 年份
	var fileName string
	var coverFileName string
	for _, song := range qm.songs {
		fileName = filepath.Join(qm.tempDir, qm.safeFileName(song.SongName, song.SongArtists, song.FileExt))
		coverFileName = filepath.Join(qm.tempDir, qm.safeCoverFileName(song.SongName, song.SongArtists))
		err := WriteTagsWithCoverFile(song, fileName, coverFileName)
		if err != nil {
			return err
		}
	}
	return nil
}

func (qm *QQMusicProcessor) NeedRemoveDRM() bool {
	return false
}

func (qm *QQMusicProcessor) DRMRemove() error {
	return nil
}

func (qm *QQMusicProcessor) TidyMusic() error {
	files, err := os.ReadDir(qm.tempDir)
	if err != nil {
		return fmt.Errorf("读取临时目录失败: %w", err)
	}
	if len(files) == 0 {
		utils.WarnWithFormat("[QQMusic] ⚠️ 未找到待整理的音乐文件")
		return errors.New("未找到待整理的音乐文件")
	}

	switch qm.cfg.Tidy.Mode {
	case 1:
		return qm.tidyToLocal(files)
	case 2:
		return qm.tidyToWebDAV(files, core.GlobalWebDAV)
	default:
		return fmt.Errorf("未知整理模式: %d", qm.cfg.Tidy.Mode)
	}
}

func (qm *QQMusicProcessor) EncryptedExts() []string {
	return make([]string, 0)
}

func (qm *QQMusicProcessor) DecryptedExts() []string {
	return []string{".aac", ".m4a", ".flac", ".mp3", ".ogg"}
}

/* ------------------------ qq-music-api ------------------------ */

// download 下载qq音乐
func (qmApi *QQMusicAPI) download(qm *QQMusicProcessor, musicLink QQMusicLink, callback func(string)) error {
	err := processor.CreateOutputDir(qm.tempDir)
	if err != nil {
		return err
	}
	if musicLink.isSong {
		return qmApi.downloadSong(qm, musicLink.id, callback)
	} else {
		return qmApi.downloadSonglist(qm, musicLink.id, callback)
	}
}

// downloadSong 单曲下载
func (qmApi *QQMusicAPI) downloadSong(qm *QQMusicProcessor, musicid string, callback func(string)) error {
	start := time.Now()

	songData, err := qmApi.querySong(musicid)
	if err != nil {
		_ = processor.RemoveTempDir(qm.tempDir)
		return err
	}

	mid := songData.Mid
	fileMetadata, err := utils.ParseQQFileMetadate(songData.File, songData.Interval)
	if err != nil {
		_ = processor.RemoveTempDir(qm.tempDir)
		return err
	}

	songUrl, err := qmApi.getSongUrl(mid, fileMetadata.Quality)
	if err != nil {
		_ = processor.RemoveTempDir(qm.tempDir)
		return err
	}

	sanitizeFileName := qm.safeFileName(songData.Title, songData.Singer[0].Name, fileMetadata.Ext)
	tempPath := filepath.Join(qm.tempDir, sanitizeFileName)

	// 并发下载封面和歌词
	var coverUrl string
	var lyric string
	var wg sync.WaitGroup
	var coverErr, lyricErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		coverUrl, coverErr = qmApi.queryCoverUrl(songData.Album.Mid)
	}()
	go func() {
		defer wg.Done()
		lyric, lyricErr = qmApi.queryLyric(mid)
	}()

	// 下载音乐文件（主任务）
	if err := utils.DownloadFile(qmApi.client, songUrl, tempPath); err != nil {
		_ = processor.RemoveTempDir(qm.tempDir)
		return err
	}

	wg.Wait()
	if coverErr != nil {
		_ = processor.RemoveTempDir(qm.tempDir)
		return coverErr
	}
	if lyricErr != nil {
		_ = processor.RemoveTempDir(qm.tempDir)
		return lyricErr
	}

	// 下载封面
	tempCoverPath := filepath.Join(qm.tempDir, qm.safeCoverFileName(songData.Title, songData.Singer[0].Name))
	if err := utils.DownloadFile(qmApi.client, coverUrl, tempCoverPath); err != nil {
		_ = processor.RemoveTempDir(qm.tempDir)
		return err
	}

	qmApi.updateSongInfo(qm, songData, fileMetadata, lyric)

	utils.InfoWithFormat("[QQMusic] ✅ 下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond))
	callback(fmt.Sprintf("下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond)))
	return nil
}

// downloadSonglist 下载歌单
func (qmApi *QQMusicAPI) downloadSonglist(qm *QQMusicProcessor, songlistId string, callback func(string)) error {
	start := time.Now()
	utils.DebugWithFormat("[QQMusic] 获取歌单数据: ID=%d", songlistId)
	var err error
	songlist, err := qmApi.querySonglist(songlistId)
	if err != nil {
		utils.ErrorWithFormat("[QQMusic] ❌ 获取歌单数据失败: %v", err)
		_ = processor.RemoveTempDir(qm.tempDir)
		return err
	}
	if len(songlist.Songlist) == 0 {
		errMsg := "未获取到有效歌曲信息或歌曲无下载地址"
		utils.ErrorWithFormat("[QQMusic] ❌ %s: 歌单ID=%d", errMsg, songlistId)
		_ = processor.RemoveTempDir(qm.tempDir)
		return errors.New(errMsg)
	}
	utils.InfoWithFormat("[QQMusic] 开始下载歌单: %s (%d首)", songlistId, len(songlist.Songlist))
	callback(fmt.Sprintf("开始下载歌单: %s (%d首)", songlistId, len(songlist.Songlist)))
	for index, song := range songlist.Songlist {
		callback(fmt.Sprintf("开始下载第%d首...", index+1))
		utils.InfoWithFormat("[QQMusic] 正在下载第%d首: %s", index+1, song.Title)
		err = qmApi.downloadPlaylistSong(qm, song, callback)
		if err != nil {
			utils.ErrorWithFormat("[QQMusic] ❌ 歌单下载中断，第%d首下载失败: %v", index+1, err)
			_ = processor.RemoveTempDir(qm.tempDir)
			return err
		}
	}
	utils.InfoWithFormat("[QQMusic] ✅ 歌单下载完成: %s （耗时 %v）", songlistId, time.Since(start).Truncate(time.Millisecond))
	callback(fmt.Sprintf("歌单下载完成: %s （耗时 %v）", songlistId, time.Since(start).Truncate(time.Millisecond)))
	return nil
}

// querySong 查询歌曲信息
func (qmApi *QQMusicAPI) querySong(songId string) (QQSong, error) {
	params := map[string]string{
		"value": songId,
	}
	songRes, err := doGetRequestWithRetry[[]QQSong](qmApi, "/song/query_song", params, 3)
	if err != nil {
		return QQSong{}, err
	}
	if songRes == nil {
		return QQSong{}, nil
	}
	return songRes.Data[0], nil
}

// downloadSong 单曲下载
func (qmApi *QQMusicAPI) downloadPlaylistSong(qm *QQMusicProcessor, songData QQSong, callback func(string)) error {
	start := time.Now()

	mid := songData.Mid
	fileMetadata, err := utils.ParseQQFileMetadate(songData.File, songData.Interval)
	if err != nil {
		return err
	}

	songUrl, err := qmApi.getSongUrl(mid, fileMetadata.Quality)
	if err != nil {
		return err
	}

	sanitizeFileName := qm.safeFileName(songData.Title, songData.Singer[0].Name, fileMetadata.Ext)
	tempPath := filepath.Join(qm.tempDir, sanitizeFileName)

	// 并发下载封面和歌词
	var coverUrl string
	var lyric string
	var wg sync.WaitGroup
	var coverErr, lyricErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		coverUrl, coverErr = qmApi.queryCoverUrl(songData.Album.Mid)
	}()
	go func() {
		defer wg.Done()
		lyric, lyricErr = qmApi.queryLyric(mid)
	}()

	// 下载音乐文件（主任务）
	if err := utils.DownloadFile(qmApi.client, songUrl, tempPath); err != nil {
		return err
	}

	wg.Wait()
	if coverErr != nil {
		return coverErr
	}
	if lyricErr != nil {
		return lyricErr
	}

	// 下载封面
	tempCoverPath := filepath.Join(qm.tempDir, qm.safeCoverFileName(songData.Title, songData.Singer[0].Name))
	if err := utils.DownloadFile(qmApi.client, coverUrl, tempCoverPath); err != nil {
		return err
	}

	qmApi.updateSongInfo(qm, songData, fileMetadata, lyric)

	utils.InfoWithFormat("[QQMusic] ✅ 下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond))
	callback(fmt.Sprintf("下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond)))
	return nil
}

// querySong 查询歌单信息
func (qmApi *QQMusicAPI) querySonglist(songlistId string) (QQPlaylist, error) {
	params := map[string]string{
		"songlist_id": songlistId, //歌单id
		"num":         "20",       //最多20首
		"page":        "1",        //页码
		"onlysong":    "true",     //是否仅返回歌曲信息
	}
	playlistRes, err := doGetRequestWithRetry[QQPlaylist](qmApi, "/songlist/get_detail", params, 3)
	if err != nil {
		return QQPlaylist{}, err
	}
	if playlistRes == nil {
		return QQPlaylist{}, nil
	}
	return playlistRes.Data, nil
}

// queryCoverUrl 获取专辑封面url
func (qmApi *QQMusicAPI) queryCoverUrl(albumMid string) (string, error) {
	params := map[string]string{
		"mid": albumMid,
	}
	coverRes, err := doGetRequestWithRetry[string](qmApi, "/album/get_cover", params, 3)
	if err != nil {
		return "", err
	}
	if coverRes == nil {
		return "", nil
	}
	return coverRes.Data, nil
}

// queryLyric 查询歌词
func (qmApi *QQMusicAPI) queryLyric(mid string) (string, error) {
	params := map[string]string{
		"value": mid,
		"trans": "false",
	}
	lyricRes, err := doGetRequestWithRetry[map[string]string](qmApi, "/lyric/get_lyric", params, 3)
	if err != nil {
		return "", err
	}
	if lyricRes == nil {
		return "", nil
	}
	return lyricRes.Data["lyric"], nil
}

// getSongUrl 获取歌曲下载链接
func (qmApi *QQMusicAPI) getSongUrl(songMid string, fileType string) (string, error) {
	params := map[string]string{
		"mid":       songMid,
		"file_type": fileType,
	}
	songUrlsRes, err := doGetRequestWithRetry[map[string]string](qmApi, "/song/get_song_urls", params, 3)
	if err != nil {
		return "", err
	}
	if songUrlsRes == nil {
		return "", err
	}
	return songUrlsRes.Data[songMid], nil
}

// doGetRequest 执行请求
func doGetRequest[T any](qm *QQMusicAPI, endpoint string, params map[string]string) (*QQApiResponse[T], error) {
	request, err := qm.newGetRequest(endpoint, params)
	if err != nil {
		return nil, err
	}

	resp, err := qm.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	result := &QQApiResponse[T]{}
	if err := json.Unmarshal(body, result); err != nil {
		return nil, err
	}

	if result.Code != http.StatusOK {
		return nil, errors.New(result.Message)
	}

	return result, nil
}

// doGetRequestWithRetry 带自动重试的请求
func doGetRequestWithRetry[T any](qm *QQMusicAPI, endpoint string, params map[string]string, retries int) (*QQApiResponse[T], error) {
	var lastErr error
	for i := 0; i < retries; i++ {
		res, err := doGetRequest[T](qm, endpoint, params)
		if err == nil {
			return res, nil
		}
		lastErr = err
		time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
	}
	return nil, lastErr
}

// initHeaders 初始化请求头
func (qmApi *QQMusicAPI) initHeaders(cfg *config.Config) {
	headers := map[string]string{
		"User-Agent": UserAgent,
		"Referer":    "https://y.qq.com/",
	}
	if cfg.QQMusicApiConfig.EnableSign {
		headers["X-Enable-Sign"] = "true"
		headers["X-Enable-Cache"] = "true"
	}

	cookiePath := filepath.Join(cfg.CookieCloud.CookieFilePath, cfg.CookieCloud.CookieFile)
	qqCookies := utils.GetCookiesByDomain(cookiePath, ".qq.com")

	switch cfg.QQMusicApiConfig.LoginType {
	case 1:
		headers["Cookie"] = fmt.Sprintf("musicid=%s;musickey=%s", qqCookies["wxuin"], qqCookies["qqmusic_key"])
	case 2:
		headers["Cookie"] = fmt.Sprintf("musicid=%s;musickey=%s", qqCookies["uin"], qqCookies["qqmusic_key"])
	}
	qmApi.headers = headers
}

// newGetRequest 创建请求
func (qmApi *QQMusicAPI) newGetRequest(path string, params map[string]string) (*http.Request, error) {
	u, err := url.Parse(qmApi.baseUrl + path)
	if err != nil {
		return nil, err
	}

	if params != nil {
		q := u.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	for k, v := range qmApi.headers {
		req.Header.Set(k, v)
	}

	return req, nil
}

// updateSongInfo 更新元信息
func (qmApi *QQMusicAPI) updateSongInfo(qm *QQMusicProcessor, data QQSong, fileMetadata utils.QQMusicFileMetadata, lyric string) {
	tidyType := processor.DetermineTidyType(qm.cfg)
	if lyric == "" {
		lyric = "[00:00:00]此歌曲为没有填词的纯音乐，请您欣赏"
	}
	//组装元信息
	songInfo := &SongInfo{
		SongName:        data.Title,
		SongArtists:     data.Singer[0].Name,
		SongAlbum:       data.Album.Name,
		SongAlbumArtist: data.Singer[0].Name,
		Year:            utils.ParseQQYear(data.TimePublic),
		FileExt:         fileMetadata.Ext,
		Bitrate:         strconv.Itoa(fileMetadata.Bitrate),
		MusicSize:       fileMetadata.MusicSize,
		Duration:        data.Interval,
		Lyric:           lyric,
		Tidy:            tidyType,
	}
	qm.songs = append(qm.songs, songInfo)
}

/* ------------------------ 拓展方法 ------------------------ */

// parseQQMusicLink 解析qq分享链接
func (qm *QQMusicProcessor) parseQQMusicLink(raw string) (QQMusicLink, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return QQMusicLink{}, err
	}

	var result QQMusicLink

	if needRedirectHosts[u.Host] {
		location, err := qm.getRedirectLocation(raw)
		if err != nil {
			return QQMusicLink{}, err
		}
		result = qm.tryParseDirect(location)
		return result, nil
	}

	result = qm.tryParseDirect(raw)
	if result.id != "" {
		return result, nil
	}

	location, err := qm.getRedirectLocation(raw)
	if err != nil {
		return QQMusicLink{}, err
	}
	result = qm.tryParseDirect(location)
	return result, nil
}

func (qm *QQMusicProcessor) getRedirectLocation(link string) (string, error) {
	const maxRedirects = 10
	currentURL := link

	for i := 0; i < maxRedirects; i++ {
		req, err := http.NewRequest("GET", currentURL, nil)
		if err != nil {
			return "", err
		}

		req.Header.Set("User-Agent", UserAgent)

		resp, err := qm.client.Do(req)
		if err != nil {
			return "", err
		}

		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			return currentURL, nil
		}

		loc := resp.Header.Get("Location")
		if loc == "" {
			return currentURL, nil
		}

		u, err := url.Parse(loc)
		if err != nil {
			return "", err
		}
		if !u.IsAbs() {
			base, _ := url.Parse(currentURL)
			loc = base.ResolveReference(u).String()
		}

		currentURL = loc
	}

	return "", fmt.Errorf("too many redirects (> %d)", maxRedirects)
}

func (qm *QQMusicProcessor) tryParseDirect(raw string) QQMusicLink {
	u, err := url.Parse(raw)
	if err != nil {
		return QQMusicLink{}
	}

	q := u.Query()
	path := u.Path

	var result QQMusicLink

	switch {
	case strings.Contains(path, "/playlist/") || q.Has("id"):
		result.isSong = false
		result.id = getFirstNonEmpty(q.Get("id"), lastPathSegment(path))
	case strings.Contains(path, "/songDetail/") || q.Has("songid"):
		result.isSong = true
		result.id = getFirstNonEmpty(q.Get("songid"), lastPathSegment(path))
	default:
		if sid := q.Get("songid"); sid != "" {
			result.isSong = true
			result.id = sid
		} else if pid := q.Get("id"); pid != "" {
			result.isSong = false
			result.id = pid
		}
	}

	return result
}

func lastPathSegment(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) > 0 {
		return segs[len(segs)-1]
	}
	return ""
}

func getFirstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// 整理到本地
func (qm *QQMusicProcessor) tidyToLocal(files []os.DirEntry) error {
	dstDir := qm.cfg.Tidy.DistDir
	if dstDir == "" {
		_ = processor.RemoveTempDir(qm.tempDir)
		return errors.New("未配置输出目录")
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		_ = processor.RemoveTempDir(qm.tempDir)
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	for _, f := range files {
		if !utils.FilterMusicFile(f, qm.EncryptedExts(), qm.DecryptedExts()) {
			utils.DebugWithFormat("[QQMusic] 跳过非音乐文件: %s", f.Name())
			continue
		}
		src := filepath.Join(qm.tempDir, f.Name())
		dst := filepath.Join(dstDir, utils.SanitizeFileName(f.Name()))
		if err := processor.ToLocal(src, dst); err != nil {
			return err
		}
		utils.InfoWithFormat("[QQMusic] 📦 已整理: %s", dst)
	}
	return processor.RemoveTempDir(qm.tempDir)
}

// 整理到webdav
func (qm *QQMusicProcessor) tidyToWebDAV(files []os.DirEntry, webdav *core.WebDAV) error {
	if webdav == nil {
		_ = processor.RemoveTempDir(qm.tempDir)
		return errors.New("WebDAV 未初始化")
	}

	for _, f := range files {
		if !utils.FilterMusicFile(f, qm.EncryptedExts(), qm.DecryptedExts()) {
			utils.DebugWithFormat("[QQMusic] 跳过非音乐文件: %s", f.Name())
			continue
		}
		filePath := filepath.Join(qm.tempDir, f.Name())
		if err := webdav.Upload(filePath); err != nil {
			utils.WarnWithFormat("[QQMusic] ☁️ 上传失败 %s: %v", f.Name(), err)
			continue
		}
		utils.InfoWithFormat("[QQMusic] ☁️ 已上传: %s", f.Name())
	}
	return processor.RemoveTempDir(qm.tempDir)
}

// safeFileName 合法的文件名
func (qm *QQMusicProcessor) safeFileName(songName string, songArtists string, fileExt string) string {
	return fmt.Sprintf("%s - %s.%s",
		utils.SanitizeFileName(songArtists),
		utils.SanitizeFileName(songName),
		fileExt)
}

// safeCoverFileName 合法的封面文件名
func (qm *QQMusicProcessor) safeCoverFileName(songName string, songArtists string) string {
	return fmt.Sprintf("%s - %s.%s",
		utils.SanitizeFileName(songArtists),
		utils.SanitizeFileName(songName)+"_cover", "jpg")
}
