package musicrag

import "context"

// IndexStats is the outcome of one library pass.
type IndexStats struct {
	Enqueued int
	Removed  int
	Indexed  int
}

// IndexLibrary adds songs that are missing locally, removes vectors for songs
// that left the library, then embeds a bounded batch. One song's failure is
// recorded on that row.
func IndexLibrary(ctx context.Context, store *Store, songs []LibrarySong, classify Classifier, embed Embedder, vectors interface {
	Vectors
	Delete(context.Context, string) error
}, limit int) (IndexStats, error) {
	var stats IndexStats
	var first error
	if len(songs) > 0 {
		enqueued, stale, err := SyncLibrary(ctx, store, songs)
		if err != nil {
			return stats, err
		}
		stats.Enqueued = enqueued
		for _, id := range stale {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			if err := vectors.Delete(ctx, id); err != nil {
				_ = store.SetError(ctx, id, err.Error())
				if first == nil {
					first = err
				}
				continue
			}
			if err := store.Delete(ctx, []string{id}); err != nil {
				_ = store.SetError(ctx, id, err.Error())
				if first == nil {
					first = err
				}
				continue
			}
			stats.Removed++
		}
	}
	indexed, err := IndexPending(ctx, store, classify, embed, vectors, limit)
	stats.Indexed = indexed
	if err == nil {
		err = first
	}
	return stats, err
}
