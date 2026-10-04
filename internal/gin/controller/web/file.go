package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/gin/response"
	"golang.org/x/sync/singleflight"
)

// FileEntry represents a local or WebDAV audio file or directory.
type FileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	Ext     string    `json:"ext"`
	Artist  string    `json:"artist,omitempty"`
	Album   string    `json:"album,omitempty"`
}

func webDAVDirectory(cfg *config.Config) string {
	if cfg != nil && cfg.WebDAV != nil {
		return cfg.WebDAV.WebDAVDir
	}
	if core.GlobalWebDAV != nil && core.GlobalWebDAV.Config != nil {
		return core.GlobalWebDAV.Config.WebDAVDir
	}
	return ""
}

func audioMIME(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".aac":
		return "audio/aac"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".wma":
		return "audio/x-ms-wma"
	default:
		return "application/octet-stream"
	}
}

func audioExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3", ".m4a", ".flac", ".aac", ".ogg", ".wav", ".opus", ".wma":
		return true
	default:
		return false
	}
}

func localSafePath(root, rel string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	clean := filepath.Clean(strings.TrimLeft(rel, "/"))
	full := filepath.Join(absRoot, filepath.FromSlash(clean))
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", err
	}
	realFull, err := filepath.EvalSymlinks(absFull)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(realRoot, realFull)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes library root")
	}
	return realFull, nil
}

func remoteSafePath(cfgDir, rel string) (string, error) {
	base := strings.Trim(path.Clean("/"+strings.Trim(cfgDir, "/")), "/")
	rel = strings.ReplaceAll(rel, "\\", "/")
	for _, segment := range strings.Split(rel, "/") {
		if segment == ".." {
			return "", fmt.Errorf("path traversal is not allowed")
		}
	}
	clean := path.Clean("/" + strings.TrimLeft(rel, "/"))
	if clean == "/" {
		if base == "" {
			return "/", nil
		}
		return "/" + base, nil
	}
	joined := path.Join("/", base, strings.TrimLeft(clean, "/"))
	basePath := "/" + base
	if base != "" && joined != basePath && !strings.HasPrefix(joined, basePath+"/") {
		return "", fmt.Errorf("path escapes WebDAV root")
	}
	return joined, nil
}

const maxFileSearchEntries = 5000
const maxFileSearchDepth = 64

func HandleListFiles(c *gin.Context) {
	cfg := GetWebConfig()
	if cfg == nil || cfg.Tidy == nil {
		response.Fail(c, http.StatusInternalServerError, "整理配置不可用")
		return
	}
	relPath := strings.Trim(c.DefaultQuery("path", "/"), "/")
	query := strings.TrimSpace(c.Query("q"))
	field := strings.ToLower(strings.TrimSpace(c.DefaultQuery("field", "all")))
	if len(query) > 120 {
		response.Fail(c, http.StatusBadRequest, "文件库搜索词过长")
		return
	}
	if field != "all" && field != "song" && field != "artist" && field != "album" {
		response.Fail(c, http.StatusBadRequest, "无效的文件库筛选字段")
		return
	}

	var (
		entries   []FileEntry
		truncated bool
		err       error
	)
	if cfg.Tidy.Mode == 2 {
		if core.GlobalWebDAV == nil || core.GlobalWebDAV.Client == nil {
			response.Fail(c, http.StatusBadGateway, "WebDAV 未初始化")
			return
		}
		entries, truncated, err = listWebDAVFiles(webDAVDirectory(cfg), relPath, query, field, core.GlobalWebDAV.Client)
	} else {
		entries, truncated, err = listLocalFiles(cfg.Tidy.DistDir, relPath, query, field)
	}
	if err != nil {
		status := http.StatusInternalServerError
		if cfg.Tidy.Mode == 2 {
			status = http.StatusBadGateway
		}
		response.Fail(c, status, "读取文件库失败: "+err.Error())
		return
	}
	sortFileEntries(entries)
	mode := "local"
	if cfg.Tidy.Mode == 2 {
		mode = "webdav"
	}
	response.Success(c, gin.H{
		"path":      relPath,
		"entries":   entries,
		"target":    mode,
		"truncated": truncated,
		"searching": query != "",
	})
}

func sortFileEntries(entries []FileEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].ModTime.Equal(entries[j].ModTime) {
			return entries[i].ModTime.After(entries[j].ModTime)
		}
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

func listLocalFiles(root, relPath, query, field string) ([]FileEntry, bool, error) {
	relPath = strings.Trim(filepath.ToSlash(relPath), "/")
	fullPath, err := localSafePath(root, relPath)
	if err != nil {
		return nil, false, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, false, err
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, false, err
	}
	if query == "" {
		items, err := os.ReadDir(fullPath)
		if err != nil {
			return nil, false, err
		}
		entries := make([]FileEntry, 0, len(items))
		for _, item := range items {
			info, err := item.Info()
			if err != nil || (!item.IsDir() && !audioExtension(item.Name())) {
				continue
			}
			rootRel := filepath.ToSlash(filepath.Join(relPath, item.Name()))
			if _, err := localSafePath(root, rootRel); err != nil {
				continue
			}
			artist, album := artistAlbumFromPath(rootRel)
			entries = append(entries, FileEntry{
				Name: item.Name(), Path: rootRel, IsDir: item.IsDir(), Size: info.Size(),
				ModTime: info.ModTime(), Ext: strings.ToLower(filepath.Ext(item.Name())), Artist: artist, Album: album,
			})
		}
		return entries, false, nil
	}

	entries := make([]FileEntry, 0)
	truncated := false
	visited := 0
	err = filepath.WalkDir(fullPath, func(filePath string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relFromSearchRoot, err := filepath.Rel(fullPath, filePath)
		if err != nil {
			return err
		}
		depth := len(strings.Split(filepath.ToSlash(relFromSearchRoot), "/"))
		if item.IsDir() {
			if filePath != fullPath && depth > maxFileSearchDepth {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > maxFileSearchEntries {
			truncated = true
			return filepath.SkipAll
		}
		if !audioExtension(item.Name()) {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		rootRel, err := filepath.Rel(realRoot, filePath)
		if err != nil {
			return err
		}
		rootRel = filepath.ToSlash(rootRel)
		if _, err := localSafePath(root, rootRel); err != nil {
			return nil
		}
		artist, album := artistAlbumFromPath(rootRel)
		entry := FileEntry{Name: item.Name(), Path: rootRel, Size: info.Size(), ModTime: info.ModTime(), Ext: strings.ToLower(filepath.Ext(item.Name())), Artist: artist, Album: album}
		if fileMatchesQuery(entry, query, field) {
			entries = append(entries, entry)
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return nil, truncated, err
	}
	return entries, truncated, nil
}

type fileLibraryRemote interface {
	ReadDir(string) ([]os.FileInfo, error)
}

const maxFileSearchDirs = 2000
const webDAVFileIndexTTL = 15 * time.Second
const maxWebDAVFileIndexes = 24
const webDAVFileSearchConcurrency = 8

type webDAVIndexResult struct {
	entries   []FileEntry
	truncated bool
	expiresAt time.Time
}

var webDAVFileIndexes = struct {
	sync.Mutex
	items map[string]webDAVIndexResult
}{items: make(map[string]webDAVIndexResult)}
var webDAVFileIndexFlights singleflight.Group
var webDAVFileSearchSlots = make(chan struct{}, webDAVFileSearchConcurrency)

type webDAVDirTask struct {
	remoteDir   string
	relativeDir string
	depth       int
}

type webDAVDirResult struct {
	entries []FileEntry
	dirs    []webDAVDirTask
	err     error
}

func listWebDAVFiles(baseDir, relPath, query, field string, client fileLibraryRemote) ([]FileEntry, bool, error) {
	relPath = strings.Trim(path.Clean("/"+strings.ReplaceAll(relPath, "\\", "/")), "/")
	remotePath, err := remoteSafePath(baseDir, relPath)
	if err != nil {
		return nil, false, err
	}
	if query == "" {
		items, err := client.ReadDir(remotePath)
		if err != nil {
			return nil, false, err
		}
		entries := make([]FileEntry, 0, len(items))
		for _, item := range items {
			if !item.IsDir() && !audioExtension(item.Name()) {
				continue
			}
			entryRel := path.Join(relPath, item.Name())
			if _, err := remoteSafePath(baseDir, entryRel); err != nil {
				continue
			}
			artist, album := artistAlbumFromPath(entryRel)
			entries = append(entries, FileEntry{
				Name: item.Name(), Path: entryRel, IsDir: item.IsDir(), Size: item.Size(),
				ModTime: item.ModTime(), Ext: strings.ToLower(path.Ext(item.Name())), Artist: artist, Album: album,
			})
		}
		return entries, false, nil
	}

	cacheKey := fileIndexCacheKey(baseDir, relPath)
	index, ok := getWebDAVFileIndex(cacheKey)
	if !ok {
		value, err, _ := webDAVFileIndexFlights.Do(cacheKey, func() (any, error) {
			if cached, found := getWebDAVFileIndex(cacheKey); found {
				return cached, nil
			}
			entries, truncated, err := scanWebDAVFileIndex(baseDir, relPath, client)
			if err != nil {
				return nil, err
			}
			index := webDAVIndexResult{entries: entries, truncated: truncated}
			setWebDAVFileIndex(cacheKey, index)
			return index, nil
		})
		if err != nil {
			return nil, false, err
		}
		index = value.(webDAVIndexResult)
	}

	entries := make([]FileEntry, 0)
	for _, entry := range index.entries {
		if fileMatchesQuery(entry, query, field) {
			entries = append(entries, entry)
		}
	}
	return entries, index.truncated, nil
}

func fileIndexCacheKey(baseDir, relPath string) string {
	base := path.Clean("/" + strings.Trim(baseDir, "/"))
	rel := path.Clean("/" + strings.Trim(relPath, "/"))
	return base + "\x00" + rel
}

func getWebDAVFileIndex(key string) (webDAVIndexResult, bool) {
	webDAVFileIndexes.Lock()
	defer webDAVFileIndexes.Unlock()
	item, ok := webDAVFileIndexes.items[key]
	if !ok {
		return webDAVIndexResult{}, false
	}
	if time.Now().After(item.expiresAt) {
		delete(webDAVFileIndexes.items, key)
		return webDAVIndexResult{}, false
	}
	item.entries = append([]FileEntry(nil), item.entries...)
	return item, true
}

func setWebDAVFileIndex(key string, index webDAVIndexResult) {
	webDAVFileIndexes.Lock()
	defer webDAVFileIndexes.Unlock()
	if len(webDAVFileIndexes.items) >= maxWebDAVFileIndexes {
		// Expired indexes are removed first. If the map is still full, evict the
		// index closest to expiry to keep memory bounded without a second LRU map.
		now := time.Now()
		var earliestKey string
		var earliest time.Time
		for candidateKey, candidate := range webDAVFileIndexes.items {
			if !now.Before(candidate.expiresAt) {
				delete(webDAVFileIndexes.items, candidateKey)
				continue
			}
			if earliest.IsZero() || candidate.expiresAt.Before(earliest) {
				earliestKey, earliest = candidateKey, candidate.expiresAt
			}
		}
		if len(webDAVFileIndexes.items) >= maxWebDAVFileIndexes && earliestKey != "" {
			delete(webDAVFileIndexes.items, earliestKey)
		}
	}
	index.expiresAt = time.Now().Add(webDAVFileIndexTTL)
	index.entries = append([]FileEntry(nil), index.entries...)
	webDAVFileIndexes.items[key] = index
}

func invalidateWebDAVFileIndexes() {
	webDAVFileIndexes.Lock()
	clear(webDAVFileIndexes.items)
	webDAVFileIndexes.Unlock()
}

func scanWebDAVFileIndex(baseDir, relPath string, client fileLibraryRemote) ([]FileEntry, bool, error) {
	root, err := remoteSafePath(baseDir, relPath)
	if err != nil {
		return nil, false, err
	}
	current := []webDAVDirTask{{remoteDir: root, relativeDir: relPath, depth: 0}}
	entries := make([]FileEntry, 0)
	visitedFiles := 0
	visitedDirs := 0
	truncated := false

	stopAtFileLimit := false
	for len(current) > 0 {
		results := make([]webDAVDirResult, len(current))
		workers := webDAVFileSearchConcurrency
		if workers > len(current) {
			workers = len(current)
		}
		jobs := make(chan int)
		var wg sync.WaitGroup
		for worker := 0; worker < workers; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range jobs {
					webDAVFileSearchSlots <- struct{}{}
					task := current[index]
					items, err := client.ReadDir(task.remoteDir)
					<-webDAVFileSearchSlots
					if err != nil {
						results[index].err = err
						continue
					}
					result := &results[index]
					for _, item := range items {
						entryRel := path.Join(task.relativeDir, item.Name())
						entryRemote, err := remoteSafePath(baseDir, entryRel)
						if err != nil {
							continue
						}
						if item.IsDir() {
							if task.depth < maxFileSearchDepth {
								result.dirs = append(result.dirs, webDAVDirTask{
									remoteDir: entryRemote, relativeDir: entryRel, depth: task.depth + 1,
								})
							}
							continue
						}
						if !audioExtension(item.Name()) {
							continue
						}
						artist, album := artistAlbumFromPath(entryRel)
						result.entries = append(result.entries, FileEntry{
							Name: item.Name(), Path: entryRel, Size: item.Size(), ModTime: item.ModTime(),
							Ext: strings.ToLower(path.Ext(item.Name())), Artist: artist, Album: album,
						})
					}
				}
			}()
		}
		for index := range current {
			jobs <- index
		}
		close(jobs)
		wg.Wait()

		next := make([]webDAVDirTask, 0)
		for _, result := range results {
			if result.err != nil {
				return nil, truncated, result.err
			}
			visitedDirs++
			remainingFiles := maxFileSearchEntries - visitedFiles
			if len(result.entries) > remainingFiles {
				result.entries = result.entries[:remainingFiles]
				truncated = true
				stopAtFileLimit = true
			}
			entries = append(entries, result.entries...)
			visitedFiles += len(result.entries)
			if stopAtFileLimit {
				break
			}

			remainingDirs := maxFileSearchDirs - visitedDirs - len(next)
			if len(result.dirs) > remainingDirs {
				if remainingDirs > 0 {
					next = append(next, result.dirs[:remainingDirs]...)
				}
				truncated = true
			} else {
				next = append(next, result.dirs...)
			}
		}
		current = next
	}
	return entries, truncated, nil
}

func artistAlbumFromPath(filePath string) (string, string) {
	parts := strings.Split(strings.Trim(path.Clean("/"+filepath.ToSlash(filePath)), "/"), "/")
	if len(parts) < 3 {
		return "", ""
	}
	return parts[len(parts)-3], parts[len(parts)-2]
}

func fileMatchesQuery(entry FileEntry, query, field string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	contains := func(value string) bool { return strings.Contains(strings.ToLower(value), query) }
	switch field {
	case "song":
		return contains(strings.TrimSuffix(entry.Name, filepath.Ext(entry.Name)))
	case "artist":
		return contains(entry.Artist)
	case "album":
		return contains(entry.Album)
	default:
		return contains(entry.Name) || contains(entry.Artist) || contains(entry.Album)
	}
}

func HandleStreamFile(c *gin.Context) {
	cfg := GetWebConfig()
	if cfg == nil || cfg.Tidy == nil {
		response.Fail(c, http.StatusInternalServerError, "整理配置不可用")
		return
	}
	relPath := c.Query("path")
	if relPath == "" || strings.Contains(filepath.Base(relPath), "..") || !audioExtension(relPath) {
		response.Fail(c, http.StatusBadRequest, "非法路径")
		return
	}
	c.Header("Content-Type", audioMIME(relPath))
	c.Header("Content-Disposition", fmt.Sprintf("inline; filename*=UTF-8''%s", strings.ReplaceAll(url.QueryEscape(filepath.Base(relPath)), "+", "%20")))

	if cfg.Tidy.Mode != 2 {
		fullPath, err := localSafePath(cfg.Tidy.DistDir, relPath)
		if err != nil {
			response.Fail(c, http.StatusBadRequest, "非法路径")
			return
		}
		info, err := os.Stat(fullPath)
		if err != nil || info.IsDir() {
			response.Fail(c, http.StatusNotFound, "文件不存在")
			return
		}
		http.ServeFile(c.Writer, c.Request, fullPath)
		return
	}
	if core.GlobalWebDAV == nil || core.GlobalWebDAV.Client == nil {
		response.Fail(c, http.StatusBadGateway, "WebDAV 未初始化")
		return
	}
	remotePath, err := remoteSafePath(webDAVDirectory(cfg), relPath)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, "非法路径")
		return
	}
	info, err := core.GlobalWebDAV.Client.Stat(remotePath)
	if err != nil || info.IsDir() {
		response.Fail(c, http.StatusNotFound, "文件不存在")
		return
	}
	reader, status, contentRange, err := webDAVRangeStream(c.Request, remotePath, info.Size())
	if err != nil {
		if status == http.StatusRequestedRangeNotSatisfiable {
			c.Header("Content-Range", fmt.Sprintf("bytes */%d", info.Size()))
		} else {
			status = http.StatusBadGateway
		}
		response.Fail(c, status, "读取音频失败: "+err.Error())
		return
	}
	defer reader.Close()
	if status == http.StatusPartialContent {
		c.Header("Content-Range", contentRange)
		c.Status(http.StatusPartialContent)
	} else {
		c.Status(http.StatusOK)
	}
	c.Header("Accept-Ranges", "bytes")
	if status == http.StatusPartialContent {
		start, end, _ := parseSingleRange(c.GetHeader("Range"), info.Size())
		c.Header("Content-Length", strconv.FormatInt(end-start+1, 10))
	} else {
		c.Header("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	_, _ = io.Copy(c.Writer, reader)
}

func webDAVRangeStream(r *http.Request, remotePath string, size int64) (io.ReadCloser, int, string, error) {
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		reader, err := core.GlobalWebDAV.Client.ReadStream(remotePath)
		return reader, http.StatusOK, "", err
	}
	start, end, err := parseSingleRange(rangeHeader, size)
	if err != nil {
		return nil, http.StatusRequestedRangeNotSatisfiable, "", err
	}
	reader, err := core.GlobalWebDAV.Client.ReadStreamRange(remotePath, start, end-start+1)
	return reader, http.StatusPartialContent, fmt.Sprintf("bytes %d-%d/%d", start, end, size), err
}

func parseSingleRange(header string, size int64) (int64, int64, error) {
	if size <= 0 || !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, fmt.Errorf("不支持的 Range 请求")
	}
	parts := strings.SplitN(strings.TrimPrefix(header, "bytes="), "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("Range 格式无效")
	}
	var start, end int64
	var err error
	if parts[0] == "" {
		length, parseErr := strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil || length <= 0 {
			return 0, 0, fmt.Errorf("Range 格式无效")
		}
		if length > size {
			length = size
		}
		start, end = size-length, size-1
	} else {
		start, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || start < 0 || start >= size {
			return 0, 0, fmt.Errorf("Range 超出文件范围")
		}
		if parts[1] == "" {
			end = size - 1
		} else if end, err = strconv.ParseInt(parts[1], 10, 64); err != nil || end < start {
			return 0, 0, fmt.Errorf("Range 格式无效")
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end, nil
}

func HandleDeleteFile(c *gin.Context) {
	cfg := GetWebConfig()
	if cfg == nil || cfg.Tidy == nil {
		response.Fail(c, http.StatusInternalServerError, "整理配置不可用")
		return
	}
	relPath := strings.TrimSpace(c.Query("path"))
	if relPath == "" || relPath == "/" || relPath == "." {
		response.Fail(c, http.StatusBadRequest, "不能删除根目录")
		return
	}
	if !audioExtension(relPath) {
		response.Fail(c, http.StatusBadRequest, "文件库仅支持操作音频文件")
		return
	}
	if cfg.Tidy.Mode == 2 {
		if core.GlobalWebDAV == nil || core.GlobalWebDAV.Client == nil {
			response.Fail(c, http.StatusBadGateway, "WebDAV 未初始化")
			return
		}
		remotePath, err := remoteSafePath(webDAVDirectory(cfg), relPath)
		remoteRoot, _ := remoteSafePath(webDAVDirectory(cfg), "/")
		if err != nil || remotePath == "/" || remotePath == remoteRoot {
			response.Fail(c, http.StatusBadRequest, "非法路径")
			return
		}
		info, err := core.GlobalWebDAV.Client.Stat(remotePath)
		if err != nil {
			response.Fail(c, http.StatusNotFound, "文件不存在")
			return
		}
		if info.IsDir() {
			response.Fail(c, http.StatusBadRequest, "文件库暂不支持删除目录")
			return
		}
		if err := core.GlobalWebDAV.Client.Remove(remotePath); err != nil {
			response.Fail(c, http.StatusBadGateway, "删除 WebDAV 文件失败: "+err.Error())
			return
		}
		invalidateWebDAVFileIndexes()
	} else {
		fullPath, err := localSafePath(cfg.Tidy.DistDir, relPath)
		if err != nil {
			response.Fail(c, http.StatusBadRequest, "非法路径")
			return
		}
		root, _ := filepath.Abs(cfg.Tidy.DistDir)
		if fullPath == root {
			response.Fail(c, http.StatusBadRequest, "不能删除根目录")
			return
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			response.Fail(c, http.StatusNotFound, "文件不存在")
			return
		}
		if info.IsDir() {
			response.Fail(c, http.StatusBadRequest, "文件库暂不支持删除目录")
			return
		}
		if err := os.Remove(fullPath); err != nil {
			response.Fail(c, http.StatusInternalServerError, "删除失败: "+err.Error())
			return
		}
	}
	response.Success(c, gin.H{"deleted": true})
}
