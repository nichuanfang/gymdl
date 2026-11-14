package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func TestAIPlaylistReplacementResumesPartialWriteWithoutLosingPlan(t *testing.T) {
	playlistIDs := []string{"old1", "old2"}
	deleteCalls, addCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/StartActionAndWait":
			var request oliveTinActionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode OliveTin request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.ActionID != "playlist_tracks" || len(request.Arguments) != 1 || request.Arguments[0].Value != "[AI] night" {
				t.Errorf("unexpected OliveTin request: %#v", request)
			}
			ids, _ := json.Marshal(playlistIDs)
			nestedIDs, _ := json.Marshal(string(ids))
			stdout := fmt.Sprintf(`[{"playlist_id":"playlist1","playlist_name":"[AI] night","track_ids":%s,"track_count":%d}]`, nestedIDs, len(playlistIDs))
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout}})
		case "/auth/login":
			writeJSON(t, w, map[string]string{"token": "navidrome-test-token"})
		case "/api/playlist/playlist1/tracks":
			if r.Header.Get("X-Nd-Authorization") != "Bearer navidrome-test-token" {
				t.Errorf("missing Navidrome authorization")
			}
			switch r.Method {
			case http.MethodDelete:
				deleteCalls++
				positions := r.URL.Query()["id"]
				if len(positions) != len(playlistIDs) {
					t.Errorf("delete positions = %#v for %d tracks", positions, len(playlistIDs))
				}
				for index, position := range positions {
					if position != fmt.Sprint(index+1) {
						t.Errorf("delete positions = %#v, want one-based order", positions)
					}
				}
				playlistIDs = nil
				writeJSON(t, w, map[string]any{})
			case http.MethodPost:
				var request struct {
					IDs []string `json:"ids"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode Navidrome request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				addCalls++
				if addCalls == 1 {
					playlistIDs = append(playlistIDs, request.IDs[0])
					http.Error(w, "partial add", http.StatusServiceUnavailable)
					return
				}
				playlistIDs = append(playlistIDs, request.IDs...)
				writeJSON(t, w, map[string]any{})
			default:
				t.Errorf("unexpected Navidrome method %s", r.Method)
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	organizer, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		NavidromeURL:      server.URL,
		NavidromeUsername: "nav",
		NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := newAIPlaylistServiceWithClient(&config.AIConfig{Enable: true, ApiKey: "test-key", Model: "test-model"}, &config.AIPlaylistConfig{}, organizer, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	service.resumeStore, err = openAIPlaylistResumeStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.close()

	playlist := aiPlaylist{ID: "playlist1", Name: "[AI] night", Description: "夜间氛围", SongCount: 2}
	plan := aiPlaylistResumePlan{
		PlaylistID:      playlist.ID,
		Description:     playlist.Description,
		SongIDs:         []string{"new1", "new2"},
		Intro:           "适合夜晚聆听。",
		Replace:         true,
		OriginalSongIDs: []string{"old1", "old2"},
	}
	if err := service.resumeStore.Create(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := service.replaceAIPlaylist(context.Background(), playlist, plan); err == nil || !strings.Contains(err.Error(), "续写计划已保留") {
		t.Fatalf("partial write should preserve a retryable replacement plan, err=%v", err)
	}
	saved, err := service.resumeStore.Load(context.Background(), playlist.ID)
	if err != nil || saved == nil || !reflect.DeepEqual(saved.OriginalSongIDs, plan.OriginalSongIDs) || !saved.Replace || !saved.Started {
		t.Fatalf("replacement recovery data was lost: plan=%#v err=%v", saved, err)
	}

	result, err := service.replaceAIPlaylist(context.Background(), playlist, *saved)
	if err != nil {
		t.Fatalf("resume replacement failed: %v", err)
	}
	if !result.Replaced || result.Count != 2 || !reflect.DeepEqual(playlistIDs, plan.SongIDs) || deleteCalls != 2 || addCalls != 2 {
		t.Fatalf("replacement result=%#v tracks=%#v deletes=%d adds=%d", result, playlistIDs, deleteCalls, addCalls)
	}
	if pending, err := service.resumeStore.Load(context.Background(), playlist.ID); err != nil || pending != nil {
		t.Fatalf("completed replacement plan remains: %#v err=%v", pending, err)
	}
}

func TestAIPlaylistReplacementStopsOnUnrelatedCurrentTracks(t *testing.T) {
	playlistIDs := []string{"manually-added"}
	loginCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/StartActionAndWait":
			ids, _ := json.Marshal(playlistIDs)
			stdout := fmt.Sprintf(`[{"playlist_id":"playlist1","playlist_name":"[AI] night","track_ids":%s,"track_count":%d}]`, ids, len(playlistIDs))
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout}})
		case "/auth/login":
			loginCalls++
			writeJSON(t, w, map[string]string{"token": "token"})
		default:
			t.Errorf("replacement must stop before mutation, got %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	organizer, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		NavidromeURL:      server.URL,
		NavidromeUsername: "nav",
		NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &aiPlaylistService{organizer: organizer}
	playlist := aiPlaylist{ID: "playlist1", Name: "[AI] night", Description: "夜间氛围", SongCount: 1}
	plan := aiPlaylistResumePlan{PlaylistID: playlist.ID, Description: playlist.Description, SongIDs: []string{"new1"}, Replace: true, OriginalSongIDs: []string{"old1", "old2"}}
	if _, err := service.replaceAIPlaylist(context.Background(), playlist, plan); err == nil || !strings.Contains(err.Error(), "为保护现有内容") {
		t.Fatalf("unrelated edits must prevent replacement, err=%v", err)
	}
	if loginCalls != 0 || !reflect.DeepEqual(playlistIDs, []string{"manually-added"}) {
		t.Fatalf("playlist was mutated despite an unrelated track, login=%d tracks=%#v", loginCalls, playlistIDs)
	}
	playlistIDs = []string{"old1"}
	if _, err := service.replaceAIPlaylist(context.Background(), playlist, plan); err == nil || !strings.Contains(err.Error(), "预览后发生变化") {
		t.Fatalf("a changed snapshot before replacement starts must stop the write, err=%v", err)
	}
	if loginCalls != 0 || !reflect.DeepEqual(playlistIDs, []string{"old1"}) {
		t.Fatalf("playlist was mutated despite a stale snapshot, login=%d tracks=%#v", loginCalls, playlistIDs)
	}
}

func TestAIPlaylistReplacementResumeAcceptsOnlyContiguousPartialProgress(t *testing.T) {
	original := []string{"old1", "old2", "old3", "old4"}
	planned := []string{"new1", "new2", "new3"}
	for _, test := range []struct {
		name    string
		current []string
		want    bool
	}{
		{name: "after first deletion batch", current: []string{"old3", "old4"}, want: true},
		{name: "after partial add", current: []string{"new1", "new2"}, want: true},
		{name: "replacement already complete", current: planned, want: true},
		{name: "arbitrary subset is not progress", current: []string{"old1", "old4"}},
		{name: "new external track is not progress", current: []string{"other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := playlistTracksCompatibleWithResume(test.current, original, planned); got != test.want {
				t.Fatalf("playlistTracksCompatibleWithResume(%#v) = %t, want %t", test.current, got, test.want)
			}
		})
	}
}
