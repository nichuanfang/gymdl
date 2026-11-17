package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "discovery.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testTrack(id, title, artist string) Track {
	return Track{Platform: "netease", ID: id, URL: "https://music.163.com/song?id=" + id, Title: title, Artist: artist, Verified: true}
}

func snapshot(account string, tracks ...Track) Snapshot {
	return Snapshot{Platform: "netease", Account: account, Tracks: tracks, Complete: true}
}

func TestSnapshotBaselineIncrementalRebindAndIncomplete(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := testTrack("1", "歌一", "艺人")
	b := testTrack("2", "歌二", "艺人")
	if n, err := s.Apply(ctx, 1, 1, snapshot("first", a), true); err != nil || n != 0 {
		t.Fatalf("baseline n=%d err=%v", n, err)
	}
	if jobs, _ := s.Jobs(ctx, 1, false); len(jobs) != 0 {
		t.Fatal("baseline downloaded backlog")
	}
	incomplete := snapshot("first", a, b)
	incomplete.Complete = false
	if _, err := s.Apply(ctx, 1, 1, incomplete, false); err == nil {
		t.Fatal("partial snapshot accepted")
	}
	if tracks, _ := s.Favorites(ctx, 1); len(tracks) != 1 {
		t.Fatal("partial snapshot advanced baseline")
	}
	if n, err := s.Apply(ctx, 1, 1, snapshot("first", a, b), false); err != nil || n != 1 {
		t.Fatalf("incremental n=%d err=%v", n, err)
	}
	if n, err := s.Apply(ctx, 1, 1, snapshot("first", a, b), false); err != nil || n != 0 {
		t.Fatal("repeat created jobs", n, err)
	}
	if _, err := s.Apply(ctx, 1, 1, snapshot("second", a), false); err == nil {
		t.Fatal("account switch accepted silently")
	}
	if _, err := s.Apply(ctx, 1, 1, snapshot("second", a), true); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := s.Jobs(ctx, 1, false); len(jobs) != 1 {
		t.Fatal("rebind queued backlog")
	}
	if _, err := s.Claim(ctx, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("old account job ran after rebind", err)
	}
	if tracks, _ := s.Favorites(ctx, 1); len(tracks) != 1 {
		t.Fatal("old account favourites leaked after rebind")
	}
}

func TestQueueSurvivesRestartPauseAndExplicitRetry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := testTrack("1", "一", "A"), testTrack("2", "二", "B")
	s.Apply(ctx, 1, 1, snapshot("account", a), true)
	s.Apply(ctx, 1, 1, snapshot("account", a, b), false)
	s.Pause(ctx, 1, "netease", true)
	if _, err = s.Claim(ctx, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("paused watch dispatched", err)
	}
	s.Pause(ctx, 1, "netease", false)
	if _, err = s.Claim(ctx, false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("global disable dispatched auto work")
	}
	job, err := s.Claim(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.Claim(ctx, true)
	if err != nil || recovered.ID != job.ID {
		t.Fatal("lost running task", err)
	}
	if err = s.Finish(ctx, recovered, Failed, "temporary", "", 0); err != nil {
		t.Fatal(err)
	}
	if err = s.Retry(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, false); err != nil {
		t.Fatal("explicit retry blocked by global watch setting", err)
	}
}

func TestCardsScopedExpiredAndSingleUse(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c, err := s.SaveCard(ctx, Card{User: 1, Chat: 10, Kind: "binding", Snapshot: &Snapshot{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Card(ctx, c.Token, 2, 10); err == nil {
		t.Fatal("cross-user access")
	}
	if _, err = s.Card(ctx, c.Token, 1, 11); err == nil {
		t.Fatal("cross-chat access")
	}
	if _, err = s.TakeCard(ctx, c.Token, 1, 10); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TakeCard(ctx, c.Token, 1, 10); err == nil {
		t.Fatal("baseline button reusable")
	}
	c, _ = s.SaveCard(ctx, Card{User: 1, Chat: 10})
	s.db.Exec(`UPDATE discovery_cards SET expires=0 WHERE token=?`, c.Token)
	if _, err = s.Card(ctx, c.Token, 1, 10); err == nil {
		t.Fatal("expired card accepted")
	}
}

func TestMatchingKeepsEditionAndDuration(t *testing.T) {
	a := testTrack("1", "Song", "Artist")
	a.Duration = 200
	for _, tc := range []struct {
		title, artist string
		duration      int
		want          bool
	}{{"Song", "Artist", 201, true}, {"Song (Live)", "Artist", 200, false}, {"Song Remix", "Artist", 200, false}, {"Song", "Cover Artist", 200, false}, {"Song", "Artist", 220, false}} {
		b := testTrack("2", tc.title, tc.artist)
		b.Duration = tc.duration
		if got := SameRecording(a, b); got != tc.want {
			t.Errorf("%+v got=%v", tc, got)
		}
	}
}

type stubLibrary struct {
	owned map[string]bool
	err   error
}

func (l stubLibrary) Contains(_ context.Context, t Track) (bool, error) {
	return l.owned[Identity(t)], l.err
}

type stubProvider struct {
	snap       Snapshot
	fetchErr   error
	alternate  Track
	resolveErr error
	enrich     func(Track) (Track, bool, error)
}

func (p stubProvider) Fetch(context.Context, string) (Snapshot, error) { return p.snap, p.fetchErr }
func (p stubProvider) Resolve(_ context.Context, t Track, exclude string) (Track, error) {
	if p.resolveErr != nil {
		return Track{}, p.resolveErr
	}
	if exclude != "" {
		return p.alternate, nil
	}
	return t, nil
}

func (p stubProvider) Enrich(_ context.Context, t Track) (Track, bool, error) {
	if p.enrich != nil {
		return p.enrich(t)
	}
	return t, false, nil
}

func TestRecommendRanksEvidenceAndExcludesOwnedShownDismissed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var tracks []Track
	for i := 0; i < 16; i++ {
		tr := testTrack(fmt.Sprint(i), fmt.Sprint("Song", i), fmt.Sprint("Artist", i/4))
		tr.Plays = 100 - i
		tr.LastPlayed = time.Now().AddDate(-2, 0, i)
		tracks = append(tracks, tr)
	}
	favorite := testTrack("50", "Favorite", "Other")
	s.Apply(ctx, 1, 1, snapshot("account", favorite), true)
	s.Preference(ctx, 1, tracks[0], false)
	s.Preference(ctx, 1, tracks[1], true)
	svc := Service{Store: s, Provider: stubProvider{}, Library: stubLibrary{owned: map[string]bool{Identity(tracks[2]): true}}, History: func(context.Context) ([]Track, error) { return tracks, nil }}
	out, err := svc.Recommend(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 8 || out[0].Title != "Favorite" {
		t.Fatalf("unexpected recommendations %d %+v", len(out), out)
	}
	counts := map[string]int{}
	for _, tr := range out {
		counts[tr.Artist]++
		if counts[tr.Artist] > 2 {
			t.Fatal("artist cap")
		}
		if tr.ID == "0" || tr.ID == "1" || tr.ID == "2" {
			t.Fatal("excluded track returned")
		}
	}
	svc.Library = stubLibrary{err: errors.New("offline")}
	if _, err = svc.Recommend(ctx, 1); err == nil {
		t.Fatal("offline library treated as empty")
	}
}

func TestWorkerFallbackConfirmationRetriesAndDeferredUpload(t *testing.T) {
	for _, mode := range []string{"fallback", "ambiguous", "unknown", "nonmusic", "pending", "retry"} {
		t.Run(mode, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			tr := testTrack("1", "Song", "Artist")
			if mode == "unknown" {
				tr.Verified = false
			}
			s.Enqueue(ctx, 1, 1, tr)
			calls := 0
			provider := stubProvider{alternate: Track{Platform: "qq", ID: "q", Title: tr.Title, Artist: tr.Artist, Verified: true}}
			if mode == "ambiguous" {
				provider.resolveErr = ErrAmbiguousRecording
			}
			if mode == "nonmusic" {
				provider.enrich = func(t Track) (Track, bool, error) { return t, true, nil }
			}
			svc := Service{Store: s, Provider: provider, Library: stubLibrary{}, Download: func(_ context.Context, t Track) (DownloadResult, error) {
				calls++
				if mode == "retry" || mode == "ambiguous" || (mode == "fallback" && t.Platform == "netease") {
					return DownloadResult{}, errors.New("provider offline")
				}
				return DownloadResult{Pending: mode == "pending"}, nil
			}}
			if err := svc.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			jobs, _ := s.Jobs(ctx, 1, false)
			j := jobs[0]
			want := Completed
			switch mode {
			case "ambiguous", "unknown":
				want = NeedsConfirmation
			case "nonmusic":
				want = Ignored
			case "pending":
				want = PendingUpload
			case "retry":
				want = Queued
			}
			if j.State != want {
				t.Fatalf("state=%s want %s", j.State, want)
			}
			if mode == "fallback" && calls != 2 {
				t.Fatal("did not try alternate")
			}
			if mode == "unknown" {
				if calls != 0 {
					t.Fatal("unknown video auto-downloaded")
				}
				s.ResolveJob(ctx, j.ID, 1, true)
				if err := svc.ProcessOne(ctx); err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatal("explicit confirmation did not run")
				}
			}
			if mode == "pending" {
				svc.Library = stubLibrary{owned: map[string]bool{Identity(tr): true}}
				if err := svc.Reconcile(ctx, 1); err != nil {
					t.Fatal(err)
				}
				jobs, _ = s.Jobs(ctx, 1, false)
				if jobs[0].State != Completed {
					t.Fatal("pending upload never reconciled")
				}
			}
			if mode == "retry" {
				for i := 0; i < 3; i++ {
					s.db.Exec(`UPDATE discovery_jobs SET next_run=0`)
					if err := svc.ProcessOne(ctx); err != nil {
						t.Fatal(err)
					}
				}
				jobs, _ = s.Jobs(ctx, 1, false)
				if jobs[0].State != Failed || jobs[0].Attempts != 4 {
					t.Fatal("retry limit not enforced", jobs[0])
				}
			}
		})
	}
}

func TestEnrichedVideoMergesCrossPlatformAndDigestDelivery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	canonical := testTrack("1", "Song", "Artist")
	s.Enqueue(ctx, 1, 1, canonical)
	s.Enqueue(ctx, 1, 1, Track{Platform: "youtube", ID: "abc", Title: "Video", URL: "https://youtube.com/watch?v=abc"})
	downloads := 0
	svc := Service{Store: s, Provider: stubProvider{enrich: func(t Track) (Track, bool, error) {
		if t.Platform == "youtube" {
			t.Title = canonical.Title
			t.Artist = canonical.Artist
			t.Verified = true
		}
		return t, false, nil
	}}, Library: stubLibrary{}, Download: func(context.Context, Track) (DownloadResult, error) { downloads++; return DownloadResult{}, nil }}
	svc.ProcessOne(ctx)
	svc.ProcessOne(ctx)
	if downloads != 1 {
		t.Fatal("cross-platform duplicate downloaded twice", downloads)
	}
	svc.Notify = func(int64, string) error { return errors.New("telegram down") }
	svc.Digest(ctx)
	jobs, _ := s.Jobs(ctx, 1, true)
	if len(jobs) != 2 {
		t.Fatal("failed delivery acknowledged")
	}
	svc.Notify = func(_ int64, msg string) error {
		if !strings.Contains(msg, "已入库") {
			t.Fatal(msg)
		}
		return nil
	}
	if err := svc.Digest(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, _ = s.Jobs(ctx, 1, true)
	if len(jobs) != 0 {
		t.Fatal("digest did not acknowledge")
	}
}

func TestNavidromeInventoryAndFailures(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/rest/search3.view" || r.URL.Query().Get("t") == "" {
			t.Error("wrong inventory request")
		}
		fmt.Fprint(w, `{"subsonic-response":{"status":"ok","searchResult3":{"song":[{"id":"owned","title":"Song","artist":"Artist","album":"Album","duration":200}]}}}`)
	}))
	defer server.Close()
	l := &NavidromeLibrary{Config: &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "u", NavidromePassword: "p"}}
	tr := testTrack("1", "Song", "Artist")
	owned, err := l.Contains(context.Background(), tr)
	if err != nil || !owned {
		t.Fatal(owned, err)
	}
	l.Contains(context.Background(), tr)
	if calls != 1 {
		t.Fatal("inventory not cached")
	}
	server.Close()
	l.Invalidate()
	if _, err = l.Contains(context.Background(), tr); err == nil {
		t.Fatal("failed refresh treated as missing")
	}
}

func TestTraditionalMetadataAndExplicitYoutubeMusicMode(t *testing.T) {
	if !SameRecording(testTrack("1", "愛情轉移", "陳奕迅"), testTrack("2", "爱情转移", "陈奕迅")) {
		t.Fatal("traditional source failed normalized metadata match")
	}
	tr := Track{Platform: "youtube", ID: "dQw4w9WgXcQ", URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"}
	if musicSourceURL(tr) != "https://music.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Fatal("explicit music import depends on general video mode")
	}
}

func TestDigestDoesNotAcknowledgeNewStateAtSameTimestamp(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	tr := testTrack("1", "Song", "Artist")
	s.Enqueue(ctx, 1, 1, tr)
	j, err := s.Claim(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	s.Finish(ctx, j, PendingUpload, "", "", 0)
	svc := Service{Store: s, Library: stubLibrary{}, Notify: func(int64, string) error {
		_, err := s.db.Exec(`UPDATE discovery_jobs SET state='completed' WHERE id=?`, j.ID)
		return err
	}}
	if err := svc.Digest(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Jobs(ctx, 1, true)
	if err != nil || len(jobs) != 1 || jobs[0].State != Completed {
		t.Fatal("notification raced with state transition", jobs, err)
	}
}
func TestWorkerProcessorPanicBecomesVisibleFailure(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	s.Enqueue(ctx, 1, 1, testTrack("1", "Song", "Artist"))
	svc := Service{Store: s, Provider: stubProvider{}, Library: stubLibrary{}, Download: func(context.Context, Track) (DownloadResult, error) { panic("processor failed") }}
	if err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.Jobs(ctx, 1, false)
	if len(jobs) != 1 || jobs[0].State != Failed || jobs[0].Error == "" {
		t.Fatal("panic left a stuck running task")
	}
}

func TestAlbumOnlyEditionDoesNotCollapseIntoStudioRecording(t *testing.T) {
	studio := testTrack("1", "Song", "Artist")
	live := testTrack("2", "Song", "Artist")
	live.Album = "Concert (Live)"
	if Identity(studio) == Identity(live) || SameRecording(studio, live) {
		t.Fatal("album-only live edition was treated as studio")
	}
	copy := live
	copy.Platform = "qq"
	copy.ID = "other"
	if !SameRecording(live, copy) {
		t.Fatal("same live edition did not match")
	}
	compilation := studio
	compilation.Album = "Greatest Hits"
	if !SameRecording(studio, compilation) {
		t.Fatal("ordinary compilation was treated as a different edition")
	}
}
