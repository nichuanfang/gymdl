package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

// QQFavorites reads all pages with the same active credential as search/QR login.
// A dropped request is retried. Partial pages and account mismatches still fail
// rather than becoming a new baseline.
func QQFavorites(ctx context.Context, cfg *config.Config) (string, []SearchResultItem, error) {
	if cfg == nil || cfg.QQMusicApiConfig == nil || !cfg.QQMusicApiConfig.Enable {
		return "", nil, fmt.Errorf("请启用 QQ 音乐 API，并使用 /qq_login 登录")
	}
	cred, _, err := currentQQMusicCredential(qqMusicCredentialPath, cfg.QQMusicApiConfig)
	if err != nil || cred == nil {
		return "", nil, fmt.Errorf("QQ 登录不可用，请使用 /qq_login")
	}
	client := newQQMusicClient(cred)
	cookie := qqMusicCredentialCookie(cred)
	loggedIn, err := qqLoginConfirmed(ctx, client, cookie)
	if err != nil || !loggedIn {
		if err == nil {
			err = fmt.Errorf("QQ 登录状态无法确认")
		}
		return "", nil, err
	}
	euin := qqEncryptedAccount(ctx, client, cookie, cred)
	var out []SearchResultItem
	seen := map[string]bool{}
	expected := -1
	for page := 1; page <= 1000; page++ {
		var parsed qqFavoritePage
		err = retryQQ(ctx, func() error {
			one, readErr := readQQFavoritePage(ctx, client, euin, cookie, page)
			if readErr != nil {
				return readErr
			}
			if expected >= 0 && one.total != expected {
				return fmt.Errorf("QQ 收藏读取期间发生变化，请重试")
			}
			if one.creator != "" && one.creator != cred.MusicId {
				return fmt.Errorf("QQ 收藏所属账号与登录不一致")
			}
			pageSeen := map[string]bool{}
			for _, song := range one.songs {
				if song.SongMID == "" || seen[song.SongMID] || pageSeen[song.SongMID] {
					return fmt.Errorf("QQ 收藏分页重复或歌曲 ID 缺失")
				}
				pageSeen[song.SongMID] = true
			}
			if !one.hasMore && len(out)+len(one.songs) != one.total {
				return fmt.Errorf("QQ 收藏数量不完整，未更新基线")
			}
			if one.hasMore && len(one.songs) == 0 {
				return fmt.Errorf("QQ 收藏分页提前结束")
			}
			parsed = one
			return nil
		})
		if err != nil {
			return "", nil, err
		}
		if expected < 0 {
			expected = parsed.total
		}
		for _, song := range parsed.songs {
			seen[song.SongMID] = true
			out = append(out, song)
		}
		if !parsed.hasMore {
			return cred.MusicId, out, nil
		}
	}
	return "", nil, fmt.Errorf("QQ 收藏超过分页上限")
}

func qqBackoff(ctx context.Context, attempt int) error {
	if attempt == 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(attempt) * 400 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryQQ(ctx context.Context, call func() error) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := qqBackoff(ctx, attempt); err != nil {
			return err
		}
		last = call()
		if last == nil || ctx.Err() != nil {
			return last
		}
	}
	return last
}

// qqLoginConfirmed accepts the session when any attempt says it is still valid.
// A single expired or unreadable answer is not enough to skip the round.
func qqLoginConfirmed(ctx context.Context, client *qqMusicClient, cookie string) (bool, error) {
	expiredConfirmed := 0
	unknown := 0
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := qqBackoff(ctx, attempt); err != nil {
			return false, err
		}
		check, err := client.getEnvelopeWithContext(ctx, "/login/check_expired", nil, cookie)
		if err != nil {
			last = err
			continue
		}
		expired, known := classifyQQExpired(check)
		if !known {
			unknown++
			continue
		}
		if !expired {
			return true, nil
		}
		expiredConfirmed++
	}
	if expiredConfirmed == 3 {
		return false, fmt.Errorf("QQ 登录失效，请使用 /qq_login")
	}
	if last != nil && expiredConfirmed == 0 && unknown == 0 {
		return false, fmt.Errorf("QQ 登录检查失败")
	}
	return false, fmt.Errorf("QQ 登录状态无法确认")
}

func qqEncryptedAccount(ctx context.Context, client *qqMusicClient, cookie string, cred *config.QQMusicApiConfig) string {
	euin := cred.StrMusicId
	if euin == "" {
		euin = cred.MusicId
	}
	var home qqMusicAPIResponse
	err := retryQQ(ctx, func() error {
		var readErr error
		home, readErr = client.getEnvelopeWithContext(ctx, "/user/"+url.PathEscape(euin)+"/homepage", nil, cookie)
		return readErr
	})
	if err != nil {
		return euin
	}
	raw, _ := json.Marshal(home.Data)
	var object map[string]json.RawMessage
	_ = json.Unmarshal(raw, &object)
	if base, ok := object["base_info"]; ok {
		var info map[string]json.RawMessage
		if json.Unmarshal(base, &info) == nil {
			for k, v := range info {
				object[k] = v
			}
		}
	}
	for _, key := range []string{"encryption_account", "EncryptionAccount", "encrypted_uin", "EncryptedUin", "encrypt_uin"} {
		var value string
		if json.Unmarshal(object[key], &value) == nil && value != "" {
			return value
		}
	}
	return euin
}

type qqFavoritePage struct {
	total   int
	hasMore bool
	creator string
	songs   []SearchResultItem
}

func readQQFavoritePage(ctx context.Context, client *qqMusicClient, euin, cookie string, page int) (qqFavoritePage, error) {
	res, err := client.getEnvelopeWithContext(ctx, "/user/"+url.PathEscape(euin)+"/fav/songs", map[string]string{"page": strconv.Itoa(page), "num": "100"}, cookie)
	if err != nil {
		return qqFavoritePage{}, fmt.Errorf("QQ 收藏读取失败，未更新基线")
	}
	if res.Code == nil || *res.Code != 0 {
		return qqFavoritePage{}, fmt.Errorf("QQ 收藏接口拒绝请求，请检查账号登录")
	}
	raw, err := json.Marshal(res.Data)
	if err != nil {
		return qqFavoritePage{}, err
	}
	// New QQMusicApi serializes normalized names; older releases used
	// upstream aliases. Accept both, with identical completeness checks.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return qqFavoritePage{}, fmt.Errorf("QQ 收藏数据无效")
	}
	if v, ok := fields["songs"]; ok {
		fields["songlist"] = v
	}
	if v, ok := fields["info"]; ok {
		fields["dirinfo"] = v
	}
	raw, _ = json.Marshal(fields)
	var d struct {
		Code     int  `json:"code"`
		Total    *int `json:"total_song_num"`
		TotalAlt *int `json:"total"`
		HasMore  int  `json:"hasmore"`
		Info     struct {
			Creator struct {
				MusicID json.Number `json:"musicid"`
			} `json:"creator"`
		} `json:"dirinfo"`
		Songs []struct {
			ID               int `json:"id"`
			Mid, Title, Name string
			Interval         int
			Singer           []struct{ Name string }
			Album            struct{ Name string }
		} `json:"songlist"`
	}
	if err = json.Unmarshal(raw, &d); err != nil || d.Code != 0 {
		return qqFavoritePage{}, fmt.Errorf("QQ 收藏数据格式无效")
	}
	total := d.Total
	if total == nil {
		total = d.TotalAlt
	}
	if total == nil {
		return qqFavoritePage{}, fmt.Errorf("QQ 收藏接口缺少总数，未建立基线")
	}
	songs := make([]SearchResultItem, 0, len(d.Songs))
	for _, s := range d.Songs {
		artists := []string{}
		for _, a := range s.Singer {
			artists = append(artists, a.Name)
		}
		title := s.Title
		if title == "" {
			title = s.Name
		}
		songs = append(songs, SearchResultItem{Platform: "qq", SongMID: s.Mid, SongID: strconv.Itoa(s.ID), Name: title, Artists: strings.Join(artists, " / "), Album: s.Album.Name, DurationSec: s.Interval, URL: "https://y.qq.com/n/ryqq/songDetail/" + s.Mid})
	}
	return qqFavoritePage{total: *total, hasMore: d.HasMore != 0, creator: d.Info.Creator.MusicID.String(), songs: songs}, nil
}
