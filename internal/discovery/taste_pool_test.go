package discovery

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPooledNeteaseRecommendationDoesNotSearchLive(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	favorite := testTrack("1", "旧收藏", "歌手甲")
	if _, _, err := store.Apply(ctx, 7, 7, snapshot("netease-account", favorite), true); err != nil {
		t.Fatal(err)
	}
	ready := tasteTestTrack("netease", "186016", "新歌", "歌手甲")
	if err := store.SaveTastePool(ctx, 7, []Track{ready}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: store, Library: stubLibrary{}, SlowNetease: true,
		Taste: &TasteSources{SearchPage: func(context.Context, string, string, int) ([]Track, error) {
			t.Fatal("clicking recommend searched NetEase")
			return nil, nil
		}},
	}
	got, err := svc.RecommendTaste(ctx, 7, TasteRequest{Platform: "netease"})
	if err != nil || got.Track == nil || got.Track.ID != "186016" || got.Reason != "来自你常听或收藏的艺人" {
		t.Fatalf("pooled recommendation = %+v, %v", got, err)
	}
	if _, ok, err := store.TakeTasteCandidate(ctx, 7, "netease"); err != nil || ok {
		t.Fatalf("pooled song was not consumed: ok=%v err=%v", ok, err)
	}
}

func TestTasteRefillSearchesOneArtistThenWaits(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	favorite := testTrack("1", "旧收藏", "歌手甲")
	if _, _, err := store.Apply(ctx, 7, 7, snapshot("netease-account", favorite), true); err != nil {
		t.Fatal(err)
	}
	var calls int
	svc := &Service{Store: store, SlowNetease: true, Taste: &TasteSources{SearchPage: func(_ context.Context, platform, artist string, _ int) ([]Track, error) {
		calls++
		if platform != "netease" || artist != "歌手甲" {
			t.Fatalf("search %s %s", platform, artist)
		}
		return []Track{
			tasteTestTrack("netease", "1", "旧收藏", artist),
			tasteTestTrack("netease", "186016", "新歌", artist),
		}, nil
	}}}
	if wait := svc.refillTaste(ctx); wait != tasteSearchGap || calls != 1 {
		t.Fatalf("first refill wait=%s calls=%d", wait, calls)
	}
	count, err := store.CountTastePool(ctx, 7, "netease")
	if err != nil || count != 1 {
		t.Fatalf("pool count=%d err=%v", count, err)
	}
	if wait := svc.refillTaste(ctx); wait < time.Minute || calls != 1 {
		t.Fatalf("second refill searched again: wait=%s calls=%d", wait, calls)
	}
}

func TestTasteRefillBacksOffWhenNeteaseLimitsSearch(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	favorite := testTrack("1", "旧收藏", "歌手甲")
	if _, _, err := store.Apply(ctx, 7, 7, snapshot("netease-account", favorite), true); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: store, Library: stubLibrary{}, SlowNetease: true, Taste: &TasteSources{SearchPage: func(context.Context, string, string, int) ([]Track, error) {
		return nil, errors.New("网易云搜索暂时受限，请稍后重试")
	}}}
	wait := svc.refillTaste(ctx)
	if wait < tasteLimitFloor {
		t.Fatalf("backoff = %s", wait)
	}
	count, err := store.CountTastePool(ctx, 7, "netease")
	if err != nil || count != 0 {
		t.Fatalf("limited search stored songs: %d %v", count, err)
	}
	got, err := svc.RecommendTaste(ctx, 7, TasteRequest{Platform: "netease"})
	if err != nil || got.Track != nil || !strings.Contains(got.Notice, "操作太频繁") {
		t.Fatalf("empty pooled recommendation = %+v, %v", got, err)
	}
}

func TestEmptyPoolUsesDailyRecommendationOnce(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	favorite := testTrack("1", "旧收藏", "歌手甲")
	if _, _, err := store.Apply(ctx, 7, 7, snapshot("netease-account", favorite), true); err != nil {
		t.Fatal(err)
	}
	var calls int
	daily := tasteTestTrack("netease", "99", "日推歌", "别人")
	svc := &Service{Store: store, Library: stubLibrary{}, SlowNetease: true, Taste: &TasteSources{
		SearchPage: func(context.Context, string, string, int) ([]Track, error) {
			t.Fatal("empty pool searched by keyword")
			return nil, nil
		},
		Daily: func(context.Context) ([]Track, error) {
			calls++
			return []Track{favorite, daily}, nil
		},
	}}
	first, err := svc.RecommendTaste(ctx, 7, TasteRequest{Platform: "netease"})
	if err != nil || first.Track == nil || first.Track.ID != "99" || !strings.Contains(first.Reason, "每日推荐") {
		t.Fatalf("first daily = %+v, %v", first, err)
	}
	if err = store.Preference(ctx, 7, *first.Track, false); err != nil {
		t.Fatal(err)
	}
	second, err := svc.RecommendTaste(ctx, 7, TasteRequest{Platform: "netease"})
	if err != nil || second.Track != nil || calls != 1 {
		t.Fatalf("second daily = %+v calls=%d err=%v", second, calls, err)
	}
}

func TestClickingRecommendNeverBurstsNetease(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	favorite := testTrack("1", "旧收藏", "歌手甲")
	if _, _, err := store.Apply(ctx, 7, 7, snapshot("netease-account", favorite), true); err != nil {
		t.Fatal(err)
	}
	var calls int
	svc := &Service{Store: store, Library: stubLibrary{}, SlowNetease: true, Taste: &TasteSources{SearchPage: func(_ context.Context, platform, artist string, _ int) ([]Track, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("网易云搜索暂时受限，请稍后重试")
		}
		return []Track{
			tasteTestTrack(platform, "11", "第一首", artist),
			tasteTestTrack(platform, "12", "第二首", artist),
			tasteTestTrack(platform, "13", "第三首", artist),
		}, nil
	}}}
	for range 8 {
		if _, err := svc.RecommendTaste(ctx, 7, TasteRequest{Platform: "netease"}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Fatalf("clicks searched NetEase %d times before any refill", calls)
	}
	if wait := svc.refillTaste(ctx); wait != tasteSearchGap || calls != 1 {
		t.Fatalf("refill wait=%s calls=%d", wait, calls)
	}
	var served []string
	for range 8 {
		got, err := svc.RecommendTaste(ctx, 7, TasteRequest{Platform: "netease"})
		if err != nil {
			t.Fatal(err)
		}
		if got.Track != nil {
			served = append(served, got.Track.ID)
			if strings.Contains(got.Reason, "后台") || !strings.Contains(got.Reason, "常听或收藏") {
				t.Fatalf("pooled reason = %q", got.Reason)
			}
		}
	}
	slices.Sort(served)
	if strings.Join(served, ",") != "11,12,13" || calls != 1 {
		t.Fatalf("served=%v calls=%d", served, calls)
	}
	if _, err := store.db.Exec(`UPDATE discovery_search_gate SET next_unix=? WHERE platform='netease'`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	wait := svc.refillTaste(ctx)
	if calls != 2 || wait < tasteLimitFloor {
		t.Fatalf("limited refill calls=%d wait=%s", calls, wait)
	}
	if again := svc.refillTaste(ctx); again < time.Minute || calls != 2 {
		t.Fatalf("cooldown did not hold: wait=%s calls=%d", again, calls)
	}
}

func TestQQTastePoolKeepsFiltersAndRechecksAvailability(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	favorite := testTrack("old", "旧收藏", "艺人")
	if _, _, err := store.Apply(ctx, 7, 7, snapshot("account", favorite), true); err != nil {
		t.Fatal(err)
	}
	var calls int
	owned := map[string]bool{}
	tracks := []Track{
		tasteTestTrack("qq", "1", "爵士一", favorite.Artist),
		tasteTestTrack("qq", "2", "爵士二", favorite.Artist),
		tasteTestTrack("qq", "3", "爵士三", favorite.Artist),
		tasteTestTrack("qq", "4", "爵士四", favorite.Artist),
		tasteTestTrack("qq", "5", "摇滚一", favorite.Artist),
	}
	svc := Service{Store: store, Library: stubLibrary{owned: owned}, Taste: &TasteSources{
		SearchPage: func(_ context.Context, platform, _ string, _ int) ([]Track, error) {
			if platform != "qq" {
				t.Fatalf("unexpected platform %s", platform)
			}
			calls++
			return tracks, nil
		},
		TrackTags: func(_ context.Context, track Track) ([]string, error) {
			if track.ID == "5" {
				return []string{"rock"}, nil
			}
			return []string{"jazz", "cantonese"}, nil
		},
	}}
	jazz := TasteRequest{Platform: "qq", Filter: TasteFilter{Language: "粤语", Genre: "爵士"}}
	first, err := svc.RecommendTaste(ctx, 7, jazz)
	if err != nil || first.Track == nil || first.Track.ID != "1" || calls != 1 {
		t.Fatalf("first result = %+v err=%v calls=%d", first, err, calls)
	}
	if err := store.Preference(ctx, 7, *first.Track, false); err != nil {
		t.Fatal(err)
	}
	owned[Identity(tracks[1])] = true
	if _, err := store.Enqueue(ctx, 7, 7, tracks[2]); err != nil {
		t.Fatal(err)
	}
	rock := TasteRequest{Platform: "qq", Filter: TasteFilter{Genre: "摇滚"}}
	rockResult, err := svc.RecommendTaste(ctx, 7, rock)
	if err != nil || rockResult.Track == nil || rockResult.Track.ID != "5" || calls != 2 {
		t.Fatalf("rock result = %+v err=%v calls=%d", rockResult, err, calls)
	}
	next, err := svc.RecommendTaste(ctx, 7, jazz)
	if err != nil || next.Track == nil || next.Track.ID != "4" || calls != 2 || !strings.Contains(next.Reason, "粤语 · 爵士") {
		t.Fatalf("next buffered result = %+v err=%v calls=%d", next, err, calls)
	}
	if err := store.Preference(ctx, 7, *next.Track, false); err != nil {
		t.Fatal(err)
	}
	empty, err := svc.RecommendTaste(ctx, 7, jazz)
	if err != nil || empty.Track != nil || calls != 3 {
		t.Fatalf("pool miss = %+v err=%v calls=%d", empty, err, calls)
	}
}

func TestQQTasteRefillAddsNewSongsForTheSameFilter(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	favorite := testTrack("old", "旧收藏", "艺人")
	if _, _, err := store.Apply(ctx, 7, 7, snapshot("account", favorite), true); err != nil {
		t.Fatal(err)
	}
	firstTrack := tasteTestTrack("qq", "1", "第一首", favorite.Artist)
	secondTrack := tasteTestTrack("qq", "2", "第二首", favorite.Artist)
	searches := 0
	svc := Service{Store: store, Library: stubLibrary{}, Taste: &TasteSources{
		SearchPage: func(_ context.Context, _ string, _ string, _ int) ([]Track, error) {
			searches++
			if searches == 1 {
				return []Track{firstTrack}, nil
			}
			return []Track{firstTrack, secondTrack}, nil
		},
		TrackTags: func(context.Context, Track) ([]string, error) { return []string{"ambient"}, nil },
	}}
	req := TasteRequest{Platform: "qq", Filter: TasteFilter{Scene: "睡前"}}
	first, err := svc.RecommendTaste(ctx, 7, req)
	if err != nil || first.Track == nil || first.Track.ID != firstTrack.ID {
		t.Fatalf("first result = %+v err=%v", first, err)
	}

	svc.WarmQQTaste(7, req, *first.Track)
	svc.WarmQQTaste(7, req, *first.Track)
	if queued := len(svc.qqTastePool.refills); queued != 1 || searches != 1 {
		t.Fatalf("queued=%d searches=%d", queued, searches)
	}
	svc.refillQQTaste(ctx, <-svc.qqTastePool.refills)
	svc.WarmQQTaste(7, req, *first.Track)
	if len(svc.qqTastePool.refills) != 0 {
		t.Fatal("refill cooldown did not hold")
	}
	next, err := svc.RecommendTaste(ctx, 7, req)
	if err != nil || next.Track == nil || next.Track.ID != secondTrack.ID || searches != 2 {
		t.Fatalf("refilled result = %+v err=%v searches=%d", next, err, searches)
	}
}
