package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/aichat"
	"github.com/nichuanfang/gymdl/internal/musicrag"
	"github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v4"
)

func TestParsePlaylistSuggestionsRequiresConfiguredExactNames(t *testing.T) {
	options := []config.PlaylistInfo{
		{Name: "华语"},
		{Name: "女声"},
		{Name: "摇滚"},
	}

	got, ok := parsePlaylistSuggestions("华语, 女声，华语", options)
	if !ok || strings.Join(got, ",") != "华语,女声" {
		t.Fatalf("valid suggestions = %#v, %v", got, ok)
	}
	if got, ok := parsePlaylistSuggestions("未知", options); !ok || len(got) != 0 {
		t.Fatalf("valid no-match response = %#v, %v", got, ok)
	}
	for _, noMatch := range []string{"[]", "```json\n[]\n```", `{"playlists":[]}`} {
		if got, ok := parsePlaylistSuggestions(noMatch, options); !ok || len(got) != 0 {
			t.Errorf("valid no-match response %q = %#v, %v", noMatch, got, ok)
		}
	}
	for _, invalid := range []string{"华语,不存在", "", `{"playlists":["华语","不存在"]}`} {
		if got, ok := parsePlaylistSuggestions(invalid, options); ok {
			t.Errorf("invalid suggestion %q accepted: %#v", invalid, got)
		}
	}
}

func TestCreateAIPlaylistReusesMarkerAndRejectsDuplicateNames(t *testing.T) {
	var playlists []navidromeAPIPlaylist
	var createCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth/login" && r.Method == http.MethodPost:
			writeJSON(t, w, map[string]string{"token": "test-token"})
		case r.URL.Path == "/api/playlist" && r.Method == http.MethodGet:
			if r.Header.Get("X-Nd-Authorization") != "Bearer test-token" {
				t.Errorf("playlist listing missing bearer token: %q", r.Header.Get("X-Nd-Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeJSON(t, w, playlists)
		case r.URL.Path == "/api/playlist" && r.Method == http.MethodPost:
			if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Nd-Authorization") != "Bearer test-token" {
				t.Errorf("playlist creation headers incorrect: %#v", r.Header)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var body struct {
				Name    string `json:"name"`
				Comment string `json:"comment"`
				Public  bool   `json:"public"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create playlist body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if body.Name != "[AI] 夜间" || !strings.HasPrefix(body.Comment, "gymdl-ai-playlist-create:") || body.Public {
				t.Errorf("unexpected new playlist payload: %#v", body)
			}
			createCalls++
			playlist := navidromeAPIPlaylist{ID: "playlist1", Name: body.Name, Comment: body.Comment}
			playlists = append(playlists, playlist)
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, playlist)
		default:
			t.Errorf("unexpected Navidrome request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := &playlistOrganizerService{
		config: &config.PlaylistOrganizerConfig{NavidromeURL: server.URL, NavidromeUsername: "user", NavidromePassword: "pass"},
		client: server.Client(),
	}
	marker := "gymdl-ai-playlist-create:operation1"
	created, err := service.createAIPlaylist(context.Background(), "[AI] 夜间", marker)
	if err != nil || created.ID != "playlist1" || created.Description != marker {
		t.Fatalf("createAIPlaylist = %#v, err=%v", created, err)
	}
	reused, err := service.createAIPlaylist(context.Background(), "[AI] 夜间", marker)
	if err != nil || reused.ID != "playlist1" || createCalls != 1 {
		t.Fatalf("retry should reuse created playlist: playlist=%#v err=%v createCalls=%d", reused, err, createCalls)
	}
	if _, err := service.createAIPlaylist(context.Background(), "[AI] 夜间", "gymdl-ai-playlist-create:different-marker"); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("duplicate name should be rejected before creation: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("duplicate name unexpectedly created a second playlist: %d", createCalls)
	}
}

func TestParsePlaylistSuggestionsAcceptsCommonModelFormatting(t *testing.T) {
	options := []config.PlaylistInfo{
		{Name: "华语"},
		{Name: "女声"},
		{Name: "摇滚"},
		{Name: "Chillhop&HipHop(Inst)"},
		{Name: "Hiphop"},
	}
	for _, test := range []struct {
		name   string
		output string
		want   []string
	}{
		{name: "markdown and parenthetical", output: "推荐：**华语**（匹配度较高）", want: []string{"华语"}},
		{name: "numbered lines", output: "1. 华语\n2. 女声", want: []string{"华语", "女声"}},
		{name: "json array", output: `["华语","女声"]`, want: []string{"华语", "女声"}},
		{name: "json object", output: `{"playlists":["华语","女声"]}`, want: []string{"华语", "女声"}},
		{name: "configured name ending in punctuation", output: "Chillhop&HipHop(Inst)", want: []string{"Chillhop&HipHop(Inst)"}},
		{name: "punctuated name with another suggestion", output: "Chillhop&HipHop(Inst),Hiphop", want: []string{"Chillhop&HipHop(Inst)", "Hiphop"}},
		{name: "json array preserves punctuation in configured names", output: `["Chillhop&HipHop(Inst)","Hiphop"]`, want: []string{"Chillhop&HipHop(Inst)", "Hiphop"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parsePlaylistSuggestions(test.output, options)
			if !ok || strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Fatalf("parsePlaylistSuggestions(%q) = %#v, %v; want %#v", test.output, got, ok, test.want)
			}
		})
	}
}

func TestPlaylistAssignmentUsesAIForEverySongWhenEnabled(t *testing.T) {
	for _, test := range []struct {
		name           string
		playlistAssist bool
		peerPlaylists  string
		want           bool
	}{
		{name: "AI enabled and no peers", playlistAssist: true, want: true},
		{name: "AI disabled and no peers", playlistAssist: false, want: false},
		{name: "AI enabled with peer playlists", playlistAssist: true, peerPlaylists: "华语,女声", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := playlistAssignmentUsesAI(test.playlistAssist, unplaylistSong{Playlists: test.peerPlaylists})
			if got != test.want {
				t.Fatalf("playlistAssignmentUsesAI() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPlaylistAssignmentAIAvailabilityRequiresEnabledClient(t *testing.T) {
	oldGlobalAI := core.GlobalAI
	t.Cleanup(func() { core.GlobalAI = oldGlobalAI })
	core.GlobalAI = nil
	searchTool := aichat.Tool{
		Name: "mcp_webtool_websearch",
		Parameters: map[string]any{"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		}},
		Run: func(context.Context, map[string]any) (string, error) { return "search results", nil },
	}
	app := &BotApp{cfg: &config.Config{AI: &config.AIConfig{Enable: true}}, aiMCPTools: []aichat.Tool{searchTool}}
	if app.playlistAssignmentAIAvailable() {
		t.Fatal("AI should be unavailable without the initialized client")
	}
	core.GlobalAI = &core.AI{}
	if app.playlistAssignmentAIAvailable() != true {
		t.Fatal("AI should be available when enabled and initialized")
	}
	app.cfg.AI.Enable = false
	if app.playlistAssignmentAIAvailable() {
		t.Fatal("AI should be unavailable when disabled")
	}
}

func TestPlaylistAssignmentAISuggestionsCallsConfiguredModel(t *testing.T) {
	oldGlobalAI := core.GlobalAI
	oldLogger := logger
	t.Cleanup(func() { core.GlobalAI = oldGlobalAI })
	logger = zap.NewNop()
	t.Cleanup(func() { logger = oldLogger })
	var calls int
	var prompt string
	classifierOutput := "华语"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("unexpected AI request: %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode AI request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Model != "classifier-test" || len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
			t.Errorf("unexpected AI request shape: %#v", request)
		}
		prompt = request.Messages[1].Content
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + classifierOutput + `"}}]}`))
	}))
	defer server.Close()

	clientConfig := openai.DefaultConfig("test-key")
	clientConfig.BaseURL = server.URL + "/v1"
	clientConfig.HTTPClient = server.Client()
	core.GlobalAI = &core.AI{
		Config: &config.AIConfig{Model: "classifier-test", SystemPrompt: "base prompt"},
		Client: openai.NewClientWithConfig(clientConfig),
	}
	webSearchCalls := 0
	webSearch := aichat.Tool{
		Name: "mcp_webtool_websearch",
		Parameters: map[string]any{
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
			"required":   []string{"query"},
		},
		Run: func(_ context.Context, args map[string]any) (string, error) {
			webSearchCalls++
			query, _ := args["query"].(string)
			if !strings.Contains(query, "夜行 歌手") {
				t.Errorf("WebSearch query = %q", query)
			}
			return "Search evidence: this song is jazz and soul.", nil
		},
	}
	app := &BotApp{
		cfg:        &config.Config{AI: &config.AIConfig{Enable: true}},
		aiMCPTools: []aichat.Tool{webSearch},
	}
	assignment := pendingPlaylistAssignment{
		playlistAssist: true,
		playlists:      []config.PlaylistInfo{{Name: "华语", Desc: "中文歌曲"}},
	}
	song := unplaylistSong{ID: "song-1", Title: "夜行", Artist: "歌手", Comment: "https://music.163.com/#/song?id=12345"}
	got, unavailable := app.playlistAssignmentAISuggestions(context.Background(), song, assignment)
	if unavailable || len(got) != 1 || got[0] != "华语" {
		t.Fatalf("AI suggestions = %#v, unavailable=%v", got, unavailable)
	}
	if calls != 1 || webSearchCalls != 1 || !strings.Contains(prompt, "歌名：夜行") || !strings.Contains(prompt, "https://music.163.com/#/song?id=12345") || !strings.Contains(prompt, "华语：中文歌曲") || !strings.Contains(prompt, "Search evidence: this song is jazz and soul") {
		t.Fatalf("AI classifier was not called with song, playlists, and WebSearch evidence: AI calls=%d WebSearch calls=%d prompt=%q", calls, webSearchCalls, prompt)
	}

	assignment.playlistAssist = false
	app.playlistAssignmentAISuggestions(context.Background(), song, assignment)
	if calls != 1 || webSearchCalls != 1 {
		t.Fatalf("disabled AI assistance should not call AI or WebSearch, AI=%d WebSearch=%d", calls, webSearchCalls)
	}
	assignment.playlistAssist = true
	song.Playlists = "华语"
	app.playlistAssignmentAISuggestions(context.Background(), song, assignment)
	if calls != 2 || webSearchCalls != 2 {
		t.Fatalf("AI classifier should also run with peer playlist context, AI=%d WebSearch=%d", calls, webSearchCalls)
	}
	app.aiMCPTools[0].Run = func(context.Context, map[string]any) (string, error) {
		return "", errors.New("WebSearch unavailable")
	}
	_, unavailable = app.playlistAssignmentAISuggestions(context.Background(), song, assignment)
	if !unavailable || calls != 2 {
		t.Fatalf("classification without WebSearch evidence should fail closed, unavailable=%v AI calls=%d", unavailable, calls)
	}
	app.aiMCPTools[0].Run = webSearch.Run
	classifierOutput = "未知"
	suggestions, unavailable := app.playlistAssignmentAISuggestions(context.Background(), song, assignment)
	if unavailable || len(suggestions) != 0 {
		t.Fatalf("explicit no-match response = %#v, unavailable=%v", suggestions, unavailable)
	}
	classifierOutput = "华语（推荐）"
	suggestions, unavailable = app.playlistAssignmentAISuggestions(context.Background(), song, assignment)
	if unavailable || len(suggestions) != 1 || suggestions[0] != "华语" {
		t.Fatalf("formatted classifier recommendation = %#v, unavailable=%v", suggestions, unavailable)
	}
	classifierPrompt := playlistClassifierPrompt(song, assignment.playlists, "evidence")
	if !strings.Contains(classifierPrompt, "无匹配时返回 []") || !strings.Contains(classifierPrompt, "搜索资料不足时") {
		t.Fatal("classifier prompt should request a best-fit recommendation and a clear no-match result")
	}
	classifierOutput = "华语, 不在可选列表"
	_, unavailable = app.playlistAssignmentAISuggestions(context.Background(), song, assignment)
	if !unavailable {
		t.Fatal("a response outside the playlist whitelist should be reported as an unusable AI suggestion")
	}
}

func TestNormalizePlaylistOptionsFiltersUnsafeNamesAndDeduplicates(t *testing.T) {
	got, skipped := normalizePlaylistOptions([]config.PlaylistInfo{
		{Name: " 华语 ", Desc: "语言"},
		{Name: "华语", Desc: "重复"},
		{Name: "摇滚,金属"},
		{Name: "歌手\n名"},
		{Name: "女声"},
	})
	if len(got) != 2 || got[0].Name != "华语" || got[1].Name != "女声" {
		t.Fatalf("normalized options = %#v", got)
	}
	if strings.Join(skipped, ",") != "摇滚,金属,歌手\n名" {
		t.Fatalf("skipped names = %#v", skipped)
	}
}

func TestPlaylistOrganizerOliveTinActions(t *testing.T) {
	var actions []oliveTinActionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/StartActionAndWait" || r.Method != http.MethodPost {
			t.Fatalf("unexpected OliveTin request: %s %s", r.Method, r.URL.Path)
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "olive-user" || password != "olive-pass" {
			t.Fatalf("unexpected OliveTin basic auth")
		}
		var action oliveTinActionRequest
		if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
			t.Fatalf("decode OliveTin request: %v", err)
		}
		actions = append(actions, action)
		output := `[{"id":"song-1","title":"Song","artist":"Artist","album":"Album","year":2022,"comment":"https://music.test/song","created_at":"2026-10-01T00:00:00Z","playlists":"华语","remaining_counts":3}]`
		if action.ActionID == "playlists_without_track" {
			output = `[{"playlist_id":"playlist-1","playlist_name":"华语"}]`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": output, "timedOut": false}})
	}))
	defer server.Close()

	service, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		OliveTinUsername:  "olive-user",
		OliveTinPassword:  "olive-pass",
		NavidromeURL:      server.URL,
		NavidromeUsername: "nav-user",
		NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	song, err := service.firstUnassignedSong(context.Background())
	if err != nil || song.ID != "song-1" || song.RemainingCount != 3 || song.Year != 2022 {
		t.Fatalf("first unassigned song = %#v, err=%v", song, err)
	}
	playlists, err := service.playlistsWithoutSong(context.Background(), song.ID, []string{"华语", "女声"})
	if err != nil || len(playlists) != 1 || playlists[0].ID != "playlist-1" || playlists[0].Name != "华语" {
		t.Fatalf("playlists without song = %#v, err=%v", playlists, err)
	}
	if len(actions) != 2 || actions[0].ActionID != "songs_unplaylist" || actions[1].ActionID != "playlists_without_track" {
		t.Fatalf("OliveTin action calls = %#v", actions)
	}
	if len(actions[1].Arguments) != 2 || actions[1].Arguments[0] != (oliveTinArgument{Name: "songId", Value: "song-1"}) || actions[1].Arguments[1] != (oliveTinArgument{Name: "playlists", Value: "华语,女声"}) {
		t.Fatalf("playlist query args = %#v", actions[1].Arguments)
	}
}

func TestNextUnassignedSongRejectsStaleHandledRow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"logEntry": map[string]any{
			"exitCode": 0,
			"stdout":   `{"id":"song-1","title":"Song","artist":"Artist"}`,
			"timedOut": false,
		}})
	}))
	defer server.Close()

	service, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{OliveTinURL: server.URL, NavidromeURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.nextUnassignedSong(context.Background(), map[string]struct{}{"song-1": {}})
	if !errors.Is(err, errUnplaylistQueryStale) {
		t.Fatalf("handled row should not look like an empty queue, err=%v", err)
	}
}

func TestRunOliveTinActionReadsOutputField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"logEntry": map[string]any{
			"exitCode": 0,
			"output":   `[{"id":"song-1"}]`,
			"timedOut": false,
		}})
	}))
	defer server.Close()
	service, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		OliveTinUsername:  "olive-user",
		OliveTinPassword:  "olive-pass",
		NavidromeURL:      server.URL,
		NavidromeUsername: "nav-user",
		NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := service.runOliveTinAction(context.Background(), oliveTinActionRequest{ActionID: "songs_unplaylist"})
	if err != nil || output != `[{"id":"song-1"}]` {
		t.Fatalf("OliveTin output = %q, err=%v", output, err)
	}
}

func TestPlaylistOrganizerAllowsOliveTinWithoutBasicAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); ok {
			t.Fatal("unexpected OliveTin Basic Auth")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"logEntry": map[string]any{
			"exitCode": 0,
			"stdout":   `[]`,
			"timedOut": false,
		}})
	}))
	defer server.Close()
	service, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		NavidromeURL:      server.URL,
		NavidromeUsername: "nav-user",
		NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.runOliveTinAction(context.Background(), oliveTinActionRequest{ActionID: "songs_unplaylist"}); err != nil {
		t.Fatal(err)
	}
}

func TestPlaylistOrganizerRejectsPartialOliveTinBasicAuth(t *testing.T) {
	_, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       "http://olivetin.test",
		OliveTinUsername:  "olive-user",
		NavidromeURL:      "http://navidrome.test",
		NavidromeUsername: "nav-user",
		NavidromePassword: "nav-pass",
	})
	if err == nil || !strings.Contains(err.Error(), "必须同时配置") {
		t.Fatalf("partial OliveTin Basic Auth should fail, err=%v", err)
	}
}

func TestNavidromeLoginAndAddSongToPlaylist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || !strings.HasSuffix(r.Header.Get("Referer"), "/app/") {
				t.Fatalf("unexpected Navidrome login request: %#v", r)
			}
			var credentials map[string]string
			if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil {
				t.Fatal(err)
			}
			if credentials["username"] != "nav-user" || credentials["password"] != "nav-pass" {
				t.Fatalf("unexpected Navidrome login body")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-jwt"})
		case "/api/playlist/playlist-1/tracks":
			if r.Method != http.MethodPost || r.Header.Get("X-Nd-Authorization") != "Bearer test-jwt" || r.Header.Get("X-Nd-Client-Unique-Id") == "" {
				t.Fatalf("unexpected Navidrome add request: %#v", r)
			}
			var payload struct {
				IDs []string `json:"ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.IDs) != 1 || payload.IDs[0] != "song-1" {
				t.Fatalf("unexpected Navidrome add body: %#v", payload)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected Navidrome path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	service, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		OliveTinUsername:  "olive-user",
		OliveTinPassword:  "olive-pass",
		NavidromeURL:      server.URL,
		NavidromeUsername: "nav-user",
		NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.navidromeLogin(context.Background())
	if err != nil || token != "test-jwt" {
		t.Fatalf("Navidrome login = %q, err=%v", token, err)
	}
	if err := service.addSongToPlaylist(context.Background(), token, "song-1", navidromePlaylist{ID: "playlist-1", Name: "华语"}); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceAIPlaylistTracksClearsInBoundedPositionBatches(t *testing.T) {
	loginCalls := 0
	var deleteBatchSizes []int
	addCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			loginCalls++
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-jwt"})
		case "/api/playlist/playlist-1/tracks":
			if r.Header.Get("X-Nd-Authorization") != "Bearer test-jwt" {
				t.Fatal("missing Navidrome bearer token")
			}
			switch r.Method {
			case http.MethodDelete:
				positions := r.URL.Query()["id"]
				deleteBatchSizes = append(deleteBatchSizes, len(positions))
				for index, position := range positions {
					if position != strconv.Itoa(index+1) {
						t.Errorf("delete batch positions = %#v", positions)
					}
				}
				w.WriteHeader(http.StatusOK)
			case http.MethodPost:
				addCalls++
				var request struct {
					IDs []string `json:"ids"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !reflect.DeepEqual(request.IDs, []string{"new-song"}) {
					t.Fatalf("unexpected replacement add request: %#v err=%v", request, err)
				}
				w.WriteHeader(http.StatusOK)
			default:
				t.Fatalf("unexpected Navidrome method %s", r.Method)
			}
		default:
			t.Fatalf("unexpected Navidrome path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	service, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		NavidromeURL:      server.URL,
		NavidromeUsername: "nav-user",
		NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.replaceAIPlaylistTracks(context.Background(), "playlist-1", 101, []string{"new-song"}); err != nil {
		t.Fatal(err)
	}
	if loginCalls != 1 || addCalls != 1 || !reflect.DeepEqual(deleteBatchSizes, []int{100, 1}) {
		t.Fatalf("login=%d delete batches=%#v add=%d", loginCalls, deleteBatchSizes, addCalls)
	}
}

func TestAddSongToPlaylistsVerifiesWriteAndAvoidsDuplicates(t *testing.T) {
	added := false
	queries, logins, inserts := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/StartActionAndWait":
			queries++
			var request oliveTinActionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.ActionID != "playlists_without_track" {
				t.Fatalf("unexpected OliveTin action %q", request.ActionID)
			}
			stdout := `[{"playlist_id":"playlist-1","playlist_name":"华语"}]`
			if added {
				stdout = "[]"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout}})
		case "/auth/login":
			logins++
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-jwt"})
		case "/api/playlist/playlist-1/tracks":
			inserts++
			if r.Header.Get("X-Nd-Authorization") != "Bearer test-jwt" {
				t.Fatal("missing Navidrome bearer token")
			}
			added = true
			_ = json.NewEncoder(w).Encode(map[string]int{"added": 1})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	service, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		OliveTinUsername:  "olive-user",
		OliveTinPassword:  "olive-pass",
		NavidromeURL:      server.URL,
		NavidromeUsername: "nav-user",
		NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := service.addSongToPlaylists(context.Background(), "song-1", []string{"华语"})
	if err != nil || !changed || queries != 2 || logins != 1 || inserts != 1 {
		t.Fatalf("first add = changed %v, err %v, queries %d, logins %d, inserts %d", changed, err, queries, logins, inserts)
	}
	changed, err = service.addSongToPlaylists(context.Background(), "song-1", []string{"华语"})
	if err != nil || changed || queries != 3 || logins != 1 || inserts != 1 {
		t.Fatalf("duplicate check = changed %v, err %v, queries %d, logins %d, inserts %d", changed, err, queries, logins, inserts)
	}
}

func TestAddSongToPlaylistsDoesNotTreatUnverifiedWriteAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/StartActionAndWait":
			_ = json.NewEncoder(w).Encode(map[string]any{"logEntry": map[string]any{
				"exitCode": 0,
				"stdout":   `[{"playlist_id":"playlist-1","playlist_name":"华语"}]`,
			}})
		case "/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-jwt"})
		case "/api/playlist/playlist-1/tracks":
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	service, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		OliveTinUsername:  "olive-user",
		OliveTinPassword:  "olive-pass",
		NavidromeURL:      server.URL,
		NavidromeUsername: "nav-user",
		NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := service.addSongToPlaylists(context.Background(), "song-1", []string{"华语"})
	if !changed || err == nil || !strings.Contains(err.Error(), "still reports") {
		t.Fatalf("unverified write = changed %v, err %v", changed, err)
	}
}

func TestMusicRAGSinkInsertsOnlyWhenSongIsMissing(t *testing.T) {
	sink := newMusicRAGSink(openBotMusicRAG(t))
	song := unplaylistSong{ID: "song-1", Title: "Title", Artist: "Artist", Album: "Album", Year: 2024, CreatedAt: "2026-10-01", Comment: "https://music.test/song"}
	if err := sink.enqueue(context.Background(), song); err != nil {
		t.Fatal(err)
	}
	song.Title = "另一首"
	if err := sink.enqueue(context.Background(), song); err != nil {
		t.Fatal(err)
	}
	pending, err := sink.store.Pending(context.Background(), 10)
	if err != nil || len(pending) != 1 || pending[0].Title != "Title" || pending[0].Year != "2024" || pending[0].URL != "https://music.test/song" {
		t.Fatalf("stored=%#v err=%v", pending, err)
	}
}

func openBotMusicRAG(t *testing.T) *musicrag.Store {
	t.Helper()
	store, err := musicrag.Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestParseN8NDataTableRowsRejectsMissingOrNonArrayData(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":{}}`, `{"other":[]}`} {
		if _, err := parseN8NDataTableRows([]byte(body)); err == nil {
			t.Errorf("parseN8NDataTableRows(%s) succeeded; want error", body)
		}
	}
}

func TestMusicRAGSinkSkipsExistingRowAndRequiresConfig(t *testing.T) {
	sink := newMusicRAGSink(openBotMusicRAG(t))
	if err := sink.store.Enqueue(context.Background(), musicrag.Song{MusicID: "song-1", Title: "原标题", Status: musicrag.StatusReady}); err != nil {
		t.Fatal(err)
	}
	if err := sink.enqueue(context.Background(), unplaylistSong{ID: "song-1", Title: "新标题"}); err != nil {
		t.Fatal(err)
	}
	pending, err := sink.store.Pending(context.Background(), 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("ready row was reset: %#v err=%v", pending, err)
	}
	if err := newMusicRAGSink(nil).enqueue(context.Background(), unplaylistSong{ID: "song-1"}); err != errMusicRAGNotConfigured {
		t.Fatalf("missing bridge config error = %v", err)
	}
}

func TestPlaylistAssignmentSessionLocksChatAndAcceptsWebAppSelection(t *testing.T) {
	app := &BotApp{}
	first := pendingPlaylistAssignment{
		token:          "first",
		chatID:         100,
		song:           unplaylistSong{ID: "song-1"},
		playlists:      []config.PlaylistInfo{{Name: "华语"}, {Name: "摇滚"}},
		selectionToken: "selection-1",
		webAppBaseURL:  "https://webapp.example.com",
		handledSongIDs: map[string]struct{}{},
		expiresAt:      time.Now().Add(time.Minute),
	}
	if !app.registerPlaylistAssignment(first) {
		t.Fatal("first session should register")
	}
	if app.registerPlaylistAssignment(pendingPlaylistAssignment{token: "second", chatID: 100, expiresAt: time.Now().Add(time.Minute)}) {
		t.Fatal("same chat should not have two active sessions")
	}
	assignment, ok := app.getPlaylistAssignment("first")
	if !ok || assignment.selectionToken != "selection-1" {
		t.Fatalf("WebApp selection state was not preserved: %#v", assignment)
	}
	assignment, err := app.acceptPlaylistAssignmentSelection("first", "selection-1", []string{"摇滚"})
	if err != nil || !assignment.selectionComplete || len(assignment.selectedPlaylists) != 1 || assignment.selectedPlaylists[0] != "摇滚" {
		t.Fatalf("WebApp selection was not accepted: %#v, err=%v", assignment, err)
	}
	if _, err := app.acceptPlaylistAssignmentSelection("first", "selection-1", []string{"摇滚"}); err != nil {
		t.Fatalf("idempotent callback retry failed: %v", err)
	}
	if _, err := app.acceptPlaylistAssignmentSelection("first", "selection-1", []string{"华语"}); err == nil {
		t.Fatal("same WebApp token should not accept a different selection")
	}
	if _, ok := app.beginPlaylistAssignment("first"); !ok {
		t.Fatal("confirmed selection session should enter processing")
	}
	if _, ok := app.beginPlaylistAssignmentReselection("first"); ok {
		t.Fatal("processing session must reject WebApp reselection")
	}
}

func TestPlaylistAssignmentCallbackDataFitsTelegramLimit(t *testing.T) {
	markup := playlistAssignmentMarkup(pendingPlaylistAssignment{
		token:             "01234567890123456789012345678901",
		song:              unplaylistSong{ID: "song-1"},
		selectionComplete: true,
		selectedPlaylists: []string{"摇滚"},
	})
	if len(markup.InlineKeyboard) != 2 {
		t.Fatalf("keyboard rows = %d, want primary actions and secondary actions", len(markup.InlineKeyboard))
	}
	for _, row := range markup.InlineKeyboard {
		if len(row) != 2 {
			t.Errorf("keyboard row has %d buttons; want paired buttons: %#v", len(row), row)
		}
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if len(button.Data) > 64 {
				t.Errorf("callback data exceeds Telegram limit: %d bytes", len(button.Data))
			}
		}
	}
}

func TestPlaylistAssignmentUsesLegacyWebAppForEachSong(t *testing.T) {
	menu := pendingPlaylistAssignment{
		token:           "01234567890123456789012345678901",
		playlists:       []config.PlaylistInfo{{Name: "华语"}, {Name: "摇滚"}},
		song:            unplaylistSong{ID: "song-1", Title: "Song", Artist: "Artist"},
		webAppButtonURL: "https://webapp.example.com/?data=%7B%7D",
	}
	if text := formatPlaylistAssignment(menu); !strings.Contains(text, "第 1 首") || strings.Contains(text, "还没选歌单") || strings.Contains(text, "打开歌单选择页面") {
		t.Fatalf("per-song selection copy = %q", text)
	}
	menuMarkup := playlistAssignmentMarkup(menu)
	if got := len(menuMarkup.InlineKeyboard); got != 2 {
		t.Fatalf("song preview rows = %d, want WebApp and paired secondary actions", got)
	}
	if got := menuMarkup.InlineKeyboard[0][0].Text; got != "📂 选择歌单" || menuMarkup.InlineKeyboard[0][0].WebApp == nil || menuMarkup.InlineKeyboard[0][0].WebApp.URL != menu.webAppButtonURL {
		t.Errorf("song should open the legacy WebApp selector, got %#v", menuMarkup.InlineKeyboard[0][0])
	}
	if got := []string{menuMarkup.InlineKeyboard[1][0].Text, menuMarkup.InlineKeyboard[1][1].Text}; !reflect.DeepEqual(got, []string{"跳过本首", "结束整理"}) {
		t.Errorf("secondary song actions = %#v", got)
	}
	for _, row := range menuMarkup.InlineKeyboard {
		for _, button := range row {
			if button.Data != "" && (button.Unique != playlistAssignmentCallbackUnique || len(button.Data) > 64) {
				t.Errorf("invalid callback button: %#v", button)
			}
		}
	}
	menu.selectionComplete = true
	menu.selectedPlaylists = []string{"华语", "摇滚"}
	songMarkup := playlistAssignmentMarkup(menu)
	var labels []string
	for _, row := range songMarkup.InlineKeyboard {
		for _, button := range row {
			labels = append(labels, button.Text)
		}
	}
	if strings.Join(labels, ",") != "✅ 确认加入,重新选择,跳过本首,结束整理" {
		t.Fatalf("song controls = %#v", labels)
	}
	if text := formatPlaylistAssignment(menu); !strings.Contains(text, "放入 华语、摇滚") || !strings.Contains(text, "整理歌单") || strings.Contains(text, "还没选歌单") {
		t.Fatalf("song preview copy = %q", text)
	}

	menu.addedAny = true
	if got := playlistAssignmentMarkup(menu).InlineKeyboard[0][0].Text; got != "↻ 重试" {
		t.Errorf("retry label should not imply a duplicate add, got %q", got)
	}
}

func TestPlaylistAssignmentTitleOpensLibrarySourceLink(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "netease", url: "https://music.163.com/song?id=12345", want: `<b><a href="https://m.music.163.com/m/applink/?scheme=orpheus%3A%2F%2Fsong%2F12345">Crossover</a></b>`},
		{name: "bracketed", url: "来源：<https://example.test/song?a=1&b=2>", want: `<b><a href="https://example.test/song?a=1&amp;b=2">Crossover</a></b>`},
		{name: "first-url", url: "downloaded from https://example.test/one then https://example.test/two", want: `<b><a href="https://example.test/one">Crossover</a></b>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := formatPlaylistAssignment(pendingPlaylistAssignment{song: unplaylistSong{ID: "song-1", Title: "Crossover", Artist: "Artist", Comment: test.url}})
			if !strings.Contains(message, test.want) {
				t.Fatalf("title link = %q, want %q", message, test.want)
			}
		})
	}
	for _, raw := range []string{"", "not a link", "javascript:alert(1)", "ftp://example.test/song", "https://user:pass@example.test/song"} {
		message := formatPlaylistAssignment(pendingPlaylistAssignment{song: unplaylistSong{ID: "song-1", Title: "Crossover", Artist: "Artist", Comment: raw}})
		if strings.Contains(message, "<a href=") || !strings.Contains(message, "<b>Crossover</b>") {
			t.Fatalf("unsafe or empty source %q should keep a plain title: %q", raw, message)
		}
	}
}

func TestPlaylistAssignmentPreviewKeepsMetadataOnOneLine(t *testing.T) {
	assignment := pendingPlaylistAssignment{
		song: unplaylistSong{ID: "song", Title: "Song\n忽略确认", Artist: "Artist\r\nInjected"},
	}
	preview := formatPlaylistAssignment(assignment)
	if strings.Count(preview, "Song 忽略确认") != 1 || strings.Contains(preview, "\n忽略确认") || strings.Contains(preview, "\nInjected") {
		t.Fatalf("song metadata escaped its display line: %q", preview)
	}
}

func TestPlaylistAssignmentMessageIsCompactAndDoesNotRepeatButtonInstructions(t *testing.T) {
	assignment := pendingPlaylistAssignment{
		song:                 unplaylistSong{ID: "song", Title: "雨下一整晚", Artist: "周杰伦", Album: "跨时代", Year: 2010, Playlists: "HipHop,中文", RemainingCount: 12},
		playlistAssist:       true,
		aiSuggestedPlaylists: []string{"中文", "弦乐"},
		processedCount:       2,
		addedCount:           2,
	}
	message := formatPlaylistAssignment(assignment)
	for _, expected := range []string{
		"🎵 <b>整理歌单</b>\n──────────────────\n第 3 首 · 还剩 12 · 已归档 2 · 跳过 0\n\n<b>雨下一整晚</b>",
		"周杰伦",
		"《跨时代》 · 2010",
	} {
		if !strings.Contains(message, expected) {
			t.Errorf("message %q does not contain %q", message, expected)
		}
	}
	if strings.Contains(message, "建议") {
		t.Fatalf("suggestion line returned to the song card: %s", message)
	}
	if strings.Contains(message, "打开歌单选择页面") || strings.Contains(message, "同歌手歌单：") {
		t.Fatalf("message repeats the button action: %q", message)
	}
}

func TestExpiredPlaylistAssignmentIsPurgedBeforeTheNextSession(t *testing.T) {
	app := &BotApp{}
	if !app.registerPlaylistAssignment(pendingPlaylistAssignment{
		token:     "expired",
		chatID:    10,
		expiresAt: time.Now().Add(-time.Second),
	}) {
		t.Fatal("failed to register expired assignment fixture")
	}
	if app.hasPlaylistAssignmentForChat(10) {
		t.Fatal("expired assignment should not block a new command")
	}
	if _, ok := app.getPlaylistAssignment("expired"); ok {
		t.Fatal("expired assignment should be removed")
	}
}

func TestPlaylistAssignmentStatusExplainsAIBehavior(t *testing.T) {
	app := &BotApp{}
	tests := []struct {
		name       string
		assignment pendingPlaylistAssignment
		want       string
	}{
		{
			name: "AI assistance disabled",
			assignment: pendingPlaylistAssignment{
				song: unplaylistSong{ID: "song-1", Title: "Song"},
			},
			want: "",
		},
		{
			name: "AI suggestions available",
			assignment: pendingPlaylistAssignment{
				song:                 unplaylistSong{ID: "song-1", Title: "Song"},
				playlistAssist:       true,
				aiSuggestedPlaylists: []string{"摇滚"},
			},
			want: "摇滚",
		},
		{
			name: "peer playlist details are omitted from the concise status",
			assignment: pendingPlaylistAssignment{
				song: unplaylistSong{ID: "song-1", Playlists: "华语"},
			},
			want: "",
		},
		{
			name: "AI suggestion unavailable",
			assignment: pendingPlaylistAssignment{
				song:                    unplaylistSong{ID: "song-1", Title: "Song"},
				playlistAssist:          true,
				aiSuggestionUnavailable: true,
			},
			want: "这次没有建议",
		},
		{
			name: "AI suggestions finish before the picker is shown",
			assignment: pendingPlaylistAssignment{
				song:                unplaylistSong{ID: "song-1", Title: "Song"},
				playlistAssist:      true,
				aiSuggestionLoading: true,
			},
			want: "生成中",
		},
		{
			name: "manual retry says it is reorganizing",
			assignment: pendingPlaylistAssignment{
				song:                unplaylistSong{ID: "song-1", Title: "Song"},
				playlistAssist:      true,
				aiSuggestionLoading: true,
				aiSuggestionManual:  true,
			},
			want: "正在重新整理",
		},
		{
			name: "search completed without reliable match",
			assignment: pendingPlaylistAssignment{
				song:           unplaylistSong{ID: "song-1", Title: "Song"},
				playlistAssist: true,
			},
			want: "没有明显合适的",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := app.playlistAssignmentStatus(test.assignment)
			if test.want == "" {
				if got != "" {
					t.Fatalf("playlistAssignmentStatus() = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, test.want) {
				t.Fatalf("playlistAssignmentStatus() = %q, want it to contain %q", got, test.want)
			}
		})
	}
}

func TestPlaylistAssignmentSuggestionsUpdateChooserDefaultsWithoutLosingSession(t *testing.T) {
	app := &BotApp{}
	assignment := pendingPlaylistAssignment{
		token:               "assignment",
		chatID:              10,
		song:                unplaylistSong{ID: "song-1", Title: "Song"},
		playlists:           []config.PlaylistInfo{{Name: "华语"}, {Name: "摇滚"}},
		webAppBaseURL:       "https://webapp.example.com",
		selectionToken:      "01234567890123456789012345678901",
		webAppButtonURL:     "https://webapp.example.com/?old=1",
		aiSuggestionLoading: true,
		handledSongIDs:      map[string]struct{}{},
		aiSuggestionSeq:     1,
		expiresAt:           time.Now().Add(time.Minute),
	}
	if !app.registerPlaylistAssignment(assignment) {
		t.Fatal("failed to register assignment")
	}
	loadingMarkup := playlistAssignmentMarkup(assignment)
	loadingButtons := make(map[string]bool)
	foundLoadingChooser := false
	for _, row := range loadingMarkup.InlineKeyboard {
		for _, button := range row {
			if button.WebApp != nil {
				foundLoadingChooser = true
				if button.WebApp.URL != assignment.webAppButtonURL {
					t.Fatalf("loading chooser URL = %s, want the current page %s", button.WebApp.URL, assignment.webAppButtonURL)
				}
			}
			loadingButtons[button.Text] = true
		}
	}
	if foundLoadingChooser || loadingButtons["📂 选择歌单"] || loadingButtons["重新整理"] {
		t.Fatal("the chooser should stay hidden while suggestions load")
	}
	if !loadingButtons["跳过本首"] || !loadingButtons["结束整理"] {
		t.Fatalf("skip and end controls should remain available while AI loads: %#v", loadingButtons)
	}
	if text := formatPlaylistAssignment(assignment); !strings.Contains(text, "正在生成建议…") || strings.Contains(text, "Song") {
		t.Fatalf("loading card should use the original compact loading copy: %s", text)
	}
	updated, ok := app.storePlaylistAssignmentAISuggestions(assignment.token, "song-1", 1, []string{"摇滚"}, false)
	if !ok || updated.aiSuggestionLoading || len(updated.aiSuggestedPlaylists) != 1 || updated.aiSuggestedPlaylists[0] != "摇滚" {
		t.Fatalf("suggestions were not stored: %#v, ok=%t", updated, ok)
	}
	var page struct {
		Default []string `json:"default"`
	}
	parsed, err := url.Parse(updated.webAppButtonURL)
	if err != nil || json.Unmarshal([]byte(parsed.Query().Get("data")), &page) != nil || !reflect.DeepEqual(page.Default, []string{"摇滚"}) {
		t.Fatalf("WebApp chooser did not receive suggested defaults: URL=%q defaults=%#v err=%v", updated.webAppButtonURL, page.Default, err)
	}
	readyMarkup := playlistAssignmentMarkup(updated)
	foundChooser := false
	for _, row := range readyMarkup.InlineKeyboard {
		for _, button := range row {
			if button.WebApp != nil && button.WebApp.URL == updated.webAppButtonURL {
				foundChooser = true
			}
		}
	}
	if !foundChooser {
		t.Fatal("legacy chooser should appear after AI defaults are ready")
	}
	if _, ok := app.storePlaylistAssignmentAISuggestions(assignment.token, "another-song", 1, []string{"华语"}, false); ok {
		t.Fatal("stale background result must not overwrite a different song")
	}
}

func TestPlaylistAssignmentOffersAnotherAIPassBesideTheChooser(t *testing.T) {
	app := &BotApp{}
	assignment := pendingPlaylistAssignment{
		token:                "01234567890123456789012345678901",
		chatID:               10,
		song:                 unplaylistSong{ID: "song-1", Title: "雨下一整晚", Artist: "周杰伦"},
		playlists:            []config.PlaylistInfo{{Name: "华语"}, {Name: "摇滚"}},
		webAppBaseURL:        "https://webapp.example.com",
		webAppButtonURL:      "https://webapp.example.com/?data=%7B%7D",
		selectionToken:       "selection-old",
		playlistAssist:       true,
		aiSuggestedPlaylists: []string{"摇滚"},
		aiSuggestionSeq:      1,
		handledSongIDs:       map[string]struct{}{},
		expiresAt:            time.Now().Add(time.Minute),
	}
	if !app.registerPlaylistAssignment(assignment) {
		t.Fatal("failed to register assignment")
	}
	markup := playlistAssignmentMarkup(assignment)
	row := markup.InlineKeyboard[0]
	if len(row) != 2 || row[0].Text != "📂 选择歌单" || row[0].WebApp == nil || row[1].Text != "重新整理" {
		t.Fatalf("chooser row = %#v", row)
	}
	if row[1].Unique != playlistAssignmentCallbackUnique || len(row[1].Data) > 64 || !strings.Contains(row[1].Data, "redo") {
		t.Fatalf("reorganize callback = %#v", row[1])
	}
	if playlistAssignmentTemperature(assignment) != 0.1 {
		t.Fatal("first pass should stay deterministic")
	}
	updated, ok := app.beginPlaylistAssignmentReorganize(assignment.token)
	if !ok || !updated.aiSuggestionLoading || !updated.aiSuggestionManual || updated.aiSuggestionSeq != 2 || len(updated.aiSuggestedPlaylists) != 0 || updated.selectionToken != "selection-old" {
		t.Fatalf("reorganize did not start a fresh pass: %#v", updated)
	}
	if playlistAssignmentTemperature(updated) != 0.6 {
		t.Fatal("a manual retry should be allowed to differ")
	}
	if _, ok := app.storePlaylistAssignmentAISuggestions(assignment.token, "song-1", 1, []string{"华语"}, false); ok {
		t.Fatal("the previous AI pass overwrote the retry")
	}
	stored, ok := app.storePlaylistAssignmentAISuggestions(assignment.token, "song-1", 2, []string{"华语"}, false)
	if !ok || stored.aiSuggestionLoading || stored.aiSuggestionManual || len(stored.aiSuggestedPlaylists) != 1 {
		t.Fatalf("fresh pass was not stored: %#v ok=%t", stored, ok)
	}
	ready := playlistAssignmentMarkup(stored)
	if !buttonTexts(ready)["重新整理"] || !buttonTexts(ready)["📂 选择歌单"] {
		t.Fatalf("chooser and another pass should return after suggestions land: %#v", buttonTexts(ready))
	}
	if text := formatPlaylistAssignment(updated); !strings.Contains(text, "正在重新整理") {
		t.Fatalf("retry copy = %q", text)
	}
	loading := buttonTexts(playlistAssignmentMarkup(updated))
	if loading["重新整理"] || loading["📂 选择歌单"] {
		t.Fatalf("the chooser should remain hidden during a second pass: %#v", loading)
	}
	if text := formatPlaylistAssignment(updated); !strings.Contains(text, "正在重新整理…") || strings.Contains(text, "雨下一整晚") {
		t.Fatalf("reorganize copy = %q", text)
	}
	finished := assignment
	finished.selectionComplete = true
	finished.playlistAssist = true
	if !app.registerPlaylistAssignment(pendingPlaylistAssignment{token: "other", chatID: 11, expiresAt: time.Now().Add(time.Minute)}) {
		t.Fatal("register")
	}
	app.playlistAssignmentMu.Lock()
	app.playlistAssignments[assignment.token] = finished
	app.playlistAssignmentMu.Unlock()
	if _, ok := app.beginPlaylistAssignmentReorganize(assignment.token); ok {
		t.Fatal("a confirmed selection must not be wiped by reorganize")
	}
}

func TestPlaylistAssignmentIgnoresSuggestionsAfterManualSelection(t *testing.T) {
	app := &BotApp{}
	assignment := pendingPlaylistAssignment{
		token: "test", chatID: 10, song: unplaylistSong{ID: "song-1"},
		selectionToken: "chooser", aiSuggestionSeq: 1, aiSuggestionLoading: true,
		expiresAt: time.Now().Add(time.Minute),
	}
	if !app.registerPlaylistAssignment(assignment) {
		t.Fatal("register assignment")
	}
	if _, err := app.acceptPlaylistAssignmentSelection("test", "chooser", []string{"华语"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.storePlaylistAssignmentAISuggestions("test", "song-1", 1, []string{"摇滚"}, false); ok {
		t.Fatal("late AI result was accepted")
	}
	current, _ := app.getPlaylistAssignment("test")
	if !current.selectionComplete || current.aiSuggestionLoading || !reflect.DeepEqual(current.selectedPlaylists, []string{"华语"}) {
		t.Fatalf("manual selection changed: %#v", current)
	}
}

func TestPlaylistNameListPreviewIsBoundedWithoutMutatingInput(t *testing.T) {
	names := []string{"one", "two", "three", "four", strings.Repeat("name", 20), "six"}
	before := append([]string(nil), names...)
	preview := formatPlaylistNameList(names)
	if strings.Contains(preview, names[4]) || !strings.Contains(preview, "等 2 个") || strings.Join(names, "\x00") != strings.Join(before, "\x00") {
		t.Fatalf("playlist list preview is not bounded or mutated its input: %q %#v", preview, names)
	}
}

func TestPlaylistAssignmentSummaryKeepsUnverifiedAdditionVisible(t *testing.T) {
	summary := playlistAssignmentSummary(pendingPlaylistAssignment{
		addedAny: true,
	}, false)
	if strings.Contains(summary, "尚未处理歌曲") || !strings.Contains(summary, "已确认：0 首（新加入 0 首）") || !strings.Contains(summary, "当前歌曲尚未完成核验") || !strings.Contains(summary, "重新发送 /assign_playlist") {
		t.Fatalf("summary hid an unverified add attempt: %q", summary)
	}
}

func TestPlaylistAssignmentCancellationCannotDropInFlightTask(t *testing.T) {
	app := &BotApp{}
	assignment := pendingPlaylistAssignment{
		token: "processing", chatID: 100,
		processing: true, expiresAt: time.Now().Add(time.Minute),
	}
	if !app.registerPlaylistAssignment(assignment) {
		t.Fatal("failed to register processing assignment")
	}
	if _, ok := app.cancelPlaylistAssignment("processing"); ok {
		t.Fatal("in-flight playlist assignment must not be cancelled by dropping its session")
	}
	if _, ok := app.getPlaylistAssignment("processing"); !ok {
		t.Fatal("in-flight assignment was removed")
	}
}

func TestPlaylistAssignmentCallbackMustMatchOriginalMessage(t *testing.T) {
	assignment := pendingPlaylistAssignment{
		chatID: 123,
		message: &tb.Message{
			ID:   456,
			Chat: &tb.Chat{ID: 123},
		},
	}
	valid := &tb.Callback{Message: &tb.Message{ID: 456, Chat: &tb.Chat{ID: 123}}}
	if !playlistAssignmentCallbackMatches(valid, assignment) {
		t.Fatal("callback from the original message should be accepted")
	}
	for name, callback := range map[string]*tb.Callback{
		"missing message": {Message: nil},
		"wrong message":   {Message: &tb.Message{ID: 457, Chat: &tb.Chat{ID: 123}}},
		"wrong chat":      {Message: &tb.Message{ID: 456, Chat: &tb.Chat{ID: 124}}},
		"missing chat":    {Message: &tb.Message{ID: 456}},
	} {
		t.Run(name, func(t *testing.T) {
			if playlistAssignmentCallbackMatches(callback, assignment) {
				t.Fatal("mismatched callback should be rejected")
			}
		})
	}
}

func TestDiscoveryCommandsDoNotShareOneLock(t *testing.T) {
	app := &BotApp{}
	app.favoritesCommandMu.Lock()
	t.Cleanup(app.favoritesCommandMu.Unlock)
	for _, lock := range []*sync.Mutex{&app.tasteCommandMu, &app.rediscoverCommandMu, &app.playlistDescCommandMu} {
		if !lock.TryLock() {
			t.Fatal("maintenance commands still share one lock")
		}
		lock.Unlock()
	}
}

func TestOliveTinOutageIsRemembered(t *testing.T) {
	app := &BotApp{}
	if _, down := app.oliveTinCooling(time.Now()); down {
		t.Fatal("a fresh organizer should still check OliveTin")
	}
	if !oliveTinOutage(errors.New("OliveTin 返回 HTTP 404")) || !oliveTinOutage(context.DeadlineExceeded) {
		t.Fatal("transport and HTTP failures should cool down")
	}
	if oliveTinOutage(errors.New("OliveTin action 失败（exitCode=1, timedOut=false）")) {
		t.Fatal("a finished action should stay retryable")
	}
	app.noteOliveTinDown(time.Now())
	note, down := app.oliveTinCooling(time.Now())
	if !down || !strings.Contains(note, "不会反复请求") {
		t.Fatalf("cooldown = %q down=%v", note, down)
	}
	app.noteOliveTinUp()
	if _, down = app.oliveTinCooling(time.Now()); down {
		t.Fatal("a successful check should clear the cooldown")
	}
}

func TestPlaylistAILimitSkipsLaterSongs(t *testing.T) {
	oldGlobalAI := core.GlobalAI
	oldLogger := logger
	t.Cleanup(func() {
		core.GlobalAI = oldGlobalAI
		logger = oldLogger
	})
	logger = zap.NewNop()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"usage_limit_reached","type":"rate_limit"}}`))
	}))
	defer server.Close()
	clientConfig := openai.DefaultConfig("test-key")
	clientConfig.BaseURL = server.URL + "/v1"
	clientConfig.HTTPClient = server.Client()
	core.GlobalAI = &core.AI{Config: &config.AIConfig{Model: "classifier-test", SystemPrompt: "base"}, Client: openai.NewClientWithConfig(clientConfig)}
	app := &BotApp{cfg: &config.Config{AI: &config.AIConfig{Enable: true}}, aiMCPTools: []aichat.Tool{{
		Name:       "mcp_webtool_websearch",
		Parameters: map[string]any{"properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}},
		Run:        func(context.Context, map[string]any) (string, error) { return "evidence", nil },
	}}}
	assignment := pendingPlaylistAssignment{playlistAssist: true, playlists: []config.PlaylistInfo{{Name: "华语"}}}
	song := unplaylistSong{ID: "song-1", Title: "夜行", Artist: "歌手"}
	if _, unavailable := app.playlistAssignmentAISuggestions(context.Background(), song, assignment); !unavailable || calls.Load() != 1 {
		t.Fatalf("first limited call unavailable=%v calls=%d", unavailable, calls.Load())
	}
	if !app.playlistAICooling(time.Now()) {
		t.Fatal("429 did not start a cooldown")
	}
	song.ID = "song-2"
	if _, unavailable := app.playlistAssignmentAISuggestions(context.Background(), song, assignment); !unavailable || calls.Load() != 1 {
		t.Fatalf("cooldown still called AI: unavailable=%v calls=%d", unavailable, calls.Load())
	}
	assignment.aiSuggestionUnavailable = true
	if got := app.playlistAssignmentSuggestion(assignment); got != "AI 限流中，直接选即可" {
		t.Fatalf("cooldown copy = %q", got)
	}
}
