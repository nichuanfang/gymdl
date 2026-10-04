package web

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

func TestSearchResultCacheHitExpiryAndErrors(t *testing.T) {
	cache := newSearchResultCache()
	now := time.Now()
	cache.now = func() time.Time { return now }
	var calls atomic.Int32
	search := func(context.Context) ([]SearchResultItem, error) {
		calls.Add(1)
		return []SearchResultItem{{Platform: "youtube", SongID: "1", Name: "original"}}, nil
	}
	first, err := cache.do(context.Background(), "same", search)
	if err != nil || len(first) != 1 {
		t.Fatalf("first search failed: %#v, %v", first, err)
	}
	second, err := cache.do(context.Background(), "same", search)
	if err != nil || len(second) != 1 || calls.Load() != 1 {
		t.Fatalf("expected a cache hit, calls=%d items=%#v err=%v", calls.Load(), second, err)
	}
	second[0].Name = "mutated"
	third, err := cache.do(context.Background(), "same", search)
	if err != nil || third[0].Name == "mutated" {
		t.Fatalf("cache must return an independent slice copy: %#v, %v", third, err)
	}

	now = now.Add(searchResultCacheTTL + time.Millisecond)
	if _, err := cache.do(context.Background(), "same", search); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expired entry should be fetched again; calls=%d", calls.Load())
	}

	var failures atomic.Int32
	fail := func(context.Context) ([]SearchResultItem, error) {
		failures.Add(1)
		return nil, errors.New("upstream failure")
	}
	for range 2 {
		if _, err := cache.do(context.Background(), "failure", fail); err == nil {
			t.Fatal("expected uncached failure")
		}
	}
	if failures.Load() != 2 {
		t.Fatalf("errors must not be cached; calls=%d", failures.Load())
	}
}

func TestSearchResultCacheCoalescesConcurrentRequests(t *testing.T) {
	cache := newSearchResultCache()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	search := func(ctx context.Context) ([]SearchResultItem, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return []SearchResultItem{{SongID: "shared"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	const requests = 8
	results := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := cache.do(context.Background(), "coalesced", search)
			if err == nil && (len(items) != 1 || items[0].SongID != "shared") {
				err = fmt.Errorf("unexpected coalesced result: %#v", items)
			}
			results <- err
		}()
	}
	<-started
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		cache.mu.Lock()
		waiters := cache.flights["coalesced"].waiters
		cache.mu.Unlock()
		if waiters == requests {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cache.mu.Lock()
	waiters := cache.flights["coalesced"].waiters
	cache.mu.Unlock()
	if waiters != requests {
		close(release)
		t.Fatalf("waiters were not coalesced: %d", waiters)
	}
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one provider call, got %d", calls.Load())
	}
}

func TestSearchResultCacheCancelsSharedWorkOnlyAfterLastWaiterLeaves(t *testing.T) {
	cache := newSearchResultCache()
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	started := make(chan struct{})
	canceled := make(chan struct{})
	result := make(chan error, 2)
	search := func(ctx context.Context) ([]SearchResultItem, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	}
	go func() { _, err := cache.do(ctx1, "cancel-shared", search); result <- err }()
	<-started
	go func() { _, err := cache.do(ctx2, "cancel-shared", search); result <- err }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		cache.mu.Lock()
		waiters := cache.flights["cancel-shared"].waiters
		cache.mu.Unlock()
		if waiters == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel1()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter should cancel independently: %v", err)
	}
	select {
	case <-canceled:
		t.Fatal("shared provider work was canceled while another waiter remained")
	case <-time.After(25 * time.Millisecond):
	}
	cancel2()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("second waiter should cancel: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("provider context was not canceled after the last waiter left")
	}
}

func TestSearchCacheScopeSeparatesQQAccountAndVIPLevel(t *testing.T) {
	credentialPath := useQQMusicCredentialPath(t)
	if _, err := saveQQMusicCredential(credentialPath, map[string]interface{}{
		"musicid": "123", "str_musicid": "123", "musickey": "secret-one",
	}); err != nil {
		t.Fatal(err)
	}
	previous := GetWebConfig()
	t.Cleanup(func() { SetWebConfig(previous) })
	SetWebConfig(&config.Config{QQMusicApiConfig: &config.QQMusicApiConfig{Enable: true, Endpoint: "https://qq.test", VipLevel: "high"}})
	first := searchCacheScope("qq")
	if _, err := saveQQMusicCredential(credentialPath, map[string]interface{}{
		"musicid": "456", "str_musicid": "456", "musickey": "secret-two",
	}); err != nil {
		t.Fatal(err)
	}
	if second := searchCacheScope("qq"); second == first {
		t.Fatal("QQ cache scope must differ between accounts")
	}
	SetWebConfig(&config.Config{QQMusicApiConfig: &config.QQMusicApiConfig{Enable: true, Endpoint: "https://qq.test", VipLevel: "high"}})
	accountTwoHigh := searchCacheScope("qq")
	if accountTwoHigh == first {
		t.Fatal("QQ cache scope must differ between accounts")
	}
	SetWebConfig(&config.Config{QQMusicApiConfig: &config.QQMusicApiConfig{Enable: true, Endpoint: "https://qq.test", VipLevel: "low"}})
	low := searchCacheScope("qq")
	if low == accountTwoHigh {
		t.Fatal("QQ cache scope must differ by VIP level")
	}
}

func TestSearchCacheKeyIncludesRequestPagination(t *testing.T) {
	base := searchCacheKey("netease", "same query", 10, 0, "account")
	if base == searchCacheKey("netease", "same query", 20, 0, "account") {
		t.Fatal("cache key must distinguish result limits")
	}
	if base == searchCacheKey("netease", "same query", 10, 10, "account") {
		t.Fatal("cache key must distinguish offsets")
	}
	if base == searchCacheKey("netease", "same query", 10, 0, "other account") {
		t.Fatal("cache key must distinguish credential scopes")
	}
}

func TestSearchResultCacheEvictsLeastRecentlyUsedEntry(t *testing.T) {
	cache := newSearchResultCache()
	for i := 0; i < maxSearchCacheEntries; i++ {
		cache.insert(fmt.Sprint(i), []SearchResultItem{{SongID: fmt.Sprint(i)}})
	}
	// Touch key 0 so the next insertion should evict key 1 instead.
	if _, err := cache.do(context.Background(), "0", func(context.Context) ([]SearchResultItem, error) {
		return nil, errors.New("should be cached")
	}); err != nil {
		t.Fatal(err)
	}
	cache.insert("new", []SearchResultItem{{SongID: "new"}})
	if cache.entries["0"] == nil || cache.entries["1"] != nil || cache.entries["new"] == nil {
		t.Fatal("cache should evict the least recently used entry and stay bounded")
	}
}

func TestSearchPrefixReusesProviderChunksAcrossAdjacentUIPages(t *testing.T) {
	cache := newSearchResultCache()
	var calls atomic.Int32
	var offsets []int
	search := func(ctx context.Context, platform, keyword string, limit, offset int) ([]SearchResultItem, error) {
		calls.Add(1)
		offsets = append(offsets, offset)
		items := make([]SearchResultItem, limit)
		for i := range items {
			items[i] = SearchResultItem{Platform: platform, SongID: fmt.Sprint(offset + i)}
		}
		return items, nil
	}
	cached := func(ctx context.Context, platform, keyword string, limit, offset int) ([]SearchResultItem, error) {
		return cachedSearchPageWithCache(ctx, cache, platform, keyword, limit, offset, search)
	}

	first, err := searchPrefixWithPageSize(context.Background(), "youtube", "chunk-cache-test", 11, searchProviderPageSize(10), cached, nil)
	if err != nil || len(first) != 11 {
		t.Fatalf("unexpected first UI page prefix: len=%d err=%v", len(first), err)
	}
	second, err := searchPrefixWithPageSize(context.Background(), "youtube", "chunk-cache-test", 21, searchProviderPageSize(10), cached, nil)
	if err != nil || len(second) != 21 {
		t.Fatalf("unexpected second UI page prefix: len=%d err=%v", len(second), err)
	}
	if calls.Load() != 2 || fmt.Sprint(offsets) != "[0 20]" {
		t.Fatalf("next page should reuse the cached first provider chunk and fetch only the next chunk: calls=%d offsets=%v", calls.Load(), offsets)
	}
}
