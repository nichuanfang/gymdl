package discovery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func neteaseProvider(t *testing.T, trip roundTripFunc) *Providers {
	t.Helper()
	dir := t.TempDir()
	cookie := []byte("# Netscape HTTP Cookie File\n.music.163.com\tTRUE\t/\tFALSE\t0\tMUSIC_U\tok\n")
	if err := os.WriteFile(filepath.Join(dir, "cookies.txt"), cookie, 0o600); err != nil {
		t.Fatal(err)
	}
	return &Providers{
		Config: &config.Config{CookieCloud: &config.CookieCloudConfig{CookieFilePath: dir, CookieFile: "cookies.txt"}},
		Client: &http.Client{Transport: trip},
	}
}

func TestNeteaseRequestRetriesTransientFailure(t *testing.T) {
	calls := 0
	p := neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		calls++
		cookie := r.Header.Get("Cookie")
		for _, part := range []string{"MUSIC_U=ok", "os=android", "appver=8.9.70", "buildver=", "resolution=1920x1080"} {
			if !strings.Contains(cookie, part) {
				t.Errorf("cookie header missing %s: %q", part, cookie)
			}
		}
		if calls == 1 {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("busy")), Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200}`)), Request: r}, nil
	})
	var out struct{ Code int }
	if err := p.ncm(context.Background(), "/api/w/nuser/account/get", map[string]any{}, &out); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || out.Code != 200 {
		t.Fatalf("calls=%d code=%d", calls, out.Code)
	}
}

func TestNeteaseCoversAreHydratedForCachedRecommendationSongs(t *testing.T) {
	const detailCover = "https://p2.music.126.net/ZGffiDQZrGj5s_hnR1CNbg==/109951165566379710.jpg"
	p := neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "song/detail") {
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		body := `{"code":200,"songs":[{"id":186016,"name":"Song","dt":180000,"ar":[{"name":"Artist"}],"al":{"name":"Album","picUrl":"` + detailCover + `"}}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	service := &Service{Provider: p}
	tracks := service.hydrateNeteaseCovers(t.Context(), []Track{{
		Platform: "netease", ID: "186016", Title: "Song", Artist: "Artist", Verified: true,
	}}, 1)
	if len(tracks) != 1 || tracks[0].CoverURL != detailCover+"?param=500y500" {
		t.Fatalf("hydrated track cover = %q", tracks[0].CoverURL)
	}
}

func TestNeteaseEnrichAddsCoverToAnOlderFavorite(t *testing.T) {
	const detailCover = "https://p2.music.126.net/ZGffiDQZrGj5s_hnR1CNbg==/109951165566379710.jpg"
	p := neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		body := `{"code":200,"songs":[{"id":186016,"name":"Song","dt":180000,"ar":[{"name":"Artist"}],"al":{"name":"Album","picUrl":"` + detailCover + `"}}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	track, skip, err := p.Enrich(t.Context(), Track{Platform: "netease", ID: "186016", Verified: true})
	if err != nil || skip || track.CoverURL != detailCover+"?param=500y500" {
		t.Fatalf("enriched track = %+v, skip=%t, err=%v", track, skip, err)
	}
}

func TestPooledNeteaseRecommendationHydratesAnOldPoolRow(t *testing.T) {
	const detailCover = "https://p2.music.126.net/ZGffiDQZrGj5s_hnR1CNbg==/109951165566379710.jpg"
	store := testStore(t)
	ctx := t.Context()
	favorite := testTrack("1", "旧收藏", "Artist")
	if _, _, err := store.Apply(ctx, 7, 7, snapshot("netease-account", favorite), true); err != nil {
		t.Fatal(err)
	}
	ready := tasteTestTrack("netease", "186016", "新歌", "Artist")
	if err := store.SaveTastePool(ctx, 7, []Track{ready}); err != nil {
		t.Fatal(err)
	}
	p := neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "song/detail") {
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		body := `{"code":200,"songs":[{"id":186016,"name":"新歌","dt":180000,"ar":[{"name":"Artist"}],"al":{"name":"专辑","picUrl":"` + detailCover + `"}}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	service := &Service{Store: store, Provider: p, Library: stubLibrary{}, SlowNetease: true,
		Taste: &TasteSources{SearchPage: func(context.Context, string, string, int) ([]Track, error) { return nil, nil }}}
	got, err := service.RecommendTaste(ctx, 7, TasteRequest{Platform: "netease"})
	if err != nil || got.Track == nil || got.Track.CoverURL != detailCover+"?param=500y500" {
		t.Fatalf("pooled recommendation cover = %+v, %v", got.Track, err)
	}
}

func TestNeteaseRequestKeepsTheFailureReason(t *testing.T) {
	calls := 0
	p := neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader("no")), Request: r}, nil
	})
	var out struct{ Code int }
	err := p.ncm(context.Background(), "/api/w/nuser/account/get", map[string]any{}, &out)
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") || calls != 1 {
		t.Fatal(calls, err)
	}

	calls = 0
	p = neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":301}`)), Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200}`)), Request: r}, nil
	})
	err = p.ncm(context.Background(), "/api/w/nuser/account/get", map[string]any{}, &out)
	if err != nil || calls != 3 || out.Code != 200 {
		t.Fatal(calls, err)
	}

	calls = 0
	p = neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":301}`)), Request: r}, nil
	})
	err = p.ncm(context.Background(), "/api/w/nuser/account/get", map[string]any{}, &out)
	if err == nil || strings.Contains(err.Error(), "登录失效") || !strings.Contains(err.Error(), "301") || calls != 3 {
		t.Fatal(calls, err)
	}

	calls = 0
	p = neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, timeoutErr{}
	})
	err = p.ncm(context.Background(), "/api/w/nuser/account/get", map[string]any{}, &out)
	if err == nil || !strings.Contains(err.Error(), "超时") || calls != 1 {
		t.Fatal(calls, err)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestNeteaseMissingLoginSkipsWithoutBlamingExpiry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cookies.txt"), []byte("# Netscape HTTP Cookie File\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := &Providers{
		Config: &config.Config{CookieCloud: &config.CookieCloudConfig{CookieFilePath: dir, CookieFile: "cookies.txt"}},
		Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200}`)), Request: r}, nil
		})},
	}
	var out struct{ Code int }
	err := p.ncm(context.Background(), "/api/w/nuser/account/get", map[string]any{}, &out)
	if err == nil || calls != 0 || strings.Contains(err.Error(), "登录失效") || !strings.Contains(err.Error(), "暂时读不到") {
		t.Fatal(calls, err)
	}
}

func TestNeteaseEmptyAccountIsRetried(t *testing.T) {
	accountCalls := 0
	p := neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		body := `{"code":200}`
		switch {
		case strings.Contains(r.URL.Path, "account/get"):
			accountCalls++
			if accountCalls >= 2 {
				body = `{"code":200,"account":{"id":9}}`
			}
		case strings.Contains(r.URL.Path, "user/playlist"):
			body = `{"code":200,"more":false,"playlist":[{"id":50,"specialType":5,"userId":9}]}`
		case strings.Contains(r.URL.Path, "playlist/detail"):
			body = `{"code":200,"playlist":{"trackCount":0,"trackIds":[]}}`
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			return nil, errors.New("unexpected")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	account, ids, err := p.NeteaseLikedIDs(context.Background())
	if err != nil || account != "9" || len(ids) != 0 || accountCalls != 2 {
		t.Fatalf("account=%s ids=%v calls=%d err=%v", account, ids, accountCalls, err)
	}
}

func TestSyncReusesStoredNeteaseSongs(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, _, err := store.Apply(ctx, 1, 1, snapshot("9", testTrack("1", "旧歌", "甲")), true); err != nil {
		t.Fatal(err)
	}
	detailCalls := 0
	p := neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.Contains(r.URL.Path, "account/get"):
			body = `{"code":200,"account":{"id":9}}`
		case strings.Contains(r.URL.Path, "user/playlist"):
			body = `{"code":200,"more":false,"playlist":[{"id":50,"specialType":5,"userId":9}]}`
		case strings.Contains(r.URL.Path, "playlist/detail"):
			body = `{"code":200,"playlist":{"trackCount":2,"trackIds":[{"id":1},{"id":2}]}}`
		case strings.Contains(r.URL.Path, "song/detail"):
			detailCalls++
			body = `{"code":200,"songs":[{"id":2,"name":"新歌","dt":180000,"ar":[{"name":"乙"}],"al":{"name":"专"}}]}`
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			return nil, errors.New("unexpected")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	svc := Service{Store: store, Provider: p, Enabled: true}
	fresh, err := svc.Sync(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if detailCalls != 2 || len(fresh) != 1 || fresh[0].Track.Title != "新歌" {
		t.Fatalf("detail calls=%d fresh=%+v", detailCalls, fresh)
	}
	kept, err := store.ActiveFavoriteTracks(ctx, 1, "netease", "9")
	if err != nil {
		t.Fatal(err)
	}
	if kept["1"].Title != "旧歌" || kept["2"].Title != "新歌" {
		t.Fatalf("stored %#v", kept)
	}
}

func TestNeteaseRefreshUpdatesAStoredTitle(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, _, err := store.Apply(ctx, 1, 1, snapshot("9", testTrack("1", "旧名", "甲")), true); err != nil {
		t.Fatal(err)
	}
	detailCalls := 0
	p := neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.Contains(r.URL.Path, "account/get"):
			body = `{"code":200,"account":{"id":9}}`
		case strings.Contains(r.URL.Path, "user/playlist"):
			body = `{"code":200,"more":false,"playlist":[{"id":50,"specialType":5,"userId":9}]}`
		case strings.Contains(r.URL.Path, "playlist/detail"):
			body = `{"code":200,"playlist":{"trackCount":1,"trackIds":[{"id":1}]}}`
		case strings.Contains(r.URL.Path, "song/detail"):
			detailCalls++
			body = `{"code":200,"songs":[{"id":1,"name":"新名","dt":180000,"ar":[{"name":"甲"}],"al":{"name":"专"}}]}`
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			return nil, errors.New("unexpected")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	svc := Service{Store: store, Provider: p, Enabled: true}
	fresh, err := svc.Sync(ctx, 1)
	if err != nil || len(fresh) != 0 || detailCalls != 1 {
		t.Fatalf("detail calls=%d fresh=%+v err=%v", detailCalls, fresh, err)
	}
	kept, err := store.ActiveFavoriteTracks(ctx, 1, "netease", "9")
	if err != nil {
		t.Fatal(err)
	}
	if kept["1"].Title != "新名" {
		t.Fatalf("stored title = %q", kept["1"].Title)
	}
}

func TestNeteaseRefreshFailureKeepsTheStoredTitle(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, _, err := store.Apply(ctx, 1, 1, snapshot("9", testTrack("1", "旧名", "甲")), true); err != nil {
		t.Fatal(err)
	}
	detailCalls := 0
	p := neteaseProvider(t, func(r *http.Request) (*http.Response, error) {
		var body string
		status := 200
		switch {
		case strings.Contains(r.URL.Path, "account/get"):
			body = `{"code":200,"account":{"id":9}}`
		case strings.Contains(r.URL.Path, "user/playlist"):
			body = `{"code":200,"more":false,"playlist":[{"id":50,"specialType":5,"userId":9}]}`
		case strings.Contains(r.URL.Path, "playlist/detail"):
			body = `{"code":200,"playlist":{"trackCount":1,"trackIds":[{"id":1}]}}`
		case strings.Contains(r.URL.Path, "song/detail"):
			detailCalls++
			status = 400
			body = "down"
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			return nil, errors.New("unexpected")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	svc := Service{Store: store, Provider: p, Enabled: true}
	fresh, err := svc.Sync(ctx, 1)
	if err != nil || len(fresh) != 0 || detailCalls != 1 {
		t.Fatalf("detail calls=%d fresh=%+v err=%v", detailCalls, fresh, err)
	}
	kept, err := store.ActiveFavoriteTracks(ctx, 1, "netease", "9")
	if err != nil {
		t.Fatal(err)
	}
	if kept["1"].Title != "旧名" {
		t.Fatalf("stored title = %q", kept["1"].Title)
	}
}
