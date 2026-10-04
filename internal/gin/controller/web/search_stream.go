package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/gin/response"
	"github.com/nichuanfang/gymdl/utils"
)

const searchStreamHeartbeat = 10 * time.Second

type searchPlatformEvent struct {
	Platform string             `json:"platform"`
	Status   string             `json:"status"` // searching, partial, complete, error
	Items    []SearchResultItem `json:"items,omitempty"`
	Error    string             `json:"error,omitempty"`
}

type searchCompleteEvent struct {
	Items   []SearchResultItem   `json:"items"`
	Total   int                  `json:"total"`
	HasMore bool                 `json:"has_more"`
	Errors  SearchPlatformErrors `json:"errors"`
	Keyword string               `json:"keyword"`
}

// HandleSearchStream streams platform progress/results and then a stable,
// globally paginated result. The regular JSON endpoint remains available.
func HandleSearchStream(c *gin.Context) {
	request, message := parseSearchRequest(c)
	if message != "" {
		response.Fail(c, http.StatusBadRequest, message)
		return
	}

	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.WriteHeaderNow()

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	search := func(ctx context.Context, platform, keyword string, limit, offset int) ([]SearchResultItem, error) {
		return cachedSearchPage(ctx, platform, keyword, limit, offset, searchProviderSearch)
	}
	updates := streamPlatformSearches(ctx, request.platforms, request.keyword, request.offset+request.limit+1, searchProviderPageSize(request.limit), search)

	perPlatform := make([][]SearchResultItem, len(request.platforms))
	platformIndex := make(map[string]int, len(request.platforms))
	for i, platform := range request.platforms {
		platformIndex[platform] = i
	}
	platformErrors := make(SearchPlatformErrors)
	finished := make(map[string]bool, len(request.platforms))
	finishedCount := 0
	heartbeat := time.NewTicker(searchStreamHeartbeat)
	defer heartbeat.Stop()
	for finishedCount < len(request.platforms) {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := c.Writer.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			c.Writer.Flush()
		case update, ok := <-updates:
			if !ok {
				finishedCount = len(request.platforms)
				break
			}
			if i, exists := platformIndex[update.Platform]; exists && update.Status != "searching" {
				perPlatform[i] = cloneSearchItems(update.Items)
			}
			if update.Status == "error" {
				platformErrors[update.Platform] = update.Error
			}
			if (update.Status == "complete" || update.Status == "error") && !finished[update.Platform] {
				finished[update.Platform] = true
				finishedCount++
			}
			if err := writeSearchSSE(c, "platform", update); err != nil {
				cancel()
				return
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	items, total, hasMore, errs := searchPageFromPrefixes(perPlatform, request.offset, request.limit, platformErrors)
	if utils.Logger() != nil {
		for platform, err := range errs {
			utils.WarnWithFormat("[WebSearch] 平台 %s 搜索失败: %v", platform, err)
		}
	}
	if err := writeSearchSSE(c, "complete", searchCompleteEvent{
		Items: items, Total: total, HasMore: hasMore, Errors: errs, Keyword: request.keyword,
	}); err != nil {
		return
	}
}

func streamPlatformSearches(ctx context.Context, platforms []string, keyword string, target, pageSize int, search contextSearchFunc) <-chan searchPlatformEvent {
	updates := make(chan searchPlatformEvent, max(16, len(platforms)*4))
	var workers sync.WaitGroup
	for _, platform := range platforms {
		platform := platform
		workers.Add(1)
		go func() {
			defer workers.Done()
			if !sendSearchPlatformUpdate(ctx, updates, searchPlatformEvent{Platform: platform, Status: "searching"}) {
				return
			}
			platformCtx, cancel := context.WithTimeout(ctx, searchProviderTimeout)
			defer cancel()
			items, err := searchPrefixWithPageSize(platformCtx, platform, keyword, target, pageSize, search, func(items []SearchResultItem) {
				_ = sendSearchPlatformUpdate(platformCtx, updates, searchPlatformEvent{Platform: platform, Status: "partial", Items: items})
			})
			if err != nil {
				if ctx.Err() == nil {
					_ = sendSearchPlatformUpdate(ctx, updates, searchPlatformEvent{Platform: platform, Status: "error", Items: items, Error: err.Error()})
				}
				return
			}
			_ = sendSearchPlatformUpdate(ctx, updates, searchPlatformEvent{Platform: platform, Status: "complete", Items: items})
		}()
	}
	go func() {
		workers.Wait()
		close(updates)
	}()
	return updates
}

func sendSearchPlatformUpdate(ctx context.Context, updates chan<- searchPlatformEvent, update searchPlatformEvent) bool {
	select {
	case updates <- update:
		return true
	case <-ctx.Done():
		return false
	}
}

func writeSearchSSE(c *gin.Context, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
}
