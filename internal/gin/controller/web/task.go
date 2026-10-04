package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/gin/response"
	"github.com/nichuanfang/gymdl/internal/gin/task"
)

var manager *task.TaskManager

// SetTaskManager 注入任务管理器
func SetTaskManager(tm *task.TaskManager) {
	manager = tm
}

// SubmitTaskRequest 提交任务请求
type SubmitTaskRequest struct {
	URL string `json:"url" binding:"required"`
}

// HandleSubmitTask POST /api/web/task/submit
func HandleSubmitTask(c *gin.Context) {
	var req SubmitTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, "参数错误", err.Error())
		return
	}

	t, err := manager.SubmitTask(req.URL)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, t)
}

// HandleActiveTasks GET /api/web/task/active
func HandleActiveTasks(c *gin.Context) {
	tasks := manager.GetActiveTasks()
	response.Success(c, tasks)
}

// HandleTaskEvents GET /api/web/task/:id/events (SSE)
func HandleTaskEvents(c *gin.Context) {
	taskID := c.Param("id")

	ch := manager.Subscribe(taskID)
	defer manager.Unsubscribe(taskID, ch)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	clientGone := c.Request.Context().Done()
	for {
		select {
		case <-clientGone:
			return
		case t, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(t)
			fmt.Fprintf(c.Writer, "event: task\ndata: %s\n\n", data)
			c.Writer.Flush()

			// 任务到达终态时关闭连接
			if t.Status == task.TaskStatusCompleted ||
				t.Status == task.TaskStatusFailed ||
				t.Status == task.TaskStatusCancelled {
				return
			}
		}
	}
}

// HandleCancelTask DELETE /api/web/task/:id
func HandleCancelTask(c *gin.Context) {
	taskID := c.Param("id")
	if manager.CancelTask(taskID) {
		response.Success(c, gin.H{"cancelled": true})
		return
	}
	response.Fail(c, http.StatusBadRequest, "任务不存在或已完成")
}

// HandleTaskHistory GET /api/web/task/history?offset=0&limit=20
func HandleTaskHistory(c *gin.Context) {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if offset < 0 {
		offset = 0
	}
	if limit > 100 {
		limit = 100
	}
	if limit <= 0 {
		limit = 20
	}
	from, err := parseHistoryDate(c.Query("from"), false)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, "from 必须使用 RFC3339 时间格式")
		return
	}
	to, err := parseHistoryDate(c.Query("to"), true)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, "to 必须使用 RFC3339 时间格式")
		return
	}
	filter := task.HistoryFilter{
		Query: c.Query("q"), Platform: c.Query("platform"), Status: c.Query("status"), From: from, To: to,
	}
	tasks, total := manager.GetFilteredHistory(offset, limit, filter)
	response.Success(c, gin.H{"items": tasks, "total": total})
}

func parseHistoryDate(value string, endOfDay bool) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, err
	}
	if endOfDay {
		return parsed.Add(24*time.Hour - time.Nanosecond), nil
	}
	return parsed, nil
}
