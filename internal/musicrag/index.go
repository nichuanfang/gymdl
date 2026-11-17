package musicrag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Meta is the listening description previously produced by music-rag-processor.
type Meta struct {
	Genre       string `json:"genre"`
	Mood        string `json:"mood"`
	Language    string `json:"language"`
	Description string `json:"description"`
}

// Classifier writes genre, mood, language and a short description for one song.
type Classifier interface {
	Classify(context.Context, Song) (Meta, error)
}

// Embedder returns the same 1536-dimension vector the Pinecone index already uses.
type Embedder interface {
	Embed(context.Context, string) ([]float32, error)
}

// Vectors stores one song document. ID is the Navidrome music_id.
type Vectors interface {
	Upsert(ctx context.Context, id string, values []float32, metadata map[string]string) error
}

// IndexPending classifies and embeds a bounded batch of songs still marked pending.
// A failure is recorded on that row and does not stop the rest of the batch.
func IndexPending(ctx context.Context, store *Store, classify Classifier, embed Embedder, vectors Vectors, limit int) (int, error) {
	pending, err := store.Pending(ctx, limit)
	if err != nil {
		return 0, err
	}
	done := 0
	var first error
	for _, song := range pending {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		if err := indexOne(ctx, store, classify, embed, vectors, song); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		done++
	}
	return done, first
}

func indexOne(ctx context.Context, store *Store, classify Classifier, embed Embedder, vectors Vectors, song Song) error {
	meta, err := classify.Classify(ctx, song)
	if err != nil {
		_ = store.Mark(ctx, Song{MusicID: song.MusicID, Status: StatusFailed, Error: err.Error()})
		return err
	}
	text := DocumentText(song, meta)
	values, err := embed.Embed(ctx, text)
	if err != nil {
		_ = store.Mark(ctx, Song{MusicID: song.MusicID, Genre: meta.Genre, Mood: meta.Mood, Language: meta.Language, Description: meta.Description, Status: StatusFailed, Error: err.Error()})
		return err
	}
	err = vectors.Upsert(ctx, song.MusicID, values, map[string]string{
		"id": song.MusicID, "year": song.Year, "title": song.Title, "artist": song.Artist,
		"genre": meta.Genre, "mood": meta.Mood, "language": meta.Language, "text": text,
	})
	if err != nil {
		_ = store.Mark(ctx, Song{MusicID: song.MusicID, Genre: meta.Genre, Mood: meta.Mood, Language: meta.Language, Description: meta.Description, Status: StatusFailed, Error: err.Error()})
		return err
	}
	return store.Mark(ctx, Song{MusicID: song.MusicID, Genre: meta.Genre, Mood: meta.Mood, Language: meta.Language, Description: meta.Description, Status: StatusReady})
}

// DocumentText matches the sentence stored in Pinecone by the old n8n loader.
func DocumentText(song Song, meta Meta) string {
	artist := song.Artist
	if len([]rune(artist)) > 50 {
		artist = string([]rune(artist)[:50])
	}
	return fmt.Sprintf("歌名:《%s》 歌手:%s 流派:%s 听感:%s 语言:%s 介绍:%s", song.Title, artist, meta.Genre, meta.Mood, meta.Language, meta.Description)
}

// ParseMeta accepts the JSON object the classifier is required to return.
func ParseMeta(content string) (Meta, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	var meta Meta
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &meta); err != nil {
		return Meta{}, fmt.Errorf("曲风结果不是 JSON")
	}
	if meta.Genre == "" || meta.Mood == "" || meta.Language == "" || meta.Description == "" {
		return Meta{}, fmt.Errorf("曲风结果缺少字段")
	}
	return meta, nil
}
