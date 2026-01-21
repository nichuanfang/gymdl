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
	cfg          *config.Config    //配置
	headers      map[string]string // 请求头
	client       *http.Client
	musicKeyPath string //musickey文件路径
	musicId      int    //api请求必须
	musicKey     string //api请求必须
}

type MusickeyData struct {
	RefreshKey   string `json:"refresh_key"`
	RefreshToken string `json:"refresh_token"`
	Musicid      int    `json:"musicid"`
	Musickey     string `json:"musickey"`
	ExpiredAt    int64  `json:"expired_at"`
}

type QQRefreshResponse struct {
	Code      int          `json:"code"`
	Message   string       `json:"message"`
	Data      MusickeyData `json:"data"`
	Timestamp int64        `json:"timestamp"`
}

// ⚙️ QQ 音乐常见短链/跳转域名（后缀匹配）
var redirectHostSuffixes = []string{
	"y.qq.com",
	"c.y.qq.com",
	"c6.y.qq.com",
	"music.qq.com",
}

// Init 初始化（只使用自建 API）
func (qm *QQMusicProcessor) Init(cfg *config.Config) {
	qm.cfg = cfg
	qm.songs = make([]*SongInfo, 0)
	qm.tempDir = processor.BuildOutputDir(QQTempDir)
	qm.client = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: false,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	qmApi := &QQMusicAPI{
		cfg:          cfg,
		client:       qm.client,
		musicKeyPath: filepath.Join("data", "temp", "musickey.json"),
	}
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
	var musicId int
	var musicKey string
	var err error
	//加载密钥
	if musicId, musicKey, err = qmApi.ensureValidMusickey(); err != nil {
		return err
	}
	qmApi.musicId = musicId
	qmApi.musicKey = musicKey
	//初始化请求头
	qmApi.initHeaders(qmApi.cfg)
	err = processor.CreateOutputDir(qm.tempDir)
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
	utils.InfoWithFormat("[QQMusic] 🎵 开始下载单曲: %s", musicid)

	songData, err := qmApi.querySong(musicid)
	if err != nil {
		utils.ErrorWithFormat("[QQMusic] ❌ 查询歌曲信息失败: %v", err)
		_ = processor.RemoveTempDir(qm.tempDir)
		return err
	}
	utils.DebugWithFormat("[QQMusic] 歌曲信息: Title=%s, Singer=%s, Album=%s", songData.Title, songData.Singer[0].Name, songData.Album.Name)

	mid := songData.Mid
	fileMetadata, err := utils.ParseQQFileMetadate(songData.File, songData.Interval)
	if err != nil {
		utils.ErrorWithFormat("[QQMusic] ❌ 解析文件元数据失败: %v", err)
		_ = processor.RemoveTempDir(qm.tempDir)
		return err
	}
	utils.DebugWithFormat("[QQMusic] 文件元数据: %+v", fileMetadata)

	songUrl, err := qmApi.getSongUrl(mid, fileMetadata.Quality)
	if err != nil {
		utils.ErrorWithFormat("[QQMusic] ❌ 获取下载链接失败: %v", err)
		_ = processor.RemoveTempDir(qm.tempDir)
		return err
	}
	utils.DebugWithFormat("[QQMusic] 下载链接: %s", songUrl)

	sanitizeFileName := qm.safeFileName(songData.Title, songData.Singer[0].Name, fileMetadata.Ext)
	tempPath := filepath.Join(qm.tempDir, sanitizeFileName)
	utils.InfoWithFormat("[QQMusic] ⬇️ 开始下载文件: %s", tempPath)

	// 并发下载封面和歌词
	var coverUrl string
	var lyric string
	var wg sync.WaitGroup
	var coverErr, lyricErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		utils.DebugWithFormat("[QQMusic] 🔍 请求封面URL: %s", songData.Album.Mid)
		coverUrl, coverErr = qmApi.queryCoverUrl(songData.Album.Mid)
		if coverErr != nil {
			utils.WarnWithFormat("[QQMusic] ⚠️ 获取封面失败: %v", coverErr)
		} else {
			utils.DebugWithFormat("[QQMusic] ✅ 获取封面URL成功: %s", coverUrl)
		}
	}()
	go func() {
		defer wg.Done()
		utils.DebugWithFormat("[QQMusic] 🔍 请求歌词: %s", mid)
		lyric, lyricErr = qmApi.queryLyric(mid)
		if lyricErr != nil {
			utils.WarnWithFormat("[QQMusic] ⚠️ 获取歌词失败: %v", lyricErr)
		} else {
			utils.DebugWithFormat("[QQMusic] ✅ 获取歌词成功，长度=%d", len(lyric))
		}
	}()

	// 下载音乐文件（主任务）
	downloadStart := time.Now()
	if err := utils.DownloadFile(qmApi.client, songUrl, tempPath); err != nil {
		utils.ErrorWithFormat("[QQMusic] ❌ 下载音乐文件失败: %v", err)
		_ = processor.RemoveTempDir(qm.tempDir)
		return err
	}
	utils.InfoWithFormat("[QQMusic] ✅ 文件下载完成（耗时 %v）", time.Since(downloadStart).Truncate(time.Millisecond))

	wg.Wait()
	if coverErr != nil {
		return coverErr
	}
	if lyricErr != nil {
		return lyricErr
	}

	// 下载封面
	tempCoverPath := filepath.Join(qm.tempDir, qm.safeCoverFileName(songData.Title, songData.Singer[0].Name))
	utils.DebugWithFormat("[QQMusic] ⬇️ 下载封面: %s", tempCoverPath)
	if err := utils.DownloadFile(qmApi.client, coverUrl, tempCoverPath); err != nil {
		utils.WarnWithFormat("[QQMusic] ⚠️ 下载封面失败: %v", err)
	} else {
		utils.DebugWithFormat("[QQMusic] ✅ 封面下载成功")
	}

	qmApi.updateSongInfo(qm, songData, fileMetadata, lyric)
	utils.InfoWithFormat("[QQMusic] ✅ 单曲下载完成（总耗时 %v）", time.Since(start).Truncate(time.Millisecond))
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
	start := time.Now()
	request, err := qm.newGetRequest(endpoint, params)
	if err != nil {
		utils.ErrorWithFormat("[QQMusicAPI] ❌ 构建请求失败: %v", err)
		return nil, err
	}
	utils.DebugWithFormat("[QQMusicAPI] 🌐 请求开始: %s?%s", endpoint, request.URL.RawQuery)

	resp, err := qm.client.Do(request)
	if err != nil {
		utils.ErrorWithFormat("[QQMusicAPI] ❌ 请求失败 (%s): %v", endpoint, err)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		utils.ErrorWithFormat("[QQMusicAPI] ❌ 读取响应失败: %v", err)
		return nil, err
	}
	utils.DebugWithFormat("[QQMusicAPI] ⏱️ 请求完成: %s (耗时 %v, 状态码=%d)", endpoint, time.Since(start).Truncate(time.Millisecond), resp.StatusCode)

	result := &QQApiResponse[T]{}
	if err := json.Unmarshal(body, result); err != nil {
		utils.ErrorWithFormat("[QQMusicAPI] ❌ JSON解析失败: %v", err)
		return nil, err
	}

	if result.Code != http.StatusOK {
		utils.ErrorWithFormat("[QQMusicAPI] ❌ 请求返回错误: Code=%d, Msg=%s", result.Code, result.Message)
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
	}
	headers["Cookie"] = fmt.Sprintf(
		"musicid=%d;musickey=%s",
		qmApi.musicId,
		qmApi.musicKey,
	)
	utils.DebugWithFormat("[QQMusic] 请求头: %+v", headers)
	qmApi.headers = headers
}

// newGetRequest 创建请求
func (qmApi *QQMusicAPI) newGetRequest(path string, params map[string]string) (*http.Request, error) {
	u, err := url.Parse(qmApi.cfg.QQMusicApiConfig.Endpoint + path)
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

/* ------------------------ 凭证续签 ------------------------ */

// ensureValidMusickey 确保musickey有效 返回值: music_id music_key 错误
func (qmApi *QQMusicAPI) ensureValidMusickey() (int, string, error) {
	if !qmApi.cfg.QQMusicApiConfig.Enable {
		return 0, "", errors.New("QQ音乐Api服务不可用")
	}
	data, err := qmApi.loadMusickey(qmApi.musicKeyPath)
	if errors.Is(err, os.ErrNotExist) || data == nil {
		utils.InfoWithFormat("musickey 不存在，立即刷新...")
		return qmApi.refreshAndSave(&MusickeyData{})
	} else if err != nil {
		return 0, "", fmt.Errorf("读取 musickey 失败: %w", err)
	}
	if qmApi.isExpired(data) {
		utils.InfoWithFormat("musickey 即将过期，刷新中...")
		return qmApi.refreshAndSave(data)
	}
	//未过期
	return data.Musicid, data.Musickey, nil
}

// loadMusickey 读取 musickey.json
func (qmApi *QQMusicAPI) loadMusickey(path string) (*MusickeyData, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var data MusickeyData
	if err = json.Unmarshal(bytes, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// isExpired 判断本地时间戳是否过期
func (qmApi *QQMusicAPI) isExpired(data *MusickeyData) bool {
	return time.Now().Unix() >= data.ExpiredAt-300
}

// refreshAndSave 刷新并保存到文件
func (qmApi *QQMusicAPI) refreshAndSave(data *MusickeyData) (int, string, error) {
	request, err := http.NewRequest(http.MethodGet, qmApi.cfg.QQMusicApiConfig.Endpoint+"/login/api_refresh_cookies", nil)
	if err != nil {
		return 0, "", err
	}
	//处理请求头
	headers := qmApi.refreshMusicKeyHeaders(qmApi.cfg, data)
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	resp, err := qmApi.client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	var newData QQRefreshResponse
	if err = json.NewDecoder(resp.Body).Decode(&newData); err != nil {
		return 0, "", err
	}
	bytes, _ := json.MarshalIndent(newData.Data, "", "  ")
	err = os.WriteFile(qmApi.musicKeyPath, bytes, 0644)
	if err != nil {
		return 0, "", err
	}
	//刷新musickey后的新值
	return newData.Data.Musicid, newData.Data.Musickey, nil
}

// refreshMusicKeyHeaders 刷新musickey的请求头
func (qmApi *QQMusicAPI) refreshMusicKeyHeaders(cfg *config.Config, data *MusickeyData) map[string]string {
	headers := map[string]string{
		"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
		"Referer":    "https://y.qq.com/",
	}
	if cfg.QQMusicApiConfig.EnableSign {
		headers["X-Enable-Sign"] = "true"
	}
	if data.Musickey != "" {
		//设置Cookie
		switch cfg.QQMusicApiConfig.LoginType {
		case 1:
			//wx
			headers["Cookie"] = fmt.Sprintf("login_type=1;musicid=%d;musickey=%s;refresh_key=%s;refresh_token=%s",
				data.Musicid,
				data.Musickey,
				data.RefreshKey,
				data.RefreshToken,
			)
		case 2:
			//qq
			headers["Cookie"] = fmt.Sprintf("login_type=2;musicid=%d;musickey=%s;refresh_key=%s;refresh_token=%s",
				data.Musicid,
				data.Musickey,
				data.RefreshKey,
				data.RefreshToken,
			)
		}
	} else {
		//读取cookiecloud同步的cookies文件
		cookiePath := filepath.Join(cfg.CookieCloud.CookieFilePath, cfg.CookieCloud.CookieFile)
		qqCookies := utils.GetCookiesByDomain(cookiePath, ".qq.com")
		qqmusicKey := qqCookies["qqmusic_key"]
		if qqmusicKey == "" {
			qqmusicKey = cfg.QQMusicApiConfig.MusicKey
		}
		//设置Cookie
		switch cfg.QQMusicApiConfig.LoginType {
		case 1:
			//wx
			wxuin := qqCookies["wxuin"]
			if wxuin == "" {
				wxuin = cfg.QQMusicApiConfig.MusicId
			}
			headers["Cookie"] = fmt.Sprintf("login_type=1;musicid=%s;musickey=%s;refresh_key=%s;refresh_token=%s",
				wxuin,
				qqmusicKey,
				cfg.QQMusicApiConfig.RefreshKey,
				cfg.QQMusicApiConfig.RefreshToken,
			)
		case 2:
			//qq
			uin := qqCookies["uin"]
			if uin != "" {
				uin = cfg.QQMusicApiConfig.MusicId
			}
			headers["Cookie"] = fmt.Sprintf("login_type=2;musicid=%s;musickey=%s;refresh_key=%s;refresh_token=%s",
				uin,
				qqmusicKey,
				cfg.QQMusicApiConfig.RefreshKey,
				cfg.QQMusicApiConfig.RefreshToken,
			)
		}
	}
	return headers
}

/* ------------------------ 拓展方法 ------------------------ */

// parseQQMusicLink 解析 QQ 音乐分享链接，自动识别跳转并返回最终 ID
func (qm *QQMusicProcessor) parseQQMusicLink(raw string) (QQMusicLink, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return QQMusicLink{}, err
	}

	hostname := u.Hostname()
	needRedirect := false
	for _, suffix := range redirectHostSuffixes {
		if strings.HasSuffix(hostname, suffix) {
			needRedirect = true
			break
		}
	}

	// 1️⃣ 优先尝试直接解析
	result := qm.tryParseDirect(raw)
	if result.id != "" {
		return result, nil
	}

	// 2️⃣ 需要跳转的域名 → 获取最终 URL 再解析
	if needRedirect {
		finalURL, err := qm.getFinalURL(raw)
		if err != nil {
			return QQMusicLink{}, fmt.Errorf("重定向失败: %w", err)
		}
		return qm.tryParseDirect(finalURL), nil
	}

	// 3️⃣ 兜底：再试一次最终 URL
	finalURL, err := qm.getFinalURL(raw)
	if err != nil {
		return QQMusicLink{}, err
	}
	return qm.tryParseDirect(finalURL), nil
}

// getFinalURL 返回最终重定向后的真实 URL。
func (qm *QQMusicProcessor) getFinalURL(raw string) (string, error) {
	start := time.Now()

	utils.DebugWithFormat("[QQMusic] 🔗 检查重定向: %s", raw)

	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
    req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
    req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")

	resp, err := qm.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.Request == nil || resp.Request.URL == nil {
		return "", errors.New("无法解析最终 URL")
	}

	finalURL := resp.Request.URL.String()
	utils.DebugWithFormat("[QQMusic] ✅ 最终URL: %s (耗时 %v)", finalURL, time.Since(start).Truncate(time.Millisecond))
	return finalURL, nil
}

// tryParseDirect 直接解析
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
