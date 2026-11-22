package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/audiodupe"
	"github.com/studio-b12/gowebdav"
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

func TestConfirmationAtReturnsOneOldestJob(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, _, total, err := s.ConfirmationAt(ctx, 1, 0); err != nil || total != 0 {
		t.Fatalf("empty confirmations = %d %v", total, err)
	}
	if _, err := s.Enqueue(ctx, 1, 1, testTrack("1", "第一首", "甲")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, 1, 1, testTrack("2", "第二首", "乙")); err != nil {
		t.Fatal(err)
	}
	// identity is a hash of title and artist, so match the first song by that hash.
	if _, err := s.db.ExecContext(ctx, `UPDATE discovery_jobs SET state='needs_confirmation', last_error='对不上唯一版本', updated=CASE WHEN identity=? THEN 1 ELSE 2 END`, Identity(testTrack("1", "第一首", "甲"))); err != nil {
		t.Fatal(err)
	}
	first, index, total, err := s.ConfirmationAt(ctx, 1, 0)
	if err != nil || total != 2 || index != 0 || first.Track.Title != "第一首" || first.Error != "对不上唯一版本" {
		t.Fatalf("first = %#v index=%d total=%d err=%v", first.Track, index, total, err)
	}
	second, index, total, err := s.ConfirmationAt(ctx, 1, 1)
	if err != nil || total != 2 || index != 1 || second.Track.Title != "第二首" {
		t.Fatalf("second = %#v index=%d total=%d err=%v", second.Track, index, total, err)
	}
	clamped, index, total, err := s.ConfirmationAt(ctx, 1, 9)
	if err != nil || total != 2 || index != 1 || clamped.Track.Title != "第二首" {
		t.Fatalf("clamp = %#v index=%d total=%d err=%v", clamped.Track, index, total, err)
	}
}

func TestSnapshotBaselineIncrementalRebindAndIncomplete(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := testTrack("1", "歌一", "艺人")
	b := testTrack("2", "歌二", "艺人")
	if n, _, err := s.Apply(ctx, 1, 1, snapshot("first", a), true); err != nil || n != 0 {
		t.Fatalf("baseline n=%d err=%v", n, err)
	}
	if jobs, _ := s.Jobs(ctx, 1, false); len(jobs) != 0 {
		t.Fatal("baseline downloaded backlog")
	}
	incomplete := snapshot("first", a, b)
	incomplete.Complete = false
	if _, _, err := s.Apply(ctx, 1, 1, incomplete, false); err == nil {
		t.Fatal("partial snapshot accepted")
	}
	if tracks, _ := s.Favorites(ctx, 1); len(tracks) != 1 {
		t.Fatal("partial snapshot advanced baseline")
	}
	if n, fresh, err := s.Apply(ctx, 1, 1, snapshot("first", a, b), false); err != nil || n != 1 || len(fresh) != 1 || fresh[0].Title != "歌二" {
		t.Fatalf("incremental n=%d err=%v", n, err)
	}
	if n, fresh, err := s.Apply(ctx, 1, 1, snapshot("first", a, b), false); err != nil || n != 0 || len(fresh) != 0 {
		t.Fatal("repeat created jobs", n, err)
	}
	if _, _, err := s.Apply(ctx, 1, 1, snapshot("second", a), false); err == nil {
		t.Fatal("account switch accepted silently")
	}
	if _, _, err := s.Apply(ctx, 1, 1, snapshot("second", a), true); err != nil {
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

func TestDiscoveryMigrationSeparatesManualAndAutomaticPause(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE discovery_bindings(user_id INTEGER NOT NULL,chat_id INTEGER NOT NULL,platform TEXT NOT NULL,account TEXT NOT NULL,paused INTEGER NOT NULL DEFAULT 0,last_sync INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(user_id,platform))`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO discovery_bindings VALUES(1,1,'netease','a',1,0,''),(1,1,'qq','b',1,0,'old login failure')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bindings, err := store.Bindings(context.Background(), 1)
	if err != nil || len(bindings) != 2 {
		t.Fatal(err, bindings)
	}
	byPlatform := map[string]Binding{}
	for _, binding := range bindings {
		byPlatform[binding.Platform] = binding
	}
	if !byPlatform["netease"].Paused {
		t.Fatalf("manual legacy pause = %+v", byPlatform["netease"])
	}
	if byPlatform["qq"].Paused {
		t.Fatalf("automatic legacy pause = %+v", byPlatform["qq"])
	}
	store.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("second startup migration: %v", err)
	}
	defer again.Close()
	bindings, err = again.Bindings(context.Background(), 1)
	if err != nil || len(bindings) != 2 {
		t.Fatalf("repeat migration changed bindings: %+v err=%v", bindings, err)
	}
	for _, binding := range bindings {
		if binding.Platform == "netease" && !binding.Paused {
			t.Fatalf("repeat migration lost manual pause: %+v", binding)
		}
		if binding.Platform == "qq" && binding.Paused {
			t.Fatalf("repeat migration revived automatic pause: %+v", binding)
		}
	}
}

func TestRebindDoesNotDeactivateThePreviousAccount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first := testTrack("1", "旧账号歌", "甲")
	second := testTrack("2", "新账号歌", "乙")
	if _, _, err := s.Apply(ctx, 1, 1, snapshot("first", first), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Apply(ctx, 1, 1, snapshot("second", second), true); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT account,track_id,active FROM discovery_favorites WHERE user_id=1 ORDER BY account,track_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var account, id string
		var active bool
		if err = rows.Scan(&account, &id, &active); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s:%s:%t", account, id, active))
	}
	if strings.Join(got, ",") != "first:1:true,second:2:true" {
		t.Fatalf("rebind changed the old account baseline: %v", got)
	}
}

func TestSyncAnnouncesOnlyNewFavorites(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	oldSong := testTrack("1", "旧歌", "甲")
	newSong := testTrack("2", "新歌", "乙")
	if _, _, err := s.Apply(ctx, 1, 9, snapshot("acc", oldSong), true); err != nil {
		t.Fatal(err)
	}
	var notes []string
	svc := Service{
		Store:      s,
		Enabled:    true,
		Provider:   stubProvider{snap: snapshot("acc", oldSong, newSong)},
		Authorized: func(user, chat int64) bool { return user == 1 && chat == 9 },
		Notify: func(chat int64, msg string) error {
			if chat != 9 {
				t.Fatalf("notice chat = %d", chat)
			}
			notes = append(notes, msg)
			return nil
		},
	}
	fresh, err := svc.Sync(ctx, 1)
	if err != nil || len(fresh) != 1 || fresh[0].Track.Title != "新歌" || fresh[0].Chat != 9 {
		t.Fatalf("fresh = %#v err=%v", fresh, err)
	}
	svc.notifyFresh(fresh)
	if len(notes) != 1 || !strings.Contains(notes[0], "收藏有新歌") || !strings.Contains(notes[0], "网易云 1 首") || !strings.Contains(notes[0], "新歌") || strings.Contains(notes[0], "旧歌") || !strings.Contains(notes[0], "已排队入库") {
		t.Fatalf("notice = %#v", notes)
	}
	fresh, err = svc.Sync(ctx, 1)
	if err != nil || len(fresh) != 0 {
		t.Fatalf("repeat fresh = %#v err=%v", fresh, err)
	}
	svc.notifyFresh(fresh)
	if len(notes) != 1 {
		t.Fatalf("empty sync sent another notice: %#v", notes)
	}
}

func TestRunKeepsDownloadingWhileFavoriteReadIsSlow(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, _, err := store.Apply(ctx, 1, 9, snapshot("acc", testTrack("old", "旧歌", "甲")), true); err != nil {
		t.Fatal(err)
	}
	song := testTrack("1", "新歌", "甲")
	song.Verified = false
	song.Approved = true
	if _, err := store.Enqueue(ctx, 1, 9, song); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	var fetches atomic.Int32
	var opened atomic.Bool
	svc := &Service{
		Store:           store,
		Enabled:         true,
		PollInterval:    20 * time.Millisecond,
		SummaryInterval: time.Hour,
		Library:         stubLibrary{},
		Provider: gatingProvider{fetch: func(ctx context.Context, _ string) (Snapshot, error) {
			if fetches.Add(1) == 1 && opened.CompareAndSwap(false, true) {
				close(started)
			}
			<-ctx.Done()
			return Snapshot{}, ctx.Err()
		}},
		Download: func(context.Context, Track) (DownloadResult, error) {
			return DownloadResult{Detail: "ok"}, nil
		},
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.Run(runCtx) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("favorite read did not start")
	}
	time.Sleep(150 * time.Millisecond)
	if n := fetches.Load(); n != 1 {
		t.Fatalf("overlapping favorite reads = %d", n)
	}
	downloaded := false
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		jobs, err := store.Jobs(context.Background(), 1, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			if job.Track.Title == "新歌" && job.State == Completed && job.Result == "ok" {
				downloaded = true
			}
		}
		if downloaded {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !downloaded {
		t.Fatal("download waited behind the favorite read")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestFailedJobsStaysCompleteBeyondTheOverviewLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < 105; i++ {
		if _, err := s.Enqueue(ctx, 1, 1, testTrack(fmt.Sprintf("%d", i), fmt.Sprintf("Song %d", i), "Artist")); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := s.Jobs(ctx, 1, false)
	if err != nil || len(jobs) != 100 {
		t.Fatalf("overview jobs=%d err=%v", len(jobs), err)
	}
	for _, job := range jobs {
		if err := s.Finish(ctx, job, Failed, "下载失败", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	page, total, err := s.FailedJobs(ctx, 1, 0, 1)
	if err != nil || total != 100 || len(page) != 1 || page[0].State != Failed {
		t.Fatalf("failed page=%#v total=%d err=%v", page, total, err)
	}
}

type gatingProvider struct {
	stubProvider
	fetch func(context.Context, string) (Snapshot, error)
}

func (p gatingProvider) Fetch(ctx context.Context, platform string) (Snapshot, error) {
	return p.fetch(ctx, platform)
}

func TestFavoritePollRoundTimeoutReleasesTheNextRound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, _, err := s.Apply(ctx, 1, 9, snapshot("account", testTrack("1", "Old", "Artist")), true); err != nil {
		t.Fatal(err)
	}
	oldTimeout := favoriteSyncRoundTimeout
	favoriteSyncRoundTimeout = 40 * time.Millisecond
	t.Cleanup(func() { favoriteSyncRoundTimeout = oldTimeout })

	calls := make(chan struct{}, 2)
	svc := Service{
		Store: s, Enabled: true, PollInterval: 10 * time.Millisecond,
		Provider: gatingProvider{fetch: func(ctx context.Context, platform string) (Snapshot, error) {
			calls <- struct{}{}
			select {
			case <-ctx.Done():
				return Snapshot{}, ctx.Err()
			case <-time.After(2 * time.Second):
				return Snapshot{}, errors.New("slow source did not time out")
			}
		}},
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.runFavoritePoll(runCtx, 10*time.Millisecond)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-time.After(time.Second):
			t.Fatal("next favorite round did not start after a timeout")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("favorite poll did not stop")
	}
}

func TestFreshFavoriteNoticeKeepsAShortList(t *testing.T) {
	items := make([]FreshFavorite, 0, 9)
	for i := 1; i <= 9; i++ {
		items = append(items, FreshFavorite{Chat: 3, Platform: "qq", Track: testTrack(fmt.Sprint(i), fmt.Sprintf("歌%d", i), "歌手")})
	}
	items = append(items, FreshFavorite{Chat: 3, Platform: "netease", Track: testTrack("n", "网易新歌", "另一人")})
	text := formatFreshFavoriteNotices(items)[3]
	if !strings.Contains(text, "网易云 1 首 · QQ 音乐 9 首") || !strings.Contains(text, "还有 2 首") || strings.Count(text, "\n· ") > 8 {
		t.Fatal(text)
	}
	if !strings.Contains(text, "<b>收藏有新歌</b>") || !strings.Contains(text, "已排队入库") {
		t.Fatal(text)
	}
}

func TestFreshFavoriteNoticeLinksAndEscapesTheTitle(t *testing.T) {
	track := testTrack("1", "A & B <C>", "甲&乙")
	text := formatFreshFavoriteNotices([]FreshFavorite{{Chat: 1, Platform: "netease", Track: track}})[1]
	if !strings.Contains(text, `<a href="https://music.163.com/song?id=1">A &amp; B &lt;C&gt;</a>`) || !strings.Contains(text, "甲&amp;乙") || strings.Contains(text, "A & B") {
		t.Fatal(text)
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

func (p stubProvider) ResolveOn(ctx context.Context, t Track, platform string) (Track, error) {
	if p.resolveErr != nil {
		return Track{}, p.resolveErr
	}
	out, err := p.Resolve(ctx, t, "")
	if err != nil {
		return Track{}, err
	}
	if platform != "" {
		out.Platform = platform
	}
	return out, nil
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

func TestPendingUploadDoesNotPermanentlyHideARediscoverSong(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	track := testTrack("pending", "等待上传", "甲")
	id, err := store.Enqueue(ctx, 1, 1, track)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Job(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Finish(ctx, job, PendingUpload, "WebDAV unavailable", "", 0); err != nil {
		t.Fatal(err)
	}
	exclusions, err := store.Exclusions(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, hidden := exclusions[Identity(track)]; hidden {
		t.Fatalf("pending upload was treated as a permanent rediscover exclusion: %+v", exclusions)
	}
	svc := Service{Store: store, Provider: stubProvider{}, Library: stubLibrary{}, History: func(context.Context) ([]Track, error) {
		return []Track{track}, nil
	}}
	batch, err := svc.Rediscover(ctx, 1, "netease", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Tracks) != 1 || batch.Tracks[0].Title != track.Title || batch.SkippedPending != 0 {
		t.Fatalf("pending upload disappeared from rediscover: %+v", batch)
	}
}

func TestRediscoverFindsOneMissingSong(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	favorite := testTrack("f1", "Fav", "FA")
	if _, _, err := store.Apply(ctx, 1, 1, snapshot("account", favorite), true); err != nil {
		t.Fatal(err)
	}
	history := []Track{testTrack("h1", "Hist", "HA")}
	svc := Service{Store: store, Provider: stubProvider{}, Library: stubLibrary{}, History: func(context.Context) ([]Track, error) {
		return history, nil
	}}
	batch, err := svc.Rediscover(ctx, 1, "netease", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Tracks) != 1 || batch.Tracks[0].Title != "Fav" {
		t.Fatalf("batch = %+v", batch.Tracks)
	}
	if _, err = svc.Rediscover(ctx, 1, "netease", 3); err == nil || !strings.Contains(err.Error(), "重新发起找回") {
		t.Fatalf("legacy batch size accepted: %v", err)
	}
}

func TestRediscoverPrefersALongMissedFavorite(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	fav := testTrack("fav", "久未听的收藏", "甲")
	fav.Plays = 8
	fav.LastPlayed = time.Now().AddDate(-3, 0, 0)
	history := []Track{testTrack("hist", "最近常听", "乙")}
	history[0].Plays = 100
	history[0].LastPlayed = time.Now()
	if _, _, err := store.Apply(ctx, 1, 1, snapshot("account", fav), true); err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: store, Provider: stubProvider{}, Library: stubLibrary{}, History: func(context.Context) ([]Track, error) {
		return history, nil
	}}
	batch, err := svc.Rediscover(ctx, 1, "netease", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Tracks) != 1 || batch.Tracks[0].Title != fav.Title {
		t.Fatalf("did not choose the missed favorite: %+v", batch.Tracks)
	}
}

func TestRediscoverIgnoresInactiveFavorites(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	gone := testTrack("old", "取消收藏", "甲")
	kept := testTrack("keep", "仍收藏", "乙")
	if _, _, err := store.Apply(ctx, 1, 1, snapshot("account", gone, kept), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(ctx, 1, 1, snapshot("account", kept), false); err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: store, Provider: stubProvider{}, Library: stubLibrary{}, History: func(context.Context) ([]Track, error) {
		return []Track{testTrack("h", "听过", "丙")}, nil
	}}
	batch, err := svc.Rediscover(ctx, 1, "netease", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, track := range batch.Tracks {
		if track.Title == "取消收藏" {
			t.Fatalf("unliked song used a history slot: %+v", batch.Tracks)
		}
	}
	if len(batch.Tracks) != 1 || batch.Tracks[0].Title != "仍收藏" {
		t.Fatalf("batch = %+v", batch.Tracks)
	}
}

func TestRelikeAfterCompletedRequeuesOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	oldSong := testTrack("1", "旧歌", "甲")
	song := testTrack("2", "重喜欢", "乙")
	if _, _, err := store.Apply(ctx, 1, 1, snapshot("acc", oldSong), true); err != nil {
		t.Fatal(err)
	}
	if n, _, err := store.Apply(ctx, 1, 1, snapshot("acc", oldSong, song), false); err != nil || n != 1 {
		t.Fatalf("first like n=%d err=%v", n, err)
	}
	if _, err := store.db.Exec(`UPDATE discovery_jobs SET state='completed', attempts=4 WHERE user_id=1`); err != nil {
		t.Fatal(err)
	}
	if n, _, err := store.Apply(ctx, 1, 1, snapshot("acc", oldSong), false); err != nil || n != 0 {
		t.Fatalf("unlike n=%d err=%v", n, err)
	}
	n, fresh, err := store.Apply(ctx, 1, 1, snapshot("acc", oldSong, song), false)
	if err != nil || n != 1 || len(fresh) != 1 || fresh[0].Title != "重喜欢" {
		t.Fatalf("relike n=%d fresh=%+v err=%v", n, fresh, err)
	}
	job := jobByTitle(t, store, "重喜欢")
	if job.State != Queued || job.Attempts != 0 {
		t.Fatalf("relike job = %+v", job)
	}
	if n, fresh, err = store.Apply(ctx, 1, 1, snapshot("acc", oldSong, song), false); err != nil || n != 0 || len(fresh) != 0 {
		t.Fatalf("repeat relike n=%d fresh=%+v err=%v", n, fresh, err)
	}
	if again := jobByTitle(t, store, "重喜欢"); again.State != Queued || again.ID != job.ID {
		t.Fatalf("repeat changed the queued job: %+v", again)
	}

	if _, err := store.db.Exec(`UPDATE discovery_jobs SET state='failed' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(ctx, 1, 1, snapshot("acc", oldSong), false); err != nil {
		t.Fatal(err)
	}
	if n, _, err := store.Apply(ctx, 1, 1, snapshot("acc", oldSong, song), false); err != nil || n != 0 {
		t.Fatalf("failed job was revived n=%d err=%v", n, err)
	}
	if failed := jobByTitle(t, store, "重喜欢"); failed.State != Failed {
		t.Fatalf("failed job = %+v", failed)
	}

	if _, err := store.db.Exec(`UPDATE discovery_jobs SET state='completed' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if n, _, err := store.Apply(ctx, 1, 1, snapshot("acc", oldSong, song), true); err != nil || n != 0 {
		t.Fatalf("bind requeued n=%d err=%v", n, err)
	}
	if bound := jobByTitle(t, store, "重喜欢"); bound.State != Completed {
		t.Fatalf("bind changed a completed job: %+v", bound)
	}
}

func jobByTitle(t *testing.T, store *Store, title string) Job {
	t.Helper()
	jobs, err := store.Jobs(context.Background(), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.Track.Title == title {
			return job
		}
	}
	t.Fatalf("missing job %q in %+v", title, jobs)
	return Job{}
}

func TestNeteaseRefreshRotatesStoredSongs(t *testing.T) {
	svc := &Service{}
	ids := make([]string, 25)
	for i := range ids {
		ids[i] = fmt.Sprintf("%02d", i+1)
	}
	first := svc.neteaseRefreshIDs("9", ids)
	second := svc.neteaseRefreshIDs("9", ids)
	if len(first) != 20 || first[0] != "01" || first[19] != "20" {
		t.Fatalf("first window = %#v", first)
	}
	if len(second) != 20 || second[0] != "21" || second[4] != "25" || second[5] != "01" {
		t.Fatalf("second window = %#v", second)
	}
}

func TestAcceptPlatformHitIgnoresAlbumTitleWhenVersionAndDurationMatch(t *testing.T) {
	source := Track{Title: "晴天", Artist: "周杰伦", Album: "叶惠美", Duration: 269}
	otherAlbum := Track{Title: "晴天", Artist: "周杰伦", Album: "November's Chopin", Duration: 270, Verified: true}
	if !acceptPlatformHit(source, otherAlbum) {
		t.Fatal("a different album title should still match")
	}
	live := otherAlbum
	live.Album = "The Era Tour (Live)"
	live.Duration = 269
	if acceptPlatformHit(source, live) {
		t.Fatal("a live album is a different version")
	}
	far := otherAlbum
	far.Duration = 320
	if acceptPlatformHit(source, far) {
		t.Fatal("duration must stay strict")
	}
}

func TestRediscoverSplitsDismissedQueuedAndArtistCapReasons(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	dismissed := testTrack("d", "不再推荐", "甲")
	queued := testTrack("q", "已在队列", "乙")
	artist1 := testTrack("a1", "同艺人一", "同歌手")
	artist2 := testTrack("a2", "同艺人二", "同歌手")
	artist3 := testTrack("a3", "同艺人三", "同歌手")
	artist1.Plays, artist2.Plays, artist3.Plays = 30, 20, 10
	if err := s.Preference(ctx, 1, dismissed, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, 1, 1, queued); err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: s, Provider: stubProvider{}, Library: stubLibrary{}, History: func(context.Context) ([]Track, error) {
		return []Track{dismissed, queued, artist1, artist2, artist3}, nil
	}}
	batch, err := svc.Rediscover(ctx, 1, "netease", 1)
	if err != nil {
		t.Fatal(err)
	}
	if batch.SkippedDismissed != 1 || batch.SkippedQueued != 1 {
		t.Fatalf("reason counts = %+v", batch)
	}
	if len(batch.Tracks) != 1 {
		t.Fatalf("tracks = %+v", batch.Tracks)
	}
}

func TestRediscoverCountsVersionsThePlatformCannotConfirm(t *testing.T) {
	s := testStore(t)
	tracks := []Track{
		testTrack("1", "Known", "A"),
		testTrack("2", "Ambiguous", "B"),
		testTrack("3", "Missing", "C"),
		testTrack("4", "Also", "D"),
	}
	tracks[0].Plays, tracks[1].Plays, tracks[2].Plays, tracks[3].Plays = 10, 40, 30, 20
	provider := &countingProvider{}
	provider.resolveOn = func(track Track, platform string) (Track, error) {
		switch track.Title {
		case "Ambiguous":
			return Track{}, ErrAmbiguousRecording
		case "Missing":
			return Track{}, ErrNoUniqueRecording
		case "Offline":
			return Track{}, errors.New("timeout")
		}
		track.Platform = platform
		track.Verified = true
		return track, nil
	}
	svc := Service{Store: s, Provider: provider, Library: stubLibrary{}, History: func(context.Context) ([]Track, error) {
		return tracks, nil
	}}
	batch, err := svc.Rediscover(context.Background(), 1, "netease", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Tracks) != 1 || batch.Unconfirmed == 0 || batch.SkippedOwned != 0 {
		t.Fatalf("batch=%+v", batch)
	}
}

func TestRecommendLimitedUsesOnePlatformAndReusesCachedHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var tracks []Track
	for i := 0; i < 5; i++ {
		tr := testTrack(fmt.Sprint(i), fmt.Sprint("Song", i), fmt.Sprint("Artist", i))
		tr.Plays = 10 - i
		tracks = append(tracks, tr)
	}
	historyCalls := 0
	resolveCalls := 0
	provider := &countingProvider{}
	provider.resolveOn = func(track Track, platform string) (Track, error) {
		resolveCalls++
		if platform != "netease" {
			t.Fatalf("resolved on %s", platform)
		}
		track.Platform = platform
		return track, nil
	}
	svc := Service{Store: s, Provider: provider, Library: stubLibrary{}, History: func(context.Context) ([]Track, error) {
		historyCalls++
		return tracks, nil
	}}
	out, err := svc.RecommendLimited(ctx, 1, "netease", 1)
	if err != nil || len(out) != 1 || resolveCalls != 1 || historyCalls != 1 {
		t.Fatalf("first limited result=%d resolve=%d history=%d err=%v", len(out), resolveCalls, historyCalls, err)
	}
	out, err = svc.RecommendLimited(ctx, 1, "netease", 1)
	if err != nil || len(out) != 1 || resolveCalls != 1 || historyCalls != 1 {
		t.Fatalf("cache missed result=%d resolve=%d history=%d err=%v", len(out), resolveCalls, historyCalls, err)
	}
}

type countingProvider struct {
	stubProvider
	resolveOn func(Track, string) (Track, error)
}

func (p *countingProvider) ResolveOn(_ context.Context, t Track, platform string) (Track, error) {
	if p.resolveOn != nil {
		return p.resolveOn(t, platform)
	}
	return p.stubProvider.ResolveOn(context.Background(), t, platform)
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

func TestManualQueueCompletionRetriesActionCardWithoutRedownloading(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	track := testTrack("1", "遗珠", "歌手")
	id, err := store.Enqueue(ctx, 1, 1, track)
	if err != nil {
		t.Fatal(err)
	}
	file := IngestedFile{Path: "music/artist/song.flac", Storage: "LOCAL", Extension: "flac", Size: 1000}
	downloads, notifications := 0, 0
	svc := Service{Store: store, Provider: stubProvider{}, Library: stubLibrary{},
		Download: func(context.Context, Track) (DownloadResult, error) {
			downloads++
			return DownloadResult{File: &file}, nil
		},
		NotifyIngested: func(j Job) error {
			notifications++
			if j.State != Completed || j.User != 1 || j.Chat != 1 || j.File == nil || *j.File != file {
				t.Errorf("invalid completion receipt: %#v", j)
			}
			if notifications == 1 {
				return errors.New("Telegram unavailable")
			}
			return nil
		},
	}
	if err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Job(ctx, id)
	if err != nil || saved.State != Completed || saved.File == nil || *saved.File != file || notifications != 1 {
		t.Fatalf("completion wasn't saved before notice: job=%#v notifications=%d err=%v", saved, notifications, err)
	}
	for range 2 {
		if err := svc.Digest(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if downloads != 1 || notifications != 2 {
		t.Fatalf("expected one download and one notification retry, got %d downloads and %d notifications", downloads, notifications)
	}
}

func TestStoredFileMigrationPreservesOldQueueJobs(t *testing.T) {
	ctx := t.Context()
	filename := filepath.Join(t.TempDir(), "legacy.sqlite3")
	store, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	track := testTrack("1", "旧任务", "歌手")
	id, err := store.Enqueue(ctx, 1, 1, track)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	_, err = store.db.ExecContext(ctx, "ALTER TABLE discovery_jobs DROP COLUMN stored_file_json")
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err = Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.Job(ctx, id)
	if err != nil || job.State != Queued || job.Track.Title != track.Title || job.File != nil {
		t.Fatalf("legacy queue job changed on migration: %#v, err=%v", job, err)
	}
}

func TestDeferredDiscoveryActionCardWaitsForExactWebDAVFile(t *testing.T) {
	ctx := t.Context()
	track := testTrack("1", "遗珠", "歌手")
	store := testStore(t)
	id, err := store.Enqueue(ctx, 1, 1, track)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" || r.URL.Path != "/music/artist/song.flac" {
			t.Errorf("unexpected WebDAV stat %s %s", r.Method, r.URL.Path)
		}
		if !uploaded.Load() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/music/artist/song.flac</d:href><d:propstat><d:prop><d:getcontentlength>4</d:getcontentlength><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
	}))
	defer server.Close()
	previous := core.GlobalWebDAV
	t.Cleanup(func() { core.GlobalWebDAV = previous })
	outbox := core.NewWebDAVOutbox(t.TempDir())
	core.GlobalWebDAV = &core.WebDAV{
		Config: &config.WebDAVConfig{WebDAVDir: "music"}, Client: gowebdav.NewClient(server.URL, "", ""), Outbox: outbox,
	}
	source := filepath.Join(t.TempDir(), "song.flac")
	if err := os.WriteFile(source, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.Enqueue(source, "artist"); err != nil {
		t.Fatal(err)
	}
	cards := 0
	svc := Service{Store: store, Provider: stubProvider{}, Library: stubLibrary{},
		Download: func(context.Context, Track) (DownloadResult, error) {
			return DownloadResult{Pending: true, File: &IngestedFile{Path: "artist/song.flac", Storage: "WEBDAV", Size: 4}}, nil
		},
		NotifyIngested: func(Job) error { cards++; return nil },
	}
	if err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	svc.Library = stubLibrary{owned: map[string]bool{Identity(track): true}}
	if err := svc.Digest(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Job(ctx, id)
	if err != nil || pending.State != PendingUpload || cards != 0 {
		t.Fatalf("staged file was treated as uploaded: %#v, cards=%d err=%v", pending, cards, err)
	}
	if _, err := outbox.ProcessOnce(func(string, string) error { uploaded.Store(true); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := svc.Digest(ctx); err != nil {
		t.Fatal(err)
	}
	complete, err := store.Job(ctx, id)
	if err != nil || complete.State != Completed || cards != 1 {
		t.Fatalf("confirmed upload did not get a card: %#v, cards=%d err=%v", complete, cards, err)
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

func TestFailedJobCanBeRetriedOrDismissedIndividually(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i, title := range []string{"First", "Second"} {
		j := testTrack(fmt.Sprint(i+1), title, "Artist")
		id, err := s.Enqueue(ctx, 1, 1, j)
		if err != nil {
			t.Fatal(err)
		}
		job, err := s.Job(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Finish(ctx, job, Failed, "download failed", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	jobs, total, err := s.FailedJobs(ctx, 1, 0, 10)
	if err != nil || total != 2 {
		t.Fatal(err, total)
	}
	retried, err := s.RetryJob(ctx, jobs[0].ID, 1)
	if err != nil || !retried {
		t.Fatal("retry", retried, err)
	}
	dismissed, err := s.DismissFailedJob(ctx, jobs[1].ID, 1)
	if err != nil || !dismissed {
		t.Fatal("dismiss", dismissed, err)
	}
	failed, total, err := s.FailedJobs(ctx, 1, 0, 10)
	if err != nil || total != 0 || len(failed) != 0 {
		t.Fatal("failed jobs remain", total, failed, err)
	}
	queued, _ := s.Job(ctx, jobs[0].ID)
	ignored, _ := s.Job(ctx, jobs[1].ID)
	if queued.State != Queued || ignored.State != Ignored {
		t.Fatalf("states queued=%s ignored=%s", queued.State, ignored.State)
	}
	excluded, err := s.Excluded(ctx, 1)
	if err != nil || !excluded[Identity(ignored.Track)] {
		t.Fatal("dismissed failed song will be rediscovered", excluded, err)
	}
	if again, err := s.RetryJob(ctx, jobs[1].ID, 1); err != nil || again {
		t.Fatal("dismissed job was retried", again, err)
	}
	if again, err := s.DismissFailedJob(ctx, jobs[0].ID, 1); err != nil || again {
		t.Fatal("queued job was dismissed", again, err)
	}
}

func TestCrossPlatformRetryRequiresAClaimedAlternate(t *testing.T) {
	original := testTrack("n", "东方之珠 (Live)", "罗大佑")
	original.Platform = "netease"
	alternate := testTrack("q", "东方之珠 (Live)", "罗大佑")
	alternate.Platform = "qq"
	svc := Service{Provider: stubProvider{alternate: alternate}}
	resolved, err := svc.ResolveFromAnotherPlatform(context.Background(), original)
	if err != nil || resolved.Platform != "qq" || resolved.ID != alternate.ID {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	svc.Provider = stubProvider{alternate: Track{Platform: "qq", Title: "别的歌", Verified: true}}
	if _, err = svc.ResolveFromAnotherPlatform(context.Background(), original); err == nil {
		t.Fatal("unrelated alternate was accepted")
	}
}

type resolveCounter struct {
	stubProvider
	n *int
}

func (p resolveCounter) Resolve(ctx context.Context, track Track, exclude string) (Track, error) {
	*p.n++
	return p.stubProvider.Resolve(ctx, track, exclude)
}

func TestAudioMatchStopsBeforeAnotherPlatform(t *testing.T) {
	for _, tc := range []struct {
		decision audiodupe.Decision
		want     string
	}{
		{audiodupe.DecisionSkip, Owned},
		{audiodupe.DecisionConfirm, NeedsConfirmation},
	} {
		t.Run(string(tc.decision), func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			s.Enqueue(ctx, 1, 1, testTrack("1", "Song", "Artist"))
			resolves := 0
			alt := testTrack("q", "Song", "Artist")
			alt.Platform = "qq"
			svc := Service{
				Store:    s,
				Provider: resolveCounter{stubProvider: stubProvider{alternate: alt}, n: &resolves},
				Library:  stubLibrary{},
				Download: func(context.Context, Track) (DownloadResult, error) {
					return DownloadResult{}, &audiodupe.LibraryMatch{Path: "lib/song.flac", Similarity: 0.97, Decision: tc.decision}
				},
			}
			if err := svc.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			if resolves != 0 {
				t.Fatal("tried another platform", resolves)
			}
			jobs, err := s.Jobs(ctx, 1, false)
			if err != nil || len(jobs) != 1 || jobs[0].State != tc.want || !strings.Contains(jobs[0].Error, "song.flac") {
				t.Fatal(jobs, err)
			}
		})
	}
}

func TestAlternateAudioMatchRequiresConfirmationForTheActualSource(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	track := testTrack("1", "执迷不悔", "谭咏麟")
	track.Album = "我们一起走过的日子"
	alternate := track
	alternate.Platform, alternate.ID, alternate.URL = "qq", "alt", "https://y.qq.com/n/ryqq/songDetail/alt"
	library := &NavidromeLibrary{
		byTitle: map[string][]Track{ownershipTitle(track.Title): {{Title: track.Title, Artist: "王菲", Album: "执迷不悔"}}},
		expires: time.Now().Add(time.Minute),
	}
	var attempted []Track
	svc := Service{Store: store, Library: library, Provider: stubProvider{alternate: alternate},
		Download: func(_ context.Context, got Track) (DownloadResult, error) {
			attempted = append(attempted, got)
			if got.Platform == track.Platform {
				return DownloadResult{}, errors.New("original unavailable")
			}
			return DownloadResult{}, &audiodupe.LibraryMatch{Decision: audiodupe.DecisionConfirm, Similarity: 0.9}
		},
	}
	id, err := store.Enqueue(ctx, 1, 1, track)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := store.Job(ctx, id)
	if err != nil || len(attempted) != 2 || job.State != NeedsConfirmation ||
		job.Track.URL != alternate.URL || !strings.Contains(job.Error, "听感很接近") {
		t.Fatalf("alternate fingerprint result: job=%#v calls=%d err=%v", job, len(attempted), err)
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
