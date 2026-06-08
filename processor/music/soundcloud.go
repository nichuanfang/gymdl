package music

import (
    "bufio"
    "errors"
    "fmt"
    "io"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "sync"
    "time"

    "github.com/nichuanfang/gymdl/config"
    "github.com/nichuanfang/gymdl/core"
    "github.com/nichuanfang/gymdl/processor"
    "github.com/nichuanfang/gymdl/utils"
)

/* ---------------------- 结构体与构造方法 ---------------------- */

type SoundCloudProcessor struct {
	cfg     *config.Config
	tempDir string
	songs   []*SongInfo
}

// Init  初始化
func (p *SoundCloudProcessor) Init(cfg *config.Config) {
	p.cfg = cfg
	p.songs = make([]*SongInfo, 0)
	p.tempDir = processor.BuildOutputDir(SoundcloudTempDir)
}

/* ---------------------- 基础接口实现 ---------------------- */

func (p *SoundCloudProcessor) Name() processor.LinkType {
	return processor.LinkSoundcloud
}

func (p *SoundCloudProcessor) Songs() []*SongInfo {
	return p.songs
}

/* ------------------------ 下载逻辑 ------------------------ */
func (p *SoundCloudProcessor) DownloadMusic(
    url string,
    callback func(string),
) error {

    start := time.Now()

    utils.InfoWithFormat("[SoundCloud] 🎵 开始下载: %s", url)

    cmd := p.DownloadCommand(url)
    if cmd == nil {
        return errors.New("无法构建 yt-dlp 命令")
    }

    callback("命令构建完成，开始下载...")

    utils.DebugWithFormat(
        "[SoundCloud] 执行命令: %s",
        strings.Join(cmd.Args, " "),
    )

    if err := processor.CreateOutputDir(p.tempDir); err != nil {
        return err
    }

    stdout, err := cmd.StdoutPipe()
    if err != nil {
        return err
    }

    stderr, err := cmd.StderrPipe()
    if err != nil {
        return err
    }

    if err := cmd.Start(); err != nil {
        return err
    }

    var wg sync.WaitGroup
    wg.Add(2)

    go func() {
        defer wg.Done()
        p.streamPipe(stdout, "stdout")
    }()

    go func() {
        defer wg.Done()
        p.streamPipe(stderr, "stderr")
    }()

    err = cmd.Wait()
    wg.Wait()

    if err != nil {
        _ = processor.RemoveTempDir(p.tempDir)

        utils.ErrorWithFormat(
            "[SoundCloud] ❌ 下载失败: %v",
            err,
        )

        return fmt.Errorf("yt-dlp 下载失败: %w", err)
    }

    utils.InfoWithFormat(
        "[SoundCloud] ✅ 下载完成（耗时 %v）",
        time.Since(start).Truncate(time.Millisecond),
    )

    callback(
        fmt.Sprintf(
            "下载完成（耗时 %v）",
            time.Since(start).Truncate(time.Millisecond),
        ),
    )

    return nil
}

func (p *SoundCloudProcessor) streamPipe(r io.ReadCloser, prefix string) {
    defer r.Close()

    reader := bufio.NewReaderSize(r, 64*1024)

    for {
        line, err := reader.ReadString('\n')

        if len(line) > 0 {
            line = strings.TrimSpace(line)
            if line != "" {
                utils.DebugWithFormat("[%s] %s", prefix, line)
            }
        }

        if err != nil {
            return
        }
    }
}

/* ------------------------ 命令生成 ------------------------ */

func (p *SoundCloudProcessor) DownloadCommand(url string) *exec.Cmd {

    cookiePath := filepath.Join(
        p.cfg.CookieCloud.CookieFilePath,
        p.cfg.CookieCloud.CookieFile,
    )

    if _, err := os.Stat(cookiePath); os.IsNotExist(err) {
        utils.ErrorWithFormat("[SoundCloud] ❌ Cookie 不存在: %s", cookiePath)
        return nil
    }

    args := []string{
        "--cookies", cookiePath,

        "--no-playlist",

        // SoundCloud 不需要 format 探测
        "-f", "hls_aac_160k",

        "-x",

        "--embed-metadata",
        "--embed-thumbnail",

        "--concurrent-fragments", "8",

        "--extractor-retries", "3",
        "--fragment-retries", "3",

        "--retry-sleep", "1",

        "--no-warnings",
        "--no-progress",

        "-o",
        filepath.Join(p.tempDir, "%(title)s.%(ext)s"),

        url,
    }

    return exec.Command("yt-dlp", args...)
}

/* ------------------------ 拓展方法 ------------------------ */

func (p *SoundCloudProcessor) BeforeTidy() error {
	songs, err := ReadMusicDir(p.tempDir, processor.DetermineTidyType(p.cfg), p)
	if err != nil {
		return err
	}
	// 更新元信息列表
	p.songs = songs
	return nil
}

func (p *SoundCloudProcessor) NeedRemoveDRM() bool {
	return false
}

func (p *SoundCloudProcessor) DRMRemove() error {
	return nil
}

func (p *SoundCloudProcessor) TidyMusic() error {
	files, err := os.ReadDir(p.tempDir)
	if err != nil {
		return fmt.Errorf("读取临时目录失败: %w", err)
	}
	if len(files) == 0 {
		utils.WarnWithFormat("[SoundCloud] ⚠️ 未找到待整理的音乐文件")
		return errors.New("未找到待整理的音乐文件")
	}

	switch p.cfg.Tidy.Mode {
	case 1:
		return p.tidyToLocal(files)
	case 2:
		return p.tidyToWebDAV(files, core.GlobalWebDAV)
	default:
		return fmt.Errorf("未知整理模式: %d", p.cfg.Tidy.Mode)
	}
}

func (p *SoundCloudProcessor) EncryptedExts() []string {
	return make([]string, 0)
}

func (p *SoundCloudProcessor) DecryptedExts() []string {
	return []string{".aac", ".m4a", ".flac", ".mp3", ".ogg"}
}

// 整理到本地
func (p *SoundCloudProcessor) tidyToLocal(files []os.DirEntry) error {
	dstDir := p.cfg.Tidy.DistDir
	if dstDir == "" {
		_ = processor.RemoveTempDir(p.tempDir)
		return errors.New("未配置输出目录")
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		_ = processor.RemoveTempDir(p.tempDir)
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	for _, f := range files {
		if !utils.FilterMusicFile(f, p.EncryptedExts(), p.DecryptedExts()) {
			utils.DebugWithFormat("[SoundCloud] 跳过非音乐文件: %s", f.Name())
			continue
		}
		src := filepath.Join(p.tempDir, f.Name())
		dst := filepath.Join(dstDir, utils.SanitizeFileName(f.Name()))
		err := processor.ToLocal(src, dst)
		if err != nil {
			return err
		}
		utils.InfoWithFormat("[SoundCloud] 📦 已整理: %s", dst)
	}
	// 清除临时目录
	err := processor.RemoveTempDir(p.tempDir)
	if err != nil {
		return err
	}
	return nil
}

// 整理到webdav
func (p *SoundCloudProcessor) tidyToWebDAV(files []os.DirEntry, webdav *core.WebDAV) error {
	if webdav == nil {
		_ = processor.RemoveTempDir(p.tempDir)
		return errors.New("WebDAV 未初始化")
	}
	songMap := make(map[string]*SongInfo)
	for _, song := range p.songs {
		songMap[utils.MakeSafeFileName(song.SongName)+"."+strings.ToLower(song.FileExt)] = song
	}
	for _, f := range files {
		songInfo, exists := songMap[f.Name()]
		if !exists {
			// 如果是不匹配的文件（比如过滤掉的封面，或者多余的临时文件），直接跳过
			utils.DebugWithFormat("[SoundCloud] 跳过无需处理的文件: %s", f.Name())
			continue
		}
		musicFilePath := filepath.Join(p.tempDir, f.Name())
		remoteDir := "/" + utils.SanitizeFileName(songInfo.SongArtists) + "/" + utils.SanitizeFileName(songInfo.SongAlbum)
		if err := webdav.UploadTo(musicFilePath, remoteDir); err != nil {
			utils.WarnWithFormat("[SoundCloud] ☁️ 上传失败 %s: %v", f.Name(), err)
            return err
		}
		utils.InfoWithFormat("[SoundCloud] ☁️ 已上传: %s", f.Name())
	}
	// 清除临时目录
	err := processor.RemoveTempDir(p.tempDir)
	if err != nil {
		return err
	}
	return nil
}
