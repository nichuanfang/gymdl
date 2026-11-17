package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/playlistcatalog"
)

func TestSyncedCatalogReplacesTheYamlPlaylistList(t *testing.T) {
	store, err := playlistcatalog.Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	fallback := []config.PlaylistInfo{{Name: "旧歌单", Desc: "yaml"}}
	before, synced, err := loadTidyPlaylists(ctx, store, fallback)
	if err != nil || synced || len(before) != 1 || before[0].Name != "旧歌单" {
		t.Fatalf("before sync = %#v synced=%v err=%v", before, synced, err)
	}
	if _, err = store.Apply(ctx, []playlistcatalog.Remote{{ID: "zh", Name: "中文"}}, []config.PlaylistInfo{{Name: "中文", Desc: "中文歌曲"}}, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	after, synced, err := loadTidyPlaylists(ctx, store, fallback)
	if err != nil || !synced || len(after) != 1 || after[0].Name != "中文" || after[0].Desc != "中文歌曲" {
		t.Fatalf("after sync = %#v synced=%v err=%v", after, synced, err)
	}
}

func TestPlaylistDescButtonsFitTelegram(t *testing.T) {
	session := pendingPlaylistDesc{
		token:  strings.Repeat("a", 32),
		synced: true,
		playlists: []playlistcatalog.Playlist{
			{ID: "zh", Name: "中文", Desc: ""},
			{ID: "en", Name: "英文", Desc: "英文歌曲"},
		},
	}
	_, markup := formatPlaylistDesc(session, "")
	if !buttonTexts(markup)["中文 · 未写"] || !buttonTexts(markup)["↻ 同步歌单"] || !buttonTexts(markup)["✕ 取消"] {
		t.Fatalf("labels = %#v", buttonTexts(markup))
	}
	if buttonTexts(markup)["同步歌单"] || buttonTexts(markup)["取消"] {
		t.Fatalf("plain action labels still look like playlists: %#v", buttonTexts(markup))
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			raw, err := json.Marshal(button)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Text string `json:"text"`
				Data string `json:"callback_data"`
			}
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Data) > 64 || len([]rune(wire.Text)) == 0 || len([]rune(wire.Text)) > 64 {
				t.Fatalf("button does not fit Telegram: %s", raw)
			}
		}
	}
}

func TestPlaylistDescActionsStayOffThePlaylistRows(t *testing.T) {
	playlists := make([]playlistcatalog.Playlist, 17)
	for i := range playlists {
		playlists[i] = playlistcatalog.Playlist{ID: fmt.Sprintf("id-%d", i), Name: fmt.Sprintf("歌%02d", i)}
	}
	session := pendingPlaylistDesc{
		token:     strings.Repeat("b", 32),
		synced:    true,
		page:      1,
		playlists: playlists,
	}
	_, markup := formatPlaylistDesc(session, "")
	texts := buttonTexts(markup)
	for _, label := range []string{"◀ 上一页", "下一页 ▶", "↻ 同步歌单", "✕ 取消", "歌08 · 未写"} {
		if !texts[label] {
			t.Fatalf("missing %s in %#v", label, texts)
		}
	}
	for _, plain := range []string{"上一页", "下一页", "同步歌单", "取消"} {
		if texts[plain] {
			t.Fatalf("plain action label still looks like a playlist: %s", plain)
		}
	}
	var sawSyncAlone, sawCancelAlone bool
	for _, row := range markup.InlineKeyboard {
		var kinds []string
		for _, button := range row {
			raw, err := json.Marshal(button)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Data string `json:"callback_data"`
			}
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.HasPrefix(wire.Data, "open|"):
				kinds = append(kinds, "open")
			case strings.HasPrefix(wire.Data, "page|"):
				kinds = append(kinds, "page")
			case strings.HasPrefix(wire.Data, "sync|"):
				kinds = append(kinds, "sync")
			case strings.HasPrefix(wire.Data, "cancel|"):
				kinds = append(kinds, "cancel")
			}
			if len(wire.Data) > 64 {
				t.Fatalf("callback too long: %s", raw)
			}
		}
		joined := strings.Join(kinds, ",")
		if strings.Contains(joined, "open") && joined != "open" && joined != "open,open" {
			t.Fatalf("playlist row mixed with actions: %s", joined)
		}
		if joined == "sync" && len(row) == 1 {
			sawSyncAlone = true
		}
		if joined == "cancel" && len(row) == 1 {
			sawCancelAlone = true
		}
	}
	if !sawSyncAlone || !sawCancelAlone {
		t.Fatal("sync and cancel should each sit on their own row")
	}
}
