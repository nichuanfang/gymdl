package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/aichat"
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
			} else if chatCalls > 2 {
				t.Errorf("automatic fill should not call the critic, chat=%d model=%q", chatCalls, request.Model)
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
			inputs, _ := request["input"].([]any)
			if len(inputs) != 1 || !strings.HasPrefix(fmt.Sprint(inputs[0]), "歌名:《》") || !strings.Contains(fmt.Sprint(inputs[0]), "介绍:适合夜间安静聆听的氛围音乐") {
				t.Errorf("embedding query left the library document shape: %#v", request["input"])
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
	if !strings.Contains(firstResult.Failed[0].Err.Error(), "续跑计划已保存") || !reflect.DeepEqual(playlistIDs, []string{"song2"}) {
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
	if len(addRequests) != 2 || !reflect.DeepEqual(addRequests[0], []string{"song2", "song1"}) || !reflect.DeepEqual(addRequests[1], []string{"song1"}) {
		t.Fatalf("resume must only append the missing song: %#v", addRequests)
	}
	if !reflect.DeepEqual(playlistIDs, []string{"song2", "manual-song", "song1"}) {
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
	}, aiPlaylistCandidate{}, true)
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

func TestSequencePlaylistKeepsModelOrderAndBreaksArtistRuns(t *testing.T) {
	candidates := []aiPlaylistCandidate{
		{ID: "low", Title: "Low", Artist: "A", Genre: "rock", VectorScore: 0.4},
		{ID: "mid", Title: "Mid", Artist: "C", Genre: "jazz", VectorScore: 0.5},
		{ID: "high", Title: "High", Artist: "B", Genre: "jazz", VectorScore: 0.99},
	}
	plan := sequencePlaylistPlan(aiPlaylistPlan{Playlist: []aiPlaylistPlanItem{
		{ID: "low", Reason: "先放这首"},
		{ID: "mid", Reason: "接着"},
		{ID: "high", Reason: "收在这里"},
	}}, candidates, "安静的夜晚")
	got := make([]string, 0, len(plan.Playlist))
	for _, item := range plan.Playlist {
		got = append(got, item.ID)
	}
	if !reflect.DeepEqual(got, []string{"low", "mid", "high"}) || plan.Playlist[0].Reason != "先放这首" {
		t.Fatalf("model order changed: %#v", plan.Playlist)
	}
	run := sequencePlaylistPlan(aiPlaylistPlan{Playlist: []aiPlaylistPlanItem{
		{ID: "a1"}, {ID: "a2"}, {ID: "a3"}, {ID: "b1"},
	}}, []aiPlaylistCandidate{
		{ID: "a1", Title: "A1", Artist: "Same"},
		{ID: "a2", Title: "A2", Artist: "Same"},
		{ID: "a3", Title: "A3", Artist: "Same"},
		{ID: "b1", Title: "B1", Artist: "Other"},
	}, "夜晚")
	got = got[:0]
	for _, item := range run.Playlist {
		got = append(got, item.ID)
	}
	if !reflect.DeepEqual(got, []string{"a1", "a2", "b1", "a3"}) {
		t.Fatalf("artist run was not opened: %#v", got)
	}
}

func TestPlaylistQueryUsesTheIndexedSentence(t *testing.T) {
	got := playlistQueryDocument(aiPlaylistSemanticQuery{Genre: "流行", Mood: "忧伤", Language: "国语"}, "夜间安静的钢琴和人声")
	want := musicrag.DocumentText(musicrag.Song{}, musicrag.Meta{Genre: "流行", Mood: "忧伤", Language: "国语", Description: "夜间安静的钢琴和人声"})
	if got != want || !strings.HasPrefix(got, "歌名:《》") || !strings.Contains(got, "介绍:夜间安静的钢琴和人声") {
		t.Fatalf("query document = %q", got)
	}
}

func TestRetrieveFiltersInstrumentalThenBackfillsAboveTheScoreFloor(t *testing.T) {
	var pineconeBodies []map[string]any
	var embedded string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			writeChatResponse(t, w, `{"vector_query":"适合深夜独处时循环聆听的安静钢琴纯器乐，带有空气感和缓慢节奏","genre":"轻音乐","mood":"安静","language":"国语","prefer_instrumental":true}`)
		case "/v1/embeddings":
			var request struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) != 1 {
				t.Errorf("embedding input: %v %#v", err, request.Input)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			embedded = request.Input[0]
			vector := make([]float64, vectorDimensions)
			writeJSON(t, w, map[string]any{"object": "list", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": vector}}})
		case "/query":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode query: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			pineconeBodies = append(pineconeBodies, request)
			matches := []any{
				map[string]any{"score": 0.82, "metadata": map[string]any{"id": "pure-song", "title": "Night", "artist": "A", "language": "纯音乐"}},
				map[string]any{"score": 0.2, "metadata": map[string]any{"id": "weak-song", "title": "Noise", "artist": "B", "language": "纯音乐"}},
			}
			if len(pineconeBodies) > 1 {
				matches = []any{
					map[string]any{"score": 0.91, "metadata": map[string]any{"id": "vocal-song", "title": "Voice", "artist": "C", "language": "国语"}},
					map[string]any{"score": 0.82, "metadata": map[string]any{"id": "pure-song", "title": "Night", "artist": "A", "language": "纯音乐"}},
				}
			}
			writeJSON(t, w, map[string]any{"matches": matches})
		case "/api/StartActionAndWait":
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": "[]", "timedOut": false}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	organizer, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL: server.URL, OliveTinUsername: "olive", OliveTinPassword: "olive-pass",
		NavidromeURL: server.URL, NavidromeUsername: "nav", NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := newAIPlaylistServiceWithClient(
		&config.AIConfig{Enable: true, ApiKey: "test-key", Model: "chat", BaseUrl: server.URL + "/v1", RequestTimeoutSeconds: 5},
		&config.AIPlaylistConfig{PineconeHost: server.URL, PineconeAPIKey: "pinecone-key", PineconeNamespace: "data-music", EmbeddingModel: "text-embedding-3-small", PlaylistSize: 2, CandidateCount: 4, MinScore: 0.35},
		organizer, server.Client(), server.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates, semantic, err := service.retrieveCandidates(t.Context(), "深夜纯钢琴")
	if err != nil {
		t.Fatal(err)
	}
	if !semantic.PreferInstrumental || semantic.Language != "纯音乐" || !strings.Contains(embedded, "语言:纯音乐") || !strings.Contains(embedded, "介绍:适合深夜独处") {
		t.Fatalf("query was not an instrumental library sentence: language=%q embedded=%q", semantic.Language, embedded)
	}
	if len(pineconeBodies) != 2 {
		t.Fatalf("expected a filtered query plus one backfill, got %d", len(pineconeBodies))
	}
	if _, ok := pineconeBodies[0]["filter"]; !ok {
		t.Fatalf("first recall was not filtered: %#v", pineconeBodies[0]["filter"])
	}
	if _, ok := pineconeBodies[1]["filter"]; ok {
		t.Fatal("backfill should search the whole library")
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	if !reflect.DeepEqual(ids, []string{"vocal-song", "pure-song"}) {
		t.Fatalf("weak match stayed or backfill was dropped: %#v", ids)
	}
	prepared := preparePlaylistCandidates(candidates, semantic, service.config.PlaylistSize)
	if len(prepared) != 2 || prepared[0].ID != "pure-song" || prepared[1].ID != "vocal-song" {
		t.Fatalf("instrumental tracks should lead the short pool: %#v", prepared)
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

func TestGuardPlaylistPlanKeepsOrderAndBackfills(t *testing.T) {
	candidates := []aiPlaylistCandidate{
		{ID: "seed", Title: "晴天", Artist: "周杰伦"},
		{ID: "third", Title: "三", Artist: "C"},
		{ID: "copy", Title: "晴天", Artist: "周杰伦"},
		{ID: "next", Title: "下一首", Artist: "D"},
		{ID: "later", Title: "后来", Artist: "E", VectorScore: 0.99},
	}
	plan := guardPlaylistPlan(aiPlaylistPlan{
		Intro: "简介",
		Playlist: []aiPlaylistPlanItem{
			{ID: "third", Reason: "先放这首"},
			{ID: "seed"},
			{ID: "copy"},
		},
	}, candidates, 3, aiPlaylistCandidate{ID: "seed", Title: "晴天", Artist: "周杰伦"}, nil)
	got := playlistPlanIDs(plan)
	if !reflect.DeepEqual(got, []string{"third", "next", "later"}) || plan.Intro != "简介" || plan.Playlist[0].Reason != "先放这首" {
		t.Fatalf("guard changed the arranged prefix: %#v", plan)
	}
	run := guardPlaylistPlan(aiPlaylistPlan{Playlist: []aiPlaylistPlanItem{
		{ID: "a1"}, {ID: "a2"}, {ID: "a3"}, {ID: "b1"},
	}}, []aiPlaylistCandidate{
		{ID: "a1", Title: "A1", Artist: "Same"},
		{ID: "a2", Title: "A2", Artist: "Same"},
		{ID: "a3", Title: "A3", Artist: "Same"},
		{ID: "b1", Title: "B1", Artist: "Other"},
	}, 4, aiPlaylistCandidate{}, nil)
	if !reflect.DeepEqual(playlistPlanIDs(run), []string{"a1", "a2", "b1", "a3"}) {
		t.Fatalf("artist run was not opened: %#v", playlistPlanIDs(run))
	}
}

func TestCriticDropsAnOffThemeSongWithoutReordering(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := `{"playlist":[{"id":"keep"},{"id":"off"},{"id":"tail"}],"intro":"保持原来的简介内容，足够三十个字就好。"}`
		if calls == 2 {
			var request struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 {
				t.Errorf("review request: %v", err)
			} else if user := request.Messages[1].Content; strings.Index(user, "keep") > strings.Index(user, "off") {
				t.Errorf("review saw a reordered list: %q", user)
			}
			content = `{"drop":["off","not-a-song"]}`
		}
		writeChatResponse(t, w, content)
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	service.config.PlaylistSize = 3
	plan, usedFallback, err := service.planPlaylistOrFallback(t.Context(), "安静的夜晚", aiPlaylistSemanticQuery{}, []aiPlaylistCandidate{
		{ID: "keep", Title: "留下", Artist: "A", Mood: "安静"},
		{ID: "off", Title: "跑题", Artist: "B", Genre: "金属"},
		{ID: "tail", Title: "结尾", Artist: "C"},
		{ID: "fill", Title: "补上", Artist: "D"},
	}, aiPlaylistCandidate{}, true)
	if err != nil || usedFallback {
		t.Fatalf("fallback=%v err=%v", usedFallback, err)
	}
	if !reflect.DeepEqual(playlistPlanIDs(plan), []string{"keep", "tail", "fill"}) {
		t.Fatalf("review reordered or put the rejected song back: %#v", playlistPlanIDs(plan))
	}
	if !strings.Contains(plan.Intro, "保持原来的简介") || plan.Playlist[0].Reason == "" {
		t.Fatalf("intro or reason was dropped: %#v", plan)
	}
}

func TestCriticFailureKeepsTheGuardedArrangement(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeChatResponse(t, w, `{"playlist":[{"id":"first"}],"intro":"一份已经排好的简介，这里凑够三十个字。"}`)
			return
		}
		http.Error(w, "review down", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	service.config.PlaylistSize = 2
	plan, usedFallback, err := service.planPlaylistOrFallback(t.Context(), "安静", aiPlaylistSemanticQuery{}, []aiPlaylistCandidate{
		{ID: "first", Title: "第一", Artist: "A", Mood: "安静"},
		{ID: "second", Title: "第二", Artist: "B"},
	}, aiPlaylistCandidate{ID: "second", Title: "第二", Artist: "B"}, true)
	if err != nil || usedFallback || calls != 2 {
		t.Fatalf("calls=%d fallback=%v err=%v", calls, usedFallback, err)
	}
	if !reflect.DeepEqual(playlistPlanIDs(plan), []string{"first"}) {
		t.Fatalf("a failed review should keep the guarded list and the seed out: %#v", playlistPlanIDs(plan))
	}
}

func TestAutomaticFillSkipsTheCritic(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 1 {
			t.Errorf("automatic fill called the critic")
		}
		writeChatResponse(t, w, `{"playlist":[{"id":"first"},{"id":"second"}],"intro":"一份已经排好的简介，这里凑够三十个字。"}`)
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	service.config.PlaylistSize = 2
	plan, usedFallback, err := service.planPlaylistOrFallback(t.Context(), "安静", aiPlaylistSemanticQuery{}, []aiPlaylistCandidate{
		{ID: "first", Title: "第一", Artist: "A", Mood: "安静"},
		{ID: "second", Title: "第二", Artist: "B"},
	}, aiPlaylistCandidate{}, false)
	if err != nil || usedFallback || calls != 1 {
		t.Fatalf("calls=%d fallback=%v err=%v", calls, usedFallback, err)
	}
	if !reflect.DeepEqual(playlistPlanIDs(plan), []string{"first", "second"}) {
		t.Fatalf("automatic fill changed the arrangement: %#v", playlistPlanIDs(plan))
	}
}

func TestCriticTruncationKeepsTheGuardedArrangement(t *testing.T) {
	var calls int
	var criticTokens float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeChatResponse(t, w, `{"playlist":[{"id":"first"}],"intro":"一份已经排好的简介，这里凑够三十个字。"}`)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode review: %v", err)
		}
		criticTokens, _ = request["max_completion_tokens"].(float64)
		writeJSON(t, w, map[string]any{"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": `{"drop":["first"`},
			"finish_reason": "length",
		}}})
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	service.config.PlaylistSize = 2
	plan, usedFallback, err := service.planPlaylistOrFallback(t.Context(), "安静", aiPlaylistSemanticQuery{}, []aiPlaylistCandidate{
		{ID: "first", Title: "第一", Artist: "A", Mood: "安静"},
		{ID: "second", Title: "第二", Artist: "B"},
	}, aiPlaylistCandidate{ID: "second", Title: "第二", Artist: "B"}, true)
	if err != nil || usedFallback || calls != 2 || criticTokens != criticMaxTokens {
		t.Fatalf("calls=%d tokens=%v fallback=%v err=%v", calls, criticTokens, usedFallback, err)
	}
	if !reflect.DeepEqual(playlistPlanIDs(plan), []string{"first"}) {
		t.Fatalf("a truncated review should keep the guarded list: %#v", playlistPlanIDs(plan))
	}
}

func TestRuleFallbackKeepsFusedRankOrder(t *testing.T) {
	plan := rulePlaylistPlan([]aiPlaylistCandidate{
		{ID: "a", Artist: "A", Genre: "rock", Language: "国语", VectorScore: 0.0164},
		{ID: "b", Artist: "B", Genre: "jazz", Language: "国语", VectorScore: 0.0161},
		{ID: "c", Artist: "C", Genre: "rock", Language: "国语", VectorScore: 0.0150},
	}, 3, "安静")
	if !reflect.DeepEqual(playlistPlanIDs(plan), []string{"a", "b", "c"}) {
		t.Fatalf("fused scores were split by the cosine gap: %#v", playlistPlanIDs(plan))
	}
}

func TestPlaylistSeedForRefillIgnoresPlannerBoilerplate(t *testing.T) {
	seed, ok := playlistSeedForRefill("像这首歌 晴天 - 周杰伦\n以《晴天》（周杰伦）为种子，选听感相近的歌。流派：流行。\n再安静一点")
	if !ok || seed.Title != "晴天" || seed.Artist != "周杰伦" || seed.Theme != "再安静一点" {
		t.Fatalf("%+v ok=%t", seed, ok)
	}
	seed, ok = playlistSeedForRefill("像这首歌 晴天 - 周杰伦\n曲库索引里没有《晴天》，按歌名和歌手找听感相近的歌。\n夜里")
	if !ok || seed.Theme != "夜里" {
		t.Fatalf("%+v ok=%t", seed, ok)
	}
	seed, ok = playlistSeedForRefill("像这首歌 晴天 - 周杰伦\n曲库索引里没有《晴天》，没有查到这首歌的听感特征，按歌名和歌手找听感相近的歌。\n夜里")
	if !ok || seed.Theme != "夜里" {
		t.Fatalf("missing features %+v ok=%t", seed, ok)
	}
	seed, ok = playlistSeedForRefill("像这首歌 晴天 - 周杰伦\n曲库索引里没有《晴天》，按查到的听感找相近的歌。流派：流行。\n夜里")
	if !ok || seed.Theme != "夜里" {
		t.Fatalf("found features %+v ok=%t", seed, ok)
	}
}

func TestForgetMissingSongsContinuesWhenCleanupFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/StartActionAndWait":
			var request oliveTinActionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode OliveTin: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			stdout := "[]"
			if request.ActionID == "check_missing_tracks" {
				stdout = `[{"id":"gone"}]`
			}
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout, "timedOut": false}})
		case "/vectors/delete":
			http.Error(w, "pinecone down", http.StatusInternalServerError)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	service.stateTable.failRealDelete = errors.New("db down")
	candidates := []aiPlaylistCandidate{{ID: "keep", Title: "留下", Artist: "A"}, {ID: "gone", Title: "没了", Artist: "B"}}
	kept, err := service.forgetMissingSongs(t.Context(), candidates)
	if err != nil || len(kept) != 1 || kept[0].ID != "keep" {
		t.Fatalf("kept=%#v err=%v", kept, err)
	}
	service.stateTable = nil
	if _, err = service.forgetMissingSongs(t.Context(), candidates); err == nil || !strings.Contains(err.Error(), "请配置 MusicRAG") {
		t.Fatalf("missing songs without a state table: %v", err)
	}
}

func TestAutomaticEmptyFillRecallsTheStoredSeed(t *testing.T) {
	var chats, queries, embeds int
	var wrote []string
	playlistIDs := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chats++
			var request struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode chat: %v", err)
			}
			if chats != 1 || request.Model != "planner-model" {
				t.Errorf("chat %d model %q", chats, request.Model)
			}
			writeChatResponse(t, w, `{"playlist":[{"id":"song-seed"},{"id":"song-near"}],"intro":"以晴天附近的歌排成一张短歌单，适合接着听下去。"}`)
		case "/v1/embeddings":
			embeds++
			t.Errorf("stored planner sentence was embedded as a theme")
			w.WriteHeader(http.StatusBadRequest)
		case "/vectors/fetch":
			writeJSON(t, w, map[string]any{"vectors": map[string]any{"song-seed": map[string]any{"values": testVector(0.5)}}})
		case "/query":
			queries++
			writeJSON(t, w, map[string]any{"matches": []any{
				map[string]any{"score": 0.99, "metadata": map[string]any{"id": "song-seed", "title": "晴天", "artist": "周杰伦"}},
				map[string]any{"score": 0.8, "metadata": map[string]any{"id": "song-near", "title": "七里香", "artist": "周杰伦"}},
			}})
		case "/api/StartActionAndWait":
			var request oliveTinActionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode OliveTin: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			stdout := "[]"
			if request.ActionID == "playlist_tracks" {
				trackIDs, _ := json.Marshal(playlistIDs)
				stdout = fmt.Sprintf(`[{"playlist_id":"playlist1","playlist_name":"[AI] 晴天","track_ids":%s,"track_count":%d}]`, trackIDs, len(playlistIDs))
			}
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout, "timedOut": false}})
		case "/auth/login":
			writeJSON(t, w, map[string]string{"token": "navidrome-test-token"})
		case "/api/playlist/playlist1/tracks":
			var request struct {
				IDs []string `json:"ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode tracks: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			wrote = append([]string(nil), request.IDs...)
			playlistIDs = append(playlistIDs, request.IDs...)
			writeJSON(t, w, map[string]any{})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	service.config.PlaylistModel = "planner-model"
	service.config.QueryModel = "query-model"
	service.config.PlaylistSize = 2
	if err := service.stateTable.store.Enqueue(t.Context(), musicrag.Song{MusicID: "song-seed", Title: "晴天", Artist: "周杰伦", Genre: "流行", Status: musicrag.StatusReady}); err != nil {
		t.Fatal(err)
	}
	var err error
	service.resumeStore, err = openAIPlaylistResumeStore(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.close()
	description := "像这首歌 晴天 - 周杰伦\n以《晴天》（周杰伦）为种子，选听感相近的歌。流派：流行。"
	result, err := service.processAIPlaylist(t.Context(), aiPlaylist{ID: "playlist1", Name: "[AI] 晴天", Description: description}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if chats != 1 || queries != 1 || embeds != 0 || !reflect.DeepEqual(wrote, []string{"song-near"}) || result.Count != 1 {
		t.Fatalf("chats=%d queries=%d embeds=%d wrote=%v result=%#v", chats, queries, embeds, wrote, result)
	}
}

func playlistPlanIDs(plan aiPlaylistPlan) []string {
	ids := make([]string, 0, len(plan.Playlist))
	for _, item := range plan.Playlist {
		ids = append(ids, item.ID)
	}
	return ids
}

func TestMatchReadySongsCollapsesTitleAndArtist(t *testing.T) {
	songs := []musicrag.Song{
		{MusicID: "song-b", Title: "晴天", Artist: "周杰伦", Status: musicrag.StatusReady, Description: "短"},
		{MusicID: "song-a", Title: "晴天", Artist: "周杰伦", Status: musicrag.StatusReady, Description: "更长的介绍"},
		{MusicID: "other", Title: "晴天", Artist: "陈奕迅", Status: musicrag.StatusReady, Description: "另一位"},
		{MusicID: "credit", Title: "晴天", Artist: "周杰伦、方文山", Status: musicrag.StatusReady, Description: "合作"},
		{MusicID: "pending", Title: "晴天", Artist: "周杰伦", Status: musicrag.StatusPending, Description: "还没索引"},
		{MusicID: "elsewhere", Title: "七里香", Artist: "周杰伦", Status: musicrag.StatusReady},
	}
	exact := matchReadySongs(songs, "晴天", "周杰伦")
	if len(exact) != 2 || exact[0].MusicID != "song-a" || exact[1].MusicID != "credit" || exact[0].Description != "更长的介绍" {
		t.Fatalf("artist match = %#v", exact)
	}
	if len(matchReadySongs(songs, "晴天", "")) != 3 {
		t.Fatalf("title-only = %#v", matchReadySongs(songs, "晴天", ""))
	}
	other := matchReadySongs(songs, "晴天", "陈奕迅")
	if len(other) != 1 || other[0].MusicID != "other" {
		t.Fatalf("other artist = %#v", other)
	}
	if len(matchReadySongs(songs, "晴天", "周杰伦、方文山、额外")) != 0 || len(matchReadySongs(songs, "晴天", "周")) != 0 {
		t.Fatal("artist match used the wrong direction or a one-character fragment")
	}
}

func TestSplitPlaylistSeed(t *testing.T) {
	cases := []struct {
		in, title, artist, theme string
		ok                       bool
	}{
		{"像这首歌 晴天 - 周杰伦", "晴天", "周杰伦", "", true},
		{"像这首歌:晴天 — 周杰伦", "晴天", "周杰伦", "", true},
		{"相似于 《晴天》周杰伦", "晴天", "周杰伦", "", true},
		{"像这首 晴天 / 周杰伦，再安静一点", "晴天", "周杰伦", "再安静一点", true},
		{"像这首歌 晴天 - 周杰伦\n再安静一点", "晴天", "周杰伦", "再安静一点", true},
		{"像这首歌 晴天／周杰伦", "晴天", "周杰伦", "", true},
		{"像这首歌 《晴天，现场》周杰伦", "晴天，现场", "周杰伦", "", true},
		{"夜间安静的钢琴", "", "", "", false},
		{"像夜晚一样安静", "", "", "", false},
		{"相似的夜晚", "", "", "", false},
		{"像这首歌", "", "", "", true},
	}
	for _, test := range cases {
		got, ok := splitPlaylistSeed(test.in)
		if ok != test.ok || got.Title != test.title || got.Artist != test.artist || got.Theme != test.theme {
			t.Fatalf("%q => %+v ok=%t", test.in, got, ok)
		}
	}
}

func TestRecallSimilarUsesTheIndexedVectorAndDropsTheSeed(t *testing.T) {
	var chatCalls, fetchCalls int
	var queryVector float64
	var queryFiltered bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatCalls++
			writeChatResponse(t, w, `{"vector_query":"不应调用"}`)
		case "/vectors/fetch":
			fetchCalls++
			var request struct {
				IDs       []string `json:"ids"`
				Namespace string   `json:"namespace"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Namespace != "data-music" || !reflect.DeepEqual(request.IDs, []string{"song-seed"}) {
				t.Errorf("fetch request: %v %#v", err, request)
			}
			writeJSON(t, w, map[string]any{"vectors": map[string]any{"song-seed": map[string]any{"values": testVector(0.5)}}})
		case "/query":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode query: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, ok := request["filter"]; ok {
				queryFiltered = true
			}
			if values, _ := request["vector"].([]any); len(values) > 0 {
				queryVector, _ = values[0].(float64)
			}
			writeJSON(t, w, map[string]any{"matches": []any{
				map[string]any{"score": 0.99, "metadata": map[string]any{"id": "song-seed", "title": "晴天", "artist": "周杰伦", "language": "纯音乐"}},
				map[string]any{"score": 0.81, "metadata": map[string]any{"id": "song-near", "title": "七里香", "artist": "周杰伦", "language": "国语"}},
				map[string]any{"score": 0.2, "metadata": map[string]any{"id": "song-weak", "title": "噪声", "artist": "别人"}},
			}})
		case "/api/StartActionAndWait":
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": "[]", "timedOut": false}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	var webCalls int
	service.mcpTools = func() []aichat.Tool {
		webCalls++
		return nil
	}
	if err := service.stateTable.store.Enqueue(t.Context(), musicrag.Song{
		MusicID: "song-seed", Title: "晴天", Artist: "周杰伦", Genre: "流行", Mood: "忧伤", Language: "纯音乐", Description: "夏天和吉他", Status: musicrag.StatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	candidates, semantic, description, _, err := service.recallSimilar(t.Context(), playlistSeed{Title: "晴天", Artist: "周杰伦"})
	if err != nil {
		t.Fatal(err)
	}
	if chatCalls != 0 || fetchCalls != 1 || webCalls != 0 || queryFiltered || queryVector != 0.5 || semantic.PreferInstrumental {
		t.Fatalf("chat=%d fetch=%d web=%d filtered=%t vector=%v instrumental=%t", chatCalls, fetchCalls, webCalls, queryFiltered, queryVector, semantic.PreferInstrumental)
	}
	if len(candidates) != 1 || candidates[0].ID != "song-near" {
		t.Fatalf("candidates = %#v", candidates)
	}
	if !strings.HasPrefix(description, "像这首歌 晴天 - 周杰伦\n") || !strings.Contains(description, "以《晴天》（周杰伦）为种子") || !strings.Contains(description, "流派：流行") || !strings.Contains(description, "听感：忧伤") {
		t.Fatalf("description = %q", description)
	}
}

func TestRecallSimilarFusesAnExtraThemeWithoutDroppingTheSeedFloor(t *testing.T) {
	var chats, queries int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chats++
			writeChatResponse(t, w, `{"vector_query":"再安静一点的钢琴和人声","prefer_instrumental":true}`)
		case "/v1/embeddings":
			vector := testVector(0.2)
			writeJSON(t, w, map[string]any{"object": "list", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": vector}}})
		case "/vectors/fetch":
			writeJSON(t, w, map[string]any{"vectors": map[string]any{"song-seed": map[string]any{"values": testVector(0.5)}}})
		case "/query":
			queries++
			matches := []any{
				map[string]any{"score": 0.99, "metadata": map[string]any{"id": "song-seed", "title": "晴天", "artist": "周杰伦"}},
				map[string]any{"score": 0.8, "metadata": map[string]any{"id": "song-near", "title": "七里香", "artist": "周杰伦"}},
			}
			if queries > 1 {
				matches = []any{map[string]any{"score": 0.77, "metadata": map[string]any{"id": "song-theme", "title": "安静", "artist": "别人", "language": "国语"}}}
			}
			writeJSON(t, w, map[string]any{"matches": matches})
		case "/api/StartActionAndWait":
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": "[]", "timedOut": false}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	if err := service.stateTable.store.Enqueue(t.Context(), musicrag.Song{MusicID: "song-seed", Title: "晴天", Artist: "周杰伦", Status: musicrag.StatusReady}); err != nil {
		t.Fatal(err)
	}
	candidates, semantic, description, _, err := service.recallSimilar(t.Context(), playlistSeed{Title: "晴天", Artist: "周杰伦", Theme: "再安静一点"})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	if chats != 1 || semantic.PreferInstrumental || !strings.Contains(description, "再安静一点") {
		t.Fatalf("chats=%d instrumental=%t description=%q", chats, semantic.PreferInstrumental, description)
	}
	if !reflect.DeepEqual(ids, []string{"song-near", "song-theme"}) {
		t.Fatalf("fused candidates = %#v", ids)
	}
}

func TestRecallSimilarEmbedsWhenTheSongIsNotIndexed(t *testing.T) {
	var fetches int
	var embedded string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/embeddings":
			var request struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) != 1 {
				t.Errorf("embedding: %v %#v", err, request.Input)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			embedded = request.Input[0]
			writeJSON(t, w, map[string]any{"object": "list", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": testVector(0.3)}}})
		case "/vectors/fetch":
			fetches++
			w.WriteHeader(http.StatusInternalServerError)
		case "/query":
			writeJSON(t, w, map[string]any{"matches": []any{
				map[string]any{"score": 0.7, "metadata": map[string]any{"id": "song-near", "title": "七里香", "artist": "周杰伦"}},
			}})
		case "/api/StartActionAndWait":
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": "[]", "timedOut": false}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	service.stateTable = nil
	candidates, _, description, _, err := service.recallSimilar(t.Context(), playlistSeed{Title: "晴天", Artist: "周杰伦"})
	if err != nil {
		t.Fatal(err)
	}
	if fetches != 0 || len(candidates) != 1 || candidates[0].ID != "song-near" || !strings.HasPrefix(description, "像这首歌 晴天 - 周杰伦\n") || !strings.Contains(description, "曲库索引里没有《晴天》") || !strings.Contains(description, "没有查到这首歌的听感特征") {
		t.Fatalf("fetches=%d candidates=%#v description=%q", fetches, candidates, description)
	}
	if !strings.Contains(embedded, "歌名:《晴天》") || !strings.Contains(embedded, "介绍:和这首歌听感相近") {
		t.Fatalf("embedded = %q", embedded)
	}
}

func TestRecallSimilarLooksUpFeaturesForAnUnindexedSeed(t *testing.T) {
	const evidence = "晴天是周杰伦的国语流行歌，吉他和忧伤的夏天。不要把《稻香》放进歌单。"
	metaJSON := `{"genre":"流行","mood":"忧伤","language":"国语","description":"夏天的吉他和一点忧伤，雨后的校园里慢慢走。"}`
	t.Run("embed the looked up features", func(t *testing.T) {
		var embedded, chatUser string
		var queryFiltered bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/chat/completions":
				var request struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) < 2 {
					t.Errorf("chat: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				chatUser = request.Messages[len(request.Messages)-1].Content
				if !strings.Contains(request.Messages[0].Content, "不要推荐别的歌") {
					t.Errorf("system prompt = %q", request.Messages[0].Content)
				}
				writeChatResponse(t, w, metaJSON)
			case "/v1/embeddings":
				var request struct {
					Input []string `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) != 1 {
					t.Errorf("embedding: %v %#v", err, request.Input)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				embedded = request.Input[0]
				writeJSON(t, w, map[string]any{"object": "list", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": testVector(0.3)}}})
			case "/query":
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("query: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if _, ok := request["filter"]; ok {
					queryFiltered = true
				}
				writeJSON(t, w, map[string]any{"matches": []any{
					map[string]any{"score": 0.7, "metadata": map[string]any{"id": "song-near", "title": "七里香", "artist": "周杰伦"}},
				}})
			case "/api/StartActionAndWait":
				writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": "[]", "timedOut": false}})
			default:
				t.Errorf("unexpected %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()
		service := newSeedRecallService(t, server)
		service.stateTable = nil
		var query string
		service.mcpTools = func() []aichat.Tool {
			return []aichat.Tool{{
				Name:       "mcp_webtool_websearch",
				Parameters: map[string]any{"properties": map[string]any{"query": map[string]any{"type": "string"}}},
				Run: func(_ context.Context, args map[string]any) (string, error) {
					query, _ = args["query"].(string)
					return evidence, nil
				},
			}}
		}
		candidates, semantic, description, _, err := service.recallSimilar(t.Context(), playlistSeed{Title: "晴天", Artist: "周杰伦"})
		if err != nil {
			t.Fatal(err)
		}
		want := musicrag.DocumentText(musicrag.Song{Title: "晴天", Artist: "周杰伦"}, musicrag.Meta{
			Genre: "流行", Mood: "忧伤", Language: "国语", Description: "夏天的吉他和一点忧伤，雨后的校园里慢慢走。",
		})
		if embedded != want {
			t.Fatalf("embedded = %q", embedded)
		}
		if queryFiltered || semantic.PreferInstrumental || semantic.Genre != "流行" || semantic.Mood != "忧伤" || semantic.Language != "国语" {
			t.Fatalf("filtered=%t semantic=%+v", queryFiltered, semantic)
		}
		if len(candidates) != 1 || candidates[0].ID != "song-near" || candidates[0].Title == "稻香" {
			t.Fatalf("candidates = %#v", candidates)
		}
		for _, part := range []string{"晴天", "周杰伦", "流派"} {
			if !strings.Contains(query, part) {
				t.Errorf("search query %q missing %q", query, part)
			}
		}
		if !strings.Contains(chatUser, evidence) || strings.Contains(description, "稻香") {
			t.Fatalf("chat=%q description=%q", chatUser, description)
		}
		for _, part := range []string{"曲库索引里没有《晴天》", "按查到的听感找相近的歌", "流派：流行", "听感：忧伤", "语言：国语"} {
			if !strings.Contains(description, part) {
				t.Errorf("description %q missing %q", description, part)
			}
		}
	})
	t.Run("fall back when search fails", func(t *testing.T) {
		assertUnindexedSeedFallback(t, func() (string, error) {
			return "", errors.New("search down")
		}, 0)
	})
	t.Run("fall back when features are incomplete", func(t *testing.T) {
		assertUnindexedSeedFallback(t, func() (string, error) {
			return evidence, nil
		}, 1)
	})
}

func assertUnindexedSeedFallback(t *testing.T, search func() (string, error), wantChats int) {
	t.Helper()
	var chats int
	var embedded string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chats++
			writeChatResponse(t, w, `{"genre":"流行","mood":"","language":"国语","description":"太短"}`)
		case "/v1/embeddings":
			var request struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) != 1 {
				t.Errorf("embedding: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			embedded = request.Input[0]
			writeJSON(t, w, map[string]any{"object": "list", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": testVector(0.3)}}})
		case "/query":
			writeJSON(t, w, map[string]any{"matches": []any{
				map[string]any{"score": 0.7, "metadata": map[string]any{"id": "song-near", "title": "七里香", "artist": "周杰伦"}},
			}})
		case "/api/StartActionAndWait":
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": "[]", "timedOut": false}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	service.stateTable = nil
	service.mcpTools = func() []aichat.Tool {
		return []aichat.Tool{{
			Name:       "mcp_webtool_websearch",
			Parameters: map[string]any{"properties": map[string]any{"query": map[string]any{"type": "string"}}},
			Run: func(context.Context, map[string]any) (string, error) {
				return search()
			},
		}}
	}
	candidates, semantic, description, _, err := service.recallSimilar(t.Context(), playlistSeed{Title: "晴天", Artist: "周杰伦"})
	if err != nil {
		t.Fatal(err)
	}
	if chats != wantChats || semantic.Genre != "" || semantic.Mood != "" || semantic.Language != "" || semantic.PreferInstrumental {
		t.Fatalf("chats=%d semantic=%+v", chats, semantic)
	}
	if len(candidates) != 1 || candidates[0].ID != "song-near" || !strings.Contains(description, "曲库索引里没有《晴天》") || !strings.Contains(description, "没有查到这首歌的听感特征") {
		t.Fatalf("candidates=%#v description=%q", candidates, description)
	}
	if !strings.Contains(embedded, "歌名:《晴天》") || !strings.Contains(embedded, "介绍:和这首歌听感相近") {
		t.Fatalf("embedded = %q", embedded)
	}
}

func TestRecallSimilarEmbedsTheStoredSongWhenItsVectorIsMissing(t *testing.T) {
	var embedded string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vectors/fetch":
			writeJSON(t, w, map[string]any{"vectors": map[string]any{}})
		case "/v1/embeddings":
			var request struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) != 1 {
				t.Errorf("embedding: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			embedded = request.Input[0]
			writeJSON(t, w, map[string]any{"object": "list", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": testVector(0.3)}}})
		case "/query":
			writeJSON(t, w, map[string]any{"matches": []any{
				map[string]any{"score": 0.7, "metadata": map[string]any{"id": "song-near", "title": "七里香", "artist": "周杰伦"}},
			}})
		case "/api/StartActionAndWait":
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": "[]", "timedOut": false}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	song := musicrag.Song{MusicID: "song-seed", Title: "晴天", Artist: "周杰伦", Genre: "流行", Mood: "忧伤", Language: "国语", Description: "夏天和吉他", Status: musicrag.StatusReady}
	if err := service.stateTable.store.Enqueue(t.Context(), song); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := service.recallSimilar(t.Context(), playlistSeed{Title: "晴天", Artist: "周杰伦"}); err != nil {
		t.Fatal(err)
	}
	want := musicrag.DocumentText(song, musicrag.Meta{Genre: song.Genre, Mood: song.Mood, Language: song.Language, Description: song.Description})
	if embedded != want {
		t.Fatalf("embedded = %q", embedded)
	}
}

func TestRecallSimilarRejectsAnAmbiguousTitleAndAFailedVectorRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/vectors/fetch" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		t.Errorf("unexpected %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	ctx := t.Context()
	for _, song := range []musicrag.Song{
		{MusicID: "song-jay", Title: "晴天", Artist: "周杰伦", Status: musicrag.StatusReady},
		{MusicID: "song-eason", Title: "晴天", Artist: "陈奕迅", Status: musicrag.StatusReady},
	} {
		if err := service.stateTable.store.Enqueue(ctx, song); err != nil {
			t.Fatal(err)
		}
	}
	_, _, _, _, err := service.recallSimilar(ctx, playlistSeed{Title: "晴天"})
	if err == nil || !strings.Contains(err.Error(), "像这首歌 歌名 - 歌手") || !strings.Contains(err.Error(), "周杰伦") || !strings.Contains(err.Error(), "陈奕迅") {
		t.Fatalf("ambiguous error = %v", err)
	}
	_, _, _, _, err = service.recallSimilar(ctx, playlistSeed{Title: "晴天", Artist: "周杰伦"})
	if err == nil || !strings.Contains(err.Error(), "读取歌曲向量") {
		t.Fatalf("fetch error = %v", err)
	}
}

func TestPreviewSimilarPlaylistExplainsAnEmptyNeighborhood(t *testing.T) {
	var chats int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chats++
			w.WriteHeader(http.StatusBadGateway)
		case "/vectors/fetch":
			writeJSON(t, w, map[string]any{"vectors": map[string]any{"song-seed": map[string]any{"values": testVector(0.5)}}})
		case "/query":
			writeJSON(t, w, map[string]any{"matches": []any{
				map[string]any{"score": 0.99, "metadata": map[string]any{"id": "song-seed", "title": "晴天", "artist": "周杰伦"}},
				map[string]any{"score": 0.1, "metadata": map[string]any{"id": "song-weak", "title": "噪声", "artist": "别人"}},
			}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	service := newSeedRecallService(t, server)
	if err := service.stateTable.store.Enqueue(t.Context(), musicrag.Song{MusicID: "song-seed", Title: "晴天", Artist: "周杰伦", Status: musicrag.StatusReady}); err != nil {
		t.Fatal(err)
	}
	_, err := service.previewAIPlaylist(t.Context(), aiPlaylist{}, "像这首歌 晴天 - 周杰伦", nil)
	if err == nil || !strings.Contains(err.Error(), "没有找到和这首歌足够相近") || chats != 0 {
		t.Fatalf("chats=%d err=%v", chats, err)
	}
}

func newSeedRecallService(t *testing.T, server *httptest.Server) *aiPlaylistService {
	t.Helper()
	organizer, err := newPlaylistOrganizerService(&config.PlaylistOrganizerConfig{
		OliveTinURL: server.URL, OliveTinUsername: "olive", OliveTinPassword: "olive-pass",
		NavidromeURL: server.URL, NavidromeUsername: "nav", NavidromePassword: "nav-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := newAIPlaylistServiceWithClient(
		&config.AIConfig{Enable: true, ApiKey: "test-key", Model: "chat", BaseUrl: server.URL + "/v1", RequestTimeoutSeconds: 5},
		&config.AIPlaylistConfig{PineconeHost: server.URL, PineconeAPIKey: "pinecone-key", PineconeNamespace: "data-music", EmbeddingModel: "text-embedding-3-small", PlaylistSize: 2, CandidateCount: 4, MinScore: 0.35},
		organizer, server.Client(), server.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.stateTable = newMusicRAGSink(openBotMusicRAG(t))
	return service
}

func testVector(first float64) []float64 {
	vector := make([]float64, vectorDimensions)
	vector[0] = first
	return vector
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
