package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	ncmapi "github.com/XiaoMengXinX/Music163Api-Go/api"
	ncmtypes "github.com/XiaoMengXinX/Music163Api-Go/types"
	ncmutils "github.com/XiaoMengXinX/Music163Api-Go/utils"
	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/gin/response"
	"github.com/nichuanfang/gymdl/utils"
	"golang.org/x/sync/singleflight"
)

// SearchResultItem 统一的搜索结果条目
type SearchResultItem struct {
	Platform    string `json:"platform"`     // netease / qq / youtube / bilibili
	SongID      string `json:"song_id"`      // 平台内部数字 ID 或视频 ID
	SongMID     string `json:"song_mid"`     // QQ 专有 mid
	Name        string `json:"name"`         // 歌曲或视频标题
	Artists     string `json:"artists"`      // 歌手 / UP 主
	Album       string `json:"album"`        // 专辑名（B 站无则为空）
	DurationSec int    `json:"duration_sec"` // 时长（秒）
	URL         string `json:"url"`          // 可直接提交下载的链接
	IsVIP       bool   `json:"is_vip"`       // 是否 VIP / 付费
}

// SearchPlatformErrors 各平台的错误信息
type SearchPlatformErrors map[string]string

var searchProviderSearch contextSearchFunc = searchPlatformWithContext

// searchRequest is shared by the JSON and SSE search endpoints.
type searchRequest struct {
	keyword   string
	platforms []string
	offset    int
	limit     int
}

func parseSearchRequest(c *gin.Context) (searchRequest, string) {
	keyword := strings.TrimSpace(c.Query("keyword"))
	if keyword == "" {
		return searchRequest{}, "关键词不能为空"
	}
	if len(keyword) > 200 {
		return searchRequest{}, "关键词过长"
	}
	platforms := parsePlatforms(c.DefaultQuery("platform", "netease"))
	if len(platforms) == 0 {
		return searchRequest{}, "无效的平台参数"
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if err != nil || limit <= 0 || limit > 100 {
		return searchRequest{}, "每页数量必须在 1 到 100 之间"
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 || offset > 2000 {
		return searchRequest{}, "分页位置超出范围"
	}
	return searchRequest{keyword: keyword, platforms: platforms, offset: offset, limit: limit}, ""
}

// HandleSearch GET /api/web/search?keyword=xxx&platform=netease,qq,youtube,bilibili&offset=0&limit=10
// This non-streaming endpoint remains available for existing clients.
func HandleSearch(c *gin.Context) {
	request, message := parseSearchRequest(c)
	if message != "" {
		response.Fail(c, http.StatusBadRequest, message)
		return
	}
	search := func(ctx context.Context, platform, keyword string, limit, offset int) ([]SearchResultItem, error) {
		return cachedSearchPage(ctx, platform, keyword, limit, offset, searchProviderSearch)
	}
	items, total, hasMore, errs := combinedSearchPageContext(c.Request.Context(), request.platforms, request.keyword, request.offset, request.limit, search)
	if utils.Logger() != nil {
		for platform, searchErr := range errs {
			utils.WarnWithFormat("[WebSearch] 平台 %s 搜索失败: %v", platform, searchErr)
		}
	}
	response.Success(c, gin.H{
		"items":    items,
		"total":    total,
		"has_more": hasMore,
		"errors":   errs,
		"keyword":  request.keyword,
	})
}

func searchPrefix(platform, keyword string, target int, search func(string, string, int, int) ([]SearchResultItem, error)) ([]SearchResultItem, error) {
	return searchPrefixWithProgress(context.Background(), platform, keyword, target,
		func(_ context.Context, p, q string, limit, offset int) ([]SearchResultItem, error) {
			return search(p, q, limit, offset)
		}, nil)
}

func searchProviderPageSize(limit int) int {
	if limit < 20 {
		return 20
	}
	if limit > 50 {
		return 50
	}
	return limit
}

func searchPrefixWithProgress(ctx context.Context, platform, keyword string, target int, search contextSearchFunc, onProgress func([]SearchResultItem)) ([]SearchResultItem, error) {
	pageSize := min(target, 50)
	return searchPrefixWithPageSize(ctx, platform, keyword, target, pageSize, search, onProgress)
}

func searchPrefixWithPageSize(ctx context.Context, platform, keyword string, target, pageSize int, search contextSearchFunc, onProgress func([]SearchResultItem)) ([]SearchResultItem, error) {
	if target <= 0 {
		return nil, nil
	}
	if pageSize <= 0 {
		pageSize = 1
	}
	if pageSize > 50 {
		pageSize = 50
	}
	items := make([]SearchResultItem, 0, target)
	for offset := 0; offset < target; {
		if err := ctx.Err(); err != nil {
			return items, err
		}
		batch, err := search(ctx, platform, keyword, pageSize, offset)
		if err != nil {
			return items, err
		}
		if len(batch) == 0 {
			break
		}
		remaining := target - len(items)
		if len(batch) > remaining {
			batch = batch[:remaining]
		}
		items = append(items, batch...)
		if onProgress != nil {
			onProgress(cloneSearchItems(batch))
		}
		offset += pageSize
		if len(batch) < pageSize {
			break
		}
	}
	return items, nil
}

func combinedSearchPage(platforms []string, keyword string, offset, limit int, search func(string, string, int, int) ([]SearchResultItem, error)) ([]SearchResultItem, int, bool, SearchPlatformErrors) {
	return combinedSearchPageContext(context.Background(), platforms, keyword, offset, limit,
		func(_ context.Context, platform, keyword string, pageSize, pageOffset int) ([]SearchResultItem, error) {
			return search(platform, keyword, pageSize, pageOffset)
		})
}

func combinedSearchPageContext(ctx context.Context, platforms []string, keyword string, offset, limit int, search contextSearchFunc) ([]SearchResultItem, int, bool, SearchPlatformErrors) {
	target := offset + limit + 1
	perPlatform := make([][]SearchResultItem, len(platforms))
	platformErrors := make([]error, len(platforms))
	var wg sync.WaitGroup
	for i, platform := range platforms {
		wg.Add(1)
		go func(index int, p string) {
			defer wg.Done()
			platformCtx, cancel := context.WithTimeout(ctx, searchProviderTimeout)
			defer cancel()
			items, err := searchPrefixWithPageSize(platformCtx, p, keyword, target, searchProviderPageSize(limit), search, nil)
			perPlatform[index] = items
			platformErrors[index] = err
		}(i, platform)
	}
	wg.Wait()
	errs := make(SearchPlatformErrors)
	for i, err := range platformErrors {
		if err != nil {
			errs[platforms[i]] = err.Error()
		}
	}
	return searchPageFromPrefixes(perPlatform, offset, limit, errs)
}

func searchPageFromPrefixes(perPlatform [][]SearchResultItem, offset, limit int, errs SearchPlatformErrors) ([]SearchResultItem, int, bool, SearchPlatformErrors) {
	merged := interleaveSearchResults(perPlatform)
	if offset >= len(merged) {
		return []SearchResultItem{}, 0, false, errs
	}
	end := offset + limit
	if end > len(merged) {
		end = len(merged)
	}
	page := append([]SearchResultItem(nil), merged[offset:end]...)
	return page, len(page), len(merged) > offset+limit, errs
}

func interleaveSearchResults(platforms [][]SearchResultItem) []SearchResultItem {
	total := 0
	maxItems := 0
	for _, items := range platforms {
		total += len(items)
		if len(items) > maxItems {
			maxItems = len(items)
		}
	}
	merged := make([]SearchResultItem, 0, total)
	for i := 0; i < maxItems; i++ {
		for _, items := range platforms {
			if i < len(items) {
				merged = append(merged, items[i])
			}
		}
	}
	return merged
}

// parsePlatforms 解析平台参数
func parsePlatforms(s string) []string {
	valid := map[string]bool{"netease": true, "qq": true, "youtube": true, "bilibili": true}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]bool)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if valid[p] && !seen[p] {
			result = append(result, p)
			seen[p] = true
		}
	}
	return result
}

// searchPlatform 分发到具体平台。
func searchPlatform(platform, keyword string, limit, offset int) ([]SearchResultItem, error) {
	return searchPlatformWithContext(context.Background(), platform, keyword, limit, offset)
}

func searchPlatformWithContext(ctx context.Context, platform, keyword string, limit, offset int) ([]SearchResultItem, error) {
	switch platform {
	case "netease":
		return searchNeteaseWithContext(ctx, keyword, limit, offset)
	case "qq":
		return searchQQWithContext(ctx, keyword, limit, offset)
	case "youtube":
		return searchYouTubeWithContext(ctx, keyword, limit, offset)
	case "bilibili":
		return searchBilibiliWithContext(ctx, keyword, limit, offset)
	}
	return nil, fmt.Errorf("未知平台: %s", platform)
}

/* ------------------------ 网易云 ------------------------ */

func searchNetease(keyword string, limit, offset int) ([]SearchResultItem, error) {
	return searchNeteaseWithContext(context.Background(), keyword, limit, offset)
}

func searchNeteaseWithContext(ctx context.Context, keyword string, limit, offset int) ([]SearchResultItem, error) {
	cookiePath, musicU := neteaseSearchCookie()
	if musicU == "" && cookiePath != "" {
		utils.WarnWithFormat("[WebSearch] NCM Cookie 未找到: path=%s", cookiePath)
	}
	result, err := requestNeteaseSearch(ctx, "https://music.163.com/eapi/v1/search/song/get", keyword, limit, offset, musicU)
	if err != nil {
		return nil, fmt.Errorf("网易云搜索失败: %w", err)
	}
	if utils.Logger() != nil {
		utils.DebugWithFormat("[WebSearch] NCM code=%d, songs=%d, hasCookie=%v, cookiePath=%s", result.Code, len(result.Result.Songs), musicU != "", cookiePath)
	}
	if result.Code != 200 {
		return nil, fmt.Errorf("网易云搜索失败: 上游响应 code=%d", result.Code)
	}
	items := make([]SearchResultItem, 0, len(result.Result.Songs))
	for _, song := range result.Result.Songs {
		artists := make([]string, 0, len(song.Artists))
		for _, artist := range song.Artists {
			artists = append(artists, artist.Name)
		}
		items = append(items, SearchResultItem{
			Platform: "netease", SongID: strconv.Itoa(song.Id), Name: song.Name,
			Artists: strings.Join(artists, " / "), Album: song.Album.Name,
			DurationSec: song.Duration / 1000,
			URL:         fmt.Sprintf("https://music.163.com/song?id=%d", song.Id), IsVIP: song.Fee == 1,
		})
	}
	return items, nil
}

func neteaseSearchCookie() (string, string) {
	cfg := GetWebConfig()
	if cfg == nil || cfg.CookieCloud == nil || cfg.CookieCloud.CookieFilePath == "" || cfg.CookieCloud.CookieFile == "" {
		return "", ""
	}
	cookiePath := filepath.Join(cfg.CookieCloud.CookieFilePath, cfg.CookieCloud.CookieFile)
	return cookiePath, utils.GetCookieValue(cookiePath, ".music.163.com", "MUSIC_U")
}

func requestNeteaseSearch(ctx context.Context, endpoint, keyword string, limit, offset int, musicU string) (ncmtypes.SearchSongData, error) {
	var result ncmtypes.SearchSongData
	reqJSON := ncmapi.CreateSearchSongReqJson(ncmapi.SearchSongConfig{Keyword: keyword, Limit: limit, Offset: offset})
	params := ncmutils.Format2Params(ncmutils.SpliceStr(ncmapi.SearchSongAPI, reqJSON))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(params))
	if err != nil {
		return result, err
	}
	cookies := map[string]string{
		"appver": "8.9.70", "buildver": strconv.FormatInt(time.Now().Unix(), 10),
		"resolution": "1920x1080", "os": "android",
	}
	if musicU != "" {
		cookies["MUSIC_U"] = musicU
	} else {
		cookies["MUSIC_A"] = "4ee5f776c9ed1e4d5f031b09e084c6cb333e43ee4a841afeebbef9bbf4b7e4152b51ff20ecb9e8ee9e89ab23044cf50d1609e4781e805e73a138419e5583bc7fd1e5933c52368d9127ba9ce4e2f233bf5a77ba40ea6045ae1fc612ead95d7b0e0edf70a74334194e1a190979f5fc12e9968c3666a981495b33a649814e309366"
	}
	encodedCookies := make([]string, 0, len(cookies))
	for key, value := range cookies {
		encodedCookies = append(encodedCookies, ncmCookieEscape(key)+"="+ncmCookieEscape(value))
	}
	req.Header.Set("Cookie", strings.Join(encodedCookies, "; "))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", ncmutils.ChooseUserAgent())
	client := &http.Client{Timeout: searchProviderTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return result, fmt.Errorf("网易云 API HTTP 状态码: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return result, err
	}
	// SearchSong uses ApiRequest: the request is EAPI-encrypted, but the
	// response body is plain JSON (unlike the library's EapiRequest helper).
	if err := json.Unmarshal(body, &result); err != nil {
		return result, fmt.Errorf("解析网易云响应失败: %w", err)
	}
	result.RawJson = string(body)
	return result, nil
}

func ncmCookieEscape(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

/* ------------------------ QQ 音乐 ------------------------ */

type qqSearchResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Song []struct {
			ID     int    `json:"id"`
			Mid    string `json:"mid"`
			Name   string `json:"name"`
			Title  string `json:"title"`
			Singer []struct {
				Name string `json:"name"`
			} `json:"singer"`
			Album struct {
				Name string `json:"name"`
			} `json:"album"`
			Interval int `json:"interval"`
			Pay      struct {
				PayPlay int `json:"pay_play"`
			} `json:"pay"`
		} `json:"song"`
	} `json:"data"`
}

const qqSearchMax429Retries = 3

var qqSearchRefreshFlights singleflight.Group

func searchQQ(keyword string, limit, offset int) ([]SearchResultItem, error) {
	return searchQQWithContext(context.Background(), keyword, limit, offset)
}

func searchQQWithContext(ctx context.Context, keyword string, limit, offset int) ([]SearchResultItem, error) {
	cfg := GetWebConfig()
	if cfg == nil || cfg.QQMusicApiConfig == nil || !cfg.QQMusicApiConfig.Enable {
		return nil, fmt.Errorf("QQ Music API 未启用")
	}
	if strings.TrimSpace(cfg.QQMusicApiConfig.Endpoint) == "" {
		return nil, fmt.Errorf("QQ Music API endpoint 未配置")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("QQ 音乐搜索分页参数无效")
	}

	page := offset/limit + 1
	credential, _, credentialErr := currentQQMusicCredential(qqMusicCredentialPath, cfg.QQMusicApiConfig)
	if credentialErr != nil && utils.Logger() != nil {
		utils.WarnWithFormat("[WebSearch] 读取 QQ 登录凭证失败，将先尝试无凭证搜索: %v", credentialErr)
	}
	cookie := qqSearchCredentialCookie(credential, cfg.QQMusicApiConfig)
	client := &http.Client{Timeout: searchProviderTimeout}
	searchResp, err := requestQQSearchWith429Retry(ctx, client, cfg.QQMusicApiConfig.Endpoint, keyword, limit, page, cookie)
	if err != nil && isQQRiskControlError(err.Error()) {
		if utils.Logger() != nil {
			utils.WarnWithFormat("[WebSearch] QQ 搜索触发风控，尝试刷新凭证并重试一次")
		}
		refreshedCookie, refreshErr := refreshQQCredentialForSearchContext(ctx, cfg.QQMusicApiConfig)
		if refreshErr != nil {
			return nil, fmt.Errorf("%w；自动刷新 QQ 凭证失败: %v", err, refreshErr)
		}
		searchResp, err = requestQQSearchWith429Retry(ctx, client, cfg.QQMusicApiConfig.Endpoint, keyword, limit, page, refreshedCookie)
	}
	if err != nil {
		return nil, err
	}

	items := make([]SearchResultItem, 0, len(searchResp.Data.Song))
	for _, s := range searchResp.Data.Song {
		artists := make([]string, 0, len(s.Singer))
		for _, a := range s.Singer {
			artists = append(artists, a.Name)
		}
		name := s.Title
		if name == "" {
			name = s.Name
		}
		items = append(items, SearchResultItem{
			Platform:    "qq",
			SongID:      strconv.Itoa(s.ID),
			SongMID:     s.Mid,
			Name:        name,
			Artists:     strings.Join(artists, " / "),
			Album:       s.Album.Name,
			DurationSec: s.Interval,
			URL:         fmt.Sprintf("https://y.qq.com/n/ryqq/songDetail/%s", s.Mid),
			IsVIP:       s.Pay.PayPlay != 0,
		})
	}
	return items, nil
}

type qqSearchHTTPStatusError struct {
	statusCode int
}

func (e *qqSearchHTTPStatusError) Error() string {
	return fmt.Sprintf("QQ Music API HTTP 状态码: %d", e.statusCode)
}

func requestQQSearchWith429Retry(ctx context.Context, client *http.Client, endpoint, keyword string, limit, page int, cookie string) (qqSearchResponse, error) {
	for retry := 0; ; retry++ {
		searchResp, err := requestQQSearch(ctx, client, endpoint, keyword, limit, page, cookie)
		if err == nil || !isQQSearchHTTPStatus(err, http.StatusTooManyRequests) || retry >= qqSearchMax429Retries {
			return searchResp, err
		}

		delay := 250 * time.Millisecond * time.Duration(1<<retry)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return qqSearchResponse{}, ctx.Err()
		}
	}
}

func isQQSearchHTTPStatus(err error, statusCode int) bool {
	var statusErr *qqSearchHTTPStatusError
	return errors.As(err, &statusErr) && statusErr.statusCode == statusCode
}

func requestQQSearch(ctx context.Context, client *http.Client, endpoint, keyword string, limit, page int, cookie string) (qqSearchResponse, error) {
	query := url.Values{}
	query.Set("keyword", keyword)
	query.Set("search_type", "0")
	query.Set("num", strconv.Itoa(limit))
	query.Set("page", strconv.Itoa(page))
	reqURL := strings.TrimSuffix(endpoint, "/") + "/search/search_by_type?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return qqSearchResponse{}, err
	}
	req.Header.Set("Referer", "https://y.qq.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := client.Do(req)
	if err != nil {
		return qqSearchResponse{}, fmt.Errorf("QQ Music API 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return qqSearchResponse{}, &qqSearchHTTPStatusError{statusCode: resp.StatusCode}
	}

	var searchResp qqSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return qqSearchResponse{}, fmt.Errorf("解析 QQ Music 响应失败: %w", err)
	}
	if searchResp.Code != 0 {
		return searchResp, fmt.Errorf("QQ Music API 错误: %s", searchResp.Msg)
	}
	return searchResp, nil
}

func isQQRiskControlError(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	for _, marker := range []string{"触发风控", "风控", "安全验证", "安全校验", "验证码", "captcha", "risk control", "security verification"} {
		if strings.Contains(message, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func refreshQQCredentialForSearch(fallback *config.QQMusicApiConfig) (string, error) {
	return refreshQQCredentialForSearchContext(context.Background(), fallback)
}

func refreshQQCredentialForSearchContext(ctx context.Context, fallback *config.QQMusicApiConfig) (string, error) {
	if fallback == nil {
		return "", fmt.Errorf("QQ Music API 配置不可用")
	}
	key := strings.TrimSpace(fallback.Endpoint)
	value, err, _ := qqSearchRefreshFlights.Do(key, func() (any, error) {
		return refreshQQCredentialForSearchOnce(ctx, fallback)
	})
	if err != nil {
		return "", err
	}
	cookie, ok := value.(string)
	if !ok || cookie == "" {
		return "", fmt.Errorf("QQ 凭证刷新未返回有效凭证")
	}
	return cookie, nil
}

func refreshQQCredentialForSearchOnce(ctx context.Context, fallback *config.QQMusicApiConfig) (string, error) {
	credential, _, err := currentQQMusicCredential(qqMusicCredentialPath, fallback)
	if err != nil {
		return "", err
	}
	if credential == nil || !qqCredentialConfigured(credential) {
		return "", fmt.Errorf("没有可刷新的 QQ 登录凭证")
	}

	client := newQQMusicClient(credential)
	client.httpClient.Timeout = 8 * time.Second
	refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	refreshed, err := refreshQQMusicCredential(refreshCtx, client, credential)
	if err != nil {
		return "", err
	}
	if err := persistQQLoginCredential(refreshed); err != nil {
		return "", err
	}
	updated, _, err := currentQQMusicCredential(qqMusicCredentialPath, fallback)
	if err != nil {
		return "", err
	}
	if updated == nil || !qqCredentialConfigured(updated) {
		return "", fmt.Errorf("刷新后未找到有效的 QQ 登录凭证")
	}
	return qqMusicCredentialCookie(updated), nil
}

// qqSearchCredentialCookie uses the complete runtime credential when available.
func qqSearchCredentialCookie(runtime, fallback *config.QQMusicApiConfig) string {
	if runtime != nil && qqCredentialConfigured(runtime) {
		return qqMusicCredentialCookie(runtime)
	}
	return qqMusicCredentialCookie(fallback)
}

/* ------------------------ YouTube ------------------------ */

var runYTDLPSearch = func(ctx context.Context, searchQuery string, _ int) ([]byte, error) {
	cmd := utils.CommandContext(ctx, "yt-dlp",
		searchQuery,
		"--flat-playlist",
		"--print", "%(id)s\n%(title)s\n%(uploader)s\n%(duration)s\n%(webpage_url)s",
		"--no-warnings", "--quiet",
	)
	return cmd.Output()
}

func searchYouTube(keyword string, limit, offset int) ([]SearchResultItem, error) {
	return searchYouTubeWithContext(context.Background(), keyword, limit, offset)
}

func searchYouTubeWithContext(ctx context.Context, keyword string, limit, offset int) ([]SearchResultItem, error) {
	// yt-dlp 分页不支持 offset，取 limit+offset 条然后本地截断
	total := limit + offset

	// 传入 yt-dlp 的关键词需要转义，防止注入
	searchQuery := fmt.Sprintf("ytsearch%d:%s", total, keyword)

	if utils.Logger() != nil {
		utils.DebugWithFormat("[WebSearch] PATH: %s", utils.ExtendedPATH())
	}
	// 用换行作分隔符，标题里的竖线不会破坏字段对齐；每条记录以 webpage_url 结尾
	output, err := runYTDLPSearch(ctx, searchQuery, total)
	if err != nil {
		return nil, fmt.Errorf("yt-dlp 搜索失败: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	// 不足 5 行的记录视为不完整，直接丢弃
	for len(lines) > 0 && len(lines) < 5 {
		lines = lines[:len(lines)-1]
	}

	items := make([]SearchResultItem, 0)
	resultCount := len(lines) / 5
	for i := 0; i < resultCount; i++ {
		if i < offset {
			continue
		}
		base := i * 5
		id := strings.TrimSpace(lines[base])
		title := lines[base+1]
		uploader := lines[base+2]
		duration, _ := strconv.ParseFloat(strings.TrimSpace(lines[base+3]), 64)
		pageURL := strings.TrimSpace(lines[base+4])
		if pageURL == "" || pageURL == "NA" {
			continue
		}
		items = append(items, SearchResultItem{
			Platform:    "youtube",
			SongID:      id,
			Name:        title,
			Artists:     uploader,
			DurationSec: int(duration),
			URL:         pageURL,
		})
		if len(items) >= limit {
			break
		}
	}
	return items, nil
}

/* ------------------------ B 站 ------------------------ */

type biliSearchResponse struct {
	Code int `json:"code"`
	Data struct {
		Result []struct {
			BVID     string `json:"bvid"`
			Title    string `json:"title"`
			Author   string `json:"author"`
			Duration string `json:"duration"`
		} `json:"result"`
	} `json:"data"`
}

func searchBilibili(keyword string, limit, offset int) ([]SearchResultItem, error) {
	return searchBilibiliWithContext(context.Background(), keyword, limit, offset)
}

func searchBilibiliWithContext(ctx context.Context, keyword string, limit, offset int) ([]SearchResultItem, error) {
	page := offset/limit + 1
	return requestBilibiliSearch(ctx, &http.Client{Timeout: searchProviderTimeout}, "https://api.bilibili.com", keyword, page, limit)
}

func requestBilibiliSearch(ctx context.Context, client *http.Client, endpoint, keyword string, page, limit int) ([]SearchResultItem, error) {
	var searchResp biliSearchResponse
	reqURL := strings.TrimRight(endpoint, "/") + fmt.Sprintf("/x/web-interface/search/type?search_type=video&keyword=%s&page=%d&page_size=%d", url.QueryEscape(keyword), page, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://search.bilibili.com/")
	req.Header.Set("Origin", "https://search.bilibili.com")
	// B 站搜索 API 需要 buvid3 cookie，否则 412
	req.Header.Set("Cookie", fmt.Sprintf("buvid3=%s; b_nut=%d", generateBuvid(), time.Now().Unix()))
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("B 站搜索请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("B 站搜索 API HTTP 状态码: %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("解析 B 站响应失败: %w", err)
	}
	if searchResp.Code != 0 {
		return nil, fmt.Errorf("B 站 API 错误码: %d", searchResp.Code)
	}

	items := make([]SearchResultItem, 0, len(searchResp.Data.Result))
	for _, r := range searchResp.Data.Result {
		// B 站 title 里可能带 <em class="keyword"> 高亮标签，去掉
		title := strings.NewReplacer(
			`<em class="keyword">`, "",
			`</em>`, "",
		).Replace(r.Title)
		items = append(items, SearchResultItem{
			Platform:    "bilibili",
			SongID:      r.BVID,
			Name:        title,
			Artists:     r.Author,
			DurationSec: parseBiliDuration(r.Duration),
			URL:         fmt.Sprintf("https://www.bilibili.com/video/%s", r.BVID),
		})
	}
	return items, nil
}

// parseBiliDuration "4:30" → 270 秒，"1:02:30" → 3750 秒
func parseBiliDuration(s string) int {
	parts := strings.Split(s, ":")
	result := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0
		}
		result = result*60 + n
	}
	return result
}

// generateBuvid 生成随机的 buvid3 值
func generateBuvid() string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// 极端情况退化为时间戳
		return fmt.Sprintf("%08X-infoc", time.Now().UnixNano())
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return fmt.Sprintf("%s-%s-%s-infoc", string(b[:4]), string(b[4:6]), string(b[6:]))
}
