package musicrag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatEmbedAndPineconeUseTheExistingIndexContract(t *testing.T) {
	var vector []float32
	for i := 0; i < vectorDimensions; i++ {
		vector = append(vector, 0.01)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" && r.Header.Get("Api-Key") != "pine-key" {
			t.Errorf("missing credential on %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"title\":\"夜曲\",\"artist\":\"周杰伦\",\"year\":\"2005\",\"genre\":\"流行\",\"mood\":\"忧伤\",\"language\":\"国语\",\"description\":\"简介\"}"}}]}`))
		case "/v1/embeddings":
			var request struct {
				Dimensions float64 `json:"dimensions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Dimensions != vectorDimensions {
				t.Errorf("dimensions = %#v err=%v", request, err)
			}
			encoded, _ := json.Marshal(vector)
			_, _ = w.Write([]byte(`{"data":[{"embedding":` + string(encoded) + `}]}`))
		case "/vectors/upsert":
			if r.Header.Get("X-Pinecone-Api-Version") != "2025-04" {
				t.Errorf("pinecone version header missing")
			}
			var request struct {
				Namespace string `json:"namespace"`
				Vectors   []struct {
					ID string `json:"id"`
				} `json:"vectors"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Namespace != "data-music" || len(request.Vectors) != 1 || request.Vectors[0].ID != "song-1" {
				t.Errorf("upsert = %#v err=%v", request, err)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	meta, err := (ChatClassifier{BaseURL: server.URL, APIKey: "key", Model: "music-model", Client: server.Client()}).Classify(context.Background(), Song{MusicID: "song-1", Title: "夜曲"})
	if err != nil || meta.Genre != "流行" || meta.Language != "国语" {
		t.Fatalf("meta=%#v err=%v", meta, err)
	}
	values, err := (OpenAIEmbedder{BaseURL: server.URL + "/v1", APIKey: "key", Model: "text-embedding-3-small", Client: server.Client()}).Embed(context.Background(), "文本")
	if err != nil || len(values) != vectorDimensions {
		t.Fatalf("embed len=%d err=%v", len(values), err)
	}
	if err := (Pinecone{Host: server.URL, APIKey: "pine-key", Namespace: "data-music", Client: server.Client()}).Upsert(context.Background(), "song-1", values, map[string]string{"id": "song-1", "text": "文本"}); err != nil {
		t.Fatal(err)
	}
}
