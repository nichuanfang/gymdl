package web

import (
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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/gin/response"
)

// FileEntry represents a local or WebDAV audio file or directory.
type FileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	Ext     string    `json:"ext"`
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

func HandleListFiles(c *gin.Context) {
	cfg := GetWebConfig()
	if cfg == nil || cfg.Tidy == nil {
		response.Fail(c, http.StatusInternalServerError, "整理配置不可用")
		return
	}
	relPath := c.DefaultQuery("path", "/")
	entries := make([]FileEntry, 0)
	if cfg.Tidy.Mode == 2 {
		if core.GlobalWebDAV == nil || core.GlobalWebDAV.Client == nil {
			response.Fail(c, http.StatusBadGateway, "WebDAV 未初始化")
			return
		}
		remotePath, err := remoteSafePath(webDAVDirectory(cfg), relPath)
		if err != nil {
			response.Fail(c, http.StatusBadRequest, "非法路径")
			return
		}
		items, err := core.GlobalWebDAV.Client.ReadDir(remotePath)
		if err != nil {
			response.Fail(c, http.StatusBadGateway, "读取 WebDAV 目录失败: "+err.Error())
			return
		}
		for _, item := range items {
			if !item.IsDir() && !audioExtension(item.Name()) {
				continue
			}
			rel := path.Join(strings.Trim(relPath, "/"), item.Name())
			entries = append(entries, FileEntry{Name: item.Name(), Path: rel, IsDir: item.IsDir(), Size: item.Size(), ModTime: item.ModTime(), Ext: strings.ToLower(path.Ext(item.Name()))})
		}
	} else {
		root := cfg.Tidy.DistDir
		fullPath, err := localSafePath(root, relPath)
		if err != nil {
			response.Fail(c, http.StatusBadRequest, "非法路径")
			return
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			response.Fail(c, http.StatusInternalServerError, "整理目录路径无效")
			return
		}
		realRoot, err := filepath.EvalSymlinks(absRoot)
		if err != nil {
			response.Fail(c, http.StatusInternalServerError, "整理目录不存在: "+err.Error())
			return
		}
		items, err := os.ReadDir(fullPath)
		if err != nil {
			response.Fail(c, http.StatusInternalServerError, "读取目录失败: "+err.Error())
			return
		}
		for _, item := range items {
			info, err := item.Info()
			if err != nil || (!item.IsDir() && !audioExtension(item.Name())) {
				continue
			}
			entryPath := filepath.Join(fullPath, item.Name())
			rel, err := filepath.Rel(realRoot, entryPath)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if _, err := localSafePath(root, rel); err != nil {
				continue
			}
			entries = append(entries, FileEntry{Name: item.Name(), Path: rel, IsDir: item.IsDir(), Size: info.Size(), ModTime: info.ModTime(), Ext: strings.ToLower(filepath.Ext(item.Name()))})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	mode := "local"
	if cfg.Tidy.Mode == 2 {
		mode = "webdav"
	}
	response.Success(c, gin.H{"path": strings.Trim(relPath, "/"), "entries": entries, "target": mode})
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
