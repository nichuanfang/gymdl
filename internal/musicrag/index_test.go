package musicrag

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type fakeClassify struct{ err error }

func (f fakeClassify) Classify(context.Context, Song) (Meta, error) {
	if f.err != nil {
		return Meta{}, f.err
	}
	return Meta{Genre: "流行", Mood: "温暖", Language: "国语", Description: "旋律舒展，适合夜间听。"}, nil
}

type fakeEmbed struct{ calls int }

func (f *fakeEmbed) Embed(context.Context, string) ([]float32, error) {
	f.calls++
	return []float32{0.1, 0.2}, nil
}

type fakeVectors struct {
	ids       []string
	texts     []string
	deleted   []string
	deleteErr error
}

func (f *fakeVectors) Upsert(_ context.Context, id string, _ []float32, metadata map[string]string) error {
	if metadata["text"] == "" || metadata["id"] != id {
		return errors.New("metadata")
	}
	f.ids = append(f.ids, id)
	f.texts = append(f.texts, metadata["text"])
	return nil
}

func (f *fakeVectors) Delete(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func TestIndexPendingMarksReadyAndKeepsGoingAfterFailure(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gymdl.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	for _, id := range []string{"bad", "good"} {
		if err := store.Enqueue(ctx, Song{MusicID: id, Title: id, Artist: "歌手", Status: StatusPending}); err != nil {
			t.Fatal(err)
		}
	}
	classify := fakeClassify{}
	embed := &fakeEmbed{}
	vectors := &fakeVectors{}
	// First song fails classification; the second must still be indexed.
	classify.err = errors.New("model down")
	done, err := IndexPending(ctx, store, &seqClassify{first: classify.err, rest: Meta{Genre: "流行", Mood: "温暖", Language: "国语", Description: "好"}}, embed, vectors, 10)
	if err == nil || done != 1 || len(vectors.ids) != 1 || vectors.ids[0] != "good" {
		t.Fatalf("done=%d err=%v ids=%v", done, err, vectors.ids)
	}
	pending, _ := store.Pending(ctx, 10)
	if len(pending) != 0 {
		t.Fatalf("failed row still pending: %#v", pending)
	}
}

type seqClassify struct {
	first error
	rest  Meta
	n     int
}

func (s *seqClassify) Classify(context.Context, Song) (Meta, error) {
	s.n++
	if s.n == 1 {
		return Meta{}, s.first
	}
	return s.rest, nil
}

func TestIndexPendingUsesManualMetaAndRetriesFailedRows(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.Enqueue(ctx, Song{MusicID: "manual", Title: "夜曲", Artist: "周杰伦", Genre: "流行", Mood: "忧伤", Language: "国语", Description: "已有简介", Status: StatusFailed}); err != nil {
		t.Fatal(err)
	}
	classify := &countingClassify{}
	vectors := &fakeVectors{}
	done, err := IndexPending(ctx, store, classify, &fakeEmbed{}, vectors, 10)
	if err != nil || done != 1 || classify.calls != 0 || len(vectors.ids) != 1 || len(vectors.deleted) != 1 || vectors.deleted[0] != "manual" {
		t.Fatalf("done=%d err=%v calls=%d ids=%v deleted=%v", done, err, classify.calls, vectors.ids, vectors.deleted)
	}
	if !stringsContains(vectors.texts[0], "介绍:已有简介") {
		t.Fatal(vectors.texts)
	}
}

func TestIndexPendingKeepsGoingWhenManualVectorDeleteFails(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.Enqueue(ctx, Song{MusicID: "new", Title: "新", Artist: "歌手", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(ctx, Song{MusicID: "old", Title: "旧", Genre: "流行", Mood: "忧伤", Language: "国语", Description: "简介", Status: StatusFailed}); err != nil {
		t.Fatal(err)
	}
	vectors := &fakeVectors{deleteErr: errors.New("文档删除失败")}
	done, err := IndexPending(ctx, store, fakeClassify{}, &fakeEmbed{}, vectors, 10)
	if err == nil || done != 1 || len(vectors.ids) != 1 || vectors.ids[0] != "new" {
		t.Fatalf("done=%d err=%v ids=%v", done, err, vectors.ids)
	}
	rows, err := store.Indexable(ctx, 10)
	if err != nil || len(rows) != 1 || rows[0].MusicID != "old" || rows[0].Status != StatusFailed || rows[0].Description != "简介" || rows[0].Error == "" {
		t.Fatalf("failed manual row was not kept for retry: %#v err=%v", rows, err)
	}
}

type countingClassify struct{ calls int }

func (c *countingClassify) Classify(context.Context, Song) (Meta, error) {
	c.calls++
	return Meta{}, errors.New("should not be called")
}

func TestDocumentTextMatchesIndexedSentence(t *testing.T) {
	text := DocumentText(Song{Title: "夜曲", Artist: stringsRepeat("周", 60)}, Meta{Genre: "流行", Mood: "忧伤", Language: "国语", Description: "简介"})
	if !stringsHasPrefix(text, "歌名:《夜曲》 歌手:") || !stringsContains(text, "流派:流行") || !stringsContains(text, "介绍:简介") {
		t.Fatal(text)
	}
}

func stringsRepeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
func stringsContains(s, part string) bool {
	return len(part) == 0 || (len(s) >= len(part) && (s == part || len(s) > 0 && (func() bool {
		for i := 0; i+len(part) <= len(s); i++ {
			if s[i:i+len(part)] == part {
				return true
			}
		}
		return false
	})()))
}
