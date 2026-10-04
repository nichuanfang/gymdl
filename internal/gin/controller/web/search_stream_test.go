package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSearchPrefixStreamsNewBatchesAndKeepsPartialResultsOnFailure(t *testing.T) {
	ctx := context.Background()
	var progress [][]SearchResultItem
	search := func(_ context.Context, _, _ string, limit, offset int) ([]SearchResultItem, error) {
		if offset >= 50 {
			return nil, fmt.Errorf("second upstream page failed")
		}
		items := make([]SearchResultItem, limit)
		for i := range items {
			items[i] = SearchResultItem{SongID: fmt.Sprint(i), Name: "song"}
		}
		return items, nil
	}
	items, err := searchPrefixWithProgress(ctx, "netease", "query", 101, search, func(batch []SearchResultItem) {
		progress = append(progress, batch)
	})
	if err == nil || !strings.Contains(err.Error(), "second upstream page failed") {
		t.Fatalf("expected page failure, got %v", err)
	}
	if len(items) != 50 || len(progress) != 1 || len(progress[0]) != 50 {
		t.Fatalf("expected the first 50 items to stream and survive failure: items=%d updates=%d", len(items), len(progress))
	}
}

func TestSearchStreamEmitsFastPlatformBeforeSlowAndFinalStablePage(t *testing.T) {
	previousSearch := searchProviderSearch
	previousTimeout := searchProviderTimeout
	t.Cleanup(func() {
		searchProviderSearch = previousSearch
		searchProviderTimeout = previousTimeout
	})
	searchProviderTimeout = time.Second
	searchProviderSearch = func(ctx context.Context, platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		if platform == "bilibili" {
			return nil, fmt.Errorf("simulated provider failure")
		}
		if platform == "qq" {
			select {
			case <-time.After(140 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []SearchResultItem{
			{Platform: platform, SongID: "0", Name: platform + " 0"},
			{Platform: platform, SongID: "1", Name: platform + " 1"},
		}, nil
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/web/search/stream", HandleSearchStream)
	server := httptest.NewServer(router)
	defer server.Close()
	response, err := http.Get(server.URL + "/api/web/search/stream?keyword=stream-order-test&platform=netease,qq,bilibili&offset=1&limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected SSE response: %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}

	reader := bufio.NewReader(response.Body)
	var blocks []string
	firstFastPartial := -1
	slowComplete := -1
	var complete searchCompleteEvent
	for {
		block, err := readSSEBlock(reader)
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		blocks = append(blocks, block)
		if strings.Contains(block, `"platform":"netease","status":"partial"`) && firstFastPartial < 0 {
			firstFastPartial = len(blocks) - 1
		}
		if strings.Contains(block, `"platform":"qq","status":"complete"`) {
			slowComplete = len(blocks) - 1
		}
		if strings.HasPrefix(block, "event: complete\n") {
			lines := strings.Split(block, "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "data: ") {
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &complete); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	if firstFastPartial < 0 || slowComplete < 0 || firstFastPartial >= slowComplete {
		t.Fatalf("fast-platform results should be sent before the slow platform completes: fast=%d slow=%d blocks=%v", firstFastPartial, slowComplete, blocks)
	}
	if len(complete.Items) != 2 || complete.Items[0].Platform != "qq" || complete.Items[0].SongID != "0" || complete.Items[1].Platform != "netease" || complete.Items[1].SongID != "1" {
		t.Fatalf("final page should use stable selected-platform interleave and global offset: %#v", complete.Items)
	}
	if !complete.HasMore || complete.Total != 2 {
		t.Fatalf("unexpected final paging fields: %#v", complete)
	}
	if !strings.Contains(complete.Errors["bilibili"], "simulated provider failure") {
		t.Fatalf("one provider failure should be reported without discarding other results: %#v", complete.Errors)
	}
}

func readSSEBlock(reader *bufio.Reader) (string, error) {
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if len(lines) > 0 {
				return strings.Join(lines, "\n"), nil
			}
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(lines) == 0 {
				continue
			}
			return strings.Join(lines, "\n"), nil
		}
		lines = append(lines, line)
	}
}

func TestSearchStreamCancelsProviderWhenClientContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopped := make(chan struct{})
	search := func(ctx context.Context, _, _ string, _ int, _ int) ([]SearchResultItem, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}
	updates := streamFilteredPlatformSearches(ctx, []string{"netease"}, "cancel-test", 10, maxSearchCandidatesPerPlatform, 20, searchResultFilters{}, search)
	<-started
	// Drain the initial searching status then cancel the requesting client.
	<-updates
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("provider context did not stop after the client disconnected")
	}
}

func TestSearchStreamClientDisconnectCancelsProvider(t *testing.T) {
	previousSearch := searchProviderSearch
	previousTimeout := searchProviderTimeout
	t.Cleanup(func() {
		searchProviderSearch = previousSearch
		searchProviderTimeout = previousTimeout
	})
	searchProviderTimeout = time.Second
	started := make(chan struct{})
	stopped := make(chan struct{})
	searchProviderSearch = func(ctx context.Context, _, _ string, _, _ int) ([]SearchResultItem, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/web/search/stream", HandleSearchStream)
	server := httptest.NewServer(router)
	defer server.Close()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(requestCtx, http.MethodGet, server.URL+"/api/web/search/stream?keyword=disconnect-test&platform=netease&limit=10", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(line, `"status":"searching"`) {
			break
		}
	}
	<-started
	cancelRequest()
	_ = response.Body.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("closing the SSE client did not cancel its provider request")
	}
}

func TestSearchStreamTimesOutOnlyTheSlowPlatform(t *testing.T) {
	previousTimeout := searchProviderTimeout
	t.Cleanup(func() { searchProviderTimeout = previousTimeout })
	searchProviderTimeout = 35 * time.Millisecond
	var slowStopped atomic.Bool
	search := func(ctx context.Context, platform, _ string, limit, _ int) ([]SearchResultItem, error) {
		if platform == "youtube" {
			<-ctx.Done()
			slowStopped.Store(true)
			return nil, ctx.Err()
		}
		return []SearchResultItem{{Platform: platform, SongID: "ok"}}, nil
	}
	updates := streamFilteredPlatformSearches(context.Background(), []string{"netease", "youtube"}, "timeout-test", 10, maxSearchCandidatesPerPlatform, 20, searchResultFilters{}, search)
	statuses := make(map[string]string)
	for update := range updates {
		if update.Status == "complete" || update.Status == "error" {
			statuses[update.Platform] = update.Status
		}
	}
	if statuses["netease"] != "complete" || statuses["youtube"] != "error" || !slowStopped.Load() {
		t.Fatalf("a timed out provider must fail without blocking completed providers: statuses=%v stopped=%v", statuses, slowStopped.Load())
	}
}

func TestLegacyJSONSearchEndpointRemainsCompatibleAndUsesSearchCache(t *testing.T) {
	previousSearch := searchProviderSearch
	t.Cleanup(func() { searchProviderSearch = previousSearch })
	var calls atomic.Int32
	searchProviderSearch = func(_ context.Context, platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		calls.Add(1)
		if platform != "youtube" || offset != 0 {
			t.Errorf("unexpected legacy search provider request: platform=%s offset=%d", platform, offset)
		}
		count := min(limit, 3)
		items := make([]SearchResultItem, count)
		for i := range items {
			items[i] = SearchResultItem{Platform: platform, SongID: fmt.Sprint(i), Name: "legacy"}
		}
		return items, nil
	}
	gin.SetMode(gin.TestMode)
	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/web/search?keyword=json-cache-compat&platform=youtube&offset=0&limit=10", nil)
		HandleSearch(ctx)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"has_more":false`) || !strings.Contains(recorder.Body.String(), `"song_id":"0"`) {
			t.Fatalf("legacy JSON endpoint response changed: status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("repeated identical request should hit short result cache, provider calls=%d", calls.Load())
	}
}
