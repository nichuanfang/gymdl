package discovery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

func TestRediscoverHydratesSelectedQQCover(t *testing.T) {
	const mid = "0029Odqi2cHY2K"
	const cover = "https://y.gtimg.cn/music/photo_new/T002R500x500M000001LGrR936b0WW_1.jpg"
	for _, tc := range []struct {
		name, cover, responseMID, wantCover string
		wantRequests                        int
	}{
		{name: "missing cover", responseMID: mid, wantCover: cover, wantRequests: 1},
		{name: "existing cover", cover: cover, wantCover: cover},
		{name: "unexpected song", responseMID: "different", wantRequests: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			track := tasteTestTrack("qq", mid, "Luv(sic) Pt.6 (Uyama Hiroto Remix)", "Nujabes • Shing02")
			track.CoverURL = tc.cover
			requests := 0
			provider := &Providers{
				Config: &config.Config{QQMusicApiConfig: &config.QQMusicApiConfig{Enable: true, Endpoint: "http://qq.test"}},
				Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					requests++
					if r.Method != http.MethodGet || r.URL.Path != "/song/"+mid+"/detail" || r.Header.Get("Cookie") != "" {
						t.Fatalf("unexpected QQ detail request: %s %s", r.Method, r.URL.Path)
					}
					body := `{"code":0,"data":{"track":{"mid":"` + tc.responseMID + `","album":{"pmid":"001LGrR936b0WW_1"}}}}`
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
				})},
			}
			service := &Service{Store: testStore(t), Provider: provider, Library: stubLibrary{}}
			pool := &service.rediscoverPool
			pool.queueLocked()
			pool.entries[rediscoverPoolKey{user: 7, platform: "qq"}] = &rediscoverPoolEntry{
				candidates: []bufferedRediscoverCandidate{{
					rediscoverCandidate: rediscoverCandidate{source: track, track: track},
					expires:             time.Now().Add(time.Minute),
				}},
			}

			batch, err := service.RediscoverBuffered(t.Context(), 7, "qq", 1)
			track.CoverURL = tc.wantCover
			if err != nil || len(batch.Tracks) != 1 || batch.Tracks[0] != track {
				t.Fatalf("rediscover = %+v, err=%v, want track %+v", batch, err, track)
			}
			if requests != tc.wantRequests {
				t.Fatalf("detail requests = %d, want %d", requests, tc.wantRequests)
			}
		})
	}
}

func TestRediscoverBufferRefillsOffTheRequestAndServesOneWithoutSearching(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	var history []Track
	for i := range rediscoverPoolTarget + 2 {
		track := testTrack(fmt.Sprint(i+1), fmt.Sprintf("Song %d", i), "Same Artist")
		track.Plays = 10 - i
		history = append(history, track)
	}
	var searches atomic.Int32
	var unexpectedPlatform atomic.Bool
	started := make(chan struct{})
	release := make(chan struct{})
	service := &Service{Store: store, Library: stubLibrary{}, Provider: &countingProvider{
		resolveOn: func(track Track, platform string) (Track, error) {
			if platform != "qq" {
				unexpectedPlatform.Store(true)
			}
			if searches.Add(1) == 2 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return Track{}, ctx.Err()
				}
			}
			track.Platform = platform
			track.URL = "https://y.qq.com/n/ryqq/songDetail/" + track.ID
			return track, nil
		},
	}, History: func(context.Context) ([]Track, error) { return history, nil }}
	key := rediscoverPoolKey{user: 1, platform: "qq"}
	first, err := service.RediscoverBuffered(ctx, key.user, key.platform, 1)
	if err != nil || len(first.Tracks) != 1 || first.Tracks[0].ID != "1" || searches.Load() != 1 {
		t.Fatalf("cold lookup = %+v, err=%v searches=%d", first, err, searches.Load())
	}
	if err := store.Preference(ctx, key.user, first.Tracks[0], false); err != nil {
		t.Fatal(err)
	}

	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.runRediscoverRefills(workerCtx)
	}()
	defer func() { stop(); <-done }()
	defer closeUnlessClosed(release)
	service.WarmRediscover(key.user, key.platform, first.Tracks[0])
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refill did not start")
	}
	service.WarmRediscover(key.user, key.platform, first.Tracks[0])
	if len(service.rediscoverPool.requests) != 0 {
		t.Fatal("another refill was queued while one was running")
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for len(service.rediscoverPool.snapshot(key)) < rediscoverPoolTarget {
		if time.Now().After(deadline) {
			t.Fatalf("pool was not filled, searches=%d", searches.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if searches.Load() != 1+rediscoverPoolTarget || unexpectedPlatform.Load() {
		t.Fatalf("refill searches=%d unexpected platform=%t", searches.Load(), unexpectedPlatform.Load())
	}
	if got := service.rediscoverPool.snapshot(rediscoverPoolKey{user: 2, platform: "qq"}); len(got) != 0 {
		t.Fatalf("another user received %d candidates", len(got))
	}
	if got := service.rediscoverPool.snapshot(rediscoverPoolKey{user: 1, platform: "youtube"}); len(got) != 0 {
		t.Fatalf("another platform received %d candidates", len(got))
	}
	queued, err := store.Jobs(ctx, key.user, false)
	if err != nil || len(queued) != 0 {
		t.Fatalf("warming changed the download queue: %+v err=%v", queued, err)
	}

	// Read-only previewing and failing the library check both leave the pool intact.
	preview, err := service.Rediscover(ctx, key.user, key.platform, 1)
	if err != nil || len(preview.Tracks) != 1 || len(service.rediscoverPool.snapshot(key)) != rediscoverPoolTarget {
		t.Fatalf("preview = %+v err=%v", preview, err)
	}
	stop()
	<-done
	service.Library = stubLibrary{err: fmt.Errorf("offline")}
	if _, err := service.RediscoverBuffered(ctx, key.user, key.platform, 1); err == nil || len(service.rediscoverPool.snapshot(key)) != rediscoverPoolTarget {
		t.Fatalf("failed check consumed pool stock: err=%v", err)
	}
	service.Library = stubLibrary{}
	service.History = nil
	for _, want := range []string{"2", "3"} {
		result, err := service.RediscoverBuffered(ctx, key.user, key.platform, 1)
		if err != nil || len(result.Tracks) != 1 || result.Tracks[0].ID != want || searches.Load() != 1+rediscoverPoolTarget {
			t.Fatalf("pool lookup = %+v, err=%v searches=%d, want %s", result, err, searches.Load(), want)
		}
	}
}

func TestRediscoverBufferRechecksCandidatesBeforeShowingThem(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	key := rediscoverPoolKey{user: 1, platform: "netease"}
	owned := testTrack("1", "Owned", "Artist A")
	queued := testTrack("2", "Queued", "Artist B")
	dismissed := testTrack("3", "Dismissed", "Artist C")
	shown := testTrack("4", "Shown", "Artist D")
	unliked := testTrack("5", "Unliked", "Artist E")
	pending := testTrack("6", "Pending", "Artist F")
	expired := testTrack("7", "Expired", "Artist G")
	valid := testTrack("8", "Valid", "Artist H")
	unliked.Favorite = true
	if _, _, err := store.Apply(ctx, key.user, key.user, snapshot("account", unliked), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(ctx, key.user, key.user, snapshot("account"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(ctx, key.user, key.user, queued); err != nil {
		t.Fatal(err)
	}
	jobID, err := store.Enqueue(ctx, key.user, key.user, pending)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, job, PendingUpload, "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Preference(ctx, key.user, dismissed, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Preference(ctx, key.user, shown, false); err != nil {
		t.Fatal(err)
	}

	service := &Service{Store: store, Library: stubLibrary{owned: map[string]bool{Identity(owned): true}}}
	pool := &service.rediscoverPool
	pool.queueLocked()
	entry := &rediscoverPoolEntry{}
	for _, track := range []Track{expired, owned, queued, dismissed, shown, unliked, pending, valid} {
		expires := time.Now().Add(time.Minute)
		if track == expired {
			expires = time.Now().Add(-time.Minute)
		}
		entry.candidates = append(entry.candidates, bufferedRediscoverCandidate{
			rediscoverCandidate: rediscoverCandidate{source: track, track: track}, expires: expires,
		})
	}
	pool.entries[key] = entry
	result, err := service.RediscoverBuffered(ctx, key.user, key.platform, 1)
	if err != nil || len(result.Tracks) != 1 || result.Tracks[0].ID != valid.ID {
		t.Fatalf("buffer produced an ineligible song: %+v err=%v", result, err)
	}
	if result.SkippedOwned != 1 || result.SkippedQueued != 1 || result.SkippedDismissed != 1 || result.SkippedPending != 1 || result.SkippedShown != 1 {
		t.Fatalf("skip counts = %+v", result)
	}
	if len(pool.snapshot(key)) != 0 {
		t.Fatal("ineligible candidates remained in the pool")
	}
}

func closeUnlessClosed(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}
