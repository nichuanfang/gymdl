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
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
)

/* ---------------------- 结构体与构造方法 ---------------------- */

type QQMusicProcessor struct {
	cfg         *config.Config
	tempDir     string
	songs       []*SongInfo
	apiProvider QQApiProvider // api提供者
	client      *http.Client  // http请求池
}

type QQMusicLink struct {
	id     string // 歌曲或者歌单id
	isSong bool   // 是否为歌曲
}

type QQApiProvider interface {
	download(url QQMusicLink, dir string, callback func(string)) error
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

// QQMusicAPI 自建qq-music-api
type QQMusicAPI struct {
	baseUrl string            // 服务端点
	headers map[string]string // 请求头
	client  *http.Client
}

// LXMusicAPI 洛雪api
type LXMusicAPI struct {
	baseUrl    string            // 服务端点
	apiVersion string            // 接口版本
	key        string            // 密钥
	headers    map[string]string // 请求头
	client     *http.Client
}

// 已知需要重定向的域名列表
var needRedirectHosts = map[string]bool{
	"c6.y.qq.com": true,
}

// Init  初始化
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
	if cfg.QQMusicApiConfig.Enable {
		qmApi := &QQMusicAPI{
			baseUrl: cfg.QQMusicApiConfig.Endpoint,
			client:  qm.client,
		}
		qmApi.initHeaders(cfg)
		qm.apiProvider = qmApi
	} else {
		// 默认启用洛雪
		qm.apiProvider = &LXMusicAPI{
			client: qm.client,
		}
	}
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
	err = qm.apiProvider.download(qqMusicLink, qm.tempDir, callback)
	if err != nil {
		return err
	}
	return nil
}

func (qm *QQMusicProcessor) DownloadCommand(url string) *exec.Cmd {
	return nil
}

func (qm *QQMusicProcessor) BeforeTidy() error {
	songs, err := ReadMusicDir(qm.tempDir, processor.DetermineTidyType(qm.cfg), qm)
	if err != nil {
		return err
	}
	// 更新元信息列表
	qm.songs = songs
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
func (qm *QQMusicAPI) download(musicLink QQMusicLink, tempDir string, callback func(string)) error {
	err := processor.CreateOutputDir(tempDir)
	if err != nil {
		return err
	}
	if musicLink.isSong {
		return qm.downloadSong(musicLink.id, tempDir, callback)
	} else {
		return qm.downloadSonglist(musicLink.id, tempDir, callback)
	}
}

// download 下载单曲
func (qm *QQMusicAPI) downloadSong(musicid string, tempDir string, callback func(string)) error {
	start := time.Now()
	songData, err := qm.querySong(musicid)
	if err != nil {
		return err
	}
	// 获取mid
	mid := songData.Mid
	// 获取最高音质的文件类型
	fileData, err := json.Marshal(songData.File)
	if err != nil {
		return err
	}
	quality, ext, err := utils.GetBestQuality(fileData)
	if err != nil {
		return err
	}
	// ----------------下载音乐------------------------
	// 获取下载链接
	songUrl, err := qm.getSongUrl(mid, string(quality))
	if err != nil {
		return err
	}
	sanitizeFileName := utils.SanitizeFileName(songData.Title)
	tempPath := filepath.Join(tempDir, sanitizeFileName+ext)
	// 下载单曲
	err = utils.DownloadFile(songUrl, tempPath)
	if err != nil {
		return err
	}
	// ----------------下载封面图片------------------------

	// ----------------获取歌词------------------------

	// ----------------更新歌曲元信息------------------------

	utils.InfoWithFormat("[QQMusic] ✅ 下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond))
	callback(fmt.Sprintf("下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond)))
	return nil
}

// download 下载歌单
func (qm *QQMusicAPI) downloadSonglist(url string, tempDir string, callback func(string)) error {
	return nil
}

// querySong 查询歌曲信息
func (qm *QQMusicAPI) querySong(songId string) (QQSong, error) {
	params := map[string]string{
		"value": songId,
	}
	songRes, err := doGetRequest[[]QQSong](qm, "/song/query_song", params)
	if err != nil {
		return QQSong{}, err
	}
	return songRes.Data[0], nil
}

// querySong 查询专辑封面url
func (qm *QQMusicAPI) queryCoverUrl(albumMid string) (string, error) {
	params := map[string]string{
		"mid": albumMid,
	}
	coverRes, err := doGetRequest[string](qm, "/album/get_cover", params)
	if err != nil {
		return "", err
	}
	return coverRes.Data, nil
}

// querySong 查询歌词
func (qm *QQMusicAPI) queryLyric(mid string) (string, error) {
	params := map[string]string{
		"value": mid,
		"trans": "false",
	}
	lyricRes, err := doGetRequest[map[string]string](qm, "/lyric/get_lyric", params)
	if err != nil {
		return "", err
	}
	return lyricRes.Data["lyric"], nil
}

// querySong 查询歌曲下载链接
func (qm *QQMusicAPI) getSongUrl(songMid string, fileType string) (string, error) {
	params := map[string]string{
		"mid":       songMid,
		"file_type": fileType,
	}
	songUrlsRes, err := doGetRequest[map[string]string](qm, "/song/get_song_urls", params)
	if err != nil {
		return "", err
	}
	return songUrlsRes.Data[songMid], nil
}

// doGetRequest 发送QQMusicApi请求
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

// initHeaders 初始化请求头
func (qm *QQMusicAPI) initHeaders(cfg *config.Config) {
	headers := map[string]string{
		"User-Agent": UserAgent,
		"Referer":    "https://y.qq.com/",
	}
	if cfg.QQMusicApiConfig.EnableSign {
		headers["X-Enable-Sign"] = "true"
	}
	if cfg.QQMusicApiConfig.EnableSign {
		headers["X-Enable-Cache"] = "true"
	}
	// 获取cookie
	qqCookies := utils.GetCookiesByDomain(filepath.Join(cfg.CookieCloud.CookieFilePath, cfg.CookieCloud.CookieFile), ".qq.com")
	switch cfg.QQMusicApiConfig.LoginType {
	case 1:
		// 微信
		headers["Cookie"] = fmt.Sprintf("musicid=%s;musickey=%s", qqCookies["wxuin"], qqCookies["qqmusic_key"])
	case 2:
		// qq
		headers["Cookie"] = fmt.Sprintf("musicid=%s;musickey=%s", qqCookies["uin"], qqCookies["qqmusic_key"])
	}
	qm.headers = headers
}

// newGetRequest 创建请求
func (qm *QQMusicAPI) newGetRequest(path string, params map[string]string) (*http.Request, error) {
	// 构造 URL
	u, err := url.Parse(qm.baseUrl + path)
	if err != nil {
		return nil, err
	}

	// 添加 GET 参数
	if params != nil {
		q := u.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	// 创建请求
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	// 设置 headers
	for k, v := range qm.headers {
		req.Header.Set(k, v)
	}

	return req, nil
}

/* ------------------------ 洛雪 ------------------------ */

// download 下载qq音乐
func (lx *LXMusicAPI) download(url QQMusicLink, tempDir string, callback func(string)) error {
	// todo
	return nil
}

/* ------------------------ 拓展方法 ------------------------ */

// parseQQMusicLink 解析 QQ 音乐短链
func (qm *QQMusicProcessor) parseQQMusicLink(raw string) (QQMusicLink, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return QQMusicLink{}, err
	}

	var result QQMusicLink

	// 1. 域名判断：已知短链入口必须重定向
	if needRedirectHosts[u.Host] {
		location, err := qm.getRedirectLocation(raw)
		if err != nil {
			return QQMusicLink{}, err
		}
		result = qm.tryParseDirect(location)
		return result, nil
	}

	// 2. 尝试直接解析
	result = qm.tryParseDirect(raw)
	if result.id != "" {
		return result, nil
	}

	// 3. 兜底：直接解析失败，再走一次重定向
	location, err := qm.getRedirectLocation(raw)
	if err != nil {
		return QQMusicLink{}, err
	}
	result = qm.tryParseDirect(location)
	return result, nil
}

// getRedirectLocation 获取重定向的Location
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

		// 如果不是重定向，返回当前 URL
		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			return currentURL, nil
		}

		loc := resp.Header.Get("Location")
		if loc == "" {
			return currentURL, nil
		}

		// 处理相对路径
		u, err := url.Parse(loc)
		if err != nil {
			return "", err
		}
		if !u.IsAbs() {
			base, err := url.Parse(currentURL)
			if err != nil {
				return "", err
			}
			loc = base.ResolveReference(u).String()
		}

		currentURL = loc
	}

	return "", fmt.Errorf("too many redirects (> %d)", maxRedirects)
}

// tryParseDirect 尝试直接从 URL 提取 ID 和类型
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

// lastPathSegment 获取 path 最后一个非空片段
func lastPathSegment(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) > 0 {
		return segs[len(segs)-1]
	}
	return ""
}

// getFirstNonEmpty 从两个候选值中取第一个非空值
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
		err := processor.ToLocal(src, dst)
		if err != nil {
			return err
		}
		utils.InfoWithFormat("[QQMusic] 📦 已整理: %s", dst)
	}
	// 清除临时目录
	err := processor.RemoveTempDir(qm.tempDir)
	if err != nil {
		return err
	}
	return nil
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
	// 清除临时目录
	err := processor.RemoveTempDir(qm.tempDir)
	if err != nil {
		return err
	}
	return nil
}
