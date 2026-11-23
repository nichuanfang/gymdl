package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	ncmapi "github.com/XiaoMengXinX/Music163Api-Go/api"
	ncmtypes "github.com/XiaoMengXinX/Music163Api-Go/types"
	ncmutils "github.com/XiaoMengXinX/Music163Api-Go/utils"
	"github.com/nichuanfang/gymdl/utils"
)

const neteaseDailyEndpoint = "https://music.163.com/api/v3/discovery/recommend/songs"

// NeteaseDailySongs reads the logged-in account's daily recommendations.
// It does not use the keyword-search cooldown; a failed call is cached by discovery.
func NeteaseDailySongs(ctx context.Context) ([]SearchResultItem, error) {
	_, musicU := neteaseSearchCookie()
	body, err := postNeteaseEAPI(ctx, neteaseDailyEndpoint, ncmapi.RecommendSongsAPI, "{}", musicU)
	if err != nil {
		return nil, fmt.Errorf("网易云每日推荐失败: %w", err)
	}
	var payload ncmtypes.RecommendSongsData
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析网易云每日推荐失败: %w", err)
	}
	if payload.Code != 200 {
		if payload.Code == 405 {
			return nil, fmt.Errorf("网易云每日推荐暂时受限，请稍后重试")
		}
		return nil, fmt.Errorf("网易云每日推荐失败: 上游响应 code=%d", payload.Code)
	}
	items := make([]SearchResultItem, 0, len(payload.Data.DailySongs))
	for _, song := range payload.Data.DailySongs {
		if song.Id <= 0 || strings.TrimSpace(song.Name) == "" {
			continue
		}
		artists := make([]string, 0, len(song.Ar))
		for _, artist := range song.Ar {
			if name := strings.TrimSpace(artist.Name); name != "" {
				artists = append(artists, name)
			}
		}
		if len(artists) == 0 {
			continue
		}
		items = append(items, SearchResultItem{
			Platform: "netease", SongID: strconv.Itoa(song.Id), Name: song.Name,
			Artists: strings.Join(artists, " / "), Album: song.Al.Name,
			CoverURL: song.Al.PicUrl, DurationSec: song.Dt / 1000,
			URL: fmt.Sprintf("https://music.163.com/song?id=%d", song.Id), IsVIP: song.Fee == 1,
		})
	}
	return items, nil
}

func postNeteaseEAPI(ctx context.Context, endpoint, path, reqJSON, musicU string) ([]byte, error) {
	params := ncmutils.Format2Params(ncmutils.SpliceStr(path, reqJSON))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(params))
	if err != nil {
		return nil, err
	}
	cookies := map[string]string{
		"appver": "8.9.70", "buildver": strconv.FormatInt(time.Now().Unix(), 10),
		"resolution": "1920x1080", "os": "android",
	}
	if musicU != "" {
		cookies["MUSIC_U"] = musicU
	}
	parts := make([]string, 0, len(cookies))
	for key, value := range cookies {
		parts = append(parts, key+"="+value)
	}
	req.Header.Set("Cookie", strings.Join(parts, "; "))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", ncmutils.ChooseUserAgent())
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("网易云 API HTTP 状态码: %d", resp.StatusCode)
	}
	utils.DebugWithFormat("[WebSearch] NCM daily bytes=%d", len(body))
	return body, nil
}
