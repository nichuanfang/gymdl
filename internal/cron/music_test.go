package cron

import (
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func TestMusicJobGatesStayOffUntilConfigured(t *testing.T) {
	if musicRAGImportConfigured(&config.Config{}) || musicLibraryIndexConfigured(&config.Config{}) || scrobbleSyncConfigured(&config.Config{}) {
		t.Fatal("unconfigured music jobs must stay off")
	}
	if err := importMusicRAG(&config.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := indexMusicLibrary(&config.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := syncScrobbles(&config.Config{Lastfm: &config.LastfmConfig{Enable: true, Username: "listener", APIKey: "key"}}); err != nil {
		t.Fatal(err)
	}

	ready := &config.Config{
		N8NConfig: &config.N8NConfig{N8NBaseUrl: "https://n8n.example", APIKey: "n8n-key", MusicRAGTableID: "table"},
		AI: &config.AIConfig{
			BaseUrl: "https://ai.example/v1", ApiKey: "ai-key", Model: "chat",
			HistoryWriteDSN: "postgres://writer@db/n8n", HistoryTable: "music_scrobble_records",
		},
		AIPlaylist:        &config.AIPlaylistConfig{PineconeHost: "https://pinecone.example", PineconeAPIKey: "pinecone-key", PineconeNamespace: "data-music"},
		PlaylistOrganizer: &config.PlaylistOrganizerConfig{OliveTinURL: "https://olive.example"},
		Lastfm:            &config.LastfmConfig{Enable: true, Username: "listener", APIKey: "lastfm-key"},
	}
	if !musicRAGImportConfigured(ready) || !musicLibraryIndexConfigured(ready) || !scrobbleSyncConfigured(ready) {
		t.Fatal("configured music jobs were not armed")
	}
	ready.AI.HistoryWriteDSN = ""
	if scrobbleSyncConfigured(ready) {
		t.Fatal("listening sync must keep the read-only DSN and wait for a writer")
	}
	if jobLabel("importMusicRAG") != "导入曲库状态" || jobLabel("indexMusicLibrary") != "索引曲库" || jobLabel("syncScrobbles") != "同步收听记录" {
		t.Fatalf("labels = %q %q %q", jobLabel("importMusicRAG"), jobLabel("indexMusicLibrary"), jobLabel("syncScrobbles"))
	}
}
