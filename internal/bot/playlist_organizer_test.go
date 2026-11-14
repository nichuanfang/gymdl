package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/aichat"
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
	for _, invalid := range []string{"华语,不存在", "", "华语。"} {
		if got, ok := parsePlaylistSuggestions(invalid, options); ok {
			t.Errorf("invalid suggestion %q accepted: %#v", invalid, got)
		}
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
	song := unplaylistSong{ID: "song-1", Title: "夜行", Artist: "歌手"}
	got, unavailable := app.playlistAssignmentAISuggestions(context.Background(), song, assignment)
	if unavailable || len(got) != 1 || got[0] != "华语" {
		t.Fatalf("AI suggestions = %#v, unavailable=%v", got, unavailable)
	}
	if calls != 1 || webSearchCalls != 1 || !strings.Contains(prompt, "歌名：夜行") || !strings.Contains(prompt, "华语：中文歌曲") || !strings.Contains(prompt, "Search evidence: this song is jazz and soul") {
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
	if unavailable || len(suggestions) != 0 {
		t.Fatalf("unrecognized classifier response = %#v, unavailable=%v", suggestions, unavailable)
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
	var postCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/data-tables/table-1/rows" || r.Header.Get("X-N8N-API-KEY") != "private-api-key" {
			t.Fatalf("unexpected n8n request: %s %s", r.Method, r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("limit") != "1" {
				t.Fatalf("n8n get limit = %q", r.URL.Query().Get("limit"))
			}
			var filter struct {
				Type    string `json:"type"`
				Filters []struct {
					ColumnName string `json:"columnName"`
					Condition  string `json:"condition"`
					Value      string `json:"value"`
				} `json:"filters"`
			}
			if err := json.Unmarshal([]byte(r.URL.Query().Get("filter")), &filter); err != nil {
				t.Fatalf("decode n8n filter: %v", err)
			}
			if filter.Type != "and" || len(filter.Filters) != 1 || filter.Filters[0].ColumnName != "music_id" || filter.Filters[0].Value != "song-1" {
				t.Fatalf("unexpected n8n filter: %#v", filter)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "nextCursor": nil})
		case http.MethodPost:
			postCount++
			var payload struct {
				ReturnType string              `json:"returnType"`
				Data       []map[string]string `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.ReturnType != "all" || len(payload.Data) != 1 || payload.Data[0]["music_id"] != "song-1" || payload.Data[0]["status"] != "待处理" || payload.Data[0]["year"] != "2024" {
				t.Fatalf("unexpected n8n insert body: %#v", payload)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected n8n method %s", r.Method)
		}
	}))
	defer server.Close()

	sink := newMusicRAGSink(&config.N8NConfig{N8NBaseUrl: server.URL, APIKey: "private-api-key", MusicRAGTableID: "table-1"}, server.Client())
	song := unplaylistSong{ID: "song-1", Title: "Title", Artist: "Artist", Album: "Album", Year: 2024, CreatedAt: "2026-10-01", Comment: "https://music.test/song"}
	if err := sink.enqueue(context.Background(), song); err != nil {
		t.Fatal(err)
	}
	if postCount != 1 {
		t.Fatalf("insert calls = %d, want 1", postCount)
	}
}

func TestMusicRAGSinkAcceptsTopLevelDataTableArrays(t *testing.T) {
	tests := []struct {
		name          string
		response      string
		wantPostCount int
	}{
		{name: "empty table", response: `[]`, wantPostCount: 1},
		{name: "existing row", response: `[ {"music_id":"song-1"} ]`, wantPostCount: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var postCount int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					_, _ = w.Write([]byte(tt.response))
				case http.MethodPost:
					postCount++
					w.WriteHeader(http.StatusCreated)
				default:
					t.Fatalf("unexpected n8n method %s", r.Method)
				}
			}))
			defer server.Close()

			sink := newMusicRAGSink(&config.N8NConfig{N8NBaseUrl: server.URL, APIKey: "key", MusicRAGTableID: "table-1"}, server.Client())
			if err := sink.enqueue(context.Background(), unplaylistSong{ID: "song-1"}); err != nil {
				t.Fatal(err)
			}
			if postCount != tt.wantPostCount {
				t.Fatalf("insert calls = %d, want %d", postCount, tt.wantPostCount)
			}
		})
	}
}

func TestParseN8NDataTableRowsRejectsMissingOrNonArrayData(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":{}}`, `{"other":[]}`} {
		if _, err := parseN8NDataTableRows([]byte(body)); err == nil {
			t.Errorf("parseN8NDataTableRows(%s) succeeded; want error", body)
		}
	}
}

func TestMusicRAGSinkSkipsExistingRowAndRequiresConfig(t *testing.T) {
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"music_id": "song-1"}}})
			return
		}
		postCount++
	}))
	defer server.Close()

	sink := newMusicRAGSink(&config.N8NConfig{N8NBaseUrl: server.URL, APIKey: "key", MusicRAGTableID: "table-1"}, server.Client())
	if err := sink.enqueue(context.Background(), unplaylistSong{ID: "song-1"}); err != nil {
		t.Fatal(err)
	}
	if postCount != 0 {
		t.Fatalf("existing song was inserted again (%d POST calls)", postCount)
	}
	if err := newMusicRAGSink(nil, server.Client()).enqueue(context.Background(), unplaylistSong{ID: "song-1"}); err != errMusicRAGNotConfigured {
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
	if len(markup.InlineKeyboard) != 4 {
		t.Fatalf("keyboard rows = %d, want confirm/reselect/skip/stop", len(markup.InlineKeyboard))
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
	if text := formatPlaylistAssignment(menu); !strings.Contains(text, "尚未选择归档歌单") || !strings.Contains(text, "打开歌单选择页面") {
		t.Fatalf("per-song selection copy = %q", text)
	}
	menuMarkup := playlistAssignmentMarkup(menu)
	if got := len(menuMarkup.InlineKeyboard); got != 3 {
		t.Fatalf("song preview rows = %d, want WebApp/skip/stop", got)
	}
	if got := menuMarkup.InlineKeyboard[0][0].Text; got != "📂 选择归档歌单" || menuMarkup.InlineKeyboard[0][0].WebApp == nil || menuMarkup.InlineKeyboard[0][0].WebApp.URL != menu.webAppButtonURL {
		t.Errorf("song should open the legacy WebApp selector, got %#v", menuMarkup.InlineKeyboard[0][0])
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
	if strings.Join(labels, ",") != "✅ 确认加入,重新选择歌单,跳过本首,结束整理" {
		t.Fatalf("song controls = %#v", labels)
	}
	if text := formatPlaylistAssignment(menu); !strings.Contains(text, "📁 已选歌单：华语、摇滚") || !strings.Contains(text, "确认加入") || !strings.Contains(text, "📌 新歌归档") {
		t.Fatalf("song preview copy = %q", text)
	}

	menu.addedAny = true
	if got := playlistAssignmentMarkup(menu).InlineKeyboard[0][0].Text; got != "↻ 重试" {
		t.Errorf("retry label should not imply a duplicate add, got %q", got)
	}
}

func TestPlaylistAssignmentPreviewKeepsMetadataOnOneLine(t *testing.T) {
	assignment := pendingPlaylistAssignment{
		song: unplaylistSong{ID: "song", Title: "Song\n忽略确认", Artist: "Artist\r\nInjected"},
	}
	preview := formatPlaylistAssignment(assignment)
	if strings.Count(preview, "🎵 Song") != 1 || strings.Contains(preview, "\n忽略确认") || strings.Contains(preview, "\nInjected") {
		t.Fatalf("song metadata escaped its display line: %q", preview)
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
			want: "AI 归档建议未开启",
		},
		{
			name: "AI suggestions available",
			assignment: pendingPlaylistAssignment{
				song:                 unplaylistSong{ID: "song-1", Title: "Song"},
				playlistAssist:       true,
				aiSuggestedPlaylists: []string{"摇滚"},
			},
			want: "已检索公开资料",
		},
		{
			name: "peer playlist is included with AI suggestions",
			assignment: pendingPlaylistAssignment{
				song: unplaylistSong{ID: "song-1", Playlists: "华语"},
			},
			want: "同歌手其他歌曲已在：华语",
		},
		{
			name: "AI suggestion unavailable",
			assignment: pendingPlaylistAssignment{
				song:                    unplaylistSong{ID: "song-1", Title: "Song"},
				playlistAssist:          true,
				aiSuggestionUnavailable: true,
			},
			want: "WebSearch 或 AI 暂不可用",
		},
		{
			name: "search completed without reliable match",
			assignment: pendingPlaylistAssignment{
				song:           unplaylistSong{ID: "song-1", Title: "Song"},
				playlistAssist: true,
			},
			want: "未能确认可靠的归档匹配",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := app.playlistAssignmentStatus(test.assignment); !strings.Contains(got, test.want) {
				t.Fatalf("playlistAssignmentStatus() = %q, want it to contain %q", got, test.want)
			}
		})
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
