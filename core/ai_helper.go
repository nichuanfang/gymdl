package core

import (
	"context"
	"fmt"
    "net/http"
    "strings"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/utils"
	"github.com/sashabaranov/go-openai"
)

type AI struct {
	Config *config.AIConfig
	Client *openai.Client
}

var (
	GlobalAI *AI
)

// InitAI 初始化全局 AI，只会执行一次，并支持自定义 BaseURL
func InitAI(cfg *config.AIConfig) {
    if logger == nil {
        logger = utils.Logger()
    }
    if cfg == nil || cfg.ApiKey == "" || cfg.Model == "" {
        panic("AI config is invalid")
    }
    clientCfg := openai.DefaultConfig(cfg.ApiKey)

    if cfg.BaseUrl != "" {
        clientCfg.BaseURL = cfg.BaseUrl
        clientCfg.APIType = openai.APITypeOpenAI
    }

    if cfg.SystemPrompt == "" {
        cfg.SystemPrompt = "You are a helpful assistant."
    }

    // 共享Transport连接池，但不设置Client.Timeout（流式请求由context控制超时）
    clientCfg.HTTPClient = &http.Client{
        Transport: utils.SharedHTTPClient().Transport,
    }

    GlobalAI = &AI{
        Config: cfg,
        Client: openai.NewClientWithConfig(clientCfg),
    }
}

// -------------------- Context 辅助 --------------------

func withTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// -------------------- 连接检测 --------------------

var lastCheck time.Time
var lastCheckResult bool
var checkMutex sync.Mutex

func (ai *AI) CheckConnection() bool {
	checkMutex.Lock()
	defer checkMutex.Unlock()

	// 1分钟内复用结果
	if time.Since(lastCheck) < time.Minute {
		return lastCheckResult
	}

	ctx, cancel := withTimeout(5 * time.Second)
	defer cancel()
	req := openai.ChatCompletionRequest{
		Model: ai.Config.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: ai.Config.SystemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "测试连接"},
		},
		MaxCompletionTokens: 1,
	}

	_, err := ai.Client.CreateChatCompletion(ctx, req)
	lastCheck = time.Now()
	lastCheckResult = err == nil
	return lastCheckResult
}

// -------------------- 普通问答 --------------------

func (ai *AI) Ask(prompt string, opts ...func(*openai.ChatCompletionRequest)) (string, error) {
	if prompt == "" {
		return "", fmt.Errorf("prompt cannot be empty")
	}

	req := openai.ChatCompletionRequest{
		Model: ai.Config.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: ai.Config.SystemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: prompt},
		},
		MaxCompletionTokens: 1024,
		Temperature:         0.7,
	}

	for _, opt := range opts {
		opt(&req)
	}

	ctx, cancel := withTimeout(30 * time.Second)
	defer cancel()

	resp, err := ai.Client.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("AI request failed: %v", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("AI returned empty response")
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// -------------------- 流式问答 --------------------

func (ai *AI) AskStream(prompt string, callback func(string) bool, opts ...func(*openai.ChatCompletionRequest)) error {
	if prompt == "" {
		return fmt.Errorf("prompt cannot be empty")
	}

	req := openai.ChatCompletionRequest{
		Model: ai.Config.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: ai.Config.SystemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: prompt},
		},
		Stream:              true,
		MaxCompletionTokens: 1024,
		Temperature:         0.7,
	}

	for _, opt := range opts {
		opt(&req)
	}

	ctx, cancel := withTimeout(60 * time.Second)
	defer cancel()

	stream, err := ai.Client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create stream: %v", err)
	}
	defer stream.Close()

	for {
		resp, err := stream.Recv()
		if err != nil {
			if strings.Contains(err.Error(), "EOF") || err == context.Canceled {
				break
			}
			return fmt.Errorf("stream error: %v", err)
		}
		if len(resp.Choices) > 0 {
			// callback 返回 true 表示终止
			if stop := callback(resp.Choices[0].Delta.Content); stop {
				break
			}
		}
	}

	return nil
}

// -------------------- 可选参数 --------------------

// WithMaxTokens 设置返回最大长度
func WithMaxTokens(n int) func(*openai.ChatCompletionRequest) {
	return func(req *openai.ChatCompletionRequest) {
		req.MaxCompletionTokens = n
	}
}

// WithTemperature 设置温度
func WithTemperature(t float32) func(*openai.ChatCompletionRequest) {
	return func(req *openai.ChatCompletionRequest) {
		req.Temperature = t
	}
}
