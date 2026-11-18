package scrobble

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShouldInsertAllowsAnotherPlayOfTheSameTrack(t *testing.T) {
	if ShouldInsert(true, true, "https://song") {
		t.Fatal("exact replay was inserted")
	}
	if !ShouldInsert(true, false, "https://song") {
		t.Fatal("a later play of the same URL was skipped")
	}
	if ShouldInsert(false, true, "") {
		t.Fatal("empty URL ignored the stored title")
	}
	if !ShouldInsert(false, false, "") {
		t.Fatal("new untitled URL was skipped")
	}
}

func TestParseRecentSkipsNowPlayingAndUsesShanghaiTime(t *testing.T) {
	body := []byte(`{"recenttracks":{"track":[
		{"name":"正在听","artist":{"#text":"甲"},"album":{"#text":"专辑"},"image":[{"#text":"small"},{"#text":"medium"}],"url":"https://now"},
		{"name":"夜曲","artist":{"#text":"周杰伦"},"album":{"#text":"十一月的萧邦"},"image":[{"#text":"small"},{"#text":"medium"}],"mbid":"mb","url":"https://song","date":{"uts":"1735689600"}}
	],"@attr":{"totalPages":"2"}}}`)
	tracks, pages, err := ParseRecent(body)
	if err != nil || pages != 2 || len(tracks) != 2 {
		t.Fatalf("tracks=%d pages=%d err=%v", len(tracks), pages, err)
	}
	if tracks[0].Date != "" || tracks[1].Date != "2025-01-01 08:00:00" || tracks[1].Image != "medium" {
		t.Fatalf("parsed=%#v", tracks)
	}
	parsed, err := ParseStored(tracks[1].Date)
	if err != nil || parsed.Unix() != 1735689600 {
		t.Fatalf("parsed=%s err=%v", parsed, err)
	}
}

type memoryStore struct {
	rows []Track
	fail string
}

func (m *memoryStore) Latest(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (m *memoryStore) InsertIfNew(_ context.Context, track Track) (bool, error) {
	if track.Name == m.fail {
		return false, errors.New("row failed")
	}
	urlSeen := false
	tupleSeen := false
	for _, row := range m.rows {
		if track.URL != "" && row.URL == track.URL {
			urlSeen = true
		}
		if row.Name == track.Name && row.Artist == track.Artist && row.Date == track.Date {
			tupleSeen = true
		}
	}
	if !ShouldInsert(urlSeen, tupleSeen, track.URL) {
		return false, nil
	}
	m.rows = append(m.rows, track)
	return true, nil
}

func TestSyncContinuesAfterOneRowAndDedups(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		if r.URL.Query().Get("from") == "" || r.URL.Query().Get("user") != "listener" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if pages == 1 {
			_, _ = w.Write([]byte(`{"recenttracks":{"track":[
				{"name":"坏掉","artist":{"#text":"甲"},"url":"https://bad","date":{"uts":"1735689600"}},
				{"name":"夜曲","artist":{"#text":"周杰伦"},"url":"https://song","date":{"uts":"1735689660"}},
				{"name":"正在听","artist":{"#text":"甲"},"url":"https://now"}
			],"@attr":{"totalPages":"1"}}}`))
			return
		}
		t.Errorf("unexpected extra page")
	}))
	defer server.Close()
	original := fetchURL
	fetchURL = server.URL
	t.Cleanup(func() { fetchURL = original })

	store := &memoryStore{fail: "坏掉"}
	since := time.Unix(1735689500, 0)
	inserted, err := Sync(context.Background(), server.Client(), "listener", "key", since, store)
	if err == nil || inserted != 1 || len(store.rows) != 1 || store.rows[0].Name != "夜曲" {
		t.Fatalf("inserted=%d rows=%#v err=%v", inserted, store.rows, err)
	}
}
