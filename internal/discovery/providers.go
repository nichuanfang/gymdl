package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	ncmtypes "github.com/XiaoMengXinX/Music163Api-Go/types"
	ncmutils "github.com/XiaoMengXinX/Music163Api-Go/utils"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core/linkparser"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/utils"
)

type Provider interface {
	Fetch(context.Context, string) (Snapshot, error)
	Resolve(context.Context, Track, string) (Track, error)
	ResolveOn(context.Context, Track, string) (Track, error)
	Enrich(context.Context, Track) (Track, bool, error)
}
type Providers struct {
	Config *config.Config
	Client *http.Client
}

func (p *Providers) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Transport: utils.SharedHTTPClient().Transport, Timeout: 30 * time.Second}
}

func (p *Providers) Fetch(ctx context.Context, platform string) (Snapshot, error) {
	switch platform {
	case "netease":
		return p.netease(ctx)
	case "qq":
		account, items, err := web.QQFavorites(ctx, p.Config)
		s := Snapshot{Platform: platform, Account: account, Complete: err == nil}
		for _, i := range items {
			s.Tracks = append(s.Tracks, fromSearch(i))
		}
		return s, err
	case "youtube":
		return p.youtube(ctx)
	}
	return Snapshot{}, fmt.Errorf("不支持的收藏来源")
}

func fromSearch(i web.SearchResultItem) Track {
	id := i.SongID
	if i.Platform == "qq" {
		id = i.SongMID
	}
	sourceURL := i.URL
	if i.Platform == "youtube" && id != "" {
		sourceURL = "https://music.youtube.com/watch?v=" + id
	}
	return Track{Platform: i.Platform, ID: id, URL: sourceURL, CoverURL: i.CoverURL, Title: i.Name, Artist: i.Artists, Album: i.Album, Duration: i.DurationSec, Verified: i.Name != "" && i.Artists != ""}
}

// neteaseRequestError is a transport failure. HTTP 400 is not retried.
// A non-200 API body uses neteaseAPIError and is retried like a dropped connection.
type neteaseRequestError struct {
	status int
	cause  error
}

// neteaseAPIError is a readable HTTP response that is not a usable snapshot.
// One 301 or a short body is not treated as a dead login.
type neteaseAPIError struct {
	code    int
	invalid bool
}

func (e neteaseAPIError) Error() string {
	if e.invalid || e.code == 0 {
		return "网易云收藏暂时读不到，本轮跳过"
	}
	return fmt.Sprintf("网易云收藏暂时读不到（%d），本轮跳过", e.code)
}

func (e neteaseRequestError) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("网易云收藏请求失败（HTTP %d）", e.status)
	}
	if neteaseTimeout(e.cause) {
		return "网易云收藏请求超时"
	}
	if e.cause != nil {
		return "网易云收藏连接中断"
	}
	return "网易云收藏请求失败"
}

func (e neteaseRequestError) Unwrap() error { return e.cause }

func neteaseTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err)
}

func retryableNetease(err error) bool {
	var api neteaseAPIError
	if errors.As(err, &api) {
		return true
	}
	var httpErr neteaseRequestError
	if !errors.As(err, &httpErr) {
		return false
	}
	if errors.Is(httpErr.cause, context.Canceled) {
		return false
	}
	if httpErr.status == 0 {
		return true
	}
	return httpErr.status == 403 || httpErr.status == 408 || httpErr.status == 429 || httpErr.status >= 500
}

func waitCtx(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *Providers) ncm(ctx context.Context, endpoint string, input any, out any) error {
	if p.Config == nil || p.Config.CookieCloud == nil {
		return fmt.Errorf("请配置 CookieCloud")
	}
	cc := p.Config.CookieCloud
	cookiePath := filepath.Join(cc.CookieFilePath, cc.CookieFile)
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := waitCtx(ctx, time.Duration(attempt)*400*time.Millisecond); err != nil {
			return err
		}
		// CookieCloud replaces the file by rename. Read it on every attempt so
		// a missed open is not reported as a dead login.
		musicU := utils.GetCookieValue(cookiePath, ".music.163.com", "MUSIC_U")
		if musicU == "" {
			last = fmt.Errorf("网易云收藏暂时读不到登录态，本轮跳过")
			continue
		}
		err := p.ncmOnce(ctx, endpoint, input, musicU, out)
		if err == nil || ctx.Err() != nil || !retryableNetease(err) {
			return err
		}
		last = err
	}
	return last
}

func (p *Providers) ncmOnce(ctx context.Context, endpoint string, input any, musicU string, out any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	body := ncmutils.Format2Params(ncmutils.SpliceStr(endpoint, string(raw)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://music.163.com/eapi/"+strings.TrimPrefix(endpoint, "/api/"), strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "https://music.163.com/")
	req.Header.Set("User-Agent", "NeteaseMusic/8.9.70")
	// Music163Api-Go always sends these with the session. A valid MUSIC_U
	// without buildver can be answered as an anonymous 301.
	req.AddCookie(&http.Cookie{Name: "os", Value: "android"})
	req.AddCookie(&http.Cookie{Name: "appver", Value: "8.9.70"})
	req.AddCookie(&http.Cookie{Name: "buildver", Value: strconv.FormatInt(time.Now().Unix(), 10)})
	req.AddCookie(&http.Cookie{Name: "resolution", Value: "1920x1080"})
	req.AddCookie(&http.Cookie{Name: "MUSIC_U", Value: musicU})
	resp, err := p.client().Do(req)
	if err != nil {
		return neteaseRequestError{cause: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return neteaseRequestError{cause: err}
	}
	if resp.StatusCode != http.StatusOK {
		return neteaseRequestError{status: resp.StatusCode}
	}
	var code struct{ Code int }
	if json.Unmarshal(data, &code) != nil {
		return neteaseAPIError{invalid: true}
	}
	if code.Code != 200 {
		return neteaseAPIError{code: code.Code}
	}
	if json.Unmarshal(data, out) != nil {
		return neteaseAPIError{invalid: true}
	}
	return nil
}

func (p *Providers) netease(ctx context.Context) (Snapshot, error) {
	account, ids, err := p.NeteaseLikedIDs(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	tracks, err := p.NeteaseTracks(ctx, ids)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Platform: "netease", Account: account, Tracks: tracks, Complete: true}
	return snap, validSnapshot(snap)
}

// NeteaseLikedIDs reads the account and the liked-song id list. It does not
// download song details.
func (p *Providers) NeteaseLikedIDs(ctx context.Context) (string, []string, error) {
	var login ncmtypes.LoginStatusData
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = waitCtx(ctx, time.Duration(attempt)*400*time.Millisecond); err != nil {
			return "", nil, err
		}
		err = p.ncm(ctx, "/api/w/nuser/account/get", map[string]any{}, &login)
		if err != nil {
			return "", nil, err
		}
		if login.Account.Id > 0 {
			break
		}
		err = fmt.Errorf("网易云收藏暂时读不到账号，本轮跳过")
	}
	if err != nil {
		return "", nil, err
	}
	uid := login.Account.Id
	var likedID int
	for offset := 0; offset < 10000; offset += 100 {
		var playlists ncmtypes.UserPlaylistData
		var pageErr error
		for attempt := 0; attempt < 3; attempt++ {
			if pageErr = waitCtx(ctx, time.Duration(attempt)*400*time.Millisecond); pageErr != nil {
				return "", nil, pageErr
			}
			pageErr = p.ncm(ctx, "/api/user/playlist", map[string]any{"uid": uid, "offset": offset, "limit": 100}, &playlists)
			if pageErr != nil {
				return "", nil, pageErr
			}
			found := 0
			for _, list := range playlists.Playlist {
				if list.SpecialType == 5 && list.UserId == uid {
					found = list.Id
					break
				}
			}
			if found > 0 {
				likedID = found
				pageErr = nil
				break
			}
			if !playlists.More || len(playlists.Playlist) > 0 {
				pageErr = nil
				break
			}
			pageErr = fmt.Errorf("网易云歌单分页不完整")
		}
		if pageErr != nil {
			return "", nil, pageErr
		}
		if likedID > 0 || !playlists.More {
			break
		}
	}
	if likedID <= 0 {
		return "", nil, fmt.Errorf("未找到当前网易云账号的我喜欢的音乐")
	}
	var detail ncmtypes.PlaylistDetailData
	for attempt := 0; attempt < 3; attempt++ {
		if err = waitCtx(ctx, time.Duration(attempt)*400*time.Millisecond); err != nil {
			return "", nil, err
		}
		err = p.ncm(ctx, "/api/v6/playlist/detail", map[string]any{"id": likedID, "n": 100000, "s": 0}, &detail)
		if err != nil {
			return "", nil, err
		}
		if neteaseIDsComplete(detail) {
			err = nil
			break
		}
		err = fmt.Errorf("网易云收藏 ID 数量不完整，未建立基线")
	}
	if err != nil {
		return "", nil, err
	}
	ids := make([]string, 0, len(detail.Playlist.TrackIds))
	for _, id := range detail.Playlist.TrackIds {
		ids = append(ids, strconv.Itoa(id.Id))
	}
	return strconv.Itoa(uid), ids, nil
}

func neteaseIDsComplete(detail ncmtypes.PlaylistDetailData) bool {
	if len(detail.Playlist.TrackIds) != detail.Playlist.TrackCount {
		return false
	}
	for _, id := range detail.Playlist.TrackIds {
		if id.Id <= 0 {
			return false
		}
	}
	return true
}

// NeteaseTracks loads details for the given song ids, in that order.
func (p *Providers) NeteaseTracks(ctx context.Context, ids []string) ([]Track, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tracks := make([]Track, 0, len(ids))
	for start := 0; start < len(ids); start += 100 {
		batch := ids[start:min(start+100, len(ids))]
		body := make([]map[string]int, 0, len(batch))
		expected := map[int]bool{}
		for _, id := range batch {
			n, err := strconv.Atoi(id)
			if err != nil || n <= 0 || expected[n] {
				return nil, fmt.Errorf("网易云收藏歌曲详情不一致")
			}
			body = append(body, map[string]int{"id": n})
			expected[n] = true
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		var songs ncmtypes.SongsDetailData
		if err = p.ncm(ctx, "/api/v3/song/detail", map[string]any{"c": string(raw)}, &songs); err != nil {
			return nil, err
		}
		found := map[int]Track{}
		for _, s := range songs.Songs {
			if !expected[s.Id] {
				return nil, fmt.Errorf("网易云收藏歌曲详情不一致")
			}
			delete(expected, s.Id)
			artists := []string{}
			for _, a := range s.Ar {
				artists = append(artists, a.Name)
			}
			found[s.Id] = Track{Platform: "netease", ID: strconv.Itoa(s.Id), URL: fmt.Sprintf("https://music.163.com/song?id=%d", s.Id), Title: s.Name, Artist: strings.Join(artists, " / "), Album: s.Al.Name, Duration: s.Dt / 1000, Verified: s.Name != "" && len(artists) > 0}
		}
		if len(expected) > 0 {
			return nil, fmt.Errorf("网易云有收藏歌曲详情缺失，未更新基线")
		}
		for _, id := range batch {
			n, _ := strconv.Atoi(id)
			tracks = append(tracks, found[n])
		}
	}
	return tracks, nil
}

type ytEntry struct {
	ID, Title, Track, Artist, Album string
	URL                             string `json:"webpage_url"`
	ChannelID                       string `json:"channel_id"`
	UploaderID                      string `json:"uploader_id"`
	Duration                        float64
	Categories                      []string
	IsLive                          bool `json:"is_live"`
}
type limitedOutput struct{ data []byte }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 32<<20 {
		return 0, fmt.Errorf("元数据超过安全大小")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

// Use a private cookie copy: yt-dlp writes its cookie jar on exit and must not
// replace CookieCloud's file, especially while other downloads are running.
// A dropped read is tried twice. The stored baseline is left unchanged.
func (p *Providers) yt(ctx context.Context, target any, args ...string) error {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if err := waitCtx(ctx, time.Duration(attempt)*700*time.Millisecond); err != nil {
			return err
		}
		err := p.ytOnce(ctx, target, args...)
		if err == nil || ctx.Err() != nil || !retryableYoutube(err) {
			return err
		}
		last = err
	}
	return last
}

func (p *Providers) ytOnce(ctx context.Context, target any, args ...string) error {
	if p.Config == nil || p.Config.CookieCloud == nil {
		return fmt.Errorf("请配置 CookieCloud")
	}
	cc := p.Config.CookieCloud
	raw, err := os.ReadFile(filepath.Join(cc.CookieFilePath, cc.CookieFile))
	if err != nil {
		return youtubeReadFailure{msg: "YouTube Cookie 文件暂时不可读，本轮跳过", retry: true}
	}
	f, err := os.CreateTemp("", "gymdl-youtube-cookies-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	base := []string{"--ignore-config", "--no-cache-dir", "--skip-download", "--no-warnings", "--no-progress", "--cookies", f.Name(), "--dump-single-json"}
	cmd := utils.CommandContext(ctx, "yt-dlp", append(base, args...)...)
	var out limitedOutput
	cmd.Stdout = &out
	var diagnostic limitedOutput
	cmd.Stderr = &diagnostic
	if err = cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return youtubeReadFailure{msg: "YouTube 读取超时，本轮跳过", retry: true}
		}
		if ctx.Err() != nil {
			return fmt.Errorf("YouTube 读取已取消，未更新基线: %w", ctx.Err())
		}
		return youtubeReadError(err, string(diagnostic.data))
	}
	if json.Unmarshal(out.data, target) != nil {
		return youtubeReadFailure{msg: "YouTube 返回数据无效，本轮跳过", retry: true}
	}
	return nil
}

func (p *Providers) youtube(ctx context.Context) (Snapshot, error) {
	var list struct {
		ID         string
		ChannelID  string `json:"channel_id"`
		UploaderID string `json:"uploader_id"`
		Count      *int   `json:"playlist_count"`
		Entries    []ytEntry
	}
	var countErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := waitCtx(ctx, time.Duration(attempt)*700*time.Millisecond); err != nil {
			return Snapshot{}, err
		}
		list.ID, list.ChannelID, list.UploaderID = "", "", ""
		list.Count, list.Entries = nil, nil
		if err := p.yt(ctx, &list, "--flat-playlist", "--yes-playlist", ":ytfav"); err != nil {
			return Snapshot{}, err
		}
		if list.Count != nil && len(list.Entries) != *list.Count {
			countErr = fmt.Errorf("YouTube 收藏分页数量不完整，本轮跳过")
			continue
		}
		countErr = nil
		break
	}
	if countErr != nil {
		return Snapshot{}, countErr
	}
	account := list.ChannelID
	if account == "" {
		account = list.UploaderID
	}
	if account == "" {
		var err error
		account, err = p.youtubeAccount(ctx)
		if err != nil {
			return Snapshot{}, err
		}
	}
	if list.Count != nil && len(list.Entries) != *list.Count {
		return Snapshot{}, fmt.Errorf("YouTube 收藏分页数量不完整")
	}
	snap := Snapshot{Platform: "youtube", Account: account, Complete: true}
	for _, e := range list.Entries {
		snap.Tracks = append(snap.Tracks, Track{Platform: "youtube", ID: e.ID, URL: "https://music.youtube.com/watch?v=" + e.ID, Title: e.Title, Duration: int(e.Duration)})
	}
	return snap, validSnapshot(snap)
}

func (p *Providers) Enrich(ctx context.Context, t Track) (Track, bool, error) {
	if t.Platform != "youtube" || t.Verified || t.Approved {
		return t, false, nil
	}
	var e ytEntry
	if err := p.yt(ctx, &e, "--no-playlist", t.URL); err != nil {
		return t, false, err
	}
	if e.ID != t.ID || e.IsLive {
		t.NonMusic = true
		return t, true, nil
	}
	musicCategory := false
	for _, c := range e.Categories {
		if strings.EqualFold(c, "music") {
			musicCategory = true
		}
	}
	if len(e.Categories) > 0 && !musicCategory {
		t.NonMusic = true
		return t, true, nil
	}
	t.Duration = int(e.Duration)
	if e.Track != "" && e.Artist != "" {
		t.Title = e.Track
		t.Artist = e.Artist
		t.Album = e.Album
		t.Verified = true
	}
	return t, false, nil
}

// acceptPlatformHit reports that a store result is the recording we searched for.
// Album titles often disagree between Last.fm and the download platform.
// Edition markers and duration still have to agree.
func acceptPlatformHit(source, hit Track) bool {
	return SameRecording(source, hit)
}

// Resolve returns a unique matching recording from real provider results.
// An explicit source URL is preserved; searches never invent links or editions.
var ErrAmbiguousRecording = errors.New("跨平台存在多个可能版本，需要确认")
var ErrNoUniqueRecording = errors.New("未找到可唯一确认的同歌手、同版本曲目")

func (p *Providers) Resolve(ctx context.Context, t Track, exclude string) (Track, error) {
	if t.URL != "" && t.Verified && t.Platform != exclude {
		kind, executor := linkparser.ParseLinkWithConfig(p.Config, musicSourceURL(t))
		if single, ok := executor.(music.Processor); ok && kind != "" {
			meta, err := resolveSourceMetadata(config.WithLiveTaskOverrides(p.Config), single.Name(), musicSourceURL(t))
			if err == nil && meta != nil && SameRecording(t, Track{Title: meta.SongName, Artist: meta.SongArtists, Album: meta.SongAlbum, Duration: meta.Duration, Verified: true}) {
				return t, nil
			}
		}
		exclude = t.Platform
	}
	for _, platform := range []string{"netease", "qq", "youtube"} {
		if platform == exclude {
			continue
		}
		if platform == "qq" && (p.Config == nil || p.Config.QQMusicApiConfig == nil || !p.Config.QQMusicApiConfig.Enable) {
			continue
		}
		hit, err := p.matchOnPlatform(ctx, t, platform)
		if errors.Is(err, ErrAmbiguousRecording) {
			return Track{}, err
		}
		if err == nil {
			return hit, nil
		}
	}
	return Track{}, ErrNoUniqueRecording
}

// ResolveOn searches a single platform. A track that already has a verified
// link on that platform is returned without another lookup.
func (p *Providers) ResolveOn(ctx context.Context, t Track, platform string) (Track, error) {
	if platform != "netease" && platform != "qq" && platform != "youtube" {
		return Track{}, fmt.Errorf("不支持的平台")
	}
	if t.URL != "" && t.Verified && t.Platform == platform {
		return t, nil
	}
	if platform == "qq" && (p.Config == nil || p.Config.QQMusicApiConfig == nil || !p.Config.QQMusicApiConfig.Enable) {
		return Track{}, fmt.Errorf("QQ 音乐未启用")
	}
	return p.matchOnPlatform(ctx, t, platform)
}

func (p *Providers) matchOnPlatform(ctx context.Context, t Track, platform string) (Track, error) {
	hits, _, err := web.SearchSinglesForAgent(ctx, t.Title+" "+t.Artist, []string{platform}, 10)
	if err != nil {
		return Track{}, err
	}
	matches := map[string]Track{}
	for _, h := range hits {
		other := fromSearch(h.SearchResultItem)
		if !acceptPlatformHit(t, other) {
			continue
		}
		matches[other.ID] = other
	}
	if len(matches) > 1 {
		return Track{}, ErrAmbiguousRecording
	}
	if len(matches) == 1 {
		for _, hit := range matches {
			return hit, nil
		}
	}
	return Track{}, ErrNoUniqueRecording
}

// Personal special playlists do not always expose a channel owner. DATASYNC_ID
// identifies the authenticated account context across rotating cookies. Store
// only its hash; never use a cookie value as an account identity.
func (p *Providers) youtubeAccount(ctx context.Context) (string, error) {
	if p.Config == nil || p.Config.CookieCloud == nil {
		return "", fmt.Errorf("YouTube 登录配置缺失")
	}
	cc := p.Config.CookieCloud
	cookiePath := filepath.Join(cc.CookieFilePath, cc.CookieFile)
	raw, err := os.ReadFile(cookiePath)
	if err != nil {
		return "", errors.New("YouTube 账号核验无法读取 Cookie 文件，请检查 CookieCloud")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.youtube.com/", nil)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(strings.TrimPrefix(strings.TrimSpace(line), "#HttpOnly_"), "\t")
		if len(fields) < 7 {
			continue
		}
		domain := strings.TrimPrefix(fields[0], ".")
		if domain != "youtube.com" && domain != "www.youtube.com" {
			continue
		}
		expiry, _ := strconv.ParseInt(fields[4], 10, 64)
		if expiry > 0 && expiry < time.Now().Unix() {
			continue
		}
		req.AddCookie(&http.Cookie{Name: fields[5], Value: fields[6]})
	}
	if len(req.Cookies()) == 0 {
		return "", errors.New("Cookie 文件里没有可用的 YouTube Cookie，请重新同步")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	loggedIn := regexp.MustCompile(`"LOGGED_IN"\s*:\s*true`)
	loggedOut := regexp.MustCompile(`"LOGGED_IN"\s*:\s*false`)
	datasync := regexp.MustCompile(`"DATASYNC_ID"\s*:\s*"([^"\\]+)"`)
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := waitCtx(ctx, time.Duration(attempt)*400*time.Millisecond); err != nil {
			return "", err
		}
		resp, err := p.client().Do(req.Clone(ctx))
		if err != nil {
			last = errors.New("连接 YouTube 账号页面失败，请检查网络")
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			last = fmt.Errorf("YouTube 账号页面返回 HTTP %d", resp.StatusCode)
			continue
		}
		if readErr != nil {
			last = errors.New("读取 YouTube 账号页面失败，请稍后重试")
			continue
		}
		if loggedOut.Match(body) && !loggedIn.Match(body) {
			return "", errors.New("YouTube 账号页面显示未登录，请重新登录并同步 Cookie")
		}
		id := datasync.FindSubmatch(body)
		if !loggedIn.Match(body) || len(id) != 2 || len(id[1]) == 0 {
			return "", errors.New("YouTube 账号页面缺少可验证的登录或账号标识，原基线保留")
		}
		hash := sha256.Sum256(id[1])
		return "account-" + hex.EncodeToString(hash[:]), nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return "", last
}

type youtubeReadFailure struct {
	msg   string
	retry bool
}

func (e youtubeReadFailure) Error() string { return e.msg }

func retryableYoutube(err error) bool {
	var fail youtubeReadFailure
	return errors.As(err, &fail) && fail.retry
}

func youtubeReadError(err error, diagnostic string) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrPermission) {
		return youtubeReadFailure{msg: "YouTube 收藏读取需要可执行的 yt-dlp；当前运行环境缺少该工具或 PATH 配置有误"}
	}
	message := strings.ToLower(diagnostic)
	if strings.Contains(message, "playlist does not exist") {
		return youtubeReadFailure{msg: "YouTube 喜欢列表暂时读不到，未更新基线"}
	}
	if strings.Contains(message, "cookies are no longer valid") || strings.Contains(message, "login required") || strings.Contains(message, "sign in") {
		return youtubeReadFailure{msg: "YouTube 喜欢列表需要有效登录，请重新登录并同步 Cookie", retry: true}
	}
	return youtubeReadFailure{msg: "YouTube 收藏或元数据读取失败，请检查网络、登录和 CookieCloud", retry: true}
}
