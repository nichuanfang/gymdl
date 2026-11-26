package core

import (
	"errors"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

// A PUT is answered only after the server consumed the whole body, so the
// header wait has to cover a multi-megabyte FLAC over a slow tunnel. The old
// fixed 15s aborted every large upload and sent the file to the outbox.
func TestWebDAVResponseHeaderTimeoutDefaultsAboveAVoiceFileUpload(t *testing.T) {
	if got := webDAVResponseHeaderTimeout(nil); got < 2*time.Minute {
		t.Fatalf("default header wait = %v, want at least 2m", got)
	}
	if got := webDAVResponseHeaderTimeout(&config.WebDAVConfig{}); got < 2*time.Minute {
		t.Fatalf("zero config header wait = %v, want at least 2m", got)
	}
}

func TestWebDAVResponseHeaderTimeoutHonoursConfig(t *testing.T) {
	cfg := &config.WebDAVConfig{ResponseHeaderTimeoutSeconds: 600}
	if got := webDAVResponseHeaderTimeout(cfg); got != 10*time.Minute {
		t.Fatalf("header wait = %v, want 10m", got)
	}
}

// The client budget must stay above the header budget, otherwise the client
// timeout fires first and the configured header wait never applies.
func TestWebDAVClientTimeoutExceedsHeaderBudget(t *testing.T) {
	for _, cfg := range []*config.WebDAVConfig{
		nil,
		{},
		{ResponseHeaderTimeoutSeconds: 600},
		{ResponseHeaderTimeoutSeconds: 30},
	} {
		client := webDAVClientTimeout(cfg)
		header := webDAVResponseHeaderTimeout(cfg)
		if client <= header {
			t.Fatalf("cfg=%+v: client timeout %v must exceed header wait %v", cfg, client, header)
		}
	}
}

// Proxy-level failures come from a tunnel that lost its client; retrying a
// second later lands inside the same outage and wastes an attempt.
func TestWebDAVRetryDelayBacksOffFurtherForTunnelOutages(t *testing.T) {
	plain := webDAVRetryDelay(1, errors.New("mkdirall /a/b/: 404"))
	for _, msg := range []string{
		"no route found: dav.example.org /a.flac",
		"connection write timeout",
		"context canceled",
		"session shutdown",
	} {
		got := webDAVRetryDelay(1, errors.New(msg))
		if got <= plain {
			t.Fatalf("delay for %q = %v, want more than %v", msg, got, plain)
		}
	}
	if webDAVRetryDelay(2, errors.New("no route found")) <= webDAVRetryDelay(1, errors.New("no route found")) {
		t.Fatal("delay should grow with the attempt number")
	}
}

func TestWebDAVRetryDelayHandlesNilError(t *testing.T) {
	if got := webDAVRetryDelay(1, nil); got <= 0 {
		t.Fatalf("delay for nil error = %v, want positive", got)
	}
}
