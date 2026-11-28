package music

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testQQShortURL = "https://c6.y.qq.com/base/fcgi-bin/u?__=share-token"
const testQQMobileURL = "https://i2.y.qq.com/n3/other/pages/playsong/index.html?songid=&songmid=000EqCkp3nm7Fb&media_mid=004W7Rqj2iFu0b&hosteuin=share-account"
const testQQSongURL = "https://y.qq.com/n/ryqq/songDetail/000EqCkp3nm7Fb"

type qqTestTransport func(*http.Request) (*http.Response, error)

func (f qqTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func qqTestResponse(status int, location string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: http.Header{"Location": []string{location}},
		Body: io.NopCloser(strings.NewReader("")),
	}
}

func TestQQShareStopsAtFirstRedirectWithSongID(t *testing.T) {
	var requests []string
	p := &QQMusicProcessor{client: &http.Client{Transport: qqTestTransport(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.String())
		switch req.URL.Path {
		case "/base/fcgi-bin/u":
			if req.URL.Query().Get("step") == "" {
				return qqTestResponse(http.StatusFound, "?step=2"), nil
			}
			return qqTestResponse(http.StatusFound, testQQMobileURL), nil
		default:
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
	})}}

	got, err := p.ResolveShareURL(testQQShortURL)
	if err != nil || got != testQQSongURL {
		t.Fatalf("ResolveShareURL = %q, %v", got, err)
	}
	if len(requests) != 2 || requests[1] != "https://c6.y.qq.com/base/fcgi-bin/u?step=2" {
		t.Fatalf("expected two short-link requests and no playback-page request, got %v", requests)
	}
}

func TestQQShareDirectLinks(t *testing.T) {
	p := &QQMusicProcessor{client: &http.Client{Transport: qqTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("direct link made a network request")
		return nil, nil
	})}}
	for _, tc := range []struct{ raw, want string }{
		{testQQMobileURL, testQQSongURL},
		{testQQShortURL + "&songmid=000EqCkp3nm7Fb", testQQSongURL},
		{testQQSongURL + "?cover=Example_2", testQQSongURL},
		{"https://i.y.qq.com/v8/playsong.html?songid=12345", "https://y.qq.com/n/ryqq/songDetail/12345"},
		{"https://m.y.qq.com/n2/m/share/details/song.html?songmid=000EqCkp3nm7Fb&songid=12345", testQQSongURL},
		{"https://i.y.qq.com/n2/m/share/details/taoge.html?id=123&songmid=000EqCkp3nm7Fb", "https://y.qq.com/n/ryqq/playlist/123"},
		{"https://y.qq.com/n/ryqq/playlist/456", "https://y.qq.com/n/ryqq/playlist/456"},
	} {
		got, err := p.ResolveShareURL(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("ResolveShareURL(%q) = %q, %v, want %q", tc.raw, got, err, tc.want)
		}
	}
}

func TestQQShareRetriesOnlyTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		requestErr error
		recover    bool
		attempts   int
		wantErr    error
	}{
		{name: "timeout recovers", requestErr: context.DeadlineExceeded, recover: true, attempts: 2},
		{name: "timeout twice", requestErr: context.DeadlineExceeded, attempts: 2, wantErr: ErrQQShareUnavailable},
		{name: "connection reset", requestErr: syscall.ECONNRESET, recover: true, attempts: 2},
		{name: "502", status: http.StatusBadGateway, recover: true, attempts: 2},
		{name: "503 twice", status: http.StatusServiceUnavailable, attempts: 2, wantErr: ErrQQShareUnavailable},
		{name: "504", status: http.StatusGatewayTimeout, recover: true, attempts: 2},
		{name: "404", status: http.StatusNotFound, attempts: 1, wantErr: ErrQQShareInvalid},
		{name: "429", status: http.StatusTooManyRequests, attempts: 1, wantErr: ErrQQShareUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			p := &QQMusicProcessor{client: &http.Client{Transport: qqTestTransport(func(req *http.Request) (*http.Response, error) {
				requests++
				deadline, ok := req.Context().Deadline()
				if remaining := time.Until(deadline); !ok || remaining > 4*time.Second || remaining <= 0 {
					t.Errorf("request deadline = %v, remaining = %v", deadline, remaining)
				}
				if tc.recover && requests == 2 {
					return qqTestResponse(http.StatusFound, testQQMobileURL), nil
				}
				if tc.requestErr != nil {
					return nil, fmt.Errorf("GET %s: %w", req.URL, tc.requestErr)
				}
				return qqTestResponse(tc.status, ""), nil
			})}}

			got, err := p.ResolveShareURL(testQQShortURL)
			if !errors.Is(err, tc.wantErr) || (err == nil && got != testQQSongURL) || requests != tc.attempts {
				t.Fatalf("ResolveShareURL = %q, %v after %d requests; want %v after %d", got, err, requests, tc.wantErr, tc.attempts)
			}
			if err != nil && (strings.Contains(err.Error(), "share-token") || strings.Contains(err.Error(), "GET")) {
				t.Fatalf("link error exposed the request: %v", err)
			}
		})
	}
}

func TestQQShareRejectsUnsafeOrUnusableRedirects(t *testing.T) {
	for _, tc := range []struct {
		name, location string
		requests       int
	}{
		{name: "same URL", location: testQQShortURL + "#loop", requests: 1},
		{name: "off-site", location: "https://y.qq.com.example.test/n/ryqq/songDetail/000EqCkp3nm7Fb", requests: 1},
		{name: "nonstandard port", location: "https://y.qq.com:8443/n/ryqq/songDetail/000EqCkp3nm7Fb", requests: 1},
		{name: "malformed Location", location: "http://%zz", requests: 1},
		{name: "missing Location", requests: 1},
		{name: "media ID only", location: "https://i.y.qq.com/v8/playsong.html?songid=&media_mid=004W7Rqj2iFu0b", requests: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			p := &QQMusicProcessor{client: &http.Client{Transport: qqTestTransport(func(*http.Request) (*http.Response, error) {
				requests++
				if requests > 1 {
					return qqTestResponse(http.StatusOK, ""), nil
				}
				return qqTestResponse(http.StatusFound, tc.location), nil
			})}}
			got, err := p.ResolveShareURL(testQQShortURL)
			if got != "" || !errors.Is(err, ErrQQShareInvalid) || requests != tc.requests {
				t.Fatalf("ResolveShareURL = %q, %v after %d requests; want invalid after %d", got, err, requests, tc.requests)
			}
		})
	}
}
