package playlistcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

func TestApplyKeepsEditedDescriptionsAndRetiresMissingPlaylists(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	seeds := []config.PlaylistInfo{{Name: "中文", Desc: "中文歌曲"}, {Name: "英文", Desc: "英文歌曲"}}
	first, err := store.Apply(ctx, []Remote{
		{ID: "zh", Name: "中文", Comment: "旧备注"},
		{ID: "ai", Name: "[AI] 夜色", Comment: "gymdl-ai-playlist-create:abc"},
		{ID: "bad", Name: "不要,这个"},
		{ID: "trash", Name: "回收站"},
		{ID: "holder", Name: "歌单"},
		{ID: "fresh", Name: "新歌单", Comment: "来自备注"},
	}, seeds, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.Added != 2 {
		t.Fatalf("added = %d, want the two usable playlists", first.Added)
	}
	rows, err := store.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Name != "中文" || rows[0].Desc != "中文歌曲" || rows[1].Name != "新歌单" || rows[1].Desc != "来自备注" {
		t.Fatalf("active = %#v", rows)
	}
	if err = store.SetDesc(ctx, "zh", "改过的描述"); err != nil {
		t.Fatal(err)
	}
	second, err := store.Apply(ctx, []Remote{{ID: "zh", Name: "中文流行", Comment: "不要覆盖描述"}, {ID: "en", Name: "英文"}}, seeds, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if second.Added != 1 || second.Updated != 1 || second.Inactive != 1 {
		t.Fatalf("second sync = %+v", second)
	}
	rows, err = store.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Playlist{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	if byName["中文流行"].Desc != "改过的描述" || byName["英文"].Desc != "英文歌曲" || len(rows) != 2 {
		t.Fatalf("descriptions were not preserved: %#v", rows)
	}
	synced, err := store.Synced(ctx)
	if err != nil || !synced {
		t.Fatalf("synced = %v err=%v", synced, err)
	}
}

func TestSetDescRejectsLongTextAndInactiveRows(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, err := store.Apply(ctx, []Remote{{ID: "zh", Name: "中文"}}, nil, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(ctx, nil, nil, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDesc(ctx, "zh", "回来"); err == nil {
		t.Fatal("inactive playlist accepted a description")
	}
	if _, err := store.Apply(ctx, []Remote{{ID: "zh", Name: "中文"}}, nil, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDesc(ctx, "zh", strings.Repeat("字", 201)); err == nil {
		t.Fatal("overlong description accepted")
	}
}

func TestSyncReadsNavidromeWithoutReplacingASavedDescription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"nd-token"}`))
		case "/api/playlist":
			if r.Header.Get("X-Nd-Authorization") != "Bearer nd-token" {
				http.Error(w, "no token", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`[{"id":"zh","name":"中文","comment":"线上备注"},{"id":"ai","name":"[AI] 夜色","comment":"gymdl-ai"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "gymdl.sqlite3")
	ctx := context.Background()
	organizer := &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "user", NavidromePassword: "secret"}
	if _, err := Sync(ctx, organizer, path, []config.PlaylistInfo{{Name: "中文", Desc: "yaml 描述"}}); err != nil {
		t.Fatal(err)
	}
	store := testStoreAt(t, path)
	if err := store.SetDesc(ctx, "zh", "手写描述"); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(ctx, organizer, path, []config.PlaylistInfo{{Name: "中文", Desc: "yaml 描述"}}); err != nil {
		t.Fatal(err)
	}
	store = testStoreAt(t, path)
	rows, err := store.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Desc != "手写描述" || rows[0].Name != "中文" {
		t.Fatalf("sync replaced the edited description: %#v", rows)
	}
}

func TestFunctionalPlaylistsStayOutOfTheEditor(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO playlist_catalog(id,name,description,comment,active,synced_at,updated_at) VALUES('trash','回收站','', '',1,1,1),('holder','歌单','', '',1,1,1),('zh','中文','', '',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "中文" {
		t.Fatalf("functional playlists were offered: %#v", rows)
	}
	if err = store.SetDesc(ctx, "trash", "不该写"); err == nil {
		t.Fatal("trash playlist accepted a description")
	}
	if _, err = store.Apply(ctx, []Remote{{ID: "zh", Name: "中文"}, {ID: "trash", Name: "回收站"}, {ID: "holder", Name: "歌单"}}, nil, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	var active int
	if err = store.db.QueryRowContext(ctx, `SELECT active FROM playlist_catalog WHERE id='trash'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("trash playlist stayed active after sync")
	}
}

func testStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
