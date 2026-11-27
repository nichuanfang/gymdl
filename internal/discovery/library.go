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
	"go.uber.org/zap"
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

type navidromeWarningMode uint8

const (
	navidromeNoWarning navidromeWarningMode = iota
	navidromeNoSnapshot
	navidromeStaleSnapshot
	navidromeExpiredSnapshot
)

type navidromeWarningState struct {
	mode navidromeWarningMode
	kind navidromeFailureKind
}

type NavidromeLibrary struct {
	Config     *config.PlaylistOrganizerConfig
	Client     *http.Client
	Logger     *zap.Logger
	mu         sync.Mutex
	byTitle    map[string][]Track
	expires    time.Time
	snapshotAt time.Time
	retryAt    time.Time
	stale      bool
	lastErr    error
	warned     navidromeWarningState
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
	hadFailure := l.warned.mode != navidromeNoWarning
	stats, err := l.load(ctx)
	if err != nil {
		l.lastErr = err
		l.retryAt = now.Add(libraryRetryDelay)
		if l.canUseStaleLocked(now) {
			l.stale = true
			l.warnRefreshFailureLocked(navidromeStaleSnapshot, err, stats, now)
			return nil
		}
		mode := navidromeNoSnapshot
		if l.byTitle != nil && !l.snapshotAt.IsZero() {
			mode = navidromeExpiredSnapshot
		}
		l.warnRefreshFailureLocked(mode, err, stats, now)
		return err
	}
	if hadFailure {
		libraryLogger(l.Logger).Info("[Navidrome] 曲库连接已恢复，快照刷新成功",
			zap.Int("songs", stats.Songs),
			zap.Int("pages", stats.Pages),
			zap.Int("retries", stats.Retries),
			zap.Time("snapshot_at", l.snapshotAt),
		)
	}
	l.warned = navidromeWarningState{}
	return nil
}

func (l *NavidromeLibrary) warnRefreshFailureLocked(mode navidromeWarningMode, err error, stats navidromeReadStats, now time.Time) {
	failure := navidromeFailureInfo(err)
	if failure.canceled {
		return
	}
	state := navidromeWarningState{mode: mode, kind: failure.kind}
	if l.warned == state {
		return
	}

	fields := []zap.Field{
		zap.String("failure_kind", failure.kind.logValue()),
		zap.String("reason", failure.message),
		zap.Int("retries", stats.Retries),
	}
	if stats.Attempts > 0 {
		fields = append(fields, zap.Int("attempts", stats.Attempts))
	}
	if failure.statusCode != 0 {
		fields = append(fields, zap.Int("status_code", failure.statusCode))
	}
	if stats.LastRetryOffset >= 0 {
		fields = append(fields, zap.Int("last_retry_offset", stats.LastRetryOffset))
	}
	if stats.LastRetryStatus != 0 {
		fields = append(fields, zap.Int("last_retry_status", stats.LastRetryStatus))
	}

	message := "Navidrome 曲库连接失败，暂无可用的历史快照"
	switch mode {
	case navidromeStaleSnapshot:
		age := now.Sub(l.snapshotAt)
		message = "Navidrome 曲库刷新失败，继续使用最近快照"
		fields = append(fields,
			zap.Time("snapshot_at", l.snapshotAt),
			zap.Duration("snapshot_age", age),
			zap.Duration("snapshot_max_age", libraryMaxAge),
		)
	case navidromeExpiredSnapshot:
		age := now.Sub(l.snapshotAt)
		message = "Navidrome 最近快照已超过可用时限，暂时无法核对曲库"
		fields = append(fields,
			zap.Time("snapshot_at", l.snapshotAt),
			zap.Duration("snapshot_age", age),
			zap.Duration("snapshot_max_age", libraryMaxAge),
		)
	}
	libraryLogger(l.Logger).Warn(message, fields...)
	l.warned = state
}

func libraryLogger(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return zap.NewNop()
	}
	return logger
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

func (l *NavidromeLibrary) load(ctx context.Context) (navidromeReadStats, error) {
	initialLoad := l.byTitle == nil
	audio, stats, err := readNavidromeAudio(ctx, l.Config, l.Client)
	if err != nil {
		return stats, err
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
	if l.logger() != nil && l.warned.mode == navidromeNoWarning && (initialLoad || stats.Retries > 0) {
		message := "Navidrome 曲库首次加载完成"
		if !initialLoad {
			message = "Navidrome 曲库重试后刷新完成"
		}
		fields := []zap.Field{
			zap.Int("songs", stats.Songs),
			zap.Int("pages", stats.Pages),
			zap.Int("retries", stats.Retries),
		}
		if stats.LastRetryOffset >= 0 {
			fields = append(fields, zap.Int("last_retry_offset", stats.LastRetryOffset))
		}
		if stats.LastRetryStatus != 0 {
			fields = append(fields, zap.Int("last_retry_status", stats.LastRetryStatus))
		}
		l.logger().Info(message, fields...)
	}
	return stats, nil
}

func (l *NavidromeLibrary) logger() *zap.Logger {
	if l == nil || l.Logger == nil {
		return nil
	}
	return l.Logger
}

type NavidromeAudio struct {
	ID, Title, Artist, Album string
	Path, Suffix             string
	Duration                 float64
	BitRate                  int
	Size                     int64
}

func ReadNavidromeAudio(ctx context.Context, c *config.PlaylistOrganizerConfig, client *http.Client) ([]NavidromeAudio, error) {
	audio, _, err := readNavidromeAudio(ctx, c, client)
	return audio, err
}

type navidromeReadStats struct {
	Songs           int
	Pages           int
	Retries         int
	Attempts        int
	LastRetryOffset int
	LastRetryStatus int
}

func readNavidromeAudio(ctx context.Context, c *config.PlaylistOrganizerConfig, client *http.Client) ([]NavidromeAudio, navidromeReadStats, error) {
	stats := navidromeReadStats{LastRetryOffset: -1}
	if c == nil || c.NavidromeURL == "" || c.NavidromeUsername == "" || c.NavidromePassword == "" {
		return nil, stats, &navidromeFailure{kind: navidromeConfiguration, message: "请配置 Navidrome 连接后再核对实际曲库"}
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var songs []NavidromeAudio
	seen := map[string]bool{}
	for offset := 0; offset < 100000; offset += 500 {
		page, retries, lastStatus, err := readNavidromePage(ctx, c, client, offset)
		stats.Attempts = retries + 1
		if retries > 0 {
			stats.Retries += retries
			stats.LastRetryOffset = offset
			stats.LastRetryStatus = lastStatus
		}
		if err != nil {
			return nil, stats, err
		}
		for _, s := range page {
			if s.ID == "" || seen[s.ID] {
				return nil, stats, &navidromeFailure{kind: navidromeInvalidResponse, message: "Navidrome 曲库响应异常，请稍后重试"}
			}
			seen[s.ID] = true
			songs = append(songs, s)
		}
		stats.Songs = len(songs)
		stats.Pages++
		if len(page) < 500 {
			return songs, stats, nil
		}
	}
	return nil, stats, &navidromeFailure{kind: navidromeInvalidResponse, message: "Navidrome 曲库超过安全分页上限，请检查曲库状态"}
}

func readNavidromePage(ctx context.Context, c *config.PlaylistOrganizerConfig, client *http.Client, offset int) ([]NavidromeAudio, int, int, error) {
	var lastErr error
	retries := 0
	lastStatus := 0
	for attempt := 0; attempt < navidromeAttempts; attempt++ {
		songs, err := readNavidromePageOnce(ctx, c, client, offset)
		if err == nil {
			return songs, retries, lastStatus, nil
		}
		lastErr = err
		var failure *navidromeFailure
		if !errors.As(err, &failure) || !failure.retryable() || attempt == navidromeAttempts-1 {
			return nil, retries, lastStatus, err
		}
		retries++
		lastStatus = failure.statusCode
		timer := time.NewTimer(navidromeRetryDelay(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, retries, lastStatus, navidromeContextFailure(ctx)
		case <-timer.C:
		}
	}
	return nil, retries, lastStatus, lastErr
}

type navidromeFailureInfoValue struct {
	kind       navidromeFailureKind
	statusCode int
	message    string
	canceled   bool
}

func navidromeFailureInfo(err error) navidromeFailureInfoValue {
	info := navidromeFailureInfoValue{message: "unknown failure"}
	var failure *navidromeFailure
	if errors.As(err, &failure) {
		info.kind = failure.kind
		info.statusCode = failure.statusCode
		info.message = failure.message
		info.canceled = errors.Is(err, context.Canceled)
	}
	return info
}

func (kind navidromeFailureKind) logValue() string {
	switch kind {
	case navidromeAuthentication:
		return "authentication"
	case navidromeInvalidResponse:
		return "invalid_response"
	case navidromeConfiguration:
		return "configuration"
	default:
		return "unavailable"
	}
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
