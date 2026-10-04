package web

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ncmapi "github.com/XiaoMengXinX/Music163Api-Go/api"
	ncmutils "github.com/XiaoMengXinX/Music163Api-Go/utils"
)

func TestNeteaseSearchRequestUsesEAPIAndContext(t *testing.T) {
	const responseBody = `{"code":200,"result":{"songs":[{"id":42,"name":"Song","artists":[{"name":"Artist"}],"album":{"name":"Album"},"duration":123000,"fee":1}],"hasMore":false,"songCount":1}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/eapi/v1/search/song/get" {
			t.Errorf("unexpected NetEase request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Header.Get("User-Agent") == "" {
			t.Errorf("missing NetEase request headers: %#v", r.Header)
		}
		if !strings.Contains(r.Header.Get("Cookie"), "MUSIC_U=test%20music%20cookie") {
			t.Errorf("MUSIC_U cookie was not preserved: %q", r.Header.Get("Cookie"))
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("params") == "" {
			t.Errorf("expected encrypted EAPI params: %q err=%v", r.Form.Get("params"), err)
		} else if encrypted, decodeErr := hex.DecodeString(r.Form.Get("params")); decodeErr != nil {
			t.Errorf("invalid EAPI hex payload: %v", decodeErr)
		} else {
			decrypted := string(ncmutils.EapiDecrypt(encrypted))
			if !strings.Contains(decrypted, `"s":"keyword"`) || !strings.Contains(decrypted, ncmapi.SearchSongAPI) {
				t.Errorf("unexpected decrypted search payload: %s", decrypted)
			}
		}
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()

	result, err := requestNeteaseSearch(context.Background(), server.URL+"/eapi/v1/search/song/get", "keyword", 10, 0, "test music cookie")
	if err != nil {
		t.Fatalf("NetEase EAPI request failed: %v", err)
	}
	if result.Code != 200 || len(result.Result.Songs) != 1 || result.Result.Songs[0].Id != 42 {
		t.Fatalf("unexpected NetEase result: %#v", result)
	}
	body := ncmapi.CreateSearchSongReqJson(ncmapi.SearchSongConfig{Keyword: "keyword", Limit: 10, Offset: 0})
	if !strings.Contains(body, `"s":"keyword"`) {
		t.Fatalf("search request JSON changed unexpectedly: %s", body)
	}
}

func TestNeteaseAndQQSearchRequestsHonorCancellation(t *testing.T) {
	for _, platform := range []string{"netease", "qq"} {
		t.Run(platform, func(t *testing.T) {
			entered := make(chan struct{})
			releaseHandler := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-r.Context().Done():
				case <-releaseHandler:
				}
			}))
			defer server.Close()
			defer close(releaseHandler)
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() {
				var err error
				if platform == "netease" {
					_, err = requestNeteaseSearch(ctx, server.URL+"/eapi/v1/search/song/get", "cancel", 10, 0, "")
				} else {
					_, err = requestQQSearch(ctx, &http.Client{Timeout: time.Second}, server.URL, "cancel", 10, 1, "")
				}
				result <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("upstream request was not started")
			}
			cancel()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("expected canceled upstream request to return an error")
				}
			case <-time.After(time.Second):
				t.Fatal("upstream request did not stop after cancellation")
			}
		})
	}
}

func TestBilibiliSearchRequestHonorsCancellationAndDecodesResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("keyword") != "雪" || r.URL.Query().Get("page") != "2" || r.URL.Query().Get("page_size") != "10" {
			t.Errorf("unexpected Bilibili paging query: %s", r.URL.RawQuery)
		}
		if !strings.Contains(r.Header.Get("Cookie"), "buvid3=") {
			t.Errorf("Bilibili buvid cookie missing: %q", r.Header.Get("Cookie"))
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"result":[{"bvid":"BV1","title":"<em class=\"keyword\">Snow</em>","author":"Artist","duration":"3:12"}]}}`))
	}))
	defer server.Close()
	items, err := requestBilibiliSearch(context.Background(), server.Client(), server.URL, "雪", 2, 10)
	if err != nil || len(items) != 1 || items[0].Name != "Snow" || items[0].DurationSec != 192 {
		t.Fatalf("unexpected Bilibili result: %#v err=%v", items, err)
	}

	entered := make(chan struct{})
	releaseHandler := make(chan struct{})
	blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-releaseHandler:
		}
	}))
	defer blocking.Close()
	defer close(releaseHandler)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := requestBilibiliSearch(ctx, &http.Client{Timeout: time.Second}, blocking.URL, "cancel", 1, 10)
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Bilibili request was not started")
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("expected Bilibili cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("Bilibili request did not stop after cancellation")
	}
}

func TestSearchYouTubePassesCancellationToCommand(t *testing.T) {
	previous := runYTDLPSearch
	t.Cleanup(func() { runYTDLPSearch = previous })
	started := make(chan struct{})
	stopped := make(chan struct{})
	runYTDLPSearch = func(ctx context.Context, _ string, _ int) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := searchYouTubeWithContext(ctx, "keyword", 10, 0)
		finished <- err
	}()
	<-started
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation to reach yt-dlp search, got %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("yt-dlp runner was not canceled")
	}
}
