package cron

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/musicrag"
	"github.com/nichuanfang/gymdl/internal/scrobble"
	"github.com/nichuanfang/gymdl/internal/storage"
)

const libraryIndexBatch = 20

func musicRAGImportConfigured(c *config.Config) bool {
	return c != nil && c.N8NConfig != nil &&
		strings.TrimSpace(c.N8NConfig.N8NBaseUrl) != "" &&
		strings.TrimSpace(c.N8NConfig.APIKey) != "" &&
		strings.TrimSpace(c.N8NConfig.MusicRAGTableID) != ""
}

func musicLibraryIndexConfigured(c *config.Config) bool {
	if c == nil || c.AI == nil || c.AIPlaylist == nil || c.PlaylistOrganizer == nil {
		return false
	}
	if strings.TrimSpace(c.AI.BaseUrl) == "" || strings.TrimSpace(c.AI.ApiKey) == "" || strings.TrimSpace(c.AI.Model) == "" {
		return false
	}
	if strings.TrimSpace(c.AIPlaylist.PineconeHost) == "" || strings.TrimSpace(c.AIPlaylist.PineconeAPIKey) == "" || strings.TrimSpace(c.AIPlaylist.PineconeNamespace) == "" {
		return false
	}
	return strings.TrimSpace(c.PlaylistOrganizer.OliveTinURL) != ""
}

func scrobbleSyncConfigured(c *config.Config) bool {
	return c != nil && c.Lastfm != nil && c.Lastfm.Enable &&
		strings.TrimSpace(c.Lastfm.Username) != "" &&
		strings.TrimSpace(c.Lastfm.APIKey) != "" &&
		c.AI != nil && strings.TrimSpace(c.AI.HistoryWriteDSN) != ""
}

// importMusicRAG copies the live n8n table once. It does not call a model.
func importMusicRAG(c *config.Config) error {
	if !musicRAGImportConfigured(c) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, err := musicrag.Open(storage.DefaultDatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	_, err = musicrag.ImportFromN8N(ctx, store, musicrag.N8NSource{
		BaseURL: c.N8NConfig.N8NBaseUrl,
		APIKey:  c.N8NConfig.APIKey,
		TableID: c.N8NConfig.MusicRAGTableID,
	})
	return err
}

// indexMusicLibrary refreshes the local catalog and embeds a small batch.
// An empty or failed library listing does not delete existing rows.
func indexMusicLibrary(c *config.Config) error {
	if !musicLibraryIndexConfigured(c) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	store, err := musicrag.Open(storage.DefaultDatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if musicRAGImportConfigured(c) {
		if _, err := musicrag.ImportFromN8N(ctx, store, musicrag.N8NSource{
			BaseURL: c.N8NConfig.N8NBaseUrl,
			APIKey:  c.N8NConfig.APIKey,
			TableID: c.N8NConfig.MusicRAGTableID,
		}); err != nil {
			return err
		}
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	library := musicrag.OliveTinLibrary{
		BaseURL:  c.PlaylistOrganizer.OliveTinURL,
		Username: c.PlaylistOrganizer.OliveTinUsername,
		Password: c.PlaylistOrganizer.OliveTinPassword,
		Client:   client,
	}
	songs, err := library.Songs(ctx)
	if err != nil {
		return err
	}
	embedKey := strings.TrimSpace(c.AIPlaylist.EmbeddingAPIKey)
	if embedKey == "" {
		embedKey = strings.TrimSpace(c.AI.ApiKey)
	}
	embedBase := strings.TrimSpace(c.AIPlaylist.EmbeddingBaseURL)
	if embedBase == "" {
		embedBase = strings.TrimSpace(c.AI.BaseUrl)
	}
	embedModel := strings.TrimSpace(c.AIPlaylist.EmbeddingModel)
	if embedModel == "" {
		embedModel = "text-embedding-3-small"
	}
	_, err = musicrag.IndexLibrary(ctx, store, songs,
		musicrag.ChatClassifier{BaseURL: c.AI.BaseUrl, APIKey: c.AI.ApiKey, Model: c.AI.Model, Client: client},
		musicrag.OpenAIEmbedder{BaseURL: embedBase, APIKey: embedKey, Model: embedModel, Client: client},
		musicrag.Pinecone{Host: c.AIPlaylist.PineconeHost, APIKey: c.AIPlaylist.PineconeAPIKey, Namespace: c.AIPlaylist.PineconeNamespace, Client: client},
		libraryIndexBatch)
	return err
}

// syncScrobbles appends new Last.fm listens to the table reports already read.
func syncScrobbles(c *config.Config) error {
	if !scrobbleSyncConfigured(c) {
		return nil
	}
	table := strings.TrimSpace(c.AI.HistoryTable)
	if table == "" {
		table = "music_scrobble_records"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	store, err := scrobble.OpenPostgres(c.AI.HistoryWriteDSN, table)
	if err != nil {
		return err
	}
	defer store.Close()
	since, _, err := store.Latest(ctx)
	if err != nil {
		return err
	}
	_, err = scrobble.Sync(ctx, &http.Client{Timeout: time.Minute}, c.Lastfm.Username, c.Lastfm.APIKey, since, store)
	return err
}
