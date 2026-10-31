package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/studio-b12/gowebdav"
	"go.uber.org/zap"
)

func TestWebDAVOutboxEnqueueKeepsPayloadUntilUploadSucceeds(t *testing.T) {
	root := t.TempDir()
	outbox := NewWebDAVOutbox(root)
	src := filepath.Join(t.TempDir(), "song.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	item, err := outbox.Enqueue(src, "/artist/album")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source should be moved into outbox, stat err=%v", err)
	}
	if item.RemoteDir != "artist/album" || item.FileName != "song.m4a" {
		t.Fatalf("unexpected item: %#v", item)
	}
	if data, err := os.ReadFile(item.PayloadPath); err != nil || string(data) != "audio" {
		t.Fatalf("payload not preserved: %q, %v", data, err)
	}

	uploaded := 0
	_, err = outbox.ProcessOnce(func(string, string) error {
		return errors.New("nas offline")
	})
	if err == nil {
		t.Fatal("expected upload failure")
	}
	items, err := outbox.List()
	if err != nil || len(items) != 1 || items[0].Attempts != 1 || items[0].LastError == "" {
		t.Fatalf("failure should be recorded without dropping payload: %#v, %v", items, err)
	}

	_, err = outbox.ProcessOnce(func(localPath, remoteDir string) error {
		if remoteDir != "artist/album" {
			t.Fatalf("remoteDir = %q", remoteDir)
		}
		uploaded++
		return nil
	})
	if err != nil || uploaded != 0 {
		t.Fatalf("backoff should postpone immediate retry: uploaded=%d err=%v", uploaded, err)
	}

	items[0].Attempts = 0
	items[0].LastAttempt = items[0].LastAttempt.Add(-time.Hour)
	if err := writePendingMeta(outbox.metaPath(items[0].ID), items[0]); err != nil {
		t.Fatal(err)
	}
	_, err = outbox.ProcessOnce(func(string, string) error {
		uploaded++
		return nil
	})
	if err != nil || uploaded != 1 {
		t.Fatalf("retry after backoff: uploaded=%d err=%v", uploaded, err)
	}
	if left, err := outbox.List(); err != nil || len(left) != 0 {
		t.Fatalf("successful upload should clear outbox: %#v, %v", left, err)
	}
}

func TestWebDAVUploadOrStageFallsBackToQueue(t *testing.T) {
	previous := GlobalWebDAV
	t.Cleanup(func() { GlobalWebDAV = previous })
	previousLogger := logger
	t.Cleanup(func() { logger = previousLogger })
	logger = zap.NewNop()

	wd := &WebDAV{
		Config: &config.WebDAVConfig{WebDAVUrl: "http://127.0.0.1:1", WebDAVUser: "u", WebDAVPass: "p"},
		Client: gowebdav.NewClient("http://127.0.0.1:1", "u", "p"),
		Outbox: NewWebDAVOutbox(t.TempDir()),
	}
	wd.Client.SetTimeout(200 * time.Millisecond)
	GlobalWebDAV = wd

	src := filepath.Join(t.TempDir(), "song.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	item, err := wd.UploadOrStage(src, "/artist/album")
	if !errors.Is(err, ErrWebDAVDeferred) || item == nil {
		t.Fatalf("expected deferred upload, item=%#v err=%v", item, err)
	}
	if GlobalWebDAV.PendingUploadCount() != 1 {
		t.Fatalf("pending count = %d", GlobalWebDAV.PendingUploadCount())
	}

}
