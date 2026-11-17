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
type NavidromeLibrary struct {
	Config  *config.PlaylistOrganizerConfig
	Client  *http.Client
	mu      sync.Mutex
	tracks  map[string][]Track
	expires time.Time
}

func (l *NavidromeLibrary) Invalidate() { l.mu.Lock(); l.expires = time.Time{}; l.mu.Unlock() }
func (l *NavidromeLibrary) Contains(ctx context.Context, t Track) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Now().After(l.expires) {
		if err := l.load(ctx); err != nil {
			return false, err
		}
	}
	for _, other := range l.tracks[Identity(t)] {
		if SameRecording(t, other) {
			return true, nil
		}
	}
	return false, nil
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
			index[Identity(t)] = append(index[Identity(t)], t)
		}
		if len(body.Response.Search.Songs) < 500 {
			l.tracks = index
			l.expires = time.Now().Add(time.Minute)
			return nil
		}
	}
	return fmt.Errorf("Navidrome 曲库超过安全分页上限，未判断曲库缺失")
}
