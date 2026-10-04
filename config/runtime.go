package config

import "sync/atomic"

var liveRuntime atomic.Pointer[Config]

// SetLiveRuntimeConfig publishes the active immutable config snapshot used by
// future task submissions. Callers should provide a new snapshot, not mutate it.
func SetLiveRuntimeConfig(cfg *Config) {
	if cfg == nil {
		liveRuntime.Store(nil)
		return
	}
	copy := *cfg
	if cfg.Tidy != nil {
		tidy := *cfg.Tidy
		copy.Tidy = &tidy
	}
	if cfg.AdditionalConfig != nil {
		additional := *cfg.AdditionalConfig
		additional.MonitorDirs = append([]string(nil), cfg.AdditionalConfig.MonitorDirs...)
		copy.AdditionalConfig = &additional
	}
	if cfg.QQMusicApiConfig != nil {
		qq := *cfg.QQMusicApiConfig
		copy.QQMusicApiConfig = &qq
	}
	liveRuntime.Store(&copy)
}

// WithLiveTaskOverrides returns a per-task view with the supported hot-reload
// fields overlaid while leaving the caller's long-lived config untouched.
func WithLiveTaskOverrides(base *Config) *Config {
	if base == nil {
		base = &Config{}
	}
	live := liveRuntime.Load()
	copy := *base
	if base.Tidy != nil {
		tidy := *base.Tidy
		if live != nil && live.Tidy != nil {
			tidy.DistDir = live.Tidy.DistDir
		}
		copy.Tidy = &tidy
	}
	if base.AdditionalConfig != nil {
		additional := *base.AdditionalConfig
		additional.MonitorDirs = append([]string(nil), base.AdditionalConfig.MonitorDirs...)
		if live != nil && live.AdditionalConfig != nil {
			additional.MusicMode = live.AdditionalConfig.MusicMode
		}
		copy.AdditionalConfig = &additional
	}
	if base.QQMusicApiConfig != nil {
		qq := *base.QQMusicApiConfig
		if live != nil && live.QQMusicApiConfig != nil {
			qq.LoginType = live.QQMusicApiConfig.LoginType
			qq.RefreshKey = live.QQMusicApiConfig.RefreshKey
			qq.RefreshToken = live.QQMusicApiConfig.RefreshToken
			qq.AccessToken = live.QQMusicApiConfig.AccessToken
			qq.MusicId = live.QQMusicApiConfig.MusicId
			qq.StrMusicId = live.QQMusicApiConfig.StrMusicId
			qq.OpenID = live.QQMusicApiConfig.OpenID
			qq.UnionID = live.QQMusicApiConfig.UnionID
			qq.MusicKey = live.QQMusicApiConfig.MusicKey
			qq.ExpiredAt = live.QQMusicApiConfig.ExpiredAt
		}
		copy.QQMusicApiConfig = &qq
	}
	return &copy
}
