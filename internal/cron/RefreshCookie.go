package cron

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/utils"
)

type MusickeyData struct {
	RefreshKey   string `json:"refresh_key"`
	RefreshToken string `json:"refresh_token"`
	Musicid      int    `json:"musicid"`
	Musickey     string `json:"musickey"`
	ExpiredAt    int64  `json:"expired_at"`
}

type QQRefreshResponse struct {
	Code      int          `json:"code"`
	Message   string       `json:"message"`
	Data      MusickeyData `json:"data"`
	Timestamp int64        `json:"timestamp"`
}

// refreshMusicKey 刷新musickey
func refreshMusicKey(c *config.Config, client *http.Client) {
	musickeyPath := filepath.Join("data", "temp", "musickey.json")

	data, err := loadMusickey(musickeyPath)
	if errors.Is(err, os.ErrNotExist) {
		utils.InfoWithFormat("musickey.json 不存在，直接刷新")
		if err = refreshAndSave(true, musickeyPath, c, client); err != nil {
			utils.ErrorWithFormat("刷新失败: %v", err)
		}
		return
	} else if err != nil {
		utils.ErrorWithFormat("读取 musickey.json 出错: %v", err)
		return
	}

	if isExpired(data) {
		utils.InfoWithFormat("Musickey 已过期 刷新中...")
		if err = refreshAndSave(false, musickeyPath, c, client); err != nil {
			utils.ErrorWithFormat("刷新失败: %v", err)
		}
	} else {
		utils.Infof("Musickey 未过期 无需刷新")
	}
}

// loadMusickey 读取 musickey.json
func loadMusickey(path string) (*MusickeyData, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var data MusickeyData
	if err := json.Unmarshal(bytes, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// isExpired 判断本地时间戳是否过期
func isExpired(data *MusickeyData) bool {
	return time.Now().Unix() >= data.ExpiredAt
}

// refreshAndSave 调用接口B刷新并保存到文件
func refreshAndSave(isNew bool, path string, cfg *config.Config, client *http.Client) error {
	request, err := http.NewRequest(http.MethodGet, cfg.QQMusicApiConfig.Endpoint+"/login/api_refresh_cookies", nil)
	if err != nil {
		return err
	}
	//处理请求头
	var headers map[string]string
	if isNew {
		headers = refreshMusicKeyHeaders(cfg, &MusickeyData{})
	} else {
		var bytes []byte
		bytes, err = os.ReadFile(path)
		if err != nil {
			return err
		}
		var musicKeyData MusickeyData
		err = json.Unmarshal(bytes, &musicKeyData)
		if err != nil {
			return err
		}
		headers = refreshMusicKeyHeaders(cfg, &musicKeyData)
	}
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	resp, err := client.Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var newData QQRefreshResponse
	if err = json.NewDecoder(resp.Body).Decode(&newData); err != nil {
		return err
	}
	bytes, _ := json.MarshalIndent(newData.Data, "", "  ")
	return os.WriteFile(path, bytes, 0644)
}

// refreshMusicKeyHeaders 刷新musickey的请求头
func refreshMusicKeyHeaders(cfg *config.Config, data *MusickeyData) map[string]string {
	headers := map[string]string{
		"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
		"Referer":    "https://y.qq.com/",
	}
	if cfg.QQMusicApiConfig.EnableSign {
		headers["X-Enable-Sign"] = "true"
		headers["X-Enable-Cache"] = "true"
	}
	if data.Musickey != "" {
		//设置Cookie
		switch cfg.QQMusicApiConfig.LoginType {
		case 1:
			//wx
			headers["Cookie"] = fmt.Sprintf("login_type=1;musicid=%d;musickey=%s;refresh_key=%s;refresh_token=%s",
				data.Musicid,
				data.Musickey,
				data.RefreshKey,
				data.RefreshToken,
			)
		case 2:
			//qq
			headers["Cookie"] = fmt.Sprintf("login_type=2;musicid=%d;musickey=%s;refresh_key=%s;refresh_token=%s",
				data.Musicid,
				data.Musickey,
				data.RefreshKey,
				data.RefreshToken,
			)
		}
	} else {
		//读取cookiecloud同步的cookies文件
		cookiePath := filepath.Join(cfg.CookieCloud.CookieFilePath, cfg.CookieCloud.CookieFile)
		qqCookies := utils.GetCookiesByDomain(cookiePath, ".qq.com")
		qqmusicKey := qqCookies["qqmusic_key"]
		if qqmusicKey != "" {
			qqmusicKey = cfg.QQMusicApiConfig.MusicKey
		}
		//设置Cookie
		switch cfg.QQMusicApiConfig.LoginType {
		case 1:
			//wx
			wxuin := qqCookies["wxuin"]
			if wxuin != "" {
				wxuin = cfg.QQMusicApiConfig.MusicId
			}
			headers["Cookie"] = fmt.Sprintf("login_type=1;musicid=%s;musickey=%s;refresh_key=%s;refresh_token=%s",
				wxuin,
				qqmusicKey,
				cfg.QQMusicApiConfig.RefreshKey,
				cfg.QQMusicApiConfig.RefreshToken,
			)
		case 2:
			//qq
			uin := qqCookies["uin"]
			if uin != "" {
				uin = cfg.QQMusicApiConfig.MusicId
			}
			headers["Cookie"] = fmt.Sprintf("login_type=2;musicid=%s;musickey=%s;refresh_key=%s;refresh_token=%s",
				uin,
				qqmusicKey,
				cfg.QQMusicApiConfig.RefreshKey,
				cfg.QQMusicApiConfig.RefreshToken,
			)
		}
	}
	return headers
}
