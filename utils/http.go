package utils

import (
    "io"
    "net"
    "net/http"
    "net/url"
    "time"
)

// BaseResponse 通用的 API 响应结构
type BaseResponse[T any] struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
    Data    T      `json:"data"`
}

type CommonClient struct {
    HttpClient *http.Client
    BaseURL    string
    Headers    map[string]string
}

// NewCommonClient 创建一个高性能通用客户端
func NewCommonClient(baseURL string, timeout time.Duration) *CommonClient {
    return &CommonClient{
        BaseURL: baseURL,
        Headers: make(map[string]string),
        HttpClient: &http.Client{
            Timeout: timeout,
            Transport: &http.Transport{
                DialContext: (&net.Dialer{
                    Timeout:   5 * time.Second,
                    KeepAlive: 30 * time.Second,
                }).DialContext,
                MaxIdleConns:          100,
                IdleConnTimeout:       90 * time.Second,
                TLSHandshakeTimeout:   5 * time.Second,
                ExpectContinueTimeout: 1 * time.Second,
            },
        },
    }
}

// SetHeader 设置全局 Header
func (c *CommonClient) SetHeader(key, value string) {
    c.Headers[key] = value
}

// Request 执行通用请求
func (c *CommonClient) Request(method, path string, params map[string]string, body io.Reader) ([]byte, error) {
    fullURL, _ := url.Parse(c.BaseURL + path)
    if params != nil {
        query := fullURL.Query()
        for k, v := range params {
            query.Set(k, v)
        }
        fullURL.RawQuery = query.Encode()
    }

    req, err := http.NewRequest(method, fullURL.String(), body)
    if err != nil {
        return nil, err
    }

    // 注入通用 Headers
    for k, v := range c.Headers {
        req.Header.Set(k, v)
    }

    resp, err := c.HttpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    return io.ReadAll(resp.Body)
}