package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/gin/response"
	"github.com/nichuanfang/gymdl/internal/playlistcatalog"
	"github.com/nichuanfang/gymdl/internal/storage"
)

// DiscoveryTrack is one confirmed recording the console can queue or dismiss.
// Card actions are not accepted from the browser.
type DiscoveryTrack struct {
	Platform   string `json:"platform"`
	ID         string `json:"id"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album,omitempty"`
	Duration   int    `json:"duration,omitempty"`
	Plays      int    `json:"plays,omitempty"`
	LastPlayed string `json:"last_played,omitempty"`
	Verified   bool   `json:"verified"`
}

// DiscoveryBatch is one rediscover result. Counts match the Telegram card.
type DiscoveryBatch struct {
	Tracks            []DiscoveryTrack
	SkippedOwned      int
	Unconfirmed       int
	SkippedShown      int
	SkippedPending    int
	SkippedDismissed  int
	SkippedQueued     int
	SkippedArtistCap  int
	SkippedDuplicate  int
	SkippedOther      int
	Unavailable       int
	LibraryStale      bool
	LibrarySnapshotAt int64
}

// DiscoveryEnqueueResult is one row's queue decision. The browser must use
// this instead of inferring per-song state from aggregate counts.
type DiscoveryEnqueueResult struct {
	Platform string `json:"platform"`
	ID       string `json:"id"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
}

// DiscoveryAPI is the running遗珠 service. The bot registers it at startup.
type DiscoveryAPI interface {
	Rediscover(ctx context.Context, platform string, limit int) (DiscoveryBatch, error)
	Enqueue(ctx context.Context, tracks []DiscoveryTrack) ([]DiscoveryEnqueueResult, error)
	Dismiss(ctx context.Context, tracks []DiscoveryTrack) (int, error)
	Restore(ctx context.Context, tracks []DiscoveryTrack) (int, error)
}

// AIPlaylistAPI reuses the same preview-then-confirm pipeline as /ai_playlist.
type AIPlaylistAPI interface {
	List(ctx context.Context) (AIPlaylistPage, error)
	Preview(ctx context.Context, req AIPreviewRequest) (AIPreviewPage, error)
	Confirm(ctx context.Context, token string) (AIConfirmPage, error)
}

type AIPlaylistPage struct {
	Presets   []AIPreset       `json:"presets"`
	Playlists []AIPlaylistItem `json:"playlists"`
	Warning   string           `json:"warning,omitempty"`
}

type AIPreset struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Text  string `json:"text"`
}

type AIPlaylistItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SongCount   int    `json:"song_count"`
}

type AIPreviewRequest struct {
	PlaylistID string `json:"playlist_id"`
	Name       string `json:"name"`
	Request    string `json:"request"`
	Preset     string `json:"preset"`
	Seed       string `json:"seed"`
}

type AIPreviewSong struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Reason string `json:"reason,omitempty"`
}

type AIPreviewPage struct {
	Token        string          `json:"token"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Intro        string          `json:"intro"`
	Total        int             `json:"total"`
	Preview      []AIPreviewSong `json:"preview"`
	UsedFallback bool            `json:"used_fallback"`
	Creating     bool            `json:"creating"`
	Existing     int             `json:"existing"`
}

type AIConfirmPage struct {
	Name         string `json:"name"`
	Count        int    `json:"count"`
	Intro        string `json:"intro"`
	Created      bool   `json:"created"`
	Replaced     bool   `json:"replaced"`
	UsedFallback bool   `json:"used_fallback"`
}

type discoveryTrackBody struct {
	Platform string `json:"platform"`
	ID       string `json:"id"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Duration int    `json:"duration"`
	Verified bool   `json:"verified"`
}

var (
	studioMu       sync.RWMutex
	discoveryAPI   DiscoveryAPI
	aiPlaylistAPI  AIPlaylistAPI
	playlistDBPath string
)

func SetDiscoveryAPI(api DiscoveryAPI) {
	studioMu.Lock()
	discoveryAPI = api
	studioMu.Unlock()
}

func SetAIPlaylistAPI(api AIPlaylistAPI) {
	studioMu.Lock()
	aiPlaylistAPI = api
	studioMu.Unlock()
}

// SetPlaylistDBPath overrides the catalog database. Tests use a temporary file.
func SetPlaylistDBPath(path string) {
	studioMu.Lock()
	playlistDBPath = path
	studioMu.Unlock()
}

func currentDiscoveryAPI() DiscoveryAPI {
	studioMu.RLock()
	defer studioMu.RUnlock()
	return discoveryAPI
}

func currentAIPlaylistAPI() AIPlaylistAPI {
	studioMu.RLock()
	defer studioMu.RUnlock()
	return aiPlaylistAPI
}

func currentPlaylistDBPath() string {
	studioMu.RLock()
	defer studioMu.RUnlock()
	if playlistDBPath != "" {
		return playlistDBPath
	}
	return storage.DefaultDatabasePath
}

func HandlePlaylistDescriptions(c *gin.Context) {
	store, err := playlistcatalog.Open(currentPlaylistDBPath())
	if err != nil {
		response.Fail(c, http.StatusServiceUnavailable, "歌单库还没准备好")
		return
	}
	defer store.Close()
	ctx := c.Request.Context()
	synced, err := store.Synced(ctx)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, "读取歌单同步状态失败")
		return
	}
	rows, err := store.Active(ctx)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, "读取歌单失败")
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{"id": row.ID, "name": row.Name, "desc": row.Desc})
	}
	response.Success(c, gin.H{"synced": synced, "items": items})
}

func HandleUpdatePlaylistDescription(c *gin.Context) {
	var body struct {
		Desc string `json:"desc"`
	}
	if !bindStudioJSON(c, 8*1024, &body) {
		return
	}
	store, err := playlistcatalog.Open(currentPlaylistDBPath())
	if err != nil {
		response.Fail(c, http.StatusServiceUnavailable, "歌单库还没准备好")
		return
	}
	defer store.Close()
	if err = store.SetDesc(c.Request.Context(), c.Param("id"), body.Desc); err != nil {
		response.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, gin.H{"id": c.Param("id"), "desc": strings.TrimSpace(body.Desc)})
}

func HandleSyncPlaylists(c *gin.Context) {
	cfg := GetWebConfig()
	if cfg == nil || cfg.PlaylistOrganizer == nil {
		response.Fail(c, http.StatusBadRequest, "尚未配置 Navidrome")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	result, err := playlistcatalog.Sync(ctx, cfg.PlaylistOrganizer, currentPlaylistDBPath(), nil)
	if err != nil {
		response.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	response.Success(c, gin.H{"added": result.Added, "updated": result.Updated, "inactive": result.Inactive, "synced": true})
}

func HandleRediscover(c *gin.Context) {
	var body struct {
		Platform string `json:"platform"`
		Limit    int    `json:"limit"`
	}
	if !bindStudioJSON(c, 1024, &body) {
		return
	}
	platform := strings.ToLower(strings.TrimSpace(body.Platform))
	if platform != "netease" && platform != "qq" && platform != "youtube" {
		response.Fail(c, http.StatusBadRequest, "请选择网易云、QQ 音乐或 YouTube")
		return
	}
	if body.Limit != 1 {
		response.Fail(c, http.StatusBadRequest, "请重新发起找回")
		return
	}
	api := currentDiscoveryAPI()
	if api == nil {
		response.Fail(c, http.StatusServiceUnavailable, "遗珠服务尚未就绪")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	batch, err := api.Rediscover(ctx, platform, body.Limit)
	if err != nil {
		response.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	response.Success(c, gin.H{
		"tracks":             batch.Tracks,
		"skipped_owned":      batch.SkippedOwned,
		"unconfirmed":        batch.Unconfirmed,
		"skipped_shown":      batch.SkippedShown,
		"skipped_pending":    batch.SkippedPending,
		"skipped_dismissed":  batch.SkippedDismissed,
		"skipped_queued":     batch.SkippedQueued,
		"skipped_artist_cap": batch.SkippedArtistCap,
		"skipped_duplicate":  batch.SkippedDuplicate,
		"skipped_other":      batch.SkippedOther,
		"unavailable":        batch.Unavailable,
		"requested":          body.Limit,
		"notice":             discoveryNotice(platform, body.Limit, batch),
	})
}

func HandleRediscoverEnqueue(c *gin.Context) {
	tracks, ok := readDiscoveryTracks(c)
	if !ok {
		return
	}
	api := currentDiscoveryAPI()
	if api == nil {
		response.Fail(c, http.StatusServiceUnavailable, "遗珠服务尚未就绪")
		return
	}
	results, err := api.Enqueue(c.Request.Context(), tracks)
	if err != nil {
		queued, skipped, failed := 0, 0, 0
		for _, result := range results {
			switch result.State {
			case "queued":
				queued++
			case "skipped":
				skipped++
			case "failed":
				failed++
			}
		}
		msg := fmt.Sprintf("提交 %d 首：实际排队 %d 首，跳过 %d 首，失败 %d 首。%s", len(tracks), queued, skipped, failed, studioPublicError(err))
		response.Fail(c, http.StatusBadRequest, msg)
		return
	}
	queued, skipped, failed := 0, 0, 0
	for _, result := range results {
		switch result.State {
		case "queued":
			queued++
		case "skipped":
			skipped++
		case "failed":
			failed++
		}
	}
	response.Success(c, gin.H{"queued": queued, "skipped": skipped, "failed": failed, "results": results})
}

func HandleRediscoverRestore(c *gin.Context) {
	tracks, ok := readDiscoveryTracks(c)
	if !ok {
		return
	}
	api := currentDiscoveryAPI()
	if api == nil {
		response.Fail(c, http.StatusServiceUnavailable, "遗珠服务尚未就绪")
		return
	}
	count, err := api.Restore(c.Request.Context(), tracks)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, gin.H{"restored": count})
}

func HandleRediscoverDismiss(c *gin.Context) {
	tracks, ok := readDiscoveryTracks(c)
	if !ok {
		return
	}
	api := currentDiscoveryAPI()
	if api == nil {
		response.Fail(c, http.StatusServiceUnavailable, "遗珠服务尚未就绪")
		return
	}
	count, err := api.Dismiss(c.Request.Context(), tracks)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, gin.H{"dismissed": count})
}

func HandleAIPlaylists(c *gin.Context) {
	api := currentAIPlaylistAPI()
	if api == nil {
		response.Fail(c, http.StatusServiceUnavailable, "AI 智能歌单服务尚未就绪")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	page, err := api.List(ctx)
	if err != nil {
		if len(page.Presets) == 0 {
			response.Fail(c, http.StatusBadGateway, err.Error())
			return
		}
		page.Warning = err.Error()
	}
	response.Success(c, page)
}

func HandleAIPlaylistPreview(c *gin.Context) {
	var body AIPreviewRequest
	if !bindStudioJSON(c, 8*1024, &body) {
		return
	}
	api := currentAIPlaylistAPI()
	if api == nil {
		response.Fail(c, http.StatusServiceUnavailable, "AI 智能歌单服务尚未就绪")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	page, err := api.Preview(ctx, body)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, page)
}

func HandleAIPlaylistConfirm(c *gin.Context) {
	var body struct {
		Token string `json:"token"`
	}
	if !bindStudioJSON(c, 1024, &body) {
		return
	}
	if strings.TrimSpace(body.Token) == "" {
		response.Fail(c, http.StatusBadRequest, "请先生成预览")
		return
	}
	api := currentAIPlaylistAPI()
	if api == nil {
		response.Fail(c, http.StatusServiceUnavailable, "AI 智能歌单服务尚未就绪")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	page, err := api.Confirm(ctx, strings.TrimSpace(body.Token))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, page)
}

func bindStudioJSON(c *gin.Context, limit int64, target any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	if err := c.ShouldBindJSON(target); err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) || errors.Is(err, io.EOF) {
			response.Fail(c, http.StatusBadRequest, "请求内容无效")
			return false
		}
		response.Fail(c, http.StatusBadRequest, "请求内容无效")
		return false
	}
	return true
}

func readDiscoveryTracks(c *gin.Context) ([]DiscoveryTrack, bool) {
	var body struct {
		Tracks []discoveryTrackBody `json:"tracks"`
	}
	if !bindStudioJSON(c, 32*1024, &body) {
		return nil, false
	}
	if len(body.Tracks) == 0 || len(body.Tracks) > 9 {
		response.Fail(c, http.StatusBadRequest, "一次最多处理 9 首")
		return nil, false
	}
	tracks := make([]DiscoveryTrack, 0, len(body.Tracks))
	for _, item := range body.Tracks {
		track, err := normalizeDiscoveryTrack(item)
		if err != nil {
			response.Fail(c, http.StatusBadRequest, err.Error())
			return nil, false
		}
		tracks = append(tracks, track)
	}
	return tracks, true
}

func normalizeDiscoveryTrack(item discoveryTrackBody) (DiscoveryTrack, error) {
	item.Platform = strings.ToLower(strings.TrimSpace(item.Platform))
	if item.Platform != "netease" && item.Platform != "qq" && item.Platform != "youtube" {
		return DiscoveryTrack{}, fmt.Errorf("平台无效")
	}
	item.ID = strings.TrimSpace(item.ID)
	item.Title = strings.TrimSpace(item.Title)
	item.Artist = strings.TrimSpace(item.Artist)
	item.URL = strings.TrimSpace(item.URL)
	if item.ID == "" || utf8.RuneCountInString(item.ID) > 128 || item.Title == "" || item.Artist == "" {
		return DiscoveryTrack{}, fmt.Errorf("歌曲信息不完整")
	}
	if item.URL != "" && !strings.HasPrefix(item.URL, "https://") && !strings.HasPrefix(item.URL, "http://") {
		return DiscoveryTrack{}, fmt.Errorf("试听链接无效")
	}
	if !item.Verified {
		return DiscoveryTrack{}, fmt.Errorf("只能处理已确认的版本")
	}
	return DiscoveryTrack{
		Platform: item.Platform, ID: item.ID, URL: item.URL, Title: item.Title,
		Artist: item.Artist, Album: strings.TrimSpace(item.Album), Duration: item.Duration, Verified: true,
	}, nil
}

func discoveryNotice(platform string, requested int, batch DiscoveryBatch) string {
	name := discoveryPlatformName(platform)
	count := len(batch.Tracks)
	reasonText := discoveryCandidateSummary(batch)
	snapshotNotice := rediscoverLibrarySnapshotNotice(batch.LibraryStale, batch.LibrarySnapshotAt)
	if count == 0 {
		var text string
		if reasonText != "" {
			text = fmt.Sprintf("这次在%s没有找到可入库的遗珠。候选池里：%s。可以换个平台，或稍后再试。", name, reasonText)
		} else {
			text = "在" + name + "里，这批历史和收藏暂时没有可确认的缺失歌曲。可以换个平台或稍后再试。"
		}
		return withSnapshotNotice(text, snapshotNotice)
	}
	if reasonText == "" {
		return snapshotNotice
	}
	return withSnapshotNotice(fmt.Sprintf("这次先听这首。候选池里还有：%s。", reasonText), snapshotNotice)
}

func withSnapshotNotice(text, notice string) string {
	if notice == "" {
		return text
	}
	if text == "" {
		return notice
	}
	return notice + "\n" + text
}

func rediscoverLibrarySnapshotNotice(stale bool, snapshotUnix int64) string {
	if !stale || snapshotUnix <= 0 {
		return ""
	}
	age := time.Since(time.Unix(snapshotUnix, 0))
	minutes := int(age / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	return fmt.Sprintf("曲库连接波动，本次使用了约 %d 分钟前的快照核对。", minutes)
}

// discoveryCandidateSummary separates the one result the user receives from
// the whole prefiltered candidate pool. Showing every raw counter inline made
// it look like the app had searched more than 600 songs for this one request.
func discoveryCandidateSummary(batch DiscoveryBatch) string {
	type group struct {
		counts []int
		label  string
	}
	groups := []group{
		{[]int{batch.SkippedOwned}, "曲库已有"},
		{[]int{batch.SkippedQueued, batch.SkippedPending}, "已在队列或待上传"},
		{[]int{batch.SkippedShown}, "最近看过"},
		{[]int{batch.SkippedDismissed, batch.SkippedOther}, "不再推荐或已处理"},
		{[]int{batch.Unconfirmed, batch.Unavailable}, "版本无法确认"},
		{[]int{batch.SkippedArtistCap, batch.SkippedDuplicate}, "本次去重或同艺人限流"},
	}
	var notes []string
	for _, item := range groups {
		total := 0
		for _, n := range item.counts {
			total += n
		}
		if total > 0 {
			notes = append(notes, fmt.Sprintf("%d 首%s", total, item.label))
		}
	}
	return strings.Join(notes, "，")
}

func discoveryPlatformName(platform string) string {
	switch platform {
	case "netease":
		return "网易云"
	case "qq":
		return "QQ 音乐"
	case "youtube":
		return "YouTube"
	default:
		return platform
	}
}
