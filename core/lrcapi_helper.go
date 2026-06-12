package core

import (
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "strings"
    "sync"
    "time"

    "github.com/nichuanfang/gymdl/config"
    "github.com/nichuanfang/gymdl/utils"
)

type LrcAPI struct {
    Config          *config.LrcAPIConfig
    lastCheck       time.Time
    lastCheckResult bool
    checkMutex      sync.Mutex
}

var (
    GlobalLrcAPI *LrcAPI
)

// lrcApiLyricItem lrcapi返回的单条歌词结果
type lrcApiLyricItem struct {
    ID     string `json:"id"`
    Title  string `json:"title"`
    Artist string `json:"artist"`
    Lyrics string `json:"lyrics"`
}

// InitLrcAPI 初始化全局 LrcAPI
func InitLrcAPI(cfg *config.LrcAPIConfig) {
    if logger == nil {
        logger = utils.Logger()
    }
    if cfg == nil || !cfg.Enable {
        return
    }
    if cfg.LrcApiUrl == "" {
        panic("⚠️ LrcAPI config is invalid")
    }

    GlobalLrcAPI = &LrcAPI{
        Config: cfg,
    }
}

// -------------------- 连接检测 --------------------

// CheckConnection 健康检查（缓存1分钟，避免频繁请求）
func (l *LrcAPI) CheckConnection() bool {
    l.checkMutex.Lock()
    defer l.checkMutex.Unlock()

    if time.Since(l.lastCheck) < time.Minute {
        return l.lastCheckResult
    }

    reqURL := l.buildURL("/api/v1/lyrics/advance", url.Values{
        "title": []string{"test"},
    })
    
    headers := map[string]string{
        "Authorization": l.Config.LrcApiKey,
    }
    if l.Config.LrcApiHost!=""{
        headers["Host"] = l.Config.LrcApiHost
    } 

    result := utils.CheckHealth(utils.HealthCheckOption{
        URL:    reqURL,
        Method: http.MethodGet,
        Headers: headers,
    })

    l.lastCheck = time.Now()
    l.lastCheckResult = result.OK

    if !result.OK {
        logger.Warn(fmt.Sprintf("⚠️ LrcAPI connection check failed: %v (耗时: %v)", result.Err, result.Latency))
    }

    return l.lastCheckResult
}

// -------------------- 歌词查询 --------------------

// GetLyrics 获取单曲歌词（优先匹配）
func (l *LrcAPI) GetLyrics(title, artist, album string) (string, error) {
    if title == "" {
        return "", fmt.Errorf("title cannot be empty")
    }

    params := url.Values{}
    params.Set("title", title)
    if artist != "" {
        params.Set("artist", artist)
    }
    if album != "" {
        params.Set("album", album)
    }

    reqURL := l.buildURL("/api/v1/lyrics/advance", params)

    req, err := http.NewRequest(http.MethodGet, reqURL, nil)
    if err != nil {
        return "", fmt.Errorf("failed to build request: %w", err)
    }
    req.Header.Set("Authorization", l.Config.LrcApiKey)
    if l.Config.LrcApiHost != "" {
        req.Host = l.Config.LrcApiHost
    }
    resp, err := utils.SharedHTTPClient().Do(req)
    if err != nil {
        logger.Warn(fmt.Sprintf("⚠️ LrcAPI request failed for %s - %s: %v", title, artist, err))
        return "", fmt.Errorf("request failed: %w", err)
    }
    defer func() {
        _, _ = io.Copy(io.Discard, resp.Body)
        resp.Body.Close()
    }()

    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
    }

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return "", fmt.Errorf("failed to read response body: %w", err)
    }

    var items []lrcApiLyricItem
    if err = json.Unmarshal(body, &items); err != nil {
        return "", fmt.Errorf("failed to parse response: %w", err)
    }

    if len(items) == 0 || items[0].Lyrics == "" {
        return "", fmt.Errorf("no lyrics found for %s - %s", title, artist)
    }

    logger.Info(fmt.Sprintf("💡 LrcAPI fetched lyrics successfully: %s - %s", title, artist))
    return items[0].Lyrics, nil
}

// -------------------- 工具方法 --------------------

// buildURL 拼接lrcapi请求地址（容器名+frp场景优先使用代理主机）
func (l *LrcAPI) buildURL(apiPath string, params url.Values) string {
    base := strings.TrimSuffix(l.Config.LrcApiUrl, "/")

    reqURL := base + apiPath
    if len(params) > 0 {
        reqURL += "?" + params.Encode()
    }
    return reqURL
}