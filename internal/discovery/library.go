package discovery

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

type Library interface {
	Contains(context.Context, Track) (bool, error)
}

// libraryCacheTTL keeps the Navidrome catalog warm across /rediscover and the
// ownership checks that follow it. Downloads still go through the dedupe store.
const libraryCacheTTL = 10 * time.Minute

type NavidromeLibrary struct {
	Config  *config.PlaylistOrganizerConfig
	Client  *http.Client
	mu      sync.Mutex
	byTitle map[string][]Track
	expires time.Time
}

func (l *NavidromeLibrary) Invalidate() { l.mu.Lock(); l.expires = time.Time{}; l.mu.Unlock() }
func (l *NavidromeLibrary) Contains(ctx context.Context, t Track) (bool, error) {
	owned, _, err := l.matchOwnership(ctx, t)
	return owned, err
}

func (l *NavidromeLibrary) matchOwnership(ctx context.Context, t Track) (owned, sameTitle bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Now().After(l.expires) {
		if err := l.load(ctx); err != nil {
			return false, false, err
		}
	}
	owned, sameTitle = l.ownsLocked(t)
	return owned, sameTitle, nil
}

func (l *NavidromeLibrary) ownsLocked(t Track) (owned, sameTitle bool) {
	key := ownershipTitle(t.Title)
	if key == "" {
		return false, false
	}
	tracks := l.byTitle[key]
	for _, other := range tracks {
		if sameOwnedWork(t, other) {
			return true, true
		}
	}
	return false, len(tracks) != 0
}

func (l *NavidromeLibrary) load(ctx context.Context) error {
	c := l.Config
	if c == nil || c.NavidromeURL == "" || c.NavidromeUsername == "" || c.NavidromePassword == "" {
		return fmt.Errorf("请配置 Navidrome 连接后再核对实际曲库")
	}
	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	index := map[string][]Track{}
	seen := map[string]bool{}
	for offset := 0; offset < 100000; offset += 500 {
		salt := token()
		sum := md5.Sum([]byte(c.NavidromePassword + salt))
		q := url.Values{"u": {c.NavidromeUsername}, "t": {fmt.Sprintf("%x", sum)}, "s": {salt}, "v": {"1.16.1"}, "c": {"gymdl"}, "f": {"json"}, "query": {""}, "artistCount": {"0"}, "albumCount": {"0"}, "songCount": {"500"}, "songOffset": {strconv.Itoa(offset)}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.NavidromeURL, "/")+"/rest/search3.view?"+q.Encode(), nil)
		if err != nil {
			return fmt.Errorf("Navidrome 地址无效")
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("无法连接 Navidrome，未判断曲库缺失")
		}
		var body struct {
			Response struct {
				Status string `json:"status"`
				Search struct {
					Songs []struct {
						ID, Title, Artist, Album string
						Duration                 float64
					} `json:"song"`
				} `json:"searchResult3"`
			} `json:"subsonic-response"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || body.Response.Status != "ok" {
			return fmt.Errorf("Navidrome 曲库读取失败，未判断曲库缺失")
		}
		for _, s := range body.Response.Search.Songs {
			if s.ID == "" || seen[s.ID] {
				return fmt.Errorf("Navidrome 分页不完整，未判断曲库缺失")
			}
			seen[s.ID] = true
			t := Track{ID: s.ID, Title: s.Title, Artist: s.Artist, Album: s.Album, Duration: int(s.Duration), Verified: true}
			key := ownershipTitle(t.Title)
			if key == "" {
				key = "id:" + s.ID
			}
			index[key] = append(index[key], t)
		}
		if len(body.Response.Search.Songs) < 500 {
			l.byTitle = index
			l.expires = time.Now().Add(libraryCacheTTL)
			return nil
		}
	}
	return fmt.Errorf("Navidrome 曲库超过安全分页上限，未判断曲库缺失")
}
