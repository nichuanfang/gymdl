package discovery

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/utils"
)

type Library interface {
	Contains(context.Context, Track) (bool, error)
}

const (
	// A fresh snapshot avoids loading the full catalog for every click. A
	// recently expired snapshot remains usable for a short outage so a song is
	// not declared missing just because the tunnel is reconnecting.
	libraryCacheTTL   = 10 * time.Minute
	libraryMaxAge     = 30 * time.Minute
	libraryRetryDelay = 30 * time.Second
	navidromeAttempts = 3
)

var navidromeRetryDelay = func(attempt int) time.Duration {
	base := 2 * time.Second
	if attempt > 0 {
		base = 6 * time.Second
	}
	// Keep simultaneous callers from retrying on exactly the same boundary.
	return time.Duration(float64(base) * (0.8 + rand.Float64()*0.4))
}

type navidromeFailureKind int

const (
	navidromeUnavailable navidromeFailureKind = iota
	navidromeAuthentication
	navidromeInvalidResponse
	navidromeConfiguration
)

type navidromeFailure struct {
	kind       navidromeFailureKind
	statusCode int
	cause      error
	message    string
}

func (e *navidromeFailure) Error() string { return e.message }
func (e *navidromeFailure) Unwrap() error { return e.cause }

func (e *navidromeFailure) retryable() bool {
	if e.kind != navidromeUnavailable {
		return false
	}
	if e.statusCode >= 500 && e.statusCode <= 599 {
		return true
	}
	switch e.statusCode {
	case 0, http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooEarly,
		http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

// LibrarySnapshotState is safe to inspect after Contains returns. The caller
// can warn that ownership filtering used a recent snapshot during an outage.
type LibrarySnapshotState struct {
	Stale    bool
	LoadedAt time.Time
}

type NavidromeLibrary struct {
	Config     *config.PlaylistOrganizerConfig
	Client     *http.Client
	mu         sync.Mutex
	byTitle    map[string][]Track
	expires    time.Time
	snapshotAt time.Time
	retryAt    time.Time
	stale      bool
	lastErr    error
}

func (l *NavidromeLibrary) Invalidate() {
	l.mu.Lock()
	l.expires = time.Time{}
	l.retryAt = time.Time{}
	l.mu.Unlock()
}

func (l *NavidromeLibrary) SnapshotState() LibrarySnapshotState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return LibrarySnapshotState{Stale: l.stale, LoadedAt: l.snapshotAt}
}

func (l *NavidromeLibrary) Contains(ctx context.Context, t Track) (bool, error) {
	owned, _, err := l.matchOwnership(ctx, t)
	return owned, err
}

func (l *NavidromeLibrary) matchOwnership(ctx context.Context, t Track) (owned, ambiguous bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.After(l.expires) {
		if err := l.refreshLocked(ctx, now); err != nil {
			return false, false, err
		}
	}
	owned, ambiguous = l.ownsLocked(t)
	return owned, ambiguous, nil
}

func (l *NavidromeLibrary) refreshLocked(ctx context.Context, now time.Time) error {
	if l.byTitle != nil && now.Before(l.retryAt) {
		if l.canUseStaleLocked(now) {
			return nil
		}
		return l.lastErr
	}
	wasStale := l.stale
	if err := l.load(ctx); err != nil {
		l.lastErr = err
		l.retryAt = now.Add(libraryRetryDelay)
		if l.canUseStaleLocked(now) {
			if !l.stale {
				utils.WarnWithFormat("[Navidrome] 曲库刷新失败，暂用最近快照: %v", err)
			}
			l.stale = true
			return nil
		}
		return err
	}
	if wasStale {
		utils.InfoWithFormat("[Navidrome] 曲库连接已恢复，快照刷新成功")
	}
	return nil
}

func (l *NavidromeLibrary) canUseStaleLocked(now time.Time) bool {
	if l.byTitle == nil || l.snapshotAt.IsZero() {
		return false
	}
	age := now.Sub(l.snapshotAt)
	return age >= 0 && age <= libraryMaxAge
}

func (l *NavidromeLibrary) ownsLocked(t Track) (owned, ambiguous bool) {
	key := ownershipTitle(t.Title)
	if key == "" {
		return false, false
	}
	for _, other := range l.byTitle[key] {
		if sameOwnedWork(t, other) {
			return true, false
		}
		// Once the existing work match rules have ruled out overlapping
		// credits and related albums, require both fields on both tracks to
		// distinguish a verified source from a same-title library entry.
		if !t.Verified || !hasOwnershipCredits(t) || !hasOwnershipCredits(other) {
			ambiguous = true
		}
	}
	return false, ambiguous
}

func hasOwnershipCredits(t Track) bool {
	switch Normalize(t.Artist) {
	case "", "unknown", "unknownartist", "variousartists", "未知", "未知艺人", "未知歌手", "群星":
		return false
	}
	switch albumWorkKey(t.Album) {
	case "", "unknown", "unknownalbum", "未知", "未知专辑":
		return false
	}
	return len(artistCredits(t.Artist)) > 0
}

func (l *NavidromeLibrary) load(ctx context.Context) error {
	audio, err := ReadNavidromeAudio(ctx, l.Config, l.Client)
	if err != nil {
		return err
	}
	index := make(map[string][]Track, len(audio))
	for _, song := range audio {
		t := Track{ID: song.ID, Title: song.Title, Artist: song.Artist, Album: song.Album, Duration: int(song.Duration), Verified: true}
		key := ownershipTitle(t.Title)
		if key == "" {
			key = "id:" + song.ID
		}
		index[key] = append(index[key], t)
	}
	now := time.Now()
	l.byTitle = index
	l.snapshotAt = now
	l.expires = now.Add(libraryCacheTTL)
	l.retryAt = time.Time{}
	l.stale = false
	l.lastErr = nil
	return nil
}

type NavidromeAudio struct {
	ID, Title, Artist, Album string
	Path, Suffix             string
	Duration                 float64
	BitRate                  int
	Size                     int64
}

func ReadNavidromeAudio(ctx context.Context, c *config.PlaylistOrganizerConfig, client *http.Client) ([]NavidromeAudio, error) {
	if c == nil || c.NavidromeURL == "" || c.NavidromeUsername == "" || c.NavidromePassword == "" {
		return nil, &navidromeFailure{kind: navidromeConfiguration, message: "请配置 Navidrome 连接后再核对实际曲库"}
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var songs []NavidromeAudio
	seen := map[string]bool{}
	for offset := 0; offset < 100000; offset += 500 {
		page, err := readNavidromePage(ctx, c, client, offset)
		if err != nil {
			return nil, err
		}
		for _, s := range page {
			if s.ID == "" || seen[s.ID] {
				return nil, &navidromeFailure{kind: navidromeInvalidResponse, message: "Navidrome 曲库响应异常，请稍后重试"}
			}
			seen[s.ID] = true
			songs = append(songs, s)
		}
		if len(page) < 500 {
			return songs, nil
		}
	}
	return nil, &navidromeFailure{kind: navidromeInvalidResponse, message: "Navidrome 曲库超过安全分页上限，请检查曲库状态"}
}

func readNavidromePage(ctx context.Context, c *config.PlaylistOrganizerConfig, client *http.Client, offset int) ([]NavidromeAudio, error) {
	var lastErr error
	for attempt := 0; attempt < navidromeAttempts; attempt++ {
		songs, err := readNavidromePageOnce(ctx, c, client, offset)
		if err == nil {
			return songs, nil
		}
		lastErr = err
		var failure *navidromeFailure
		if !errors.As(err, &failure) || !failure.retryable() || attempt == navidromeAttempts-1 {
			return nil, err
		}
		timer := time.NewTimer(navidromeRetryDelay(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, navidromeContextFailure(ctx)
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func readNavidromePageOnce(ctx context.Context, c *config.PlaylistOrganizerConfig, client *http.Client, offset int) ([]NavidromeAudio, error) {
	salt := token()
	sum := md5.Sum([]byte(c.NavidromePassword + salt))
	q := url.Values{"u": {c.NavidromeUsername}, "t": {fmt.Sprintf("%x", sum)}, "s": {salt}, "v": {"1.16.1"}, "c": {"gymdl"}, "f": {"json"}, "query": {""}, "artistCount": {"0"}, "albumCount": {"0"}, "songCount": {"500"}, "songOffset": {strconv.Itoa(offset)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.NavidromeURL, "/")+"/rest/search3.view?"+q.Encode(), nil)
	if err != nil {
		return nil, &navidromeFailure{kind: navidromeConfiguration, cause: err, message: "Navidrome 地址无效"}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, navidromeContextFailure(ctx)
		}
		return nil, &navidromeFailure{kind: navidromeUnavailable, cause: err, message: "曲库隧道暂不可用，请稍后重试"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		message := "Navidrome 曲库服务返回异常，请稍后重试"
		kind := navidromeUnavailable
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			kind = navidromeAuthentication
			message = "Navidrome 登录失效，请检查账号配置"
		case http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooEarly,
			http.StatusTooManyRequests:
			message = "曲库隧道暂不可用，请稍后重试"
		default:
			if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
				message = "曲库隧道暂不可用，请稍后重试"
			}
		}
		return nil, &navidromeFailure{kind: kind, statusCode: resp.StatusCode, message: message}
	}
	var body struct {
		Response struct {
			Status string `json:"status"`
			Error  struct {
				Code int `json:"code"`
			} `json:"error"`
			Search struct {
				Songs []NavidromeAudio `json:"song"`
			} `json:"searchResult3"`
		} `json:"subsonic-response"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&body); err != nil {
		return nil, &navidromeFailure{kind: navidromeInvalidResponse, cause: err, message: "Navidrome 曲库响应异常，请稍后重试"}
	}
	if body.Response.Status != "ok" {
		kind := navidromeInvalidResponse
		message := "Navidrome 曲库响应异常，请稍后重试"
		if body.Response.Error.Code >= 40 && body.Response.Error.Code <= 43 {
			kind = navidromeAuthentication
			message = "Navidrome 登录失效，请检查账号配置"
		}
		return nil, &navidromeFailure{kind: kind, message: message}
	}
	return body.Response.Search.Songs, nil
}

func navidromeContextFailure(ctx context.Context) error {
	err := ctx.Err()
	if errors.Is(err, context.DeadlineExceeded) {
		return &navidromeFailure{kind: navidromeUnavailable, statusCode: -1, cause: err, message: "曲库连接超时，请稍后重试"}
	}
	return &navidromeFailure{kind: navidromeUnavailable, statusCode: -1, cause: err, message: "曲库核对已取消，请重新发起"}
}
