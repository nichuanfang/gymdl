package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/musicrag"
	"github.com/nichuanfang/gymdl/internal/storage"
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
	var playlistIDs []string
	var addRequests [][]string
	var playlistTrackReads int
	var deletedVector bool
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
					ID struct {
						In []string `json:"$in"`
					} `json:"id"`
				} `json:"filter"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode Pinecone delete: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			deletedVector = request.Namespace == "data-music" && reflect.DeepEqual(request.Filter.ID.In, []string{"missing"})
			writeJSON(t, w, map[string]any{})
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
				stdout = fmt.Sprintf(`[{"id":"playlist1","name":"[AI] 夜间氛围","description":"夜间安静聆听的氛围音乐","song_count":%d}]`, len(playlistIDs))
			case "playlist_tracks":
				playlistTrackReads++
				if len(request.Arguments) != 1 || request.Arguments[0].Name != "playlistName" || request.Arguments[0].Value != "[AI] 夜间氛围" {
					t.Errorf("unexpected playlist track query: %#v", request)
				}
				trackIDs, _ := json.Marshal(playlistIDs)
				stdout = fmt.Sprintf(`[{"playlist_id":"playlist1","playlist_name":"[AI] 夜间氛围","track_ids":%s,"track_count":%d}]`, trackIDs, len(playlistIDs))
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
			addRequests = append(addRequests, append([]string(nil), request.IDs...))
			if len(addRequests) == 1 {
				// Simulate a request that partially mutates Navidrome before timing out.
				playlistIDs = append(playlistIDs, request.IDs[0])
				http.Error(w, "upstream timeout after partial write", http.StatusServiceUnavailable)
				return
			}
			playlistIDs = append(playlistIDs, request.IDs...)
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
	state := openBotMusicRAG(t)
	if err := state.Enqueue(t.Context(), musicrag.Song{MusicID: "missing", Title: "Gone", Status: musicrag.StatusReady}); err != nil {
		t.Fatal(err)
	}
	service.stateTable = newMusicRAGSink(state)
	databasePath := filepath.Join(t.TempDir(), "gymdl.sqlite3")
	service.resumeStore, err = openAIPlaylistResumeStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}

	firstResult := service.run(t.Context(), nil)
	if firstResult.NoPlaylists || firstResult.NoEmpty || len(firstResult.Failed) != 1 || len(firstResult.Succeeded) != 0 {
		t.Fatalf("partial write should remain retryable: %#v", firstResult)
	}
	if !strings.Contains(firstResult.Failed[0].Err.Error(), "续跑计划已保存") || !reflect.DeepEqual(playlistIDs, []string{"song1"}) {
		t.Fatalf("first partial write was not recorded safely: result=%#v playlist=%#v", firstResult, playlistIDs)
	}
	playlistIDs = append(playlistIDs, "manual-song")
	if err := service.close(); err != nil {
		t.Fatal(err)
	}

	// Reopen the persistent store as a new process would. The retry must reuse
	// the original AI plan and append only the ID that Navidrome is still missing.
	service, err = newAIPlaylistServiceWithClient(
		&config.AIConfig{Enable: true, ApiKey: "ai-test-key", Model: "test-model", BaseUrl: server.URL + "/v1", RequestTimeoutSeconds: 5},
		&config.AIPlaylistConfig{PineconeHost: server.URL, PineconeAPIKey: "pinecone-test-key", PineconeNamespace: "data-music", EmbeddingModel: "openai/text-embedding-3-small", QueryModel: "query-model", PlaylistModel: "planner-model", PlaylistSize: 2, CandidateCount: 4},
		organizer, server.Client(), server.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.stateTable = newMusicRAGSink(state)
	service.resumeStore, err = openAIPlaylistResumeStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer service.close()

	result := service.run(t.Context(), nil)
	if result.NoPlaylists || result.NoEmpty || len(result.Failed) != 0 || len(result.Succeeded) != 1 {
		t.Fatalf("unexpected resumed run result: %#v", result)
	}
	if result.Succeeded[0].Count != 2 || strings.TrimSpace(result.Succeeded[0].Intro) == "" {
		t.Fatalf("unexpected successful playlist: %#v", result.Succeeded[0])
	}
	if result.Succeeded[0].Preserved != 1 {
		t.Fatalf("resume should retain and report manually added songs: %#v", result.Succeeded[0])
	}
	left, err := state.AllIDs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, stillThere := left["missing"]; stillThere || chatCalls != 2 || listCalls != 2 || playlistTrackReads != 3 || !deletedVector {
		t.Fatalf("expected one AI plan reused across restart and the missing row removed: chat=%d list=%d trackReads=%d vector=%v rows=%#v", chatCalls, listCalls, playlistTrackReads, deletedVector, left)
	}
	if len(addRequests) != 2 || !reflect.DeepEqual(addRequests[0], []string{"song1", "song2"}) || !reflect.DeepEqual(addRequests[1], []string{"song2"}) {
		t.Fatalf("resume must only append the missing song: %#v", addRequests)
	}
	if !reflect.DeepEqual(playlistIDs, []string{"song1", "manual-song", "song2"}) {
		t.Fatalf("unexpected Navidrome playlist after resume: %#v", playlistIDs)
	}
}

func TestDeleteStaleVectorsBatchesSortedMetadataIDs(t *testing.T) {
	var batches [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/vectors/delete" || r.Header.Get("Api-Key") != "pinecone-test-key" || r.Header.Get("X-Pinecone-Api-Version") != "2025-04" {
			t.Errorf("unexpected Pinecone delete request: %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			Namespace string `json:"namespace"`
			Filter    struct {
				ID struct {
					In []string `json:"$in"`
				} `json:"id"`
			} `json:"filter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Pinecone batch delete: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Namespace != "data-music" {
			t.Errorf("unexpected Pinecone namespace: %q", request.Namespace)
		}
		batches = append(batches, request.Filter.ID.In)
		writeJSON(t, w, map[string]any{})
	}))
	defer server.Close()

	service := &aiPlaylistService{
		config:       &config.AIPlaylistConfig{PineconeAPIKey: "pinecone-test-key", PineconeNamespace: "data-music"},
		httpClient:   server.Client(),
		pineconeHost: server.URL,
	}
	missing := make(map[string]struct{}, staleVectorDeleteBatchSize+1)
	for i := staleVectorDeleteBatchSize; i >= 0; i-- {
		missing[fmt.Sprintf("song-%04d", i)] = struct{}{}
	}
	if err := service.deleteStaleVectors(t.Context(), missing); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || len(batches[0]) != staleVectorDeleteBatchSize || len(batches[1]) != 1 {
		t.Fatalf("expected 1000+1 IDs in two requests, got batch sizes %#v", []int{len(batches[0]), len(batches[1])})
	}
	if batches[0][0] != "song-0000" || batches[0][len(batches[0])-1] != "song-0999" || batches[1][0] != "song-1000" {
		t.Fatalf("batch IDs are not deterministic and sorted: first=%q last=%q second=%#v", batches[0][0], batches[0][len(batches[0])-1], batches[1])
	}
	if err := service.deleteStaleVectors(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("empty cleanup made an unnecessary request: %d batches", len(batches))
	}
}

func TestAIPlaylistResumeStorePersistsAndDoesNotOverwritePlans(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gymdl.sqlite3")
	store, err := openAIPlaylistResumeStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := aiPlaylistResumePlan{
		PlaylistID:      "playlist1",
		Description:     "安静的氛围音乐",
		SongIDs:         []string{"song1", "song2"},
		Intro:           "夜晚聆听的温柔旋律。",
		UsedFallback:    true,
		Replace:         true,
		OriginalSongIDs: []string{"old1", "old2"},
	}
	if err := store.Create(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(t.Context(), plan); err == nil {
		t.Fatal("creating a second plan must not replace a pending recovery plan")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = openAIPlaylistResumeStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err := store.Load(t.Context(), plan.PlaylistID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || !reflect.DeepEqual(*loaded, plan) {
		t.Fatalf("resume plan did not persist exactly: got %#v want %#v", loaded, plan)
	}
	if err := store.Delete(t.Context(), plan.PlaylistID); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(t.Context(), plan.PlaylistID)
	if err != nil || loaded != nil {
		t.Fatalf("deleted resume plan is still present: plan=%#v err=%v", loaded, err)
	}
}

func TestAIPlaylistCreationIntentIsRecoverableAndRetrySafe(t *testing.T) {
	store, err := openAIPlaylistResumeStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	intent := aiPlaylistCreateIntent{
		OperationID:  "operation-1",
		Name:         "[AI] 夜间氛围",
		Description:  "安静舒缓的电子音乐",
		Marker:       "gymdl-ai-playlist-create:operation-1",
		SongIDs:      []string{"song1", "song2"},
		Intro:        "夜色中的温柔旋律",
		UsedFallback: true,
	}
	if err := store.CreateIntent(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateIntent(t.Context(), intent); err != nil {
		t.Fatalf("retrying the same creation intent should be idempotent: %v", err)
	}
	if err := store.SetIntentPlaylistID(t.Context(), intent.OperationID, "playlist1"); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadIntent(t.Context(), intent.OperationID)
	if err != nil || loaded == nil {
		t.Fatalf("load creation intent: intent=%#v err=%v", loaded, err)
	}
	intent.PlaylistID = "playlist1"
	if !sameAIPlaylistCreateIntent(*loaded, intent) {
		t.Fatalf("loaded intent = %#v, want %#v", loaded, intent)
	}
	if err := store.CreateIntent(t.Context(), aiPlaylistCreateIntent{
		OperationID: intent.OperationID,
		Name:        intent.Name,
		Description: intent.Description,
		Marker:      intent.Marker,
		SongIDs:     []string{"song2", "song1"},
	}); err == nil {
		t.Fatal("a different plan must not overwrite a confirmed creation intent")
	}
	intents, err := store.ListIntents(t.Context())
	if err != nil || len(intents) != 1 || intents[0].PlaylistID != "playlist1" {
		t.Fatalf("list creation intents = %#v, err=%v", intents, err)
	}
	if err := store.DeleteIntent(t.Context(), intent.OperationID); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.LoadIntent(t.Context(), intent.OperationID)
	if err != nil || loaded != nil {
		t.Fatalf("deleted intent is still present: intent=%#v err=%v", loaded, err)
	}
}

func TestAIPlaylistResumeStoreMigratesLegacyAppendRows(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gymdl.sqlite3")
	db, err := storage.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ai_playlist_pending (
		playlist_id TEXT PRIMARY KEY,
		description TEXT NOT NULL,
		song_ids_json TEXT NOT NULL,
		intro TEXT NOT NULL,
		used_fallback INTEGER NOT NULL CHECK (used_fallback IN (0, 1))
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ai_playlist_pending (playlist_id, description, song_ids_json, intro, used_fallback) VALUES (?, ?, ?, ?, ?)`, "playlist1", "夜间氛围", `["song1"]`, "简介", 0); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openAIPlaylistResumeStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err := store.Load(t.Context(), "playlist1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.Replace || !reflect.DeepEqual(loaded.SongIDs, []string{"song1"}) || len(loaded.OriginalSongIDs) != 0 {
		t.Fatalf("legacy append plan was not preserved: %#v", loaded)
	}
}

func TestAIPlaylistReplacementSnapshotPreservesDuplicateExistingTracks(t *testing.T) {
	store, err := openAIPlaylistResumeStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan := aiPlaylistResumePlan{
		PlaylistID:      "playlist1",
		Description:     "主题",
		SongIDs:         []string{"new-song"},
		Replace:         true,
		OriginalSongIDs: []string{"old-song", "old-song"},
	}
	if err := store.Create(t.Context(), plan); err != nil {
		t.Fatalf("duplicate existing tracks should be a valid snapshot: %v", err)
	}
	loaded, err := store.Load(t.Context(), plan.PlaylistID)
	if err != nil || loaded == nil || !reflect.DeepEqual(loaded.OriginalSongIDs, plan.OriginalSongIDs) {
		t.Fatalf("replacement snapshot did not preserve duplicate tracks: plan=%#v err=%v", loaded, err)
	}
}

func TestAIPlaylistResumeStopsWhenDescriptionChangesAndEscapesLookupName(t *testing.T) {
	var sawEscapedName bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/StartActionAndWait" {
			t.Errorf("changed description must not mutate Navidrome, got %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var request oliveTinActionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode OliveTin request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.ActionID != "playlist_tracks" || len(request.Arguments) != 1 || request.Arguments[0].Value != "[AI] night''s mood" {
			t.Errorf("playlist lookup name was not safely escaped: %#v", request)
		}
		sawEscapedName = true
		writeJSON(t, w, map[string]any{"logEntry": map[string]any{
			"exitCode": 0,
			"stdout":   `[{"playlist_id":"playlist1","playlist_name":"[AI] night's mood","track_ids":["song1"],"track_count":1}]`,
			"timedOut": false,
		}})
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
	service, err := newAIPlaylistServiceWithClient(
		&config.AIConfig{Enable: true, ApiKey: "test-key", Model: "test-model"},
		&config.AIPlaylistConfig{}, organizer, server.Client(), server.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.resumeStore, err = openAIPlaylistResumeStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.close()

	saved := aiPlaylistResumePlan{PlaylistID: "playlist1", Description: "原来的夜间氛围", SongIDs: []string{"song1", "song2"}}
	if err := service.resumeStore.Create(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	_, err = service.processAIPlaylist(t.Context(), aiPlaylist{
		ID: "playlist1", Name: "[AI] night's mood", Description: "新的爵士主题", SongCount: 1,
	}, &saved, false)
	if err == nil || !strings.Contains(err.Error(), "歌单备注已变更") {
		t.Fatalf("changed description should stop the stale plan: %v", err)
	}
	if !sawEscapedName {
		t.Fatal("playlist lookup was not issued")
	}
	loaded, err := service.resumeStore.Load(t.Context(), saved.PlaylistID)
	if err != nil || loaded == nil || !reflect.DeepEqual(*loaded, saved) {
		t.Fatalf("stale plan must remain for explicit recovery: plan=%#v err=%v", loaded, err)
	}
}

func TestAIPlaylistResumeAfterVerificationFailureDoesNotDuplicateTracks(t *testing.T) {
	playlistIDs := []string{}
	trackReads := 0
	writeCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
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
				stdout = fmt.Sprintf(`[{"id":"playlist1","name":"[AI] calm","description":"安静的氛围音乐","song_count":%d}]`, len(playlistIDs))
			case "playlist_tracks":
				trackReads++
				if trackReads == 2 {
					writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 1, "stdout": "temporary SQLite read failure", "timedOut": false}})
					return
				}
				if len(playlistIDs) == 0 {
					// The current OliveTin SQL query returns [null] for an empty playlist.
					stdout = `[{"playlist_id":"playlist1","playlist_name":"[AI] calm","track_ids":[null],"track_count":0}]`
				} else {
					trackIDs, _ := json.Marshal(playlistIDs)
					stdout = fmt.Sprintf(`[{"playlist_id":"playlist1","playlist_name":"[AI] calm","track_ids":%s,"track_count":%d}]`, trackIDs, len(playlistIDs))
				}
			default:
				t.Errorf("unexpected OliveTin action: %q", request.ActionID)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout, "timedOut": false}})
		case r.URL.Path == "/auth/login":
			writeJSON(t, w, map[string]string{"token": "navidrome-test-token"})
		case r.URL.Path == "/api/playlist/playlist1/tracks":
			var request struct {
				IDs []string `json:"ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode Navidrome add request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeCalls++
			playlistIDs = append(playlistIDs, request.IDs...)
			writeJSON(t, w, map[string]any{})
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
	service, err := newAIPlaylistServiceWithClient(
		&config.AIConfig{Enable: true, ApiKey: "test-key", Model: "test-model"},
		&config.AIPlaylistConfig{}, organizer, server.Client(), server.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.resumeStore, err = openAIPlaylistResumeStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.close()
	if err := service.resumeStore.Create(t.Context(), aiPlaylistResumePlan{
		PlaylistID: "playlist1", Description: "安静的氛围音乐", SongIDs: []string{"song1", "song2"}, Intro: "平静柔和。",
	}); err != nil {
		t.Fatal(err)
	}

	first := service.run(t.Context(), nil)
	if len(first.Failed) != 1 || !strings.Contains(first.Failed[0].Err.Error(), "无法复核") || !reflect.DeepEqual(playlistIDs, []string{"song1", "song2"}) {
		t.Fatalf("verification failure should preserve the plan after a successful write: result=%#v playlist=%#v", first, playlistIDs)
	}
	second := service.run(t.Context(), nil)
	if len(second.Failed) != 0 || len(second.Succeeded) != 1 || writeCalls != 1 || !reflect.DeepEqual(playlistIDs, []string{"song1", "song2"}) {
		t.Fatalf("retry should verify existing songs without writing duplicates: result=%#v writes=%d playlist=%#v", second, writeCalls, playlistIDs)
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
	plan, usedFallback, err := service.planPlaylistOrFallback(t.Context(), "安静氛围", aiPlaylistSemanticQuery{}, []aiPlaylistCandidate{
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

func TestPreparePlaylistCandidatesKeepsInstrumentalWhenEnough(t *testing.T) {
	candidates := []aiPlaylistCandidate{
		{ID: "vocal-high", Language: "国语", VectorScore: 0.99},
		{ID: "pure-1", Language: "纯音乐", VectorScore: 0.80},
		{ID: "pure-2", Language: "instrumental", VectorScore: 0.70},
		{ID: "vocal-low", Language: "英语", VectorScore: 0.60},
	}
	kept := preparePlaylistCandidates(candidates, aiPlaylistSemanticQuery{PreferInstrumental: true}, 2)
	if len(kept) != 2 || kept[0].ID != "pure-1" || kept[1].ID != "pure-2" {
		t.Fatalf("enough instrumental tracks should replace the vocal pool: %#v", kept)
	}
	mixed := preparePlaylistCandidates(candidates, aiPlaylistSemanticQuery{PreferInstrumental: true}, 3)
	if len(mixed) != 4 || mixed[0].ID != "pure-1" || mixed[1].ID != "pure-2" || mixed[2].ID != "vocal-high" {
		t.Fatalf("short instrumental pool should lead, then fill with vocals: %#v", mixed)
	}
	unchanged := preparePlaylistCandidates(candidates, aiPlaylistSemanticQuery{}, 2)
	if len(unchanged) != len(candidates) || unchanged[0].ID != "vocal-high" {
		t.Fatalf("vocal requests should keep vector order: %#v", unchanged)
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

func TestPlaylistRecallFusesQueriesAndKeepsOneCopyOfAWork(t *testing.T) {
	fused := fusePlaylistCandidates([][]aiPlaylistCandidate{
		{{ID: "only-a", Title: "Only", Artist: "A", VectorScore: 0.99}, {ID: "shared", Title: "Shared", Artist: "B", VectorScore: 0.5}},
		{{ID: "shared", Title: "Shared", Artist: "B", VectorScore: 0.4}, {ID: "only-c", Title: "Other", Artist: "C", VectorScore: 0.2}},
	})
	if len(fused) != 3 || fused[0].ID != "shared" {
		t.Fatalf("a song ranked on both queries should lead: %#v", fused)
	}
	unique := uniquePlaylistCandidates([]aiPlaylistCandidate{
		{ID: "low", Title: "Wound Up", Artist: "Jordyn Edmonds", VectorScore: 0.2},
		{ID: "high", Title: "Wound Up", Artist: "Jordyn Edmonds", VectorScore: 0.9},
		{ID: "other", Title: "Other", Artist: "Someone", VectorScore: 0.8},
	})
	if len(unique) != 2 || unique[0].ID != "high" || unique[1].ID != "other" {
		t.Fatalf("same title and artist should collapse to the stronger copy: %#v", unique)
	}
}

func TestPlaylistDiversityCapsARepeatedArtist(t *testing.T) {
	pool := []aiPlaylistCandidate{
		{ID: "a1", Title: "A1", Artist: "Same", VectorScore: 0.99},
		{ID: "a2", Title: "A2", Artist: "Same", VectorScore: 0.98},
		{ID: "a3", Title: "A3", Artist: "Same", VectorScore: 0.97},
		{ID: "a4", Title: "A4", Artist: "Same", VectorScore: 0.96},
		{ID: "a5", Title: "A5", Artist: "Same", VectorScore: 0.95},
		{ID: "b1", Title: "B1", Artist: "Other", VectorScore: 0.70},
		{ID: "c1", Title: "C1", Artist: "Third", VectorScore: 0.69},
		{ID: "d1", Title: "D1", Artist: "Fourth", VectorScore: 0.68},
	}
	picked := takeDiversePlaylist(pool, 6, "安静的夜晚")
	same := 0
	for _, song := range picked {
		if song.Artist == "Same" {
			same++
		}
	}
	if len(picked) != 6 || same != 3 {
		t.Fatalf("artist cap = %d in %#v", same, picked)
	}
	focused := takeDiversePlaylist(pool, 6, "我想听 Same 的歌")
	focusedSame := 0
	for _, song := range focused {
		if song.Artist == "Same" {
			focusedSame++
		}
	}
	if focusedSame < 4 {
		t.Fatalf("a named artist should stay together: %#v", focused)
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
