package web

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/gin/response"
)

// FavoritesAPI is the same discovery store the Telegram favorites card uses.
// Binding establishes a baseline and does not download the current snapshot.
type FavoritesAPI interface {
	FavoriteStatus(ctx context.Context) (FavoritesPage, error)
	PreviewFavorite(ctx context.Context, platform string) (FavoritePreview, error)
	BindFavorite(ctx context.Context, token string) (FavoritesPage, error)
	PauseFavorite(ctx context.Context, platform string, paused bool) (FavoritesPage, error)
	SyncFavorites(ctx context.Context) (FavoritesPage, error)
	RetryFavorites(ctx context.Context) (FavoritesPage, error)
	RefreshFavorites(ctx context.Context) (FavoritesPage, error)
	ResolveFavorite(ctx context.Context, id string, accept bool) (FavoritesPage, error)
	DiscoveryJobs(ctx context.Context) ([]DiscoveryJobView, error)
}

type FavoritesPage struct {
	Enabled          bool                 `json:"enabled"`
	PollMinutes      int                  `json:"poll_minutes"`
	Notice           string               `json:"notice,omitempty"`
	Platforms        []FavoritePlatform   `json:"platforms"`
	Queue            []FavoriteQueueCount `json:"queue"`
	Pending          []FavoriteJob        `json:"pending"`
	IngestExceptions []FavoriteJob        `json:"ingest_exceptions"`
}

type FavoritePlatform struct {
	Platform string `json:"platform"`
	Label    string `json:"label"`
	Bound    bool   `json:"bound"`
	Account  string `json:"account,omitempty"`
	LastSync string `json:"last_sync,omitempty"`
	Paused   bool   `json:"paused"`
	Error    string `json:"error,omitempty"`
	State    string `json:"state"`
}

type FavoriteQueueCount struct {
	State string `json:"state"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type FavoriteJob struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	TrackID  string `json:"track_id"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	State    string `json:"state"`
	Label    string `json:"label"`
	Error    string `json:"error,omitempty"`
}

type FavoriteSample struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

type FavoritePreview struct {
	Token    string           `json:"token"`
	Platform string           `json:"platform"`
	Account  string           `json:"account"`
	Count    int              `json:"count"`
	Enabled  bool             `json:"enabled"`
	Samples  []FavoriteSample `json:"samples,omitempty"`
}

type DiscoveryJobView struct {
	Platform string `json:"platform"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	State    string `json:"state"`
	Label    string `json:"label"`
	Error    string `json:"error,omitempty"`
}

func SetFavoritesAPI(api FavoritesAPI) {
	studioMu.Lock()
	favoritesAPI = api
	studioMu.Unlock()
}

func currentFavoritesAPI() FavoritesAPI {
	studioMu.RLock()
	defer studioMu.RUnlock()
	return favoritesAPI
}

var favoritesAPI FavoritesAPI

func HandleFavorites(c *gin.Context) {
	page, ok := favoriteCall(c, func(api FavoritesAPI, ctx context.Context) (FavoritesPage, error) {
		return api.FavoriteStatus(ctx)
	})
	if ok {
		response.Success(c, page)
	}
}

func HandleFavoritePreview(c *gin.Context) {
	var body struct {
		Platform string `json:"platform"`
	}
	if !bindStudioJSON(c, 1024, &body) {
		return
	}
	platform := favoritePlatform(body.Platform)
	if platform == "" {
		response.Fail(c, http.StatusBadRequest, "请选择网易云、QQ 音乐或 YouTube")
		return
	}
	api := currentFavoritesAPI()
	if api == nil {
		response.Fail(c, http.StatusServiceUnavailable, "收藏服务尚未就绪")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()
	preview, err := api.PreviewFavorite(ctx, platform)
	if err != nil {
		writeFavoriteError(c, err)
		return
	}
	response.Success(c, preview)
}

func HandleFavoriteBind(c *gin.Context) {
	var body struct {
		Token string `json:"token"`
	}
	if !bindStudioJSON(c, 1024, &body) {
		return
	}
	if strings.TrimSpace(body.Token) == "" {
		response.Fail(c, http.StatusBadRequest, "请先读取收藏")
		return
	}
	page, ok := favoriteCall(c, func(api FavoritesAPI, ctx context.Context) (FavoritesPage, error) {
		return api.BindFavorite(ctx, strings.TrimSpace(body.Token))
	})
	if ok {
		response.Success(c, page)
	}
}

func HandleFavoritePause(c *gin.Context) {
	var body struct {
		Platform string `json:"platform"`
		Paused   bool   `json:"paused"`
	}
	if !bindStudioJSON(c, 1024, &body) {
		return
	}
	platform := favoritePlatform(body.Platform)
	if platform == "" {
		response.Fail(c, http.StatusBadRequest, "请选择网易云、QQ 音乐或 YouTube")
		return
	}
	page, ok := favoriteCall(c, func(api FavoritesAPI, ctx context.Context) (FavoritesPage, error) {
		return api.PauseFavorite(ctx, platform, body.Paused)
	})
	if ok {
		response.Success(c, page)
	}
}

func HandleFavoriteSync(c *gin.Context) {
	api := currentFavoritesAPI()
	if api == nil {
		response.Fail(c, http.StatusServiceUnavailable, "收藏服务尚未就绪")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()
	page, err := api.SyncFavorites(ctx)
	if err != nil {
		writeFavoriteError(c, err)
		return
	}
	response.Success(c, page)
}

func HandleFavoriteRetry(c *gin.Context) {
	page, ok := favoriteCall(c, func(api FavoritesAPI, ctx context.Context) (FavoritesPage, error) {
		return api.RetryFavorites(ctx)
	})
	if ok {
		response.Success(c, page)
	}
}

func HandleFavoriteRefresh(c *gin.Context) {
	page, ok := favoriteCall(c, func(api FavoritesAPI, ctx context.Context) (FavoritesPage, error) {
		return api.RefreshFavorites(ctx)
	})
	if ok {
		response.Success(c, page)
	}
}

func HandleFavoriteResolve(c *gin.Context) {
	var body struct {
		ID     string `json:"id"`
		Accept bool   `json:"accept"`
	}
	if !bindStudioJSON(c, 1024, &body) {
		return
	}
	if strings.TrimSpace(body.ID) == "" {
		response.Fail(c, http.StatusBadRequest, "请选择要处理的歌曲")
		return
	}
	page, ok := favoriteCall(c, func(api FavoritesAPI, ctx context.Context) (FavoritesPage, error) {
		return api.ResolveFavorite(ctx, strings.TrimSpace(body.ID), body.Accept)
	})
	if ok {
		response.Success(c, page)
	}
}

func HandleDiscoveryJobs(c *gin.Context) {
	api := currentFavoritesAPI()
	if api == nil {
		response.Fail(c, http.StatusServiceUnavailable, "遗珠服务尚未就绪")
		return
	}
	jobs, err := api.DiscoveryJobs(c.Request.Context())
	if err != nil {
		writeFavoriteError(c, err)
		return
	}
	if jobs == nil {
		jobs = []DiscoveryJobView{}
	}
	response.Success(c, gin.H{"jobs": jobs})
}

func favoriteCall(c *gin.Context, fn func(FavoritesAPI, context.Context) (FavoritesPage, error)) (FavoritesPage, bool) {
	api := currentFavoritesAPI()
	if api == nil {
		response.Fail(c, http.StatusServiceUnavailable, "收藏服务尚未就绪")
		return FavoritesPage{}, false
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	page, err := fn(api, ctx)
	if err != nil {
		writeFavoriteError(c, err)
		return FavoritesPage{}, false
	}
	return page, true
}

func favoritePlatform(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform != "netease" && platform != "qq" && platform != "youtube" {
		return ""
	}
	return platform
}

func writeFavoriteError(c *gin.Context, err error) {
	msg := studioPublicError(err)
	code := http.StatusBadGateway
	switch {
	case strings.Contains(msg, "尚未就绪"), strings.Contains(msg, "授权用户"):
		code = http.StatusServiceUnavailable
	case strings.Contains(msg, "正在处理"):
		code = http.StatusConflict
	case strings.Contains(msg, "请先"), strings.Contains(msg, "请选择"), strings.Contains(msg, "无效"), strings.Contains(msg, "未启用"), strings.Contains(msg, "过期"), strings.Contains(msg, "不存在"), strings.Contains(msg, "没有绑定"), strings.Contains(msg, "不完整"):
		code = http.StatusBadRequest
	}
	response.Fail(c, code, msg)
}

func studioPublicError(err error) string {
	if err == nil {
		return "操作失败"
	}
	text := strings.Join(strings.Fields(err.Error()), " ")
	if utf8.RuneCountInString(text) <= 160 {
		return text
	}
	runes := []rune(text)
	return string(runes[:159]) + "…"
}
