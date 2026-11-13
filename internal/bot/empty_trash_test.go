package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	tb "gopkg.in/telebot.v4"
)

type emptyTrashCallbackTestContext struct {
	tb.Context
	args     []string
	sender   *tb.User
	callback *tb.Callback
	response int
	edited   interface{}
}

func (c *emptyTrashCallbackTestContext) Args() []string { return c.args }

func (c *emptyTrashCallbackTestContext) Sender() *tb.User { return c.sender }

func (c *emptyTrashCallbackTestContext) Callback() *tb.Callback { return c.callback }

func (c *emptyTrashCallbackTestContext) Respond(...*tb.CallbackResponse) error {
	c.response++
	return nil
}

func (c *emptyTrashCallbackTestContext) Edit(what interface{}, _ ...interface{}) error {
	c.edited = what
	return nil
}

func TestEmptyTrashDeletesFilesStateRowsAndVectors(t *testing.T) {
	deletedFiles := make([]string, 0, 2)
	stateIDs := []string(nil)
	vectorIDs := []string(nil)
	statePreflightCalls := 0
	pineconePreflightCalls := 0
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
			case "playlist_tracks":
				if len(request.Arguments) != 1 || request.Arguments[0] != (oliveTinArgument{Name: "playlistName", Value: "回收站"}) {
					t.Errorf("unexpected recycle bin lookup: %#v", request)
				}
				stdout = `[{"playlist_name":"回收站","track_ids":["song1","song2"],"file_paths":["album/one.flac","album/two.flac"],"track_count":2}]`
			case "delete_music_file":
				if len(request.Arguments) != 1 || request.Arguments[0].Name != "filePath" {
					t.Errorf("unexpected file delete request: %#v", request)
				} else {
					deletedFiles = append(deletedFiles, request.Arguments[0].Value)
				}
				stdout = ""
			default:
				t.Errorf("unexpected OliveTin action: %q", request.ActionID)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout, "timedOut": false}})
		case r.URL.Path == "/api/v1/data-tables/table123/rows/delete":
			if r.Method != http.MethodDelete || r.Header.Get("X-N8N-API-KEY") != "n8n-test-key" {
				t.Errorf("unexpected MusicRAG delete request: %s %s", r.Method, r.URL.Path)
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
				t.Errorf("decode MusicRAG filter: %v", err)
			}
			if filter.Type != "or" {
				t.Errorf("MusicRAG filter type = %q", filter.Type)
			}
			if r.URL.Query().Get("dryRun") == "true" {
				statePreflightCalls++
			} else {
				for _, condition := range filter.Filters {
					if condition.ColumnName != "music_id" || condition.Condition != "eq" {
						t.Errorf("unexpected MusicRAG filter condition: %#v", condition)
					}
					stateIDs = append(stateIDs, condition.Value)
				}
			}
			writeJSON(t, w, map[string]any{"success": true})
		case r.URL.Path == "/describe_index_stats":
			pineconePreflightCalls++
			if r.Method != http.MethodPost {
				t.Errorf("unexpected Pinecone preflight method: %s", r.Method)
			}
			writeJSON(t, w, map[string]any{"dimension": vectorDimensions, "namespaces": map[string]any{}})
		case r.URL.Path == "/vectors/delete":
			if r.Method != http.MethodPost || r.Header.Get("Api-Key") != "pinecone-test-key" {
				t.Errorf("unexpected Pinecone delete request: %s %s", r.Method, r.URL.Path)
			}
			var request struct {
				Namespace string `json:"namespace"`
				Filter    struct {
					ID map[string][]string `json:"id"`
				} `json:"filter"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode Pinecone delete request: %v", err)
			}
			if request.Namespace != "data-music" {
				t.Errorf("Pinecone namespace = %q", request.Namespace)
			}
			vectorIDs = append(vectorIDs, request.Filter.ID["$in"]...)
			writeJSON(t, w, map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	service := testEmptyTrashService(t, server)
	approvedPlan, err := service.prepare(t.Context())
	if err != nil {
		t.Fatalf("prepare confirmation preview: %v", err)
	}
	result := service.runConfirmed(t.Context(), approvedPlan, nil)
	if result.FileError != nil || result.StateError != nil || result.VectorError != nil {
		t.Fatalf("unexpected cleanup failure: %#v", result)
	}
	if result.TrackCount != 2 || result.FilesDeleted != 2 || !result.StateRowsClean || !result.VectorsDeleted {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	if !reflect.DeepEqual(deletedFiles, []string{"album/one.flac", "album/two.flac"}) {
		t.Fatalf("unexpected deleted paths: %#v", deletedFiles)
	}
	if statePreflightCalls != 2 || pineconePreflightCalls != 2 || !reflect.DeepEqual(stateIDs, []string{"song1", "song2"}) || !reflect.DeepEqual(vectorIDs, []string{"song1", "song2"}) {
		t.Fatalf("unexpected cleanup IDs: state=%#v vectors=%#v", stateIDs, vectorIDs)
	}
	if !strings.Contains(formatEmptyTrashResult(result), "共删除 2 首歌曲") {
		t.Fatalf("unexpected completion message: %q", formatEmptyTrashResult(result))
	}
}

func TestEmptyTrashReportsFileFailureAfterCleaningIndexes(t *testing.T) {
	deletedFiles := 0
	actualCleanupCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/StartActionAndWait":
			var request oliveTinActionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode OliveTin request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			stdout := `[{"playlist_name":"回收站","track_ids":["song1","song2"],"file_paths":["album/one.flac","album/two.flac"],"track_count":2}]`
			code := 0
			if request.ActionID == "delete_music_file" {
				deletedFiles++
				if deletedFiles == 2 {
					code = 1
				}
				stdout = "permission denied"
			}
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": code, "stdout": stdout, "timedOut": false}})
		case "/api/v1/data-tables/table123/rows/delete":
			if r.URL.Query().Get("dryRun") != "true" {
				actualCleanupCalls++
			}
			writeJSON(t, w, map[string]any{"success": true})
		case "/describe_index_stats":
			writeJSON(t, w, map[string]any{"dimension": vectorDimensions, "namespaces": map[string]any{}})
		case "/vectors/delete":
			actualCleanupCalls++
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	service := testEmptyTrashService(t, server)
	firstResult := service.run(t.Context(), nil)
	if firstResult.FileError == nil || firstResult.FilesDeleted != 1 {
		t.Fatalf("expected one deleted file and a fatal file error: %#v", firstResult)
	}
	if actualCleanupCalls != 2 || !firstResult.StateRowsClean || !firstResult.VectorsDeleted {
		t.Fatalf("indexes should be cleared before physical file deletion: calls=%d result=%#v", actualCleanupCalls, firstResult)
	}
	if !strings.Contains(formatEmptyTrashResult(firstResult), "已删除 1/2 个文件") || !strings.Contains(formatEmptyTrashResult(firstResult), "状态和 Pinecone 已清理") || !strings.Contains(formatEmptyTrashResult(firstResult), "可安全重试") {
		t.Fatalf("partial deletion must be reported as retryable: %q", formatEmptyTrashResult(firstResult))
	}

	secondResult := service.run(t.Context(), nil)
	if secondResult.FileError != nil || secondResult.FilesDeleted != 2 || !secondResult.StateRowsClean || !secondResult.VectorsDeleted {
		t.Fatalf("retry after a partial file deletion should finish safely: %#v", secondResult)
	}
	if actualCleanupCalls != 4 {
		t.Fatalf("retry should repeat the idempotent index cleanup, got %d mutation calls", actualCleanupCalls)
	}
}

func TestEmptyTrashValidatesAllPathsBeforeDeleting(t *testing.T) {
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/StartActionAndWait" {
			deleteCalls++
			w.WriteHeader(http.StatusOK)
			return
		}
		var request oliveTinActionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode OliveTin request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		stdout := `[{"playlist_name":"回收站","track_ids":["song1"],"file_paths":["../../outside.flac"],"track_count":1}]`
		writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout, "timedOut": false}})
	}))
	defer server.Close()

	result := testEmptyTrashService(t, server).run(t.Context(), nil)
	if result.PreflightError == nil || !strings.Contains(result.PreflightError.Error(), "文件路径无效") {
		t.Fatalf("unsafe path was not rejected: %#v", result)
	}
	if deleteCalls != 0 {
		t.Fatalf("unsafe path caused %d delete requests", deleteCalls)
	}
}

func TestEmptyTrashPreflightStopsBeforeFileDeletionWhenPineconeUnavailable(t *testing.T) {
	fileDeleteCalls := 0
	actualStateDeletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/StartActionAndWait":
			var request oliveTinActionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode OliveTin request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.ActionID == "delete_music_file" {
				fileDeleteCalls++
			}
			stdout := `[{"playlist_name":"回收站","track_ids":["song1"],"file_paths":["album/one.flac"],"track_count":1}]`
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout, "timedOut": false}})
		case "/api/v1/data-tables/table123/rows/delete":
			if r.URL.Query().Get("dryRun") != "true" {
				actualStateDeletes++
			}
			writeJSON(t, w, map[string]any{"success": true})
		case "/describe_index_stats":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	result := testEmptyTrashService(t, server).run(t.Context(), nil)
	if result.PreflightError == nil || fileDeleteCalls != 0 || actualStateDeletes != 0 {
		t.Fatalf("Pinecone preflight failure should prevent mutations: result=%#v files=%d rows=%d", result, fileDeleteCalls, actualStateDeletes)
	}
}

func TestEmptyTrashConfirmationStopsWhenQueueChanges(t *testing.T) {
	stateDryRuns := 0
	stateDeletes := 0
	vectorDeletes := 0
	fileDeletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/StartActionAndWait":
			var request oliveTinActionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode OliveTin request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch request.ActionID {
			case "playlist_tracks":
				writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": `[{"playlist_name":"回收站","track_ids":["new-song"],"file_paths":["album/new.flac"],"track_count":1}]`, "timedOut": false}})
			case "delete_music_file":
				fileDeletes++
				writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": "", "timedOut": false}})
			default:
				t.Errorf("unexpected OliveTin action: %q", request.ActionID)
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/api/v1/data-tables/table123/rows/delete":
			if r.URL.Query().Get("dryRun") == "true" {
				stateDryRuns++
			} else {
				stateDeletes++
			}
			writeJSON(t, w, map[string]any{"success": true})
		case "/describe_index_stats":
			writeJSON(t, w, map[string]any{"dimension": vectorDimensions, "namespaces": map[string]any{}})
		case "/vectors/delete":
			vectorDeletes++
			writeJSON(t, w, map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	service := testEmptyTrashService(t, server)
	approvedPlan := emptyTrashPlan{
		snapshot: emptyTrashSnapshot{PlaylistName: "回收站", TrackCount: 1},
		ids:      []string{"old-song"},
		paths:    []string{"album/old.flac"},
	}
	result := service.runConfirmed(t.Context(), approvedPlan, nil)
	if result.PreflightError == nil || !strings.Contains(result.PreflightError.Error(), "内容发生变化") {
		t.Fatalf("changed trash contents should invalidate confirmation: %#v", result)
	}
	if stateDryRuns != 1 || stateDeletes != 0 || vectorDeletes != 0 || fileDeletes != 0 {
		t.Fatalf("changed queue caused deletion: dryRuns=%d state=%d vectors=%d files=%d", stateDryRuns, stateDeletes, vectorDeletes, fileDeletes)
	}
}

func TestEmptyTrashDoesNotDeleteFilesWhenStateTablePreflightFails(t *testing.T) {
	fileDeleteCalls := 0
	pineconePreflightCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/StartActionAndWait":
			var request oliveTinActionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode OliveTin request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.ActionID == "delete_music_file" {
				fileDeleteCalls++
			}
			stdout := `[{"playlist_name":"回收站","track_ids":["song1"],"file_paths":["album/one.flac"],"track_count":1}]`
			writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout, "timedOut": false}})
		case "/api/v1/data-tables/table123/rows/delete":
			if r.URL.Query().Get("dryRun") != "true" {
				t.Errorf("expected only a dry run, got query %v", r.URL.Query())
			}
			w.WriteHeader(http.StatusForbidden)
		case "/describe_index_stats":
			pineconePreflightCalls++
			writeJSON(t, w, map[string]any{"dimension": vectorDimensions})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	result := testEmptyTrashService(t, server).run(t.Context(), nil)
	if result.PreflightError == nil || fileDeleteCalls != 0 || pineconePreflightCalls != 0 {
		t.Fatalf("MusicRAG permission failure should prevent all mutations: result=%#v files=%d pinecone=%d", result, fileDeleteCalls, pineconePreflightCalls)
	}
}

func TestEmptyTrashMetadataCleanupFailuresStopBeforeFileDeletion(t *testing.T) {
	for _, failurePoint := range []string{"state-delete", "vector-delete"} {
		t.Run(failurePoint, func(t *testing.T) {
			fileDeleteCalls := 0
			stateDeleteCalls := 0
			vectorDeleteCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/StartActionAndWait":
					var request oliveTinActionRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode OliveTin request: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if request.ActionID == "delete_music_file" {
						fileDeleteCalls++
					}
					stdout := `[{"playlist_name":"回收站","track_ids":["song1"],"file_paths":["album/one.flac"],"track_count":1}]`
					writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": stdout, "timedOut": false}})
				case "/api/v1/data-tables/table123/rows/delete":
					if r.URL.Query().Get("dryRun") == "true" {
						writeJSON(t, w, map[string]any{"success": true})
						return
					}
					stateDeleteCalls++
					if failurePoint == "state-delete" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					writeJSON(t, w, map[string]any{"success": true})
				case "/describe_index_stats":
					writeJSON(t, w, map[string]any{"dimension": vectorDimensions})
				case "/vectors/delete":
					vectorDeleteCalls++
					if failurePoint == "vector-delete" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					writeJSON(t, w, map[string]any{})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))

			result := testEmptyTrashService(t, server).run(t.Context(), nil)
			server.Close()
			if fileDeleteCalls != 0 {
				t.Fatalf("metadata cleanup failure deleted %d files", fileDeleteCalls)
			}
			switch failurePoint {
			case "state-delete":
				if result.StateError == nil || result.VectorError != nil || stateDeleteCalls != 1 || vectorDeleteCalls != 1 || !result.VectorsDeleted {
					t.Fatalf("unexpected MusicRAG delete failure state: %#v state=%d vectors=%d", result, stateDeleteCalls, vectorDeleteCalls)
				}
			case "vector-delete":
				if result.StateError != nil || result.VectorError == nil || stateDeleteCalls != 0 || vectorDeleteCalls != 1 || result.VectorsDeleted {
					t.Fatalf("unexpected Pinecone delete failure state: %#v state=%d vectors=%d", result, stateDeleteCalls, vectorDeleteCalls)
				}
			}
		})
	}
}

func TestEmptyTrashConfirmationMarkupAndPlanMatching(t *testing.T) {
	const token = "1234567890abcdef1234567890abcdef"
	markup := emptyTrashConfirmationMarkup(token)
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("confirmation markup = %#v, want confirm and cancel buttons", markup.InlineKeyboard)
	}
	for _, button := range markup.InlineKeyboard[0] {
		if callbackLength := len(button.Unique) + 1 + len(button.Data); callbackLength > 64 {
			t.Errorf("callback payload is %d bytes; Telegram allows at most 64", callbackLength)
		}
	}
	if markup.InlineKeyboard[0][0].Unique != emptyTrashCallbackUnique || !strings.HasPrefix(markup.InlineKeyboard[0][0].Data, "confirm|") {
		t.Fatalf("unexpected confirm callback: %#v", markup.InlineKeyboard[0][0])
	}
	if markup.InlineKeyboard[0][1].Unique != emptyTrashCallbackUnique || !strings.HasPrefix(markup.InlineKeyboard[0][1].Data, "cancel|") {
		t.Fatalf("unexpected cancel callback: %#v", markup.InlineKeyboard[0][1])
	}

	first := emptyTrashPlan{snapshot: emptyTrashSnapshot{TrackCount: 2}, ids: []string{"a", "b"}, paths: []string{"a.flac", "b.flac"}}
	sameContentDifferentOrder := emptyTrashPlan{snapshot: emptyTrashSnapshot{TrackCount: 2}, ids: []string{"b", "a"}, paths: []string{"b.flac", "a.flac"}}
	changedPath := emptyTrashPlan{snapshot: emptyTrashSnapshot{TrackCount: 2}, ids: []string{"a", "b"}, paths: []string{"changed.flac", "b.flac"}}
	if !sameEmptyTrashPlan(first, sameContentDifferentOrder) {
		t.Fatal("song order changes must not invalidate an otherwise identical confirmation")
	}
	if sameEmptyTrashPlan(first, changedPath) {
		t.Fatal("changed song paths must invalidate the confirmation")
	}
}

func TestEmptyTrashCallbackMustMatchOriginatingMessage(t *testing.T) {
	pending := pendingEmptyTrash{
		chatID:  10,
		message: &tb.Message{ID: 42, Chat: &tb.Chat{ID: 10}},
	}
	valid := &tb.Callback{Message: &tb.Message{ID: 42, Chat: &tb.Chat{ID: 10}}}
	wrongMessage := &tb.Callback{Message: &tb.Message{ID: 43, Chat: &tb.Chat{ID: 10}}}
	wrongChat := &tb.Callback{Message: &tb.Message{ID: 42, Chat: &tb.Chat{ID: 11}}}
	if !emptyTrashCallbackMatches(valid, pending) {
		t.Fatal("callback from the preview message should match")
	}
	if emptyTrashCallbackMatches(wrongMessage, pending) || emptyTrashCallbackMatches(wrongChat, pending) || emptyTrashCallbackMatches(nil, pending) {
		t.Fatal("callback from another message or chat must be rejected")
	}
}

func TestEmptyTrashCallbackCancelDoesNotStartDeletion(t *testing.T) {
	app := &BotApp{}
	message := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 10}}
	pending := pendingEmptyTrash{token: "token", chatID: 10, userID: 20, message: message, expiresAt: time.Now().Add(time.Minute)}
	if !app.registerPendingEmptyTrash(pending) {
		t.Fatal("failed to register pending confirmation")
	}
	ctx := &emptyTrashCallbackTestContext{
		args:     []string{"cancel", "token"},
		sender:   &tb.User{ID: 20},
		callback: &tb.Callback{Message: message},
	}
	if err := app.HandleEmptyTrashCallback(ctx); err != nil {
		t.Fatalf("cancel callback failed: %v", err)
	}
	if _, ok := app.getPendingEmptyTrash("token"); ok {
		t.Fatal("cancelled confirmation remained pending")
	}
	if len(app.emptyTrashRunning) != 0 {
		t.Fatal("cancel callback started an empty-trash run")
	}
	if ctx.edited != "已取消清空回收站。" || ctx.response != 1 {
		t.Fatalf("unexpected cancel response/edit: response=%d edited=%v", ctx.response, ctx.edited)
	}
}

func TestEmptyTrashCallbackRejectsAnotherUser(t *testing.T) {
	app := &BotApp{}
	message := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 10}}
	if !app.registerPendingEmptyTrash(pendingEmptyTrash{token: "token", chatID: 10, userID: 20, message: message, expiresAt: time.Now().Add(time.Minute)}) {
		t.Fatal("failed to register pending confirmation")
	}
	ctx := &emptyTrashCallbackTestContext{
		args:     []string{"confirm", "token"},
		sender:   &tb.User{ID: 99},
		callback: &tb.Callback{Message: message},
	}
	if err := app.HandleEmptyTrashCallback(ctx); err != nil {
		t.Fatalf("unauthorized callback handling failed: %v", err)
	}
	if ctx.response != 1 || ctx.edited != nil || len(app.emptyTrashRunning) != 0 {
		t.Fatalf("unauthorized callback changed state: response=%d edited=%v running=%v", ctx.response, ctx.edited, app.emptyTrashRunning)
	}
	if _, ok := app.getPendingEmptyTrash("token"); !ok {
		t.Fatal("unauthorized callback consumed the pending confirmation")
	}
}

func TestPendingEmptyTrashIsScopedAndExpires(t *testing.T) {
	app := &BotApp{}
	now := time.Now()
	first := pendingEmptyTrash{token: "first", chatID: 10, userID: 20, expiresAt: now.Add(time.Minute)}
	if !app.registerPendingEmptyTrash(first) {
		t.Fatal("expected first pending confirmation to register")
	}
	if !app.claimPendingEmptyTrash("first") {
		t.Fatal("first confirmation claim should succeed")
	}
	if app.claimPendingEmptyTrash("first") {
		t.Fatal("duplicate confirmation claim must not succeed")
	}
	if _, ok := app.takePendingEmptyTrash("first"); ok {
		t.Fatal("processing confirmation must not be cancelled")
	}
	app.releasePendingEmptyTrash("first")
	if app.registerPendingEmptyTrash(pendingEmptyTrash{token: "second", chatID: 10, expiresAt: now.Add(time.Minute)}) {
		t.Fatal("same chat should not have multiple pending confirmations")
	}
	if !app.registerPendingEmptyTrash(pendingEmptyTrash{token: "third", chatID: 11, expiresAt: now.Add(time.Minute)}) {
		t.Fatal("different chats should have independent pending confirmations")
	}
	if !app.hasPendingEmptyTrash(10) || !app.hasPendingEmptyTrash(11) {
		t.Fatal("registered confirmations were not found")
	}
	app.emptyTrashMu.Lock()
	pending := app.emptyTrashPending["first"]
	pending.expiresAt = now.Add(-time.Second)
	app.emptyTrashPending["first"] = pending
	app.emptyTrashMu.Unlock()
	if app.hasPendingEmptyTrash(10) {
		t.Fatal("expired confirmation must not block a new request")
	}
}

func TestNewEmptyTrashServiceRequiresMusicRAGBridge(t *testing.T) {
	_, err := newEmptyTrashService(&config.Config{
		AIPlaylist: &config.AIPlaylistConfig{
			PineconeHost:      "https://pinecone.example",
			PineconeAPIKey:    "pinecone-test-key",
			PineconeNamespace: "data-music",
		},
		PlaylistOrganizer: &config.PlaylistOrganizerConfig{
			OliveTinURL:       "https://olivetin.example",
			OliveTinUsername:  "olive",
			OliveTinPassword:  "olive-pass",
			NavidromeURL:      "https://navidrome.example",
			NavidromeUsername: "nav",
			NavidromePassword: "nav-pass",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "music_rag_table_id") {
		t.Fatalf("service without MusicRAG bridge error = %v", err)
	}
}

func TestNewEmptyTrashServiceDoesNotRequireNavidromeLogin(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	service, err := newEmptyTrashService(&config.Config{
		AIPlaylist: &config.AIPlaylistConfig{
			PineconeHost:      server.URL,
			PineconeAPIKey:    "pinecone-key",
			PineconeNamespace: "data-music",
		},
		PlaylistOrganizer: &config.PlaylistOrganizerConfig{
			OliveTinURL:  server.URL,
			NavidromeURL: server.URL,
		},
		N8NConfig: &config.N8NConfig{
			N8NBaseUrl:      server.URL,
			APIKey:          "n8n-key",
			MusicRAGTableID: "table123",
		},
	})
	if err != nil {
		t.Fatalf("empty trash must not need Navidrome login credentials: %v", err)
	}
	if service.organizer == nil || service.stateTable == nil || !service.stateTable.configured() {
		t.Fatalf("empty trash service dependencies not initialized: %#v", service)
	}
}

func TestEmptyTrashNoTracksSkipsMutationCalls(t *testing.T) {
	mutationCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/StartActionAndWait" {
			mutationCalls++
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(t, w, map[string]any{"logEntry": map[string]any{"exitCode": 0, "stdout": `[{"playlist_name":"回收站","track_ids":[null],"file_paths":[null],"track_count":0}]`, "timedOut": false}})
	}))
	defer server.Close()

	result := testEmptyTrashService(t, server).run(t.Context(), nil)
	if !result.NoTracks || mutationCalls != 0 || formatEmptyTrashResult(result) != "✅ 回收站为空" {
		t.Fatalf("unexpected empty trash result: %#v", result)
	}
}

func TestParseTrashStringArraySupportsNestedJSON(t *testing.T) {
	got, err := parseTrashStringArray(json.RawMessage(`"[\"song1\",\"song2\"]"`), "track_ids")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"song1", "song2"}) {
		t.Fatalf("unexpected nested list: %#v", got)
	}
}

func TestValidateTrashRelativePath(t *testing.T) {
	for _, value := range []string{"album/song.flac", "音乐/专辑/song.flac"} {
		if err := validateTrashRelativePath(value); err != nil {
			t.Errorf("valid path %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", ".", "../song.flac", "album/../../song.flac", "/music/song.flac", `album\\song.flac`, "album/./song.flac"} {
		if err := validateTrashRelativePath(value); err == nil {
			t.Errorf("unsafe path %q accepted", value)
		}
	}
}

func testEmptyTrashService(t *testing.T, server *httptest.Server) *emptyTrashService {
	t.Helper()
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
	n8n := &config.N8NConfig{N8NBaseUrl: server.URL, APIKey: "n8n-test-key", MusicRAGTableID: "table123"}
	stateTable := newMusicRAGSink(n8n, server.Client())
	pineconeHost, err := validatePineconeHost(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &emptyTrashService{
		organizer:    organizer,
		stateTable:   stateTable,
		pineconeHost: strings.TrimRight(pineconeHost, "/"),
		pineconeKey:  "pinecone-test-key",
		namespace:    "data-music",
		httpClient:   server.Client(),
	}
}

func TestMusicRAGDeleteRowsBatchesAndEncodesFilters(t *testing.T) {
	var gotFilters []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.Header.Get("X-N8N-API-KEY") != "key" || r.URL.Query().Get("dryRun") == "true" {
			t.Errorf("unexpected row deletion request: %s %s", r.Method, r.URL.Path)
		}
		var filter map[string]any
		if err := json.Unmarshal([]byte(r.URL.Query().Get("filter")), &filter); err != nil {
			t.Errorf("decode filter: %v", err)
		}
		gotFilters = append(gotFilters, filter)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sink := newMusicRAGSink(&config.N8NConfig{N8NBaseUrl: server.URL, APIKey: "key", MusicRAGTableID: "table123"}, server.Client())
	ids := make([]string, 41)
	for index := range ids {
		ids[index] = fmt.Sprintf("song%d", index)
	}
	if err := sink.deleteRows(t.Context(), ids, false); err != nil {
		t.Fatal(err)
	}
	if len(gotFilters) != 2 || len(gotFilters[0]["filters"].([]any)) != 40 || len(gotFilters[1]["filters"].([]any)) != 1 {
		t.Fatalf("unexpected MusicRAG delete batches: %#v", gotFilters)
	}
}
