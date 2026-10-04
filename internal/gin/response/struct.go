package response

//http 统一响应格式

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Response 统一响应结构体
type Response struct {
	Code    int      `json:"code"`             // 状态码
	Message string   `json:"message"`          // 提示信息
	Data    any      `json:"data,omitempty"`   // 返回数据，可为空
	Errors  []string `json:"errors,omitempty"` // 详细错误信息，可选
}

// Success 返回成功响应
func Success(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Response{
		Code:    http.StatusOK,
		Message: "success",
		Data:    data,
	})
}

// Fail 返回错误响应
func Fail(c *gin.Context, code int, msg string, errs ...string) {
	if code < 400 || code > 599 {
		code = http.StatusBadRequest
	}
	c.JSON(code, Response{
		Code:    code,
		Message: msg,
		Errors:  errs,
	})
}
