package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/musicrag"
	"github.com/nichuanfang/gymdl/internal/playlistcatalog"
	tb "gopkg.in/telebot.v4"
)

func TestPlaylistReviewApply(t *testing.T) {
	for _, tc := range []struct {
		name               string
		change, hole, lost bool
		want               playlistReviewApplyStatus
	}{
		{name: "atomic edit", want: playlistReviewApplied},
		{name: "lost response", lost: true, want: playlistReviewApplied},
		{name: "external edit", change: true, want: playlistReviewChanged},
		{name: "position gap", hole: true, want: playlistReviewChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := playlistcatalog.Playlist{ID: "playlist", Name: "夜间"}
			var mu sync.Mutex
			tracks := []string{"keep", "outlier", "another", "outlier"}
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/auth/login":
					writeJSON(t, w, map[string]string{"token": "test-token"})
				case "/api/playlist/playlist":
					writeJSON(t, w, map[string]any{"id": target.ID, "name": target.Name, "songCount": len(tracks)})
				case "/api/playlist/playlist/tracks":
					rows := make([]map[string]string, 0, len(tracks))
					for i, id := range tracks {
						position := i + 1
						if tc.hole && i > 0 {
							position++
						}
						rows = append(rows, map[string]string{"id": strconv.Itoa(position), "mediaFileId": id, "playlistId": target.ID, "title": id})
					}
					writeJSON(t, w, rows)
				case "/api/StartActionAndWait":
					w.Write([]byte(`{"logEntry":{"exitCode":0,"stdout":"[]"}}`))
				case "/rest/updatePlaylist.view":
					writes++
					if err := r.ParseForm(); err != nil {
						t.Errorf("parse update: %v", err)
					}
					if r.Method != http.MethodPost || r.Form.Get("playlistId") != target.ID ||
						!slices.Equal(r.Form["songIndexToRemove"], []string{"1", "3"}) ||
						!slices.Equal(r.Form["songIdToAdd"], []string{"new"}) {
						t.Errorf("unexpected track delta: %v / %v", r.Form["songIndexToRemove"], r.Form["songIdToAdd"])
					}
					tracks = []string{"keep", "another", "new"}
					if tc.lost {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Errorf("simulate lost response: %v", err)
							return
						}
						_ = conn.Close()
						return
					}
					w.Write([]byte(`{"subsonic-response":{"status":"ok"}}`))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			service, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
				OliveTinURL: server.URL, NavidromeURL: server.URL, NavidromeUsername: "test", NavidromePassword: "test",
			})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := service.readPlaylistReviewSnapshot(t.Context(), target)
			if err != nil || snapshot.incomplete != tc.hole {
				t.Fatalf("read snapshot: %+v, %v", snapshot, err)
			}
			change := buildPlaylistReviewChange(snapshot,
				[]playlistReviewSuggestion{{playlistReviewTrack: playlistReviewTrack{ID: "outlier"}}},
				[]playlistReviewSuggestion{{playlistReviewTrack: playlistReviewTrack{ID: "new"}}})
			if tc.change {
				mu.Lock()
				tracks[0], tracks[1] = tracks[1], tracks[0]
				mu.Unlock()
			}
			status, err := service.applyPlaylistReview(t.Context(), target, change)
			if status != tc.want {
				t.Fatalf("apply status %v, want %v: %v", status, tc.want, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.want == playlistReviewApplied {
				if writes != 1 || !slices.Equal(tracks, []string{"keep", "another", "new"}) {
					t.Fatalf("writes %d, final tracks %v", writes, tracks)
				}
			} else if writes != 0 {
				t.Fatalf("changed snapshot was written %d times", writes)
			}
		})
	}
}

func TestPlaylistReviewEvidenceAndAnalysisFailure(t *testing.T) {
	store, err := musicrag.Open(t.TempDir() + "/music.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.Import(t.Context(), []musicrag.Song{
		{MusicID: "outlier", Genre: "重金属", Mood: "激烈", Status: musicrag.StatusReady},
		{MusicID: "pending", Status: musicrag.StatusPending},
	})
	if err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		writeChatResponse(t, w, `{"suggestions":[{"id":"invented","reason":"理由"},{"id":"outlier","reason":"重金属编曲偏离安静主题"}]}`)
	}))
	defer server.Close()
	service, err := newAIPlaylistServiceWithClient(&config.AIConfig{BaseUrl: server.URL, Model: "test"},
		&config.AIPlaylistConfig{}, &playlistOrganizerService{}, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	service.stateTable = newMusicRAGSink(store)
	snapshot := playlistReviewSnapshot{Tracks: []playlistReviewTrack{
		{ID: "outlier"}, {ID: "pending"}, {ID: "outlier"}, {ID: "unknown"},
	}}
	report := service.checkPlaylistReview(t.Context(), "安静", snapshot)
	if report.checked != 2 || report.insufficient != 2 || len(report.remove) != 1 || report.remove[0].ID != "outlier" {
		t.Fatalf("unexpected report %+v", report)
	}
	fail.Store(true)
	report = service.checkPlaylistReview(t.Context(), "安静", snapshot)
	if !report.checkFailed || report.checked != 0 || report.insufficient != 2 || report.unreviewed != 2 || len(report.remove) != 0 {
		t.Fatalf("unexpected failure report %+v", report)
	}
}

func TestPlaylistReviewTelegramSelection(t *testing.T) {
	api, calls := maintTestBot(t)
	app := maintTestApp(t, api)
	api.Handle(&aiPlaylistCallbackEndpoint, app.HandleAIPlaylistCallback)
	message := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 7, Type: tb.ChatPrivate}}
	token := strings.Repeat("a", 32)
	review := &playlistReviewSession{stage: playlistReviewResults, selected: map[string]bool{}}
	for i := 0; i < playlistReviewPageSize+1; i++ {
		review.report.remove = append(review.report.remove, playlistReviewSuggestion{
			playlistReviewTrack: playlistReviewTrack{ID: fmt.Sprintf("song%d", i), Title: fmt.Sprintf("Track %d", i)},
			reason:              "偏离主题",
		})
	}
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: token, userID: 7, chatID: 7, message: message, review: review, expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("register review")
	}
	click := func(user int64, data string) {
		api.ProcessUpdate(tb.Update{Callback: &tb.Callback{
			ID: "callback", Sender: &tb.User{ID: user}, Message: message, Data: "\fai_playlist|" + data,
		}})
		maintWait(t, calls, "answerCallbackQuery", nil)
	}
	current := func() pendingAIPlaylistSession {
		session, ok := app.getAIPlaylistSession(token)
		if !ok {
			t.Fatal("missing review")
		}
		assertTelegramButtons(t, aiPlaylistSessionMarkup(session))
		return session
	}
	click(8, "r_toggle|"+token+"|0|0")
	if len(current().review.selected) != 0 {
		t.Fatal("another user changed the selection")
	}
	click(7, "r_toggle|"+token+"|0|0")
	click(7, "r_toggle|"+token+"|0|0")
	if !current().review.selected["song0"] {
		t.Fatal("stale callback changed the selection")
	}
	click(7, "r_page|"+token+"|1|1")
	click(7, "r_toggle|"+token+"|2|5")
	click(7, "r_check|"+token+"|3")
	session := current()
	if session.review.stage != playlistReviewConfirm || len(session.review.selected) != 2 {
		t.Fatalf("selection did not survive paging: %+v", session.review)
	}
	click(7, "r_cancel|"+token+"|4")
	if _, ok := app.getAIPlaylistSession(token); ok {
		t.Fatal("cancel kept the review active")
	}
}

func TestPlaylistReviewTelegramPreview(t *testing.T) {
	t.Chdir(t.TempDir())
	catalog, err := playlistcatalog.Open("data/gymdl.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	_, err = catalog.Apply(t.Context(), []playlistcatalog.Remote{
		{ID: "saved", Name: "夜间", Comment: "安静"},
		{ID: "infer", Name: "傍晚"},
		{ID: "empty", Name: "待整理"},
		{ID: "ai", Name: "[AI] 夜间"},
		{ID: "trash", Name: "回收站"},
	}, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store, err := musicrag.Open("data/gymdl.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	previous := openMusicRAG
	openMusicRAG = func() (*musicrag.Store, error) { return store, nil }
	t.Cleanup(func() { openMusicRAG = previous })
	if _, err := store.Import(t.Context(), []musicrag.Song{{MusicID: "in-list", Genre: "重金属", Status: musicrag.StatusReady}}); err != nil {
		t.Fatal(err)
	}

	vector := make([]float64, vectorDimensions)
	vector[0] = 1
	var inferenceCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			writeJSON(t, w, map[string]string{"token": "test-token"})
		case "/api/playlist/saved", "/api/playlist/infer", "/api/playlist/empty":
			id := strings.TrimPrefix(r.URL.Path, "/api/playlist/")
			count := 1
			if id == "empty" {
				count = 0
			}
			writeJSON(t, w, map[string]any{"id": id, "name": map[string]string{"saved": "夜间", "infer": "傍晚", "empty": "待整理"}[id], "songCount": count})
		case "/api/playlist/saved/tracks", "/api/playlist/infer/tracks":
			id := strings.Split(r.URL.Path, "/")[3]
			writeJSON(t, w, []map[string]string{{"id": "1", "mediaFileId": "in-list", "playlistId": id, "title": "Heavy"}})
		case "/api/playlist/empty/tracks":
			writeJSON(t, w, []any{})
		case "/embeddings":
			writeJSON(t, w, map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector}}})
		case "/query":
			writeJSON(t, w, map[string]any{"matches": []any{
				map[string]any{"score": 0.9, "metadata": map[string]string{"id": "in-list", "title": "Heavy"}},
				map[string]any{"score": 0.8, "metadata": map[string]string{"id": "missing", "title": "Gone"}},
				map[string]any{"score": 0.7, "metadata": map[string]string{"id": "valid", "title": "Soft", "mood": "安静"}},
			}})
		case "/api/StartActionAndWait":
			var action oliveTinActionRequest
			if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
				t.Errorf("decode OliveTin action: %v", err)
			}
			if action.ActionID != "check_missing_tracks" || len(action.Arguments) != 1 || strings.Contains(action.Arguments[0].Value, "in-list") {
				t.Errorf("unexpected read during preview: %+v", action)
			}
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": `[{"id":"missing"}]`}})
		case "/chat/completions":
			var request struct {
				Messages []struct{ Content string }
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 {
				t.Errorf("unexpected model request: %v", err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			switch prompt := request.Messages[0].Content; {
			case strings.Contains(prompt, "代表曲"):
				inferenceCalls.Add(1)
				writeChatResponse(t, w, `{"theme":"适合傍晚听的摇滚"}`)
			case strings.Contains(prompt, "向量检索"):
				writeChatResponse(t, w, `{"vector_query":"适合安静夜晚的低动态音乐"}`)
			case strings.Contains(prompt, "明显偏离"):
				writeChatResponse(t, w, `{"suggestions":[{"id":"in-list","reason":"重金属编曲偏离安静主题"}]}`)
			default:
				candidates := request.Messages[1].Content
				if strings.Contains(candidates, "in-list") || strings.Contains(candidates, "missing") {
					t.Errorf("model received an excluded candidate: %s", candidates)
				}
				writeChatResponse(t, w, `{"suggestions":[{"id":"valid","reason":"听感安静"}]}`)
			}
		default:
			t.Errorf("unexpected request during preview: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, calls := maintTestBot(t)
	app := maintTestApp(t, api)
	app.playlistCatalog = catalog
	app.cfg.AI = &config.AIConfig{Enable: true, ApiKey: "test", Model: "test", BaseUrl: server.URL}
	app.cfg.AIPlaylist = &config.AIPlaylistConfig{PineconeAPIKey: "test", PineconeHost: server.URL, PineconeNamespace: "test", EmbeddingModel: "test", PlaylistSize: 2, CandidateCount: 5}
	app.cfg.PlaylistOrganizer = &config.PlaylistOrganizerConfig{OliveTinURL: server.URL, NavidromeURL: server.URL, NavidromeUsername: "test", NavidromePassword: "test"}
	api.Handle(&aiPlaylistCallbackEndpoint, app.HandleAIPlaylistCallback)
	message := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 7, Type: tb.ChatPrivate}}
	token := strings.Repeat("b", 32)
	if !app.registerAIPlaylistSession(pendingAIPlaylistSession{
		token: token, userID: 7, chatID: 7, message: message, expiresAt: time.Now().Add(time.Minute),
	}) {
		t.Fatal("register menu")
	}
	click := func(action string, arguments ...string) {
		session, _ := app.getAIPlaylistSession(token)
		data := []string{action, token}
		if action != "r_open" {
			data = append(data, strconv.FormatUint(session.reviewRevision, 36))
		}
		data = append(data, arguments...)
		api.ProcessUpdate(tb.Update{Callback: &tb.Callback{
			ID: "callback", Sender: &tb.User{ID: 7}, Message: message, Data: "\fai_playlist|" + strings.Join(data, "|"),
		}})
	}
	wait := func(content string) {
		maintWait(t, calls, "editMessageText", func(call maintCall) bool {
			return strings.Contains(call.text, content) || strings.Contains(call.markup, content)
		})
	}
	click("r_open")
	wait("傍晚")
	session, _ := app.getAIPlaylistSession(token)
	if len(session.review.playlists) != 3 {
		t.Fatalf("ordinary playlist list: %+v", session.review.playlists)
	}
	pick := func(id string) {
		current, _ := app.getAIPlaylistSession(token)
		for i, item := range current.review.playlists {
			if item.ID == id {
				click("r_pick", strconv.Itoa(i))
				return
			}
		}
		t.Fatalf("playlist %q missing", id)
	}
	pick("empty")
	wait("发送一句本次检查用的主题")
	click("r_ls")
	wait("傍晚")
	pick("infer")
	wait("适合傍晚听的摇滚")
	click("r_ls")
	wait("傍晚")
	pick("saved")
	wait("歌单描述\n安静")
	if inferenceCalls.Load() != 1 {
		t.Fatalf("theme inference calls = %d", inferenceCalls.Load())
	}
	click("r_scan")
	wait("重金属编曲偏离安静主题")
	click("r_add")
	wait("听感安静")
	session, _ = app.getAIPlaylistSession(token)
	if len(session.review.selected) != 0 || len(session.review.report.add) != 1 || session.review.report.add[0].ID != "valid" {
		t.Fatalf("unexpected preview: %+v", session.review)
	}
}
