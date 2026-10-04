package config

import "testing"

func TestWithLiveTaskOverridesOnlyChangesSafeFields(t *testing.T) {
	base := &Config{
		Tidy:             &TidyConfig{Mode: 1, DistDir: "old"},
		AdditionalConfig: &AdditionalConfig{MusicMode: false, EnableCron: true, MonitorDirs: []string{"old-dir"}},
		QQMusicApiConfig: &QQMusicApiConfig{Enable: false, Endpoint: "", MusicId: ""},
	}
	SetLiveRuntimeConfig(&Config{
		Tidy:             &TidyConfig{Mode: 2, DistDir: "new"},
		AdditionalConfig: &AdditionalConfig{MusicMode: true, EnableCron: false, MonitorDirs: []string{"new-dir"}},
		QQMusicApiConfig: &QQMusicApiConfig{Enable: true, Endpoint: "new-endpoint", MusicId: "123", MusicKey: "new-key"},
	})
	defer SetLiveRuntimeConfig(nil)

	got := WithLiveTaskOverrides(base)
	if got.Tidy.Mode != 1 || got.Tidy.DistDir != "new" {
		t.Fatalf("unexpected tidy overrides: %#v", got.Tidy)
	}
	if !got.AdditionalConfig.MusicMode || !got.AdditionalConfig.EnableCron || got.AdditionalConfig.MonitorDirs[0] != "old-dir" {
		t.Fatalf("unexpected additional config overrides: %#v", got.AdditionalConfig)
	}
	if base.QQMusicApiConfig.Endpoint != "" || got.QQMusicApiConfig.Endpoint != "" || got.QQMusicApiConfig.MusicId != "123" || got.QQMusicApiConfig.MusicKey != "new-key" {
		t.Fatalf("QR credential overrides should apply without replacing service settings: base=%#v got=%#v", base.QQMusicApiConfig, got.QQMusicApiConfig)
	}
	if base.Tidy.DistDir != "old" || base.AdditionalConfig.MusicMode {
		t.Fatal("base runtime config was mutated")
	}
}
