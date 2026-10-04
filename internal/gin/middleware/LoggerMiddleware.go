package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/gin/response"
	"github.com/nichuanfang/gymdl/utils"
	"go.uber.org/zap"
)

// bodyWriter 包装 gin.ResponseWriter 用于捕获响应体
type bodyWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w bodyWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

// GinLoggerMiddleware 日志中间件
func GinLoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// 日志轮询接口不记录自身访问，避免制造重复日志噪声。任务 SSE 仍需绕过响应体包装。
		if strings.HasPrefix(c.Request.URL.Path, "/api/web/logs") ||
			strings.HasPrefix(c.Request.URL.Path, "/api/web/task/") && strings.HasSuffix(c.Request.URL.Path, "/events") {
			c.Next()
			return
		}

		// 包装 ResponseWriter
		bw := &bodyWriter{body: bytes.NewBufferString(""), ResponseWriter: c.Writer}
		c.Writer = bw

		path := c.Request.URL.Path
		if raw := c.Request.URL.RawQuery; raw != "" {
			path += "?" + raw
		}

		method := c.Request.Method
		clientIP := c.ClientIP()

		c.Next()

		cost := time.Since(start)

		// 默认日志字段
		fields := []zap.Field{
			zap.String("method", method),
			zap.String("path", path),
			zap.String("ip", clientIP),
			zap.String("latency", cost.String()),
		}

		// 尝试解析响应体 JSON
		var resp response.Response
		bodyBytes := bw.body.Bytes()
		if err := json.Unmarshal(bodyBytes, &resp); err == nil {
			fields = append(fields,
				zap.Int("code", resp.Code),
				zap.String("message", resp.Message),
				zap.Strings("errors", resp.Errors),
				//zap.Any("data", resp.Data),
			)
		}

		// 根据业务 Code 判断日志等级
		code := resp.Code
		logger := utils.Logger()
		// 拼接人类可读的消息用于日志流
		msg := fmt.Sprintf("[GIN] %s %s %s %s", method, path, clientIP, cost)
		if resp.Code != 0 {
			msg += fmt.Sprintf(" code=%d", resp.Code)
		}
		switch {
		case code >= 500:
			logger.Error(msg, fields...)
		case code >= 400:
			logger.Warn(msg, fields...)
		default:
			logger.Info(msg, fields...)
		}
	}
}

// GinRecoveryMiddleware 优化版
func GinRecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				req := c.Request
				requestInfo := req.Method + " " + req.URL.Path + " " + req.Proto
				logger := utils.Logger()

				// 检查网络断开错误
				if isBrokenPipeErr(rec) {
					logger.Warn("[BROKEN PIPE]",
						zap.Any("error", rec),
						zap.String("request", requestInfo),
					)
					c.Abort()
					return
				}

				// 其他 panic，返回 Fail 响应
				logger.Error("[PANIC RECOVER]",
					zap.Any("error", rec),
					zap.String("request", requestInfo),
				)
				response.Fail(c, 500, "internal server error")
				c.Abort()
			}
		}()
		c.Next()
	}
}

// isBrokenPipeErr 检查网络断开
func isBrokenPipeErr(rec any) bool {
	err, ok := rec.(error)
	if !ok || err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		msg := err.Error()
		return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer")
	}
	return false
}
