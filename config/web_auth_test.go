package config

import (
	"testing"

	"github.com/goccy/go-yaml"
)

func TestWebAuthConfigDefaultsAndYAMLRoundTrip(t *testing.T) {
	defaults := &Config{}
	defaults.setDefaults()
	if defaults.WebConfig == nil || defaults.WebConfig.Auth.Enable || defaults.WebConfig.Auth.Username != "" || defaults.WebConfig.Auth.Password != "" {
		t.Fatalf("unexpected WebUI auth defaults: %#v", defaults.WebConfig)
	}

	var got Config
	if err := yaml.Unmarshal([]byte(`web_config:
  enable: true
  auth:
    enable: true
    username: admin
    password: test-password
`), &got); err != nil {
		t.Fatalf("unmarshal WebUI auth config: %v", err)
	}
	if !got.WebConfig.Auth.Enable || got.WebConfig.Auth.Username != "admin" || got.WebConfig.Auth.Password != "test-password" {
		t.Fatalf("WebUI auth fields did not load: %#v", got.WebConfig.Auth)
	}
}
