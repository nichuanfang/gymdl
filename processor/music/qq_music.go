package music

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/processor/music/struct"
	"github.com/nichuanfang/gymdl/utils"
)

/* ---------------------- 结构体与构造方法 ---------------------- */

type QQMusicProcessor struct {
	cfg         *config.Config
	tempDir     string
	songs       []*SongInfo
	apiProvider QQApiProvider //api提供者
}

type QQMusicLink struct {
	id     string //歌曲或者歌单id
	isSong bool   //是否为歌曲
}

type QQApiProvider interface {
	download(url QQMusicLink, callback func(string)) error
}

// QQMusicAPI 自建qq-music-api
type QQMusicAPI struct {
	baseUrl string            //服务端点
	headers map[string]string //请求头
}

// LXMusicAPI 洛雪api
type LXMusicAPI struct {
	baseUrl    string            //服务端点
	apiVersion string            //接口版本
	key        string            //密钥
	headers    map[string]string //请求头
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
	if cfg.QQMusicApiConfig.Enable {
		qmApi := &QQMusicAPI{
			baseUrl: cfg.QQMusicApiConfig.Endpoint,
		}
		qmApi.initHeaders(cfg)
		qm.apiProvider = qmApi
	} else {
		//默认启用洛雪
		qm.apiProvider = &LXMusicAPI{}
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
	err = qm.apiProvider.download(qqMusicLink, callback)
	if err != nil {
		return err
	}
	return nil
}

func (qm *QQMusicProcessor) DownloadCommand(url string) *exec.Cmd {
	// TODO implement me
	return nil
}

func (qm *QQMusicProcessor) BeforeTidy() error {
	// TODO implement me
	return nil
}

func (qm *QQMusicProcessor) NeedRemoveDRM() bool {
	return false
}

func (qm *QQMusicProcessor) DRMRemove() error {
	return nil
}

func (qm *QQMusicProcessor) TidyMusic() error {
	// TODO implement me
	return nil
}

func (qm *QQMusicProcessor) EncryptedExts() []string {
	return make([]string, 0)
}

func (qm *QQMusicProcessor) DecryptedExts() []string {
	return []string{".aac", ".m4a", ".flac", ".mp3", ".ogg"}
}

/* ------------------------ qq-music-api ------------------------ */

// download 下载qq音乐
func (qm *QQMusicAPI) download(musicLink QQMusicLink, callback func(string)) error {
	if musicLink.isSong {
		return qm.downloadSong(musicLink.id, callback)
	} else {
		return qm.downloadSonglist(musicLink.id, callback)
	}
}

// download 下载单曲
func (qm *QQMusicAPI) downloadSong(musicid string, callback func(string)) error {
	songRes, err := qm.querySong(musicid)
	if err != nil {
		return err
	}
	utils.InfoWithFormat(songRes.Message)
	//todo 获取mid
	//todo 获取最高音质的文件类型
	//todo 获取下载链接
	return nil
}

// download 下载歌单
func (qm *QQMusicAPI) downloadSonglist(url string, callback func(string)) error {
	return nil
}

// querySong 查询歌曲信息
func (qm *QQMusicAPI) querySong(songId string) (*_struct.QuerySongResponse, error) {
	request, err := qm.newGetRequest("/song/query_song", map[string]string{"value": songId})
	if err != nil {
		return nil, err
	}
	// 4. 发送请求
	client := &http.Client{}
	resp, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 5. 读取响应内容
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	songRes := &_struct.QuerySongResponse{}
	_ = json.Unmarshal(body, songRes)
	return songRes, nil
}

// initHeaders 初始化请求头
func (qm *QQMusicAPI) initHeaders(cfg *config.Config) {
	headers := make(map[string]string)
	if cfg.QQMusicApiConfig.EnableSign {
		headers["X-Enable-Sign"] = "true"
	}
	if cfg.QQMusicApiConfig.EnableSign {
		headers["X-Enable-Cache"] = "true"
	}
	//获取cookie
	qqCookies := utils.GetCookiesByDomain(filepath.Join(cfg.CookieCloud.CookieFilePath, cfg.CookieCloud.CookieFile), ".qq.com")
	switch cfg.QQMusicApiConfig.LoginType {
	case 1:
		//微信
		headers["Cookie"] = fmt.Sprintf("musicid=%s;musickey=%s", qqCookies["wxuin"], qqCookies["qqmusic_key"])
	case 2:
		//qq
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
func (lx *LXMusicAPI) download(musicLink QQMusicLink, callback func(string)) error {
	//todo
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

// getRedirectLocation 获取短链的重定向地址（Location）
func (qm *QQMusicProcessor) getRedirectLocation(link string) (string, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(link)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	return resp.Header.Get("Location"), nil
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
