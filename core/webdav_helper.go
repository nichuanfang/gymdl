package core

import (
    "fmt"
    "net/url"
    "os"
    "path"
    "path/filepath"
    "strings"
    "sync"
    "time"

    "github.com/nichuanfang/gymdl/config"
    "github.com/nichuanfang/gymdl/utils"
    "github.com/studio-b12/gowebdav"
)

type WebDAV struct {
    Config          *config.WebDAVConfig
    Client          *gowebdav.Client
    lastCheck       time.Time
    lastCheckResult bool
    checkMutex      sync.Mutex
}

var (
    GlobalWebDAV *WebDAV
)

// InitWebDAV 初始化全局 WebDAV
func InitWebDAV(cfg *config.WebDAVConfig) {
    if logger == nil {
        logger = utils.Logger()
    }
    if cfg == nil || cfg.WebDAVUrl == "" || cfg.WebDAVUser == "" || cfg.WebDAVPass == "" {
        panic("⚠️ WebDAV config is invalid")
    }

    client := gowebdav.NewClient(cfg.WebDAVUrl, cfg.WebDAVUser, cfg.WebDAVPass)
    if err := client.Connect(); err != nil {
        panic(fmt.Sprintf("⚠️ Failed to connect WebDAV: %v", err))
    }

    GlobalWebDAV = &WebDAV{
        Config: cfg,
        Client: client,
    }
}

// -------------------- 连接检测 --------------------

func (w *WebDAV) CheckConnection() bool {
    w.checkMutex.Lock()
    defer w.checkMutex.Unlock()

    if time.Since(w.lastCheck) < time.Minute {
        return w.lastCheckResult
    }

    err := w.Client.Connect()
    w.lastCheck = time.Now()
    w.lastCheckResult = err == nil

    if err != nil {
        logger.Warn(fmt.Sprintf("⚠️ WebDAV connection check failed: %v", err))
    }

    return w.lastCheckResult
}

// -------------------- 文件操作 --------------------

// Upload 上传文件到配置的默认目录
func (w *WebDAV) Upload(localPath string) error {
    return w.UploadTo(localPath, "/")
}

// UploadTo 上传到指定目录，失败时最多重试 3 次，每次间隔 1 秒
func (w *WebDAV) UploadTo(localPath, remoteDir string) error {
    if localPath == "" {
        return fmt.Errorf("localPath cannot be empty")
    }
    if remoteDir == "" {
        remoteDir = "/"
    }

    file, err := os.Open(localPath)
    if err != nil {
        return fmt.Errorf("failed to open local file: %v", err)
    }
    defer file.Close()

    fileName := filepath.Base(localPath)

    // 路径处理 + 编码
    remoteDir = filepath.ToSlash(remoteDir)
    remoteDir = strings.Trim(remoteDir, "/")

    baseDir := strings.Trim(w.Config.WebDAVDir, "/")

    var segments []string
    if baseDir != "" {
        segments = append(segments, baseDir)
    }
    if remoteDir != "" {
        for _, seg := range strings.Split(remoteDir, "/") {
            if seg != "" {
                segments = append(segments, url.PathEscape(seg))
            }
        }
    }

    fullRemoteDir := "/" + strings.Join(segments, "/")
    encodedFileName := url.PathEscape(fileName)

    remoteFullPath := path.Join(fullRemoteDir, encodedFileName)

    // 调试日志
    logger.Debug(fmt.Sprintf("WebDAV Path Debug → Base: '%s', RemoteDir: '%s', EncodedDir: '%s', FullPath: '%s'",
        w.Config.WebDAVDir, remoteDir, fullRemoteDir, remoteFullPath))

    if err := w.ensureRemoteDir(fullRemoteDir); err != nil {
        return fmt.Errorf("failed to ensure remote dir %s: %v", fullRemoteDir, err)
    }

    const maxRetries = 3
    logger.Info("💡 start uploading file to webdav...")
    for attempt := 1; attempt <= maxRetries; attempt++ {
        // 每次重试都需要重新 Seek 到文件开头，确保流从头读取
        if _, err = file.Seek(0, 0); err != nil {
            return fmt.Errorf("failed to seek file: %v", err)
        }

        if err = w.Client.WriteStream(remoteFullPath, file, 0644); err == nil {
            logger.Info(fmt.Sprintf("💡 WebDAV uploaded file successfully: %s", remoteFullPath))
            return nil
        }

        logger.Warn(fmt.Sprintf("⚠️ WebDAV upload attempt %d/%d failed for %s: %v", attempt, maxRetries, remoteFullPath, err))
        if attempt < maxRetries {
            time.Sleep(time.Second)
        }
    }

    return fmt.Errorf("WebDAV upload failed after %d attempts: %v", maxRetries, err)
}

// -------------------- 其他方法同步更新 --------------------

func (w *WebDAV) Download(remotePath, localPath string) error {
    if remotePath == "" || localPath == "" {
        return fmt.Errorf("remotePath and localPath cannot be empty")
    }

    remoteFullPath := w.makeRemotePath(remotePath)
    data, err := w.Client.Read(remoteFullPath)
    if err != nil {
        logger.Warn(fmt.Sprintf("⚠️ WebDAV download failed for %s: %v", remotePath, err))
        return fmt.Errorf("failed to read remote file: %v", err)
    }

    if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
        return fmt.Errorf("failed to create local directories: %v", err)
    }

    if err := os.WriteFile(localPath, data, 0644); err != nil {
        logger.Warn(fmt.Sprintf("⚠️ WebDAV failed to write local file: %v", err))
        return err
    }

    logger.Info(fmt.Sprintf("💡 WebDAV downloaded file successfully: %s", remotePath))
    return nil
}

func (w *WebDAV) Delete(remotePath string) error {
    if remotePath == "" {
        return fmt.Errorf("remotePath cannot be empty")
    }

    remoteFullPath := w.makeRemotePath(remotePath)
    if err := w.Client.Remove(remoteFullPath); err != nil {
        logger.Warn(fmt.Sprintf("⚠️ WebDAV delete failed for %s: %v", remotePath, err))
        return err
    }

    logger.Info(fmt.Sprintf("💡 WebDAV deleted file successfully: %s", remotePath))
    return nil
}

func (w *WebDAV) List(remoteDir string) ([]string, error) {
    dir := w.makeRemotePath(remoteDir)
    files, err := w.Client.ReadDir(dir)
    if err != nil {
        logger.Warn(fmt.Sprintf("⚠️ WebDAV list failed for %s: %v", remoteDir, err))
        return nil, err
    }

    var names []string
    for _, f := range files {
        names = append(names, f.Name())
    }
    return names, nil
}

// -------------------- 工具方法（已修复） --------------------

func (w *WebDAV) makeRemotePath(pathStr string) string {
    base := strings.Trim(w.Config.WebDAVDir, "/")
    sub := strings.Trim(pathStr, "/")

    var segments []string
    if base != "" {
        segments = append(segments, base)
    }
    if sub != "" {
        for _, seg := range strings.Split(sub, "/") {
            if seg != "" {
                segments = append(segments, url.PathEscape(seg))
            }
        }
    }

    return "/" + strings.Join(segments, "/")
}

// ensureRemoteDir 确认目录存在
func (w *WebDAV) ensureRemoteDir(dir string) error {
    if dir == "" || dir == "/" {
        return nil
    }

    // 清理路径（防止出现 // 或 . 等）
    cleanDir := path.Clean(dir)
    if !strings.HasPrefix(cleanDir, "/") {
        cleanDir = "/" + cleanDir
    }

    if err := w.Client.MkdirAll(cleanDir, 0755); err != nil {
        if strings.Contains(err.Error(), "404") {
            logger.Debug(fmt.Sprintf("WebDAV mkdir got 404, ignoring: %s", cleanDir))
            return nil
        }
        logger.Warn(fmt.Sprintf("⚠️ WebDAV failed to create remote directory %s: %v", cleanDir, err))
        return err
    }
    return nil
}

// -------------------- 可选参数 --------------------
func WithDir(dir string) func(*config.WebDAVConfig) {
    return func(cfg *config.WebDAVConfig) {
        cfg.WebDAVDir = dir
    }
}