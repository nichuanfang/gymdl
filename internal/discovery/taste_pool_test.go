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
	if err != nil || got.Track == nil || got.Track.ID != "186016" {
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
