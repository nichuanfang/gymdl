package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/utils"
)

const DefaultWebDAVPendingDir = "data/webdav_pending"

// PendingUpload is a file that has been safely staged locally because the
// WebDAV server was unavailable. The payload stays under the task directory
// until a retry succeeds.
type PendingUpload struct {
	ID          string    `json:"id"`
	RemoteDir   string    `json:"remote_dir"`
	FileName    string    `json:"file_name"`
	Title       string    `json:"title,omitempty"`
	Artist      string    `json:"artist,omitempty"`
	Album       string    `json:"album,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Attempts    int       `json:"attempts"`
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	PayloadPath string    `json:"-"`
}

// SetPendingUploadMetadata attaches display metadata used by WebUI.
// It is best-effort because the payload is already durable.
func (o *WebDAVOutbox) SetPendingUploadMetadata(id, title, artist, album string) {
	if o == nil || id == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	metaPath := o.metaPath(id)
	item, err := readPendingMeta(metaPath)
	if err != nil {
		return
	}
	item.ID = id
	item.Title = title
	item.Artist = artist
	item.Album = album
	if err := writePendingMeta(metaPath, item); err != nil && logger != nil {
		logger.Warn(fmt.Sprintf("⚠️ 更新 WebDAV 待上传元数据失败 %s: %v", id, err))
	}
}

// WebDAVOutbox stores pending WebDAV uploads in a persistent directory. It is
// deliberately independent of data/temp, which is cleared on shutdown.
type WebDAVOutbox struct {
	root      string
	mu        sync.Mutex
	processMu sync.Mutex
}

func NewWebDAVOutbox(root string) *WebDAVOutbox {
	return &WebDAVOutbox{root: root}
}

func (o *WebDAVOutbox) Root() string {
	if o == nil {
		return ""
	}
	return o.root
}

func (o *WebDAVOutbox) taskDir(id string) string {
	return filepath.Join(o.root, id)
}

func (o *WebDAVOutbox) payloadPath(id, fileName string) string {
	return filepath.Join(o.taskDir(id), "payload", filepath.Base(fileName))
}

func (o *WebDAVOutbox) metaPath(id string) string {
	return filepath.Join(o.taskDir(id), "meta.json")
}

// Enqueue moves a local file into the durable outbox. A temporary task
// directory is used so an interrupted move never exposes a half-written item.
func (o *WebDAVOutbox) Enqueue(src, remoteDir string) (*PendingUpload, error) {
	if o == nil {
		return nil, fmt.Errorf("WebDAV outbox is not initialized")
	}
	if src == "" {
		return nil, fmt.Errorf("source path cannot be empty")
	}
	if err := os.MkdirAll(o.root, 0o755); err != nil {
		return nil, fmt.Errorf("create WebDAV outbox directory: %w", err)
	}

	id := newOutboxID()
	tmpDir := filepath.Join(o.root, ".tmp-"+id)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("create WebDAV outbox task directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	dst := filepath.Join(tmpDir, "payload", filepath.Base(src))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, fmt.Errorf("create WebDAV outbox payload directory: %w", err)
	}
	if err := moveOutboxFile(src, dst); err != nil {
		return nil, fmt.Errorf("stage WebDAV upload: %w", err)
	}

	item := &PendingUpload{
		ID:          id,
		RemoteDir:   normalizeRemoteDir(remoteDir),
		FileName:    filepath.Base(src),
		CreatedAt:   time.Now(),
		Attempts:    0,
		PayloadPath: dst,
	}
	if err := writePendingMeta(filepath.Join(tmpDir, "meta.json"), item); err != nil {
		_ = moveOutboxFile(dst, src)
		return nil, fmt.Errorf("write WebDAV outbox metadata: %w", err)
	}

	finalDir := o.taskDir(id)
	if err := os.Rename(tmpDir, finalDir); err != nil {
		_ = moveOutboxFile(dst, src)
		return nil, fmt.Errorf("commit WebDAV outbox item: %w", err)
	}
	cleanup = false
	item.PayloadPath = o.payloadPath(id, item.FileName)
	return item, nil
}

// List returns pending items ordered by creation time.
func (o *WebDAVOutbox) List() ([]*PendingUpload, error) {
	if o == nil {
		return nil, fmt.Errorf("WebDAV outbox is not initialized")
	}
	entries, err := os.ReadDir(o.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read WebDAV outbox: %w", err)
	}

	items := make([]*PendingUpload, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		item, err := readPendingMeta(o.metaPath(entry.Name()))
		if err != nil {
			if logger != nil {
				logger.Warn(fmt.Sprintf("⚠️ 跳过损坏的 WebDAV 待上传记录 %s: %v", entry.Name(), err))
			}
			continue
		}
		item.ID = entry.Name()
		item.PayloadPath = o.payloadPath(entry.Name(), item.FileName)
		// Only payloads written by this implementation use payload/<name>.
		// Older outbox layouts are not supported because none have shipped.
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items, nil
}

func (o *WebDAVOutbox) Count() int {
	items, err := o.List()
	if err != nil {
		return 0
	}
	return len(items)
}

// ProcessOnce uploads pending items oldest first. Any failure stops the pass
// so a NAS outage does not cause one retry storm per queued file.
func (o *WebDAVOutbox) ProcessOnce(upload func(localPath, remoteDir string) error) (int, error) {
	if o == nil {
		return 0, fmt.Errorf("WebDAV outbox is not initialized")
	}
	o.processMu.Lock()
	defer o.processMu.Unlock()

	items, err := o.List()
	if err != nil {
		return 0, err
	}

	uploaded := 0
	for _, item := range items {
		if !item.retryDue(time.Now()) {
			continue
		}
		if _, err := os.Stat(item.PayloadPath); err != nil {
			err = fmt.Errorf("待上传文件不存在: %w", err)
			o.recordFailure(item, err)
			return uploaded, err
		}
		if err := upload(item.PayloadPath, item.RemoteDir); err != nil {
			o.recordFailure(item, err)
			return uploaded, err
		}
		if err := os.RemoveAll(o.taskDir(item.ID)); err != nil {
			return uploaded, fmt.Errorf("remove completed WebDAV outbox item %s: %w", item.ID, err)
		}
		uploaded++
		if logger != nil {
			logger.Info(fmt.Sprintf("💡 WebDAV 补传成功: %s/%s", item.RemoteDir, item.FileName))
		}
	}
	return uploaded, nil
}

// RemoveByRemote removes pending items that target the same remote file.
func (o *WebDAVOutbox) RemoveByRemote(remoteDir, fileName string) (int, error) {
	if o == nil {
		return 0, nil
	}
	o.processMu.Lock()
	defer o.processMu.Unlock()

	items, err := o.List()
	if err != nil {
		return 0, err
	}
	wantDir := normalizeRemoteDir(remoteDir)
	removed := 0
	for _, item := range items {
		if item.FileName != fileName || normalizeRemoteDir(item.RemoteDir) != wantDir {
			continue
		}
		if err := os.RemoveAll(o.taskDir(item.ID)); err != nil {
			return removed, fmt.Errorf("remove pending WebDAV upload %s: %w", item.ID, err)
		}
		removed++
	}
	return removed, nil
}

func (o *WebDAVOutbox) recordFailure(item *PendingUpload, uploadErr error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	item.Attempts++
	item.LastAttempt = time.Now()
	item.LastError = utils.TruncateString(uploadErr.Error(), 500)
	if err := writePendingMeta(o.metaPath(item.ID), item); err != nil && logger != nil {
		logger.Warn(fmt.Sprintf("⚠️ 更新 WebDAV 待上传记录失败 %s: %v", item.ID, err))
	}
}

func (item *PendingUpload) retryDue(now time.Time) bool {
	if item.Attempts <= 0 || item.LastAttempt.IsZero() {
		return true
	}
	delay := time.Minute
	for i := 1; i < item.Attempts && delay < time.Hour; i++ {
		delay *= 2
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	return !now.Before(item.LastAttempt.Add(delay))
}

func normalizeRemoteDir(remoteDir string) string {
	remoteDir = strings.Trim(filepath.ToSlash(remoteDir), "/")
	clean := path.Clean("/" + remoteDir)
	return strings.Trim(clean, "/")
}

func newOutboxID() string {
	var random [6]byte
	if _, err := rand.Read(random[:]); err == nil {
		return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(random[:]))
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func writePendingMeta(metaPath string, item *PendingUpload) error {
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	tmp := metaPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath)
}

func readPendingMeta(metaPath string) (*PendingUpload, error) {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, err
	}
	var item PendingUpload
	if err := json.Unmarshal(data, &item); err != nil {
		return nil, err
	}
	if item.FileName == "" {
		return nil, fmt.Errorf("metadata has empty file name")
	}
	return &item, nil
}

func moveOutboxFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if syncErr != nil {
		_ = os.Remove(dst)
		return syncErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	if err := os.Remove(src); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

// StartPendingUploader retries the outbox in the background until ctx ends.
func (w *WebDAV) StartPendingUploader(ctx context.Context, interval time.Duration) {
	if w == nil || w.Outbox == nil {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	if logger != nil {
		logger.Info(fmt.Sprintf("💡 WebDAV 待上传队列已启动，目录: %s", w.Outbox.Root()))
	}
	w.processPendingUploads()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processPendingUploads()
		}
	}
}

func (w *WebDAV) processPendingUploads() {
	if w == nil || w.Outbox == nil {
		return
	}
	if w.Outbox.Count() == 0 {
		return
	}
	if !w.CheckConnection() {
		return
	}
	uploaded, err := w.Outbox.ProcessOnce(w.UploadTo)
	if err != nil {
		if logger != nil {
			logger.Warn(fmt.Sprintf("⚠️ WebDAV 补传暂停，稍后重试: %v", err))
		}
		return
	}
	if uploaded > 0 && logger != nil {
		logger.Info(fmt.Sprintf("💡 WebDAV 本轮补传完成: %d 个文件", uploaded))
	}
}

func (w *WebDAV) PendingUploadCount() int {
	if w == nil || w.Outbox == nil {
		return 0
	}
	return w.Outbox.Count()
}

// PendingWebDAVUploadCount reports the durable outbox size for status views.
func PendingWebDAVUploadCount() int {
	if GlobalWebDAV == nil {
		return 0
	}
	return GlobalWebDAV.PendingUploadCount()
}
