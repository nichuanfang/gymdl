package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/internal/musicrag"
)

func tasteTestTrack(platform, id, title, artist string) Track {
	track := testTrack(id, title, artist)
	track.Platform = platform
	track.Album = "专辑 " + id
	if platform == "qq" {
		track.URL = "https://y.qq.com/n/ryqq/songDetail/" + id
	}
	return track
}

func TestParseTasteRequest(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  TasteRequest
	}{
		{"", TasteRequest{Platform: TasteBoth}},
		{"粤语爵士", TasteRequest{Platform: TasteBoth, Filter: TasteFilter{Language: "粤语", Genre: "爵士"}}},
		{"qq 放松 纯音乐", TasteRequest{Platform: "qq", Filter: TasteFilter{Scene: "放松", Language: "纯音乐"}}},
		{"netease 粤语流行", TasteRequest{Platform: "netease", Filter: TasteFilter{Language: "粤语", Genre: "流行"}}},
	} {
		got, err := ParseTasteRequest(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("%q got %+v, %v, want %+v", tc.input, got, err, tc.want)
		}
	}
	for _, invalid := range []string{"日语粤语", "youtube", "随便给我编一首"} {
		if _, err := ParseTasteRequest(invalid); err == nil {
			t.Errorf("invalid filter accepted: %q", invalid)
		}
	}
	scene, ok := (TasteFilter{Language: "纯音乐"}).WithScene("sleep")
	if !ok || !scene.matches(tasteTagSet([]string{"ambient", "instrumental"})) || scene.matches(tasteTagSet([]string{"ambient"})) {
		t.Fatalf("scene and language matching = %+v", scene)
	}
	if reset, ok := scene.WithScene("taste"); !ok || reset != (TasteFilter{}) {
		t.Fatalf("taste reset = %+v", reset)
	}
}

func TestRecommendTasteUsesHistoryStyleAndFreshVerifiedSingles(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	history := Track{Title: "已听", Artist: "常听艺人", Plays: 35, LastPlayed: time.Now()}
	favorite := tasteTestTrack("netease", "2", "收藏过", history.Artist)
	if _, _, err := store.Apply(ctx, 1, 1, snapshot("account", favorite), true); err != nil {
		t.Fatal(err)
	}
	owned := tasteTestTrack("netease", "3", "库里有", history.Artist)
	pending := tasteTestTrack("netease", "4", "待上传", history.Artist)
	confirm := tasteTestTrack("netease", "5", "待确认", history.Artist)
	dismissed := tasteTestTrack("netease", "6", "不再推荐", history.Artist)
	seen := tasteTestTrack("netease", "7", "之前推荐过", history.Artist)
	for _, track := range []Track{pending, confirm} {
		id, err := store.Enqueue(ctx, 1, 1, track)
		if err != nil {
			t.Fatal(err)
		}
		job, err := store.Job(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		state := PendingUpload
		if track.ID == confirm.ID {
			state = NeedsConfirmation
		}
		if err := store.Finish(ctx, job, state, "", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Preference(ctx, 1, dismissed, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Preference(ctx, 1, seen, false); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Store: store, Library: stubLibrary{owned: map[string]bool{Identity(owned): true}},
		History: func(context.Context) ([]Track, error) { return []Track{history}, nil },
		Taste: &TasteSources{
			HistoryOwner: 1,
			Similar:      func(context.Context, string) ([]string, error) { return []string{"常听艺人", "相近艺人"}, nil },
			Search: func(_ context.Context, platform, artist string) ([]Track, error) {
				track := func(id, title string) Track { return tasteTestTrack(platform, id, title, artist) }
				if artist == "相近艺人" {
					// Different singer of the same title should survive exclusion.
					return []Track{track("11", "已听")}, nil
				}
				wrong := tasteTestTrack(platform, "12", "不相干", "其他艺人")
				unverified := track("13", "混剪")
				unverified.Verified = false
				invalidURL := track("14", "错误链接")
				invalidURL.URL = "https://example.test/14"
				return []Track{track("1", "已听"), track("2", "收藏过"), track("3", "库里有"), track("4", "待上传"),
					track("5", "待确认"), track("6", "不再推荐"), track("7", "之前推荐过"), wrong, unverified,
					invalidURL, track("8", "流行候选"), track("9", "爵士候选"), track("10", "粤语候选")}, nil
			},
			LibraryTags: func(context.Context) ([]musicrag.Song, error) {
				return []musicrag.Song{{Title: history.Title, Artist: history.Artist, Genre: "爵士", Mood: "柔和", Language: "粤语"}}, nil
			},
			TrackTags: func(_ context.Context, track Track) ([]string, error) {
				switch track.ID {
				case "9", "11":
					return []string{"jazz", "Cantonese", "mellow"}, nil
				case "10":
					return []string{"cantonese"}, nil
				}
				return []string{"pop"}, nil
			},
		},
	}
	req := TasteRequest{Platform: TasteBoth}
	got, err := svc.RecommendTaste(ctx, 1, req)
	if err != nil || got.Track == nil || got.Track.ID != "9" || got.Explore || !strings.Contains(got.Reason, "爵士") {
		t.Fatalf("familiar recommendation: %+v, %v", got, err)
	}
	if err := store.Preference(ctx, 1, *got.Track, false); err != nil {
		t.Fatal(err)
	}
	req.ExploreFirst = true
	req.Filter = TasteFilter{Genre: "爵士", Language: "粤语", Scene: "放松"}
	got, err = svc.RecommendTaste(ctx, 1, req)
	if err != nil || got.Track == nil || got.Track.ID != "11" || got.Track.Platform != "qq" ||
		!got.Explore || !strings.Contains(got.Reason, "与「常听艺人」风格相近") {
		t.Fatalf("explore recommendation: %+v, %v", got, err)
	}
	if err := store.Preference(ctx, 1, *got.Track, false); err != nil {
		t.Fatal(err)
	}
	got, err = svc.RecommendTaste(ctx, 1, req)
	if err != nil || got.Track != nil {
		t.Fatalf("no evidence for remaining candidates or cross-platform repeat: %+v, %v", got, err)
	}
}

func TestRecommendTasteKeepsHealthyPlatformAndIsolatesHistory(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	favorite := testTrack("1", "旧收藏", "歌手乙")
	if _, _, err := store.Apply(ctx, 2, 2, snapshot("qq-account", favorite), true); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: store, Library: stubLibrary{},
		History: func(context.Context) ([]Track, error) { t.Fatal("read another listener's history"); return nil, nil },
		Taste: &TasteSources{HistoryOwner: 1,
			Similar: func(context.Context, string) ([]string, error) { return nil, errors.New("offline") },
			Search: func(_ context.Context, platform, artist string) ([]Track, error) {
				if platform == "netease" {
					return nil, errors.New("service timeout")
				}
				return []Track{tasteTestTrack("qq", "song", "新歌", artist)}, nil
			},
		},
	}
	got, err := svc.RecommendTaste(ctx, 2, TasteRequest{Platform: TasteBoth})
	if err != nil || got.Track == nil || got.Track.Platform != "qq" || !strings.Contains(got.Notice, "网易云") ||
		!strings.Contains(got.Notice, "未核对已听记录") {
		t.Fatalf("partial availability: %+v, %v", got, err)
	}
	svc.Library = stubLibrary{err: errors.New("Navidrome unavailable")}
	if got, err := svc.RecommendTaste(ctx, 2, TasteRequest{Platform: TasteBoth}); err == nil || got.Track != nil {
		t.Fatalf("recommendation succeeded without library verification: %+v, %v", got, err)
	}
	if got, err := svc.RecommendTaste(ctx, 3, TasteRequest{Platform: TasteBoth}); err == nil || got.Track != nil {
		t.Fatalf("claimed recommendations for a user without taste evidence: %+v, %v", got, err)
	}
}

func TestTasteLastFMCachesResponsesAndHidesKeysOnErrors(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		q := r.URL.Query()
		if q.Get("api_key") != "testing-key" || q.Get("artist") != "歌手 & Co." {
			t.Error("incorrect Last.fm request")
		}
		switch q.Get("method") {
		case "artist.getSimilar":
			fmt.Fprint(w, `{"similarartists":{"artist":[{"name":"相似艺人"}]}}`)
		case "track.getTopTags":
			if q.Get("track") == "失败" {
				fmt.Fprint(w, `{"error":29,"message":"testing-key"}`)
				return
			}
			fmt.Fprint(w, `{"toptags":{"tag":[{"name":"jazz","count":95}]}}`)
		}
	}))
	defer server.Close()
	client := tasteLastFM{endpoint: server.URL, client: server.Client(), apiKey: "testing-key", cache: map[string]tasteMetadata{}}
	for range 2 {
		got, err := client.names(t.Context(), "artist.getSimilar", "歌手 & Co.", "")
		if err != nil || len(got) != 1 || got[0] != "相似艺人" {
			t.Fatalf("similar = %v, %v", got, err)
		}
	}
	got, err := client.names(t.Context(), "track.getTopTags", "歌手 & Co.", "新歌")
	if err != nil || len(got) != 1 || got[0] != "jazz" {
		t.Fatalf("tags = %v, %v", got, err)
	}
	if _, err = client.names(t.Context(), "track.getTopTags", "歌手 & Co.", "失败"); err == nil || strings.Contains(err.Error(), "testing-key") {
		t.Fatalf("unsafe Last.fm error: %v", err)
	}
	if requests.Load() != 3 {
		t.Fatalf("metadata request count = %d", requests.Load())
	}
}

func TestRecommendTasteExplainsKnownSongsAndContinuesOnTheNextPage(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	history := Track{Title: "已听", Artist: "常听艺人", Plays: 10, LastPlayed: time.Now()}
	svc := &Service{
		Store: store, Library: stubLibrary{},
		History: func(context.Context) ([]Track, error) { return []Track{history}, nil },
		Taste: &TasteSources{HistoryOwner: 1,
			SearchPage: func(_ context.Context, platform, artist string, offset int) ([]Track, error) {
				if offset == 0 {
					return []Track{tasteTestTrack(platform, "1", "已听", artist)}, nil
				}
				return []Track{tasteTestTrack(platform, "later", "再往后的歌", artist)}, nil
			},
		},
	}
	got, err := svc.RecommendTaste(ctx, 1, TasteRequest{Platform: "netease"})
	if err != nil || got.Track != nil || !strings.Contains(got.Notice, "已经听过或在曲库里") {
		t.Fatalf("first page should explain the miss: %+v, %v", got, err)
	}
	got, err = svc.RecommendTaste(ctx, 1, TasteRequest{Platform: "netease", Page: 1})
	if err != nil || got.Track == nil || got.Track.ID != "later" {
		t.Fatalf("next page = %+v, %v", got, err)
	}
}
