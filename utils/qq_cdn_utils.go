package utils

import (
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "strings"
    "sync"
    "time"
)

// CDNResponse 对应您提供的 QQ音乐 API 返回结构
type CDNResponse struct {
    Code int    `json:"code"`
    Msg  string `json:"msg"`
    Data struct {
        Retcode     int      `json:"retcode"`
        Sip         []string `json:"sip"`
        RefreshTime int64    `json:"refreshTime"` // 缓存刷新时间（秒）
        Expiration  int64    `json:"expiration"`  // 总过期时间（秒）
    } `json:"data"`
}

// URLBuilder 自动维护 CDN 缓存的链接拼接工具
type URLBuilder struct {
    apiURL     string       // 初始化传入的调度接口地址
    httpClient *http.Client // 内部复用 HTTP 客户端

    mu        sync.RWMutex
    cdnBytes  [][]byte  // 核心优化：预先转换为 HTTPS 的字节切片，极速拼接
    expiredAt time.Time // 缓存失效的时间点
}

// NewURLBuilder 初始化工具类，必须传入您的调度接口 URL
func NewURLBuilder(apiURL string) *URLBuilder {
    return &URLBuilder{
        apiURL: apiURL,
        httpClient: &http.Client{
            Timeout: 5 * time.Second, // 设置 5 秒超时，防止卡死
        },
    }
}

// GetURL 核心对外方法：只传入 purl，获取最优的 HTTPS 下载链接
func (b *URLBuilder) GetURL(purl string) (string, error) {
    if purl == "" {
        return "", errors.New("purl cannot be empty")
    }

    // 1. 检查缓存是否有效（读锁，高并发下性能极高）
    b.mu.RLock()
    isCacheValid := len(b.cdnBytes) > 0 && time.Now().Before(b.expiredAt)
    if isCacheValid {
        primaryCDN := b.cdnBytes[0] // 默认取第一个最优节点
        url := b.concat(primaryCDN, purl)
        b.mu.RUnlock()
        return url, nil
    }
    b.mu.RUnlock()

    // 2. 缓存失效，加写锁进行刷新
    b.mu.Lock()
    defer b.mu.Unlock()

    // 双重检查（Double-Checked Locking），防止高并发下多个请求同时穿透去请求 API
    if len(b.cdnBytes) > 0 && time.Now().Before(b.expiredAt) {
        return b.concat(b.cdnBytes[0], purl), nil
    }

    // 3. 同步触发 API 请求刷新 CDN 列表
    if err := b.refreshCDNFromAPI(); err != nil {
        // 降级容灾逻辑：如果接口偶尔报错，但本地还有旧缓存，继续用旧缓存，避免业务中断
        if len(b.cdnBytes) > 0 {
            return b.concat(b.cdnBytes[0], purl), nil
        }
        return "", fmt.Errorf("refresh cdn failed: %w", err)
    }

    if len(b.cdnBytes) == 0 {
        return "", errors.New("no available cdn nodes fetched from api")
    }

    return b.concat(b.cdnBytes[0], purl), nil
}

// GetBackupURLs 容灾方法：只传入 purl，返回所有可用的 HTTPS 备用链接
func (b *URLBuilder) GetBackupURLs(purl string) ([]string, error) {
    b.mu.RLock()
    defer b.mu.Unlock()

    if len(b.cdnBytes) == 0 {
        return nil, errors.New("cdn cache is empty, please call GetURL first to trigger initialization")
    }

    res := make([]string, len(b.cdnBytes))
    for i, cdn := range b.cdnBytes {
        res[i] = b.concat(cdn, purl)
    }
    return res, nil
}

// concat 核心微观优化：内存精准分配，直接进行字节流拷贝
func (b *URLBuilder) concat(cdn []byte, purl string) string {
    buf := make([]byte, len(cdn)+len(purl))
    copy(buf, cdn)
    copy(buf[len(cdn):], purl)
    return string(buf)
}

// refreshCDNFromAPI 请求接口并更新内存缓存
func (b *URLBuilder) refreshCDNFromAPI() error {
    resp, err := b.httpClient.Get(b.apiURL)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    var apiResp CDNResponse
    if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
        return err
    }

    if apiResp.Code != 0 || apiResp.Data.Retcode != 0 {
        return fmt.Errorf("api response code error: code=%d, retcode=%d, msg=%s", apiResp.Code, apiResp.Data.Retcode, apiResp.Msg)
    }

    // 解析并清洗 sip 列表
    newBytes := make([][]byte, 0, len(apiResp.Data.Sip))
    seen := make(map[string]bool)

    for _, cdn := range apiResp.Data.Sip {
        if cdn == "" {
            continue
        }
        // 强制转换为 HTTPS
        var httpsCDN string
        if strings.HasPrefix(cdn, "http://") {
            httpsCDN = "https://" + cdn[7:]
        } else if strings.HasPrefix(cdn, "https://") {
            httpsCDN = cdn
        } else {
            httpsCDN = "https://" + cdn
        }

        if !strings.HasSuffix(httpsCDN, "/") {
            httpsCDN += "/"
        }

        if seen[httpsCDN] {
            continue
        }
        seen[httpsCDN] = true
        newBytes = append(newBytes, []byte(httpsCDN))
    }

    if len(newBytes) == 0 {
        return errors.New("api returned sip list is empty after clean")
    }

    // 计算过期时间：使用返回的 refreshTime（例如 1800秒），并提前 60 秒失效以防临界点断流
    ttl := apiResp.Data.RefreshTime
    if ttl <= 0 {
        ttl = 1800 // 如果接口没返回，兜底 30 分钟
    }

    b.cdnBytes = newBytes
    b.expiredAt = time.Now().Add(time.Duration(ttl-60) * time.Second)

    return nil
}