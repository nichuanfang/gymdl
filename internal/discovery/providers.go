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
	return Track{Platform: i.Platform, ID: id, URL: sourceURL, Title: i.Name, Artist: i.Artists, Album: i.Album, Duration: i.DurationSec, Verified: i.Name != "" && i.Artists != ""}
}

func (p *Providers) ncm(ctx context.Context, endpoint string, input any, out any) error {
	if p.Config.CookieCloud == nil {
		return fmt.Errorf("请配置 CookieCloud")
	}
	cc := p.Config.CookieCloud
	musicU := utils.GetCookieValue(filepath.Join(cc.CookieFilePath, cc.CookieFile), ".music.163.com", "MUSIC_U")
	if musicU == "" {
		return fmt.Errorf("网易云登录失效，请同步 CookieCloud")
	}
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
	req.AddCookie(&http.Cookie{Name: "MUSIC_U", Value: musicU})
	req.AddCookie(&http.Cookie{Name: "os", Value: "android"})
	req.AddCookie(&http.Cookie{Name: "appver", Value: "8.9.70"})
	resp, err := p.client().Do(req)
	if err != nil {
		return fmt.Errorf("网易云收藏请求失败")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil || resp.StatusCode != 200 {
		return fmt.Errorf("网易云收藏请求失败")
	}
	var code struct{ Code int }
	if json.Unmarshal(data, &code) != nil {
		return fmt.Errorf("网易云返回数据无效")
	}
	if code.Code == 301 || code.Code == 401 {
		return fmt.Errorf("网易云登录失效，请同步 CookieCloud")
	}
	if code.Code != 200 {
		return fmt.Errorf("网易云收藏接口暂不可用（%d）", code.Code)
	}
	if json.Unmarshal(data, out) != nil {
		return fmt.Errorf("网易云收藏数据格式无效")
	}
	return nil
}

func (p *Providers) netease(ctx context.Context) (Snapshot, error) {
	var login ncmtypes.LoginStatusData
	if err := p.ncm(ctx, "/api/w/nuser/account/get", map[string]any{}, &login); err != nil {
		return Snapshot{}, err
	}
	uid := login.Account.Id
	if uid <= 0 {
		return Snapshot{}, fmt.Errorf("网易云登录失效，请同步 CookieCloud")
	}
	var likedID int
	for offset := 0; offset < 10000; offset += 100 {
		var playlists ncmtypes.UserPlaylistData
		if err := p.ncm(ctx, "/api/user/playlist", map[string]any{"uid": uid, "offset": offset, "limit": 100}, &playlists); err != nil {
			return Snapshot{}, err
		}
		for _, list := range playlists.Playlist {
			if list.SpecialType == 5 && list.UserId == uid {
				likedID = list.Id
				break
			}
		}
		if likedID > 0 || !playlists.More {
			break
		}
		if len(playlists.Playlist) == 0 {
			return Snapshot{}, fmt.Errorf("网易云歌单分页不完整")
		}
	}
	if likedID <= 0 {
		return Snapshot{}, fmt.Errorf("未找到当前网易云账号的我喜欢的音乐")
	}
	var detail ncmtypes.PlaylistDetailData
	if err := p.ncm(ctx, "/api/v6/playlist/detail", map[string]any{"id": likedID, "n": 100000, "s": 0}, &detail); err != nil {
		return Snapshot{}, err
	}
	if len(detail.Playlist.TrackIds) != detail.Playlist.TrackCount {
		return Snapshot{}, fmt.Errorf("网易云收藏 ID 数量不完整，未建立基线")
	}
	snap := Snapshot{Platform: "netease", Account: strconv.Itoa(uid), Complete: true}
	for start := 0; start < len(detail.Playlist.TrackIds); start += 100 {
		ids := []map[string]int{}
		expected := map[int]bool{}
		for _, id := range detail.Playlist.TrackIds[start:min(start+100, len(detail.Playlist.TrackIds))] {
			ids = append(ids, map[string]int{"id": id.Id})
			expected[id.Id] = true
		}
		raw, _ := json.Marshal(ids)
		var songs ncmtypes.SongsDetailData
		if err := p.ncm(ctx, "/api/v3/song/detail", map[string]any{"c": string(raw)}, &songs); err != nil {
			return Snapshot{}, err
		}
		for _, s := range songs.Songs {
			if !expected[s.Id] {
				return Snapshot{}, fmt.Errorf("网易云收藏歌曲详情不一致")
			}
			delete(expected, s.Id)
			artists := []string{}
			for _, a := range s.Ar {
				artists = append(artists, a.Name)
			}
			snap.Tracks = append(snap.Tracks, Track{Platform: "netease", ID: strconv.Itoa(s.Id), URL: fmt.Sprintf("https://music.163.com/song?id=%d", s.Id), Title: s.Name, Artist: strings.Join(artists, " / "), Album: s.Al.Name, Duration: s.Dt / 1000, Verified: s.Name != "" && len(artists) > 0})
		}
		if len(expected) > 0 {
			return Snapshot{}, fmt.Errorf("网易云有收藏歌曲详情缺失，未更新基线")
		}
	}
	return snap, validSnapshot(snap)
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
func (p *Providers) yt(ctx context.Context, target any, args ...string) error {
	if p.Config.CookieCloud == nil {
		return fmt.Errorf("请配置 CookieCloud")
	}
	cc := p.Config.CookieCloud
	raw, err := os.ReadFile(filepath.Join(cc.CookieFilePath, cc.CookieFile))
	if err != nil {
		return fmt.Errorf("YouTube Cookie 文件不可用")
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
		if ctx.Err() != nil {
			return fmt.Errorf("YouTube 读取超时或已取消，未更新基线: %w", ctx.Err())
		}
		return youtubeReadError(err, string(diagnostic.data))
	}
	if json.Unmarshal(out.data, target) != nil {
		return fmt.Errorf("YouTube 返回数据无效")
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
	if err := p.yt(ctx, &list, "--flat-playlist", "--yes-playlist", ":ytfav"); err != nil {
		return Snapshot{}, err
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
		if !SameRecording(t, other) {
			continue
		}
		if t.Album != "" && Normalize(t.Album) != Normalize(other.Album) {
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
	if p.Config.CookieCloud == nil {
		return "", fmt.Errorf("YouTube 登录配置缺失")
	}
	cc := p.Config.CookieCloud
	raw, err := os.ReadFile(filepath.Join(cc.CookieFilePath, cc.CookieFile))
	if err != nil {
		return "", fmt.Errorf("YouTube Cookie 不可用")
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
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := p.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("YouTube 账号核对失败，未建立基线")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || resp.StatusCode != 200 {
		return "", fmt.Errorf("YouTube 账号核对失败")
	}
	logged := regexp.MustCompile(`"LOGGED_IN"\s*:\s*true`).Match(body)
	id := regexp.MustCompile(`"DATASYNC_ID"\s*:\s*"([^"\\]+)"`).FindSubmatch(body)
	if !logged || len(id) != 2 || len(id[1]) == 0 {
		return "", fmt.Errorf("YouTube 未返回稳定登录账号，请刷新 CookieCloud 后重新绑定")
	}
	hash := sha256.Sum256(id[1])
	return "account-" + hex.EncodeToString(hash[:]), nil
}

func youtubeReadError(err error, diagnostic string) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("YouTube 收藏读取需要可执行的 yt-dlp；当前运行环境缺少该工具或 PATH 配置有误")
	}
	message := strings.ToLower(diagnostic)
	if strings.Contains(message, "playlist does not exist") {
		return fmt.Errorf("YouTube 喜欢列表不可访问；请在浏览器确认正确账号后刷新 CookieCloud，未建立基线")
	}
	if strings.Contains(message, "cookies are no longer valid") || strings.Contains(message, "login required") || strings.Contains(message, "sign in") {
		return fmt.Errorf("YouTube 登录失效，请刷新 CookieCloud")
	}
	return fmt.Errorf("YouTube 收藏或元数据读取失败，请检查网络、登录和 CookieCloud")
}
