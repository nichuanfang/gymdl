package utils

import (
    "fmt"
    "io"
    "net"
    "net/http"
    "time"
)

/* ---------------------- HTTP连接池 ---------------------- */

// 全局共享的HTTP客户端，复用连接池
var sharedHTTPClient = &http.Client{
    Timeout: 8 * time.Second,
    Transport: &http.Transport{
        Proxy:               http.ProxyFromEnvironment,
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 20,
        MaxConnsPerHost:     50,
        IdleConnTimeout:     90 * time.Second,
        DialContext: (&net.Dialer{
            Timeout:   5 * time.Second,
            KeepAlive: 30 * time.Second,
        }).DialContext,
        TLSHandshakeTimeout:   5 * time.Second,
        ExpectContinueTimeout: 1 * time.Second,
    },
}

/* ---------------------- 健康检查相关定义 ---------------------- */

// HealthCheckOption 健康检查选项
type HealthCheckOption struct {
    URL         string            // 请求地址(必填)
    Method      string            // 请求方法，默认GET
    Headers     map[string]string // 请求头
    Body        []byte            // 请求体(可选)
    Timeout     time.Duration     // 单次请求超时时间，默认使用共享客户端超时(8s)
    ExpectCodes []int             // 期望的状态码列表，为空时默认 [200,299] 区间均视为成功
}

// HealthCheckResult 健康检查结果
type HealthCheckResult struct {
    OK         bool          // 是否健康
    StatusCode int           // 响应状态码(请求失败时为0)
    Latency    time.Duration // 耗时
    Err        error         // 错误信息(请求失败/状态码不符时非nil)
}

/* ---------------------- 健康检查核心方法 ---------------------- */

// CheckHealth 执行一次HTTP健康检查
func CheckHealth(opt HealthCheckOption) HealthCheckResult {
    start := time.Now()

    method := opt.Method
    if method == "" {
        method = http.MethodGet
    }

    var bodyReader io.Reader
    if len(opt.Body) > 0 {
        bodyReader = bytesReader(opt.Body)
    }

    req, err := http.NewRequest(method, opt.URL, bodyReader)
    if err != nil {
        return HealthCheckResult{
            OK:      false,
            Latency: time.Since(start),
            Err:     fmt.Errorf("构建请求失败: %w", err),
        }
    }

    for k, v := range opt.Headers {
        req.Header.Set(k, v)
    }
    client = sharedHTTPClient
    
    if opt.Timeout > 0 {
        // 单独超时时间时，复用Transport但创建独立Client实例（避免修改共享Client）
        client = &http.Client{
            Timeout:   opt.Timeout,
            Transport: sharedHTTPClient.Transport,
        }
    }

    resp, err := client.Do(req)
    if err != nil {
        return HealthCheckResult{
            OK:      false,
            Latency: time.Since(start),
            Err:     fmt.Errorf("请求失败: %w", err),
        }
    }
    defer func() {
        // 丢弃响应体并复用连接
        _, _ = io.Copy(io.Discard, resp.Body)
        resp.Body.Close()
    }()

    latency := time.Since(start)

    if !isExpectedStatus(resp.StatusCode, opt.ExpectCodes) {
        return HealthCheckResult{
            OK:         false,
            StatusCode: resp.StatusCode,
            Latency:    latency,
            Err:        fmt.Errorf("非预期状态码: %d", resp.StatusCode),
        }
    }

    return HealthCheckResult{
        OK:         true,
        StatusCode: resp.StatusCode,
        Latency:    latency,
    }
}

// isExpectedStatus 判断状态码是否符合预期
func isExpectedStatus(code int, expect []int) bool {
    if len(expect) == 0 {
        return code >= 200 && code < 300
    }
    for _, c := range expect {
        if c == code {
            return true
        }
    }
    return false
}

// bytesReader 避免引入bytes包别名冲突的简易包装
func bytesReader(b []byte) io.Reader {
    return &byteSliceReader{data: b}
}

type byteSliceReader struct {
    data []byte
    pos  int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
    if r.pos >= len(r.data) {
        return 0, io.EOF
    }
    n := copy(p, r.data[r.pos:])
    r.pos += n
    return n, nil
}

// SharedHTTPClient 获取全局共享的HTTP客户端
func SharedHTTPClient() *http.Client {
    return sharedHTTPClient
}