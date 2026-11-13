package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func TestAIPlaylistRequiresNavidromeCredentialsBeforeStarting(t *testing.T) {
	_, err := newAIPlaylistService(&config.Config{
		AI: &config.AIConfig{Enable: true, ApiKey: "ai-key", Model: "chat-model"},
		AIPlaylist: &config.AIPlaylistConfig{
			PineconeHost:      "https://pinecone.example",
			PineconeAPIKey:    "pinecone-key",
			PineconeNamespace: "data-music",
			EmbeddingModel:    "text-embedding-3-small",
			PlaylistSize:      1,
			CandidateCount:    1,
		},
		PlaylistOrganizer: &config.PlaylistOrganizerConfig{
			OliveTinURL:  "https://olivetin.example",
			NavidromeURL: "https://navidrome.example",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "navidrome_username") {
		t.Fatalf("AI playlist should require Navidrome credentials before starting, err=%v", err)
	}
}

func TestAIPlaylistEndToEndUsesLiveCandidatesAndVerifiesWrite(t *testing.T) {
	var chatCalls int
	var listCalls int
	var writtenIDs []string
	var deletedVector bool
	var deletedStateIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/chat/completions":
			chatCalls++
			var request struct {
				Model    string `json:"model"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode chat request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			content := `{"vector_query":"适合夜间安静聆听的氛围音乐","prefer_instrumental":false}`
			if chatCalls == 2 {
				if request.Model != "planner-model" {
					t.Errorf("planner model = %q", request.Model)
				}
				if len(request.Messages) != 2 || !strings.Contains(request.Messages[1].Content, `"id":"song1"`) {
					t.Errorf("planner did not receive Pinecone candidates: %#v", request.Messages)
				}
				content = `{"playlist":[{"id":"song2"},{"id":"song1"},{"id":"unknown"},{"id":"missing"}],"intro":"为夜色准备的温柔旋律，融合舒缓节奏与细腻音色，适合安静独处和放松心情。"}`
			} else if request.Model != "query-model" {
				t.Errorf("query model = %q", request.Model)
			}
			writeChatResponse(t, w, content)
		case r.URL.Path == "/v1/embeddings":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode embedding request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request["model"] != "openai/text-embedding-3-small" || request["dimensions"] != float64(vectorDimensions) {
				t.Errorf("unexpected embedding request: %#v", request)
			}
			vector := make([]float64, vectorDimensions)
			for i := range vector {
				vector[i] = 0.01
			}
			writeJSON(t, w, map[string]any{"object": "list", "model": "openai/text-embedding-3-small", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": vector}}})
		case r.URL.Path == "/query":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode Pinecone query: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request["namespace"] != "data-music" || request["topK"] != float64(4) || request["includeMetadata"] != true {
				t.Errorf("unexpected Pinecone query: %#v", request)
			}
			writeJSON(t, w, map[string]any{"matches": []any{
				map[string]any{"score": 0.9, "metadata": map[string]any{"id": "song1", "title": "One", "artist": "A", "genre": "Ambient", "mood": "calm", "language": "instrumental", "text": "soft synths"}},
				map[string]any{"score": 0.8, "metadata": map[string]any{"id": "song2", "title": "Two", "artist": "B", "genre": "Ambient", "mood": "calm", "language": "English"}},
				map[string]any{"score": 0.7, "metadata": map[string]any{"id": "missing", "title": "Gone"}},
				map[string]any{"score": 0.6, "metadata": map[string]any{"id": "song1", "title": "One duplicate"}},
			}})
		case r.URL.Path == "/vectors/delete":
			var request struct {
				Namespace string `json:"namespace"`
				Filter    struct {
					ID map[string]string `json:"id"`
				} `json:"filter"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode Pinecone delete: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			deletedVector = request.Namespace == "data-music" && request.Filter.ID["$eq"] == "missing"
			writeJSON(t, w, map[string]any{})
		case r.URL.Path == "/api/v1/data-tables/table-1/rows/delete":
			if r.Method != http.MethodDelete || r.Header.Get("X-N8N-API-KEY") != "n8n-test-key" {
				t.Errorf("unexpected MusicRAG delete request: %s %s", r.Method, r.URL.Path)
			}
			var filter struct {
				Filters []struct {
					ColumnName string `json:"columnName"`
					Value      string `json:"value"`
				} `json:"filters"`
			}
			if err := json.Unmarshal([]byte(r.URL.Query().Get("filter")), &filter); err != nil {
				t.Errorf("decode MusicRAG filter: %v", err)
			}
			for _, condition := range filter.Filters {
				if condition.ColumnName != "music_id" {
					t.Errorf("unexpected MusicRAG delete column: %#v", condition)
				}
				deletedStateIDs = append(deletedStateIDs, condition.Value)
			}
			writeJSON(t, w, map[string]any{"success": true})
		case r.URL.Path == "/api/StartActionAndWait":
			var request oliveTinActionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode OliveTin request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var stdout string
			switch request.ActionID {
			case "ai_playlists":
				listCalls++
				count := 0
				if listCalls > 1 {
					count = len(writtenIDs)
				}
				stdout = fmt.Sprintf(`[{"id":"playlist1","name":"[AI] 夜间氛围","description":"夜间安静聆听的氛围音乐","song_count":%d}]`, count)
			case "check_missing_tracks":
				if len(request.Arguments) != 1 || request.Arguments[0].Name != "songIds" || !strings.Contains(request.Arguments[0].Value, "missing") {
					t.Errorf("unexpected missing track query: %#v", request)
				}
				stdout = `[{"id":"missing"}]`
			default:
				t.Errorf("unexpected OliveTin action: %q", request.ActionID)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout, "timedOut": false}})
		case r.URL.Path == "/auth/login":
			writeJSON(t, w, map[string]string{"token": "navidrome-test-token"})
		case r.URL.Path == "/api/playlist/playlist1/tracks":
			if r.Method != http.MethodPost || r.Header.Get("X-Nd-Authorization") != "Bearer navidrome-test-token" {
				t.Errorf("missing Navidrome authorization: method=%s", r.Method)
			}
			var request struct {
				IDs []string `json:"ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode Navidrome tracks: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writtenIDs = request.IDs
			writeJSON(t, w, map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	organizer, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		OliveTinUsername:  "olive",
		OliveTinPassword:  "olive-pass",
		NavidromeURL:      server.URL,
		NavidromeUsername: "nav",
		NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := newAIPlaylistServiceWithClient(
		&config.AIConfig{Enable: true, ApiKey: "ai-test-key", Model: "test-model", BaseUrl: server.URL + "/v1", RequestTimeoutSeconds: 5},
		&config.AIPlaylistConfig{PineconeHost: server.URL, PineconeAPIKey: "pinecone-test-key", PineconeNamespace: "data-music", EmbeddingModel: "openai/text-embedding-3-small", QueryModel: "query-model", PlaylistModel: "planner-model", PlaylistSize: 2, CandidateCount: 4},
		organizer, server.Client(), server.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.stateTable = newMusicRAGSink(&config.N8NConfig{N8NBaseUrl: server.URL, APIKey: "n8n-test-key", MusicRAGTableID: "table-1"}, server.Client())

	result := service.run(t.Context(), nil)
	if result.NoPlaylists || result.NoEmpty || len(result.Failed) != 0 || len(result.Succeeded) != 1 {
		t.Fatalf("unexpected run result: %#v", result)
	}
	if result.Succeeded[0].Count != 2 || strings.TrimSpace(result.Succeeded[0].Intro) == "" {
		t.Fatalf("unexpected successful playlist: %#v", result.Succeeded[0])
	}
	if chatCalls != 2 || listCalls != 2 || !deletedVector || len(deletedStateIDs) != 1 || deletedStateIDs[0] != "missing" {
		t.Fatalf("expected two AI calls, two playlist reads, and stale index cleanup: chat=%d list=%d vector=%v states=%#v", chatCalls, listCalls, deletedVector, deletedStateIDs)
	}
	if len(writtenIDs) != 2 || writtenIDs[0] != "song2" || writtenIDs[1] != "song1" {
		t.Fatalf("unexpected songs written to Navidrome: %#v", writtenIDs)
	}
}

func TestAIPlaylistFallsBackToRuleOrderWhenPlannerFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "planner unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	organizer, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL:       server.URL,
		OliveTinUsername:  "olive",
		OliveTinPassword:  "olive-pass",
		NavidromeURL:      server.URL,
		NavidromeUsername: "nav",
		NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := newAIPlaylistServiceWithClient(
		&config.AIConfig{Enable: true, ApiKey: "test-key", Model: "planner-model", BaseUrl: server.URL, RequestTimeoutSeconds: 5},
		&config.AIPlaylistConfig{PlaylistSize: 3}, organizer, server.Client(), server.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, usedFallback, err := service.planPlaylistOrFallback(t.Context(), "安静氛围", []aiPlaylistCandidate{
		{ID: "song1", Genre: "ambient", Language: "instrumental", VectorScore: 0.99},
		{ID: "song2", Genre: "ambient", Language: "English", VectorScore: 0.98},
		{ID: "song3", Genre: "jazz", Language: "instrumental", VectorScore: 0.97},
		{ID: "song4", Genre: "rock", Language: "English", VectorScore: 0.96},
	})
	if err != nil || !usedFallback || len(plan.Playlist) != 3 {
		t.Fatalf("planner failure should yield a rule-based playlist: plan=%#v fallback=%v err=%v", plan, usedFallback, err)
	}
	seen := make(map[string]struct{}, len(plan.Playlist))
	for _, song := range plan.Playlist {
		if song.ID == "song4" {
			t.Fatal("fallback should use only the top 3 candidates")
		}
		if _, exists := seen[song.ID]; exists {
			t.Fatalf("fallback duplicated song %q", song.ID)
		}
		seen[song.ID] = struct{}{}
	}
}

func TestAIPlaylistPlanDropsUnknownAndDuplicateIDs(t *testing.T) {
	content := `{"playlist":[{"id":"not-in-candidates"},{"id":"song1"},{"id":"song1"},{"id":"song2"}],"intro":"简介"}`
	var plan aiPlaylistPlan
	if err := unmarshalAIJSON(content, &plan); err != nil {
		t.Fatal(err)
	}
	plan = validateAIPlaylistPlan(plan, []aiPlaylistCandidate{{ID: "song1"}, {ID: "song2"}}, 2)
	if len(plan.Playlist) != 2 || plan.Playlist[0].ID != "song1" || plan.Playlist[1].ID != "song2" {
		t.Fatalf("unexpected candidate validation result: %#v", plan.Playlist)
	}
}

func TestValidatePineconeHost(t *testing.T) {
	for _, raw := range []string{"https://index.example", "http://localhost:8080"} {
		if _, err := validatePineconeHost(raw); err != nil {
			t.Errorf("valid host %q rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "ftp://index.example", "https://user:pass@index.example", "https://index.example/query", "https://index.example?token=secret"} {
		if _, err := validatePineconeHost(raw); err == nil {
			t.Errorf("invalid host %q accepted", raw)
		}
	}
}

func writeChatResponse(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	writeJSON(t, w, map[string]any{"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "test-model", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode test response: %v", err)
	}
}
