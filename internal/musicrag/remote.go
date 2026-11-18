package musicrag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const vectorDimensions = 1536

const classifyPrompt = `你是一个专业的音乐数据结构化专家，负责将歌曲信息转换为适合检索的结构化文本。

只输出一个 JSON 对象，不要代码块，不要解释。结构固定为：
{"title":"歌曲名称","artist":"歌手名称","year":"年份","genre":"流派","mood":"情绪","language":"语言","description":"详细描述"}

规则：
- title、artist、year 必须与输入逐字符一致。artist 为空时输出 Unknown。
- genre 用中文，2 到 5 个，用英文逗号分隔。纯音乐作品必须包含“纯音乐”。
- mood 用中文，2 到 4 个，用英文逗号分隔。
- language 用中文。没有人声时必须填写“纯音乐”，其余填写国语、粤语、英语、日语、韩语或小语种。
- description 用中文，75 到 115 个字，中间不要换行。先写情感，再写音色和画面，最后收束氛围。
- 输入里的 url 只作为风格线索。打不开就依据歌名、歌手、专辑和你已有的音乐知识判断。
- 每个字段都不能为空。`

// ChatClassifier asks an OpenAI-compatible chat model for genre, mood, language and a description.
type ChatClassifier struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

func (c ChatClassifier) Classify(ctx context.Context, song Song) (Meta, error) {
	payload, err := json.Marshal(map[string]string{
		"id": song.MusicID, "title": song.Title, "artist": song.Artist,
		"album": song.Album, "year": song.Year, "url": song.URL,
	})
	if err != nil {
		return Meta{}, err
	}
	body, err := json.Marshal(map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": classifyPrompt},
			{"role": "user", "content": string(payload)},
		},
		"temperature": 0.2,
	})
	if err != nil {
		return Meta{}, err
	}
	response, err := postJSON(ctx, c.Client, joinAPI(c.BaseURL, "/chat/completions"), c.APIKey, body)
	if err != nil {
		return Meta{}, err
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(response, &parsed); err != nil || len(parsed.Choices) == 0 {
		return Meta{}, fmt.Errorf("曲风结果无法读取")
	}
	meta, err := ParseMeta(parsed.Choices[0].Message.Content)
	if err != nil {
		return Meta{}, err
	}
	meta.Genre = strings.TrimSpace(meta.Genre)
	meta.Mood = strings.TrimSpace(meta.Mood)
	meta.Language = strings.TrimSpace(meta.Language)
	meta.Description = strings.TrimSpace(meta.Description)
	return meta, nil
}

// OpenAIEmbedder requests the same 1536-dimension vector the Pinecone index already uses.
type OpenAIEmbedder struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

func (e OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(map[string]any{
		"model": e.Model, "input": text, "dimensions": vectorDimensions,
	})
	if err != nil {
		return nil, err
	}
	response, err := postJSON(ctx, e.Client, joinAPI(e.BaseURL, "/embeddings"), e.APIKey, body)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response, &parsed); err != nil || len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) != vectorDimensions {
		return nil, fmt.Errorf("嵌入维度不是 %d", vectorDimensions)
	}
	return parsed.Data[0].Embedding, nil
}

// Pinecone writes one document into the existing data-music namespace.
type Pinecone struct {
	Host      string
	APIKey    string
	Namespace string
	Client    *http.Client
}

func (p Pinecone) Upsert(ctx context.Context, id string, values []float32, metadata map[string]string) error {
	meta := make(map[string]string, len(metadata))
	for key, value := range metadata {
		meta[key] = value
	}
	body, err := json.Marshal(map[string]any{
		"namespace": p.Namespace,
		"vectors": []map[string]any{{
			"id": id, "values": values, "metadata": meta,
		}},
	})
	if err != nil {
		return err
	}
	_, err = postPinecone(ctx, p, "/vectors/upsert", body)
	return err
}

func (p Pinecone) Delete(ctx context.Context, id string) error {
	body, err := json.Marshal(map[string]any{
		"namespace": p.Namespace,
		"filter":    map[string]any{"id": map[string]any{"$eq": id}},
	})
	if err != nil {
		return err
	}
	_, err = postPinecone(ctx, p, "/vectors/delete", body)
	return err
}

func postPinecone(ctx context.Context, p Pinecone, path string, body []byte) ([]byte, error) {
	endpoint, err := url.JoinPath(strings.TrimRight(p.Host, "/"), strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Api-Key", p.APIKey)
	req.Header.Set("X-Pinecone-Api-Version", "2025-04")
	return doJSON(ctx, p.Client, req)
}

func postJSON(ctx context.Context, client *http.Client, endpoint, apiKey string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	return doJSON(ctx, client, req)
}

func doJSON(ctx context.Context, client *http.Client, req *http.Request) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	return payload, nil
}

func joinAPI(baseURL, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + path
}
