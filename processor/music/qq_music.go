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
	qqMusicLink, err = qm.parseQQMusicLink(url)
	if err != nil {
		return err
	}
	err = qm.qmApi.download(qqMusicLink, qm.tempDir, callback)
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

func (qm *QQMusicAPI) downloadSong(musicid string, tempDir string, callback func(string)) error {
	start := time.Now()
	songData, err := qm.querySong(musicid)
	if err != nil {
		return err
	}
	mid := songData.Mid

	fileData, err := json.Marshal(songData.File)
	if err != nil {
		return err
	}
	quality, ext, err := utils.GetBestQuality(fileData)
	if err != nil {
		return err
	}

	songUrl, err := qm.getSongUrl(mid, string(quality))
	if err != nil {
		return err
	}
	sanitizeFileName := utils.SanitizeFileName(songData.Title)
	tempPath := filepath.Join(tempDir, sanitizeFileName+ext)

	err = utils.DownloadFile(songUrl, tempPath)
	if err != nil {
		return err
	}

	utils.InfoWithFormat("[QQMusic] ✅ 下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond))
	callback(fmt.Sprintf("下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond)))
	return nil
}

func (qm *QQMusicAPI) downloadSonglist(url string, tempDir string, callback func(string)) error {
	return nil
}

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

func (qm *QQMusicAPI) initHeaders(cfg *config.Config) {
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
	qm.headers = headers
}

func (qm *QQMusicAPI) newGetRequest(path string, params map[string]string) (*http.Request, error) {
	u, err := url.Parse(qm.baseUrl + path)
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

	for k, v := range qm.headers {
		req.Header.Set(k, v)
	}

	return req, nil
}

/* ------------------------ 拓展方法 ------------------------ */

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
