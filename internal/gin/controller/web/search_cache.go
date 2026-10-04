package web

import (
	"container/list"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

const (
	searchResultCacheTTL  = 15 * time.Second
	maxSearchCacheEntries = 512
)

var searchProviderTimeout = 20 * time.Second

type contextSearchFunc func(context.Context, string, string, int, int) ([]SearchResultItem, error)

type searchCacheEntry struct {
	key       string
	items     []SearchResultItem
	expiresAt time.Time
}

type searchCacheFlight struct {
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	waiters  int
	finished bool
	items    []SearchResultItem
	err      error
}

type searchResultCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
	flights map[string]*searchCacheFlight
	now     func() time.Time
}

func newSearchResultCache() *searchResultCache {
	return &searchResultCache{
		entries: make(map[string]*list.Element),
		lru:     list.New(),
		flights: make(map[string]*searchCacheFlight),
		now:     time.Now,
	}
}

var webSearchCache = newSearchResultCache()

// searchCacheScope prevents cached results from crossing credential, account,
// endpoint, or VIP-level boundaries without retaining secrets in cache keys.
func searchCacheScope(platform string) string {
	cfg := GetWebConfig()
	scope := platform
	switch platform {
	case "qq":
		var qq *config.QQMusicApiConfig
		if cfg != nil {
			qq = cfg.QQMusicApiConfig
		}
		credential, _, _ := currentQQMusicCredential(qqMusicCredentialPath, qq)
		account, vip, endpoint := "", "", ""
		if credential != nil {
			account = strings.TrimSpace(credential.MusicId) + ":" + strings.TrimSpace(credential.StrMusicId)
		}
		if qq != nil {
			vip = strings.ToLower(strings.TrimSpace(qq.VipLevel))
			endpoint = strings.TrimRight(strings.TrimSpace(qq.Endpoint), "/")
		}
		scope = fmt.Sprintf("qq\x00%s\x00%s\x00%s", endpoint, account, vip)
	case "netease":
		cookiePath, musicU := neteaseSearchCookie()
		scope = fmt.Sprintf("netease\x00%s\x00%x", cookiePath, sha256.Sum256([]byte(musicU)))
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(scope)))
}

func (c *searchResultCache) do(ctx context.Context, key string, search func(context.Context) ([]SearchResultItem, error)) ([]SearchResultItem, error) {
	c.mu.Lock()
	if element := c.entries[key]; element != nil {
		entry := element.Value.(*searchCacheEntry)
		if c.now().Before(entry.expiresAt) {
			c.lru.MoveToFront(element)
			items := cloneSearchItems(entry.items)
			c.mu.Unlock()
			return items, nil
		}
		c.removeElement(element)
	}
	if flight := c.flights[key]; flight != nil {
		if flight.ctx.Err() == nil && !flight.finished {
			flight.waiters++
			c.mu.Unlock()
			return c.waitForFlight(ctx, key, flight)
		}
		delete(c.flights, key)
	}

	flightCtx, cancel := context.WithTimeout(context.Background(), searchProviderTimeout)
	flight := &searchCacheFlight{ctx: flightCtx, cancel: cancel, done: make(chan struct{}), waiters: 1}
	c.flights[key] = flight
	c.mu.Unlock()

	go func() {
		items, err := search(flight.ctx)
		c.mu.Lock()
		flight.items = cloneSearchItems(items)
		flight.err = err
		flight.finished = true
		if current := c.flights[key]; current == flight {
			delete(c.flights, key)
		}
		if err == nil && flight.ctx.Err() == nil {
			c.insert(key, items)
		}
		close(flight.done)
		c.mu.Unlock()
		flight.cancel()
	}()

	return c.waitForFlight(ctx, key, flight)
}

func (c *searchResultCache) waitForFlight(ctx context.Context, key string, flight *searchCacheFlight) ([]SearchResultItem, error) {
	select {
	case <-flight.done:
		return cloneSearchItems(flight.items), flight.err
	case <-ctx.Done():
		c.mu.Lock()
		if !flight.finished {
			flight.waiters--
			if flight.waiters == 0 {
				flight.cancel()
			}
		}
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *searchResultCache) insert(key string, items []SearchResultItem) {
	if element := c.entries[key]; element != nil {
		entry := element.Value.(*searchCacheEntry)
		entry.items = cloneSearchItems(items)
		entry.expiresAt = c.now().Add(searchResultCacheTTL)
		c.lru.MoveToFront(element)
		return
	}
	entry := &searchCacheEntry{key: key, items: cloneSearchItems(items), expiresAt: c.now().Add(searchResultCacheTTL)}
	c.entries[key] = c.lru.PushFront(entry)
	for len(c.entries) > maxSearchCacheEntries {
		c.removeElement(c.lru.Back())
	}
}

func (c *searchResultCache) removeElement(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*searchCacheEntry)
	delete(c.entries, entry.key)
	c.lru.Remove(element)
}

func cloneSearchItems(items []SearchResultItem) []SearchResultItem {
	return append([]SearchResultItem(nil), items...)
}

func cachedSearchPage(ctx context.Context, platform, keyword string, limit, offset int, search contextSearchFunc) ([]SearchResultItem, error) {
	return cachedSearchPageWithCache(ctx, webSearchCache, platform, keyword, limit, offset, search)
}

func cachedSearchPageWithCache(ctx context.Context, cache *searchResultCache, platform, keyword string, limit, offset int, search contextSearchFunc) ([]SearchResultItem, error) {
	key := searchCacheKey(platform, keyword, limit, offset, searchCacheScope(platform))
	return cache.do(ctx, key, func(ctx context.Context) ([]SearchResultItem, error) {
		return search(ctx, platform, keyword, limit, offset)
	})
}

func searchCacheKey(platform, keyword string, limit, offset int, scope string) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s", platform, keyword, limit, offset, scope)
}
