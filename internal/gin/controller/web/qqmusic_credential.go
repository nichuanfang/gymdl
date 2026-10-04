package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/utils"
)

var qqMusicCredentialPath = utils.QQMusicCredentialPath

type qqMusicStoredCredential struct {
	MusicID      int64  `json:"musicid"`
	MusicKey     string `json:"musickey"`
	StrMusicID   string `json:"str_musicid"`
	OpenID       string `json:"openid"`
	UnionID      string `json:"unionid"`
	LoginType    int    `json:"login_type"`
	RefreshKey   string `json:"refresh_key"`
	RefreshToken string `json:"refresh_token"`
	AccessToken  string `json:"access_token"`
	ExpiredAt    int64  `json:"expired_at"`
}

// currentQQMusicCredential treats musickey.json as the active credential cache.
// config.yaml is only a bootstrap source when that cache is not usable yet.
func currentQQMusicCredential(path string, fallback *config.QQMusicApiConfig) (*config.QQMusicApiConfig, string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		var stored qqMusicStoredCredential
		if decodeErr := json.Unmarshal(data, &stored); decodeErr == nil && stored.MusicID > 0 && strings.TrimSpace(stored.MusicKey) != "" {
			return stored.toConfig(fallback), "musickey.json", nil
		}
		// An incomplete/corrupted cache can be recovered using the initial
		// credential from config.yaml, but never replace that file from status.
		if qqCredentialConfigured(fallback) {
			return cloneQQMusicCredential(fallback), "config.yaml", nil
		}
		return nil, "", errors.New("本地 QQ 凭证缓存格式无效")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("读取本地 QQ 凭证缓存失败: %w", err)
	}
	if qqCredentialConfigured(fallback) {
		return cloneQQMusicCredential(fallback), "config.yaml", nil
	}
	return nil, "", nil
}

func (stored qqMusicStoredCredential) toConfig(base *config.QQMusicApiConfig) *config.QQMusicApiConfig {
	credential := cloneQQMusicCredential(base)
	if credential == nil {
		credential = &config.QQMusicApiConfig{}
	}
	credential.MusicId = strconv.FormatInt(stored.MusicID, 10)
	credential.MusicKey = stored.MusicKey
	credential.StrMusicId = stored.StrMusicID
	credential.OpenID = stored.OpenID
	credential.UnionID = stored.UnionID
	credential.LoginType = stored.LoginType
	credential.RefreshKey = stored.RefreshKey
	credential.RefreshToken = stored.RefreshToken
	credential.AccessToken = stored.AccessToken
	credential.ExpiredAt = int(stored.ExpiredAt)
	return credential
}

func cloneQQMusicCredential(source *config.QQMusicApiConfig) *config.QQMusicApiConfig {
	if source == nil {
		return nil
	}
	copy := *source
	return &copy
}

func applyQQMusicCredentialToRuntime(credential *config.QQMusicApiConfig) {
	if credential == nil {
		return
	}
	active := cloneWebConfig(GetWebConfig())
	if active.QQMusicApiConfig == nil {
		active.QQMusicApiConfig = &config.QQMusicApiConfig{}
	}
	base := active.QQMusicApiConfig
	base.MusicId = credential.MusicId
	base.MusicKey = credential.MusicKey
	base.StrMusicId = credential.StrMusicId
	base.OpenID = credential.OpenID
	base.UnionID = credential.UnionID
	base.LoginType = credential.LoginType
	base.RefreshKey = credential.RefreshKey
	base.RefreshToken = credential.RefreshToken
	base.AccessToken = credential.AccessToken
	base.ExpiredAt = credential.ExpiredAt
	replaceWebConfig(active)
	config.SetLiveRuntimeConfig(active)
	if manager != nil {
		manager.SetConfig(active)
	}
}

func qqMusicStoredCredentialFromMap(credential map[string]interface{}) (qqMusicStoredCredential, error) {
	musicIDText := credentialString(credential, "str_musicid", "str_music_id", "strMusicid")
	if musicIDText == "" {
		musicIDText = credentialString(credential, "musicid", "music_id", "musicId")
	}
	musicID, err := strconv.ParseInt(strings.TrimSpace(musicIDText), 10, 64)
	if err != nil || musicID <= 0 {
		return qqMusicStoredCredential{}, errors.New("QQMusicApi 未返回有效账号")
	}
	musicKey := credentialString(credential, "musickey", "music_key", "musicKey")
	if musicKey == "" {
		return qqMusicStoredCredential{}, errors.New("QQMusicApi 未返回有效音乐凭证")
	}
	loginType, _ := credentialInt(credential, "login_type", "loginType")
	expiredAt, _ := credentialInt(credential, "expired_at", "expiredAt")
	return qqMusicStoredCredential{
		MusicID:      musicID,
		MusicKey:     musicKey,
		StrMusicID:   credentialString(credential, "str_musicid", "str_music_id", "strMusicid"),
		OpenID:       credentialString(credential, "openid", "open_id", "openId"),
		UnionID:      credentialString(credential, "unionid", "union_id", "unionId"),
		LoginType:    loginType,
		RefreshKey:   credentialString(credential, "refresh_key", "refreshKey"),
		RefreshToken: credentialString(credential, "refresh_token", "refreshToken"),
		AccessToken:  credentialString(credential, "access_token", "accessToken"),
		ExpiredAt:    int64(expiredAt),
	}, nil
}

// saveQQMusicCredential writes only the runtime musickey cache. It deliberately
// never edits config.yaml, which remains the bootstrap credential source.
func saveQQMusicCredential(path string, credential map[string]interface{}) (*qqMusicStoredCredential, error) {
	stored, err := qqMusicStoredCredentialFromMap(credential)
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化 QQ 凭证失败: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 QQ 凭证目录失败: %w", err)
	}
	file, err := os.CreateTemp(dir, ".musickey-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("创建 QQ 凭证临时文件失败: %w", err)
	}
	tmpPath := file.Name()
	defer os.Remove(tmpPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("设置 QQ 凭证文件权限失败: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("写入 QQ 凭证失败: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("同步 QQ 凭证失败: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("关闭 QQ 凭证文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("保存 QQ 凭证失败: %w", err)
	}
	return &stored, nil
}
