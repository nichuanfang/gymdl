package bot

import (
	"sync"
	"time"
)

const (
	oliveTinReadCacheTTL = 2 * time.Minute
	navidromeTokenTTL    = 15 * time.Minute
)

// Repeated reads of the same Navidrome login and the two OliveTin lists used
// by /assign_playlist and /ai_playlist stay warm for a short time. Writes do
// not use this cache. A new playlist still shows up after the daily sync or
// the next cache window.
type timedText struct {
	value   string
	expires time.Time
}

var (
	readCacheMu     sync.Mutex
	oliveTinReads   = map[string]timedText{}
	navidromeTokens = map[string]timedText{}
)

func cacheableOliveTinAction(actionID string) bool {
	// ai_playlists stays uncached: /ai_playlist has to see a playlist created
	// moments earlier, including its current song count.
	return actionID == "songs_unplaylist"
}

func loadTimedText(cache map[string]timedText, key string, now time.Time) (string, bool) {
	item, ok := cache[key]
	if !ok || now.After(item.expires) {
		return "", false
	}
	return item.value, true
}

func storeTimedText(cache map[string]timedText, key, value string, ttl time.Duration, now time.Time) {
	if len(cache) > 64 {
		for existing := range cache {
			delete(cache, existing)
		}
	}
	cache[key] = timedText{value: value, expires: now.Add(ttl)}
}

func loadOliveTinRead(base, actionID string, now time.Time) (string, bool) {
	readCacheMu.Lock()
	defer readCacheMu.Unlock()
	return loadTimedText(oliveTinReads, base+"\n"+actionID, now)
}

func storeOliveTinRead(base, actionID, output string, now time.Time) {
	readCacheMu.Lock()
	defer readCacheMu.Unlock()
	storeTimedText(oliveTinReads, base+"\n"+actionID, output, oliveTinReadCacheTTL, now)
}

func loadNavidromeToken(base, username, password string, now time.Time) (string, bool) {
	readCacheMu.Lock()
	defer readCacheMu.Unlock()
	return loadTimedText(navidromeTokens, base+"\n"+username+"\n"+password, now)
}

func storeNavidromeToken(base, username, password, token string, now time.Time) {
	readCacheMu.Lock()
	defer readCacheMu.Unlock()
	storeTimedText(navidromeTokens, base+"\n"+username+"\n"+password, token, navidromeTokenTTL, now)
}

func dropOliveTinRead(base, actionID string) {
	readCacheMu.Lock()
	defer readCacheMu.Unlock()
	delete(oliveTinReads, base+"\n"+actionID)
}
