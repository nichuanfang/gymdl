package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/audiodupe"
	"github.com/nichuanfang/gymdl/internal/gin/response"
	"github.com/nichuanfang/gymdl/internal/storage"
)

// Library duplicate scan compares audio that is already in the library.
// Scanning never deletes files and never changes download or rediscover decisions.
// A later keep request deletes only the versions the user checked in that group.

type duplicateScanner struct {
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	report  audiodupe.Report
}

var libraryDuplicates duplicateScanner

func init() {
	libraryDuplicates.report = audiodupe.Report{Status: "idle", Groups: []audiodupe.Group{}}
}

func HandleLibraryDuplicates(c *gin.Context) {
	report := currentDuplicateReport()
	if report.Status == "idle" {
		if store, err := audiodupe.Open(storage.DefaultDatabasePath); err == nil {
			if saved, err := store.LoadReport(c.Request.Context()); err == nil && saved.Status != "" {
				report = saved
			}
			_ = store.Close()
		}
	}
	response.Success(c, report)
}

func HandleStartLibraryDuplicateScan(c *gin.Context) {
	if _, err := exec.LookPath("fpcalc"); err != nil {
		response.Fail(c, http.StatusServiceUnavailable, "运行环境还没有安装 fpcalc，暂时不能比对音频")
		return
	}
	cfg := GetWebConfig()
	if cfg == nil || cfg.Tidy == nil {
		response.Fail(c, http.StatusServiceUnavailable, "整理目录还没准备好")
		return
	}
	libraryDuplicates.mu.Lock()
	if libraryDuplicates.running {
		libraryDuplicates.mu.Unlock()
		response.Fail(c, http.StatusConflict, "已有一次查重在进行")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	libraryDuplicates.running = true
	libraryDuplicates.cancel = cancel
	libraryDuplicates.report = audiodupe.Report{Status: "running", UpdatedAt: time.Now().UTC(), Groups: []audiodupe.Group{}}
	libraryDuplicates.mu.Unlock()
	go runLibraryDuplicateScan(ctx, cfg)
	response.Success(c, currentDuplicateReport())
}

func currentDuplicateReport() audiodupe.Report {
	libraryDuplicates.mu.Lock()
	defer libraryDuplicates.mu.Unlock()
	report := libraryDuplicates.report
	if report.Groups == nil {
		report.Groups = []audiodupe.Group{}
	}
	return report
}

func setDuplicateReport(report audiodupe.Report) {
	libraryDuplicates.mu.Lock()
	libraryDuplicates.report = report
	libraryDuplicates.mu.Unlock()
}

func runLibraryDuplicateScan(ctx context.Context, cfg *config.Config) {
	defer func() {
		libraryDuplicates.mu.Lock()
		libraryDuplicates.running = false
		if libraryDuplicates.cancel != nil {
			libraryDuplicates.cancel()
			libraryDuplicates.cancel = nil
		}
		libraryDuplicates.mu.Unlock()
	}()
	report := currentDuplicateReport()
	fail := func(err error) {
		report.Status = "error"
		report.Error = err.Error()
		report.UpdatedAt = time.Now().UTC()
		setDuplicateReport(report)
		persistDuplicateReport(report)
	}
	files, err := collectLibraryAudio(cfg)
	if err != nil {
		fail(err)
		return
	}
	report.Total = len(files)
	report.UpdatedAt = time.Now().UTC()
	setDuplicateReport(report)
	store, err := audiodupe.Open(storage.DefaultDatabasePath)
	if err != nil {
		fail(fmt.Errorf("查重记录还没准备好"))
		return
	}
	defer store.Close()
	tracks := make([]audiodupe.Track, 0, len(files))
	var tracksMu sync.Mutex
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, file := range files {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(file FileEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			track, ferr := fingerprintLibraryFile(ctx, cfg, store, file)
			tracksMu.Lock()
			report.Scanned++
			if ferr != nil {
				report.Skipped++
			} else {
				tracks = append(tracks, track)
			}
			report.UpdatedAt = time.Now().UTC()
			snapshot := report
			tracksMu.Unlock()
			setDuplicateReport(snapshot)
		}(file)
	}
	wg.Wait()
	if ctx.Err() != nil {
		fail(fmt.Errorf("查重已中断"))
		return
	}
	report.Groups = audiodupe.Cluster(tracks)
	report.Status = "done"
	report.UpdatedAt = time.Now().UTC()
	setDuplicateReport(report)
	persistDuplicateReport(report)
}

func persistDuplicateReport(report audiodupe.Report) {
	store, err := audiodupe.Open(storage.DefaultDatabasePath)
	if err != nil {
		return
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = store.SaveReport(ctx, report)
}

func collectLibraryAudio(cfg *config.Config) ([]FileEntry, error) {
	if cfg.Tidy.Mode == 2 {
		if core.GlobalWebDAV == nil || core.GlobalWebDAV.Client == nil {
			return nil, fmt.Errorf("WebDAV 未初始化")
		}
		entries, _, err := scanWebDAVFileIndex(webDAVDirectory(cfg), "/", core.GlobalWebDAV.Client)
		if err != nil {
			return nil, fmt.Errorf("读取曲库失败")
		}
		return audioOnly(entries), nil
	}
	root := cfg.Tidy.DistDir
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("还没有配置整理目录")
	}
	var entries []FileEntry
	err := filepath.WalkDir(root, func(full string, item os.DirEntry, err error) error {
		if err != nil || item.IsDir() || !audioExtension(item.Name()) {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		artist, album := artistAlbumFromPath(rel)
		entries = append(entries, FileEntry{
			Name: item.Name(), Path: rel, Size: info.Size(), ModTime: info.ModTime(),
			Ext: strings.ToLower(filepath.Ext(item.Name())), Artist: artist, Album: album,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("读取曲库失败")
	}
	return entries, nil
}

func audioOnly(entries []FileEntry) []FileEntry {
	out := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir && audioExtension(entry.Name) {
			out = append(out, entry)
		}
	}
	return out
}

func fingerprintLibraryFile(ctx context.Context, cfg *config.Config, store *audiodupe.Store, file FileEntry) (audiodupe.Track, error) {
	const maxBytes = 512 << 20
	if file.Size > maxBytes {
		return audiodupe.Track{}, fmt.Errorf("文件过大")
	}
	mod := file.ModTime.UnixNano()
	if fp, ok, err := store.LoadFingerprint(ctx, file.Path, file.Size, mod); err != nil {
		return audiodupe.Track{}, err
	} else if ok {
		return libraryTrack(file, mod, fp), nil
	}
	localPath, cleanup, err := materializeLibraryFile(ctx, cfg, file)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return audiodupe.Track{}, err
	}
	command := audiodupe.Command("fpcalc", localPath)
	command.WaitDelay = 2 * time.Second
	output, err := exec.CommandContext(ctx, command.Path, command.Args[1:]...).Output()
	if err != nil {
		return audiodupe.Track{}, fmt.Errorf("无法计算指纹")
	}
	fp, err := audiodupe.ParseFpcalc(output)
	if err != nil {
		return audiodupe.Track{}, err
	}
	if err = store.SaveFingerprint(ctx, file.Path, file.Size, mod, fp); err != nil {
		return audiodupe.Track{}, err
	}
	return libraryTrack(file, mod, fp), nil
}

func libraryTrack(file FileEntry, mod int64, fp audiodupe.Fingerprint) audiodupe.Track {
	return audiodupe.Track{Path: file.Path, Name: file.Name, Ext: file.Ext, Size: file.Size, ModTimeUnix: mod, Fingerprint: fp}
}

func materializeLibraryFile(ctx context.Context, cfg *config.Config, file FileEntry) (string, func(), error) {
	if cfg.Tidy.Mode != 2 {
		full, err := localSafePath(cfg.Tidy.DistDir, file.Path)
		return full, nil, err
	}
	if core.GlobalWebDAV == nil || core.GlobalWebDAV.Client == nil {
		return "", nil, fmt.Errorf("WebDAV 未初始化")
	}
	remote, err := remoteSafePath(webDAVDirectory(cfg), file.Path)
	if err != nil {
		return "", nil, err
	}
	reader, err := core.GlobalWebDAV.Client.ReadStream(remote)
	if err != nil {
		return "", nil, fmt.Errorf("读取音频失败")
	}
	defer reader.Close()
	temp, err := os.CreateTemp("", "gymdl-fp-*"+file.Ext)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		_ = os.Remove(temp.Name())
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = temp.Close()
		case <-done:
		}
	}()
	_, copyErr := io.Copy(temp, reader)
	close(done)
	closeErr := temp.Close()
	if copyErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("读取音频失败")
	}
	if closeErr != nil {
		cleanup()
		return "", nil, closeErr
	}
	return temp.Name(), cleanup, nil
}
