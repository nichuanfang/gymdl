# yt-dlp 模式参考模板

基于 `bilibili_music.go`、`youtube_music.go`、`soundcloud.go` 的模式。使用 yt-dlp 工具下载，支持流式输出和 format 探测。

## 适用场景

- 使用 yt-dlp 作为下载后端
- 需要流式输出管道（stdout/stderr 分开读取）
- 可能需要动态探测可用 format

## 完整代码模板

```go
package music

import (
	"bufio"
	"encoding/json"
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

type {{PROCESSOR}} struct {
	cfg     *config.Config
	tempDir string
	songs   []*SongInfo
}

// Init  初始化
func ({{RECEIVER}} *{{PROCESSOR}}) Init(cfg *config.Config) {
	{{RECEIVER}}.cfg = cfg
	{{RECEIVER}}.songs = make([]*SongInfo, 0)
	{{RECEIVER}}.tempDir = processor.BuildOutputDir({{TEMP_DIR}})
}

/* ---------------------- 基础接口实现 ---------------------- */

func ({{RECEIVER}} *{{PROCESSOR}}) Name() processor.LinkType {
	return processor.{{LINK_TYPE}}
}

func ({{RECEIVER}} *{{PROCESSOR}}) Songs() []*SongInfo {
	return {{RECEIVER}}.songs
}

/* ------------------------ 下载逻辑 ------------------------ */

func ({{RECEIVER}} *{{PROCESSOR}}) DownloadMusic(
	url string,
	callback func(string),
) error {

	start := time.Now()

	utils.InfoWithFormat("{{TAG}} 🎵 开始下载: %s", url)

	cmd := {{RECEIVER}}.DownloadCommand(url)
	if cmd == nil {
		return errors.New("无法构建 yt-dlp 命令")
	}

	utils.DebugWithFormat(
		"{{TAG}} 执行命令: %s",
		strings.Join(cmd.Args, " "),
	)

	if err := processor.CreateOutputDir({{RECEIVER}}.tempDir); err != nil {
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
		{{RECEIVER}}.streamPipe(stdout, "stdout")
	}()

	go func() {
		defer wg.Done()
		{{RECEIVER}}.streamPipe(stderr, "stderr")
	}()

	err = cmd.Wait()
	wg.Wait()

	if err != nil {
		_ = processor.RemoveTempDir({{RECEIVER}}.tempDir)

		utils.ErrorWithFormat(
			"{{TAG}} ❌ 下载失败: %v",
			err,
		)

		return fmt.Errorf("yt-dlp 下载失败: %w", err)
	}

	utils.InfoWithFormat(
		"{{TAG}} ✅ 下载完成（耗时 %v）",
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

func ({{RECEIVER}} *{{PROCESSOR}}) streamPipe(r io.ReadCloser, prefix string) {
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

{{FORMAT_PARSE_SECTION}}

/* ------------------------ 命令生成 ------------------------ */

func ({{RECEIVER}} *{{PROCESSOR}}) DownloadCommand(
	url string,
) *exec.Cmd {

	cookiePath := filepath.Join(
		{{RECEIVER}}.cfg.CookieCloud.CookieFilePath,
		{{RECEIVER}}.cfg.CookieCloud.CookieFile,
	)

	if _, err := os.Stat(cookiePath); os.IsNotExist(err) {
		utils.ErrorWithFormat("{{TAG}} ❌ Cookie 文件不存在: %s", cookiePath)
		return nil
	}

	args := []string{
		"-x",
		"--no-playlist",
		"--embed-metadata",
		"--embed-thumbnail",
		"--cookies", cookiePath,

		// 性能优化
		"--concurrent-fragments", "8",
		"--extractor-retries", "3",
		"--fragment-retries", "3",
		"--retry-sleep", "1",

		"--no-warnings",
		"--no-progress",

		"-f", "{{YTDLP_FORMAT}}",
		"-o", filepath.Join({{RECEIVER}}.tempDir, "%(title)s.%(ext)s"),
	}

	args = append(args, url)

	return exec.Command("yt-dlp", args...)
}

func ({{RECEIVER}} *{{PROCESSOR}}) BeforeTidy() error {
	songs, err := ReadMusicDir({{RECEIVER}}.tempDir, processor.DetermineTidyType({{RECEIVER}}.cfg), {{RECEIVER}})
	if err != nil {
		return err
	}
	// 更新元信息列表
	{{RECEIVER}}.songs = songs
	return nil
}

func ({{RECEIVER}} *{{PROCESSOR}}) NeedRemoveDRM() bool {
	return false
}

func ({{RECEIVER}} *{{PROCESSOR}}) DRMRemove() error {
	return nil
}

func ({{RECEIVER}} *{{PROCESSOR}}) TidyMusic() error {
	files, err := os.ReadDir({{RECEIVER}}.tempDir)
	if err != nil {
		return fmt.Errorf("读取临时目录失败: %w", err)
	}
	if len(files) == 0 {
		utils.WarnWithFormat("{{TAG}} ⚠️ 未找到待整理的音乐文件")
		return errors.New("未找到待整理的音乐文件")
	}

	switch {{RECEIVER}}.cfg.Tidy.Mode {
	case 1:
		return {{RECEIVER}}.tidyToLocal(files)
	case 2:
		return {{RECEIVER}}.tidyToWebDAV(files, core.GlobalWebDAV)
	default:
		return fmt.Errorf("未知整理模式: %d", {{RECEIVER}}.cfg.Tidy.Mode)
	}
}

func ({{RECEIVER}} *{{PROCESSOR}}) EncryptedExts() []string {
	return make([]string, 0)
}

func ({{RECEIVER}} *{{PROCESSOR}}) DecryptedExts() []string {
	return []string{".aac", ".m4a", ".flac", ".mp3", ".ogg"{{DECRYPTED_EXTRAS}}}
}

/* ------------------------ 拓展方法 ------------------------ */
// 整理到本地
func ({{RECEIVER}} *{{PROCESSOR}}) tidyToLocal(files []os.DirEntry) error {
	dstDir := {{RECEIVER}}.cfg.Tidy.DistDir
	if dstDir == "" {
		_ = processor.RemoveTempDir({{RECEIVER}}.tempDir)
		return errors.New("未配置输出目录")
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		_ = processor.RemoveTempDir({{RECEIVER}}.tempDir)
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	for _, f := range files {
		if !utils.FilterMusicFile(f, {{RECEIVER}}.EncryptedExts(), {{RECEIVER}}.DecryptedExts()) {
			utils.DebugWithFormat("{{TAG}} 跳过非音乐文件: %s", f.Name())
			continue
		}
		src := filepath.Join({{RECEIVER}}.tempDir, f.Name())
		dst := filepath.Join(dstDir, utils.SanitizeFileName(f.Name()))
		err := processor.ToLocal(src, dst)
		if err != nil {
			return err
		}
		utils.InfoWithFormat("{{TAG}} 📦 已整理: %s", dst)
	}
	// 清除临时目录
	err := processor.RemoveTempDir({{RECEIVER}}.tempDir)
	if err != nil {
		return err
	}
	return nil
}

// 整理到webdav
func ({{RECEIVER}} *{{PROCESSOR}}) tidyToWebDAV(files []os.DirEntry, webdav *core.WebDAV) error {
	if webdav == nil {
		_ = processor.RemoveTempDir({{RECEIVER}}.tempDir)
		return errors.New("WebDAV 未初始化")
	}
	songMap := make(map[string]*SongInfo)
	for _, song := range {{RECEIVER}}.songs {
		songMap[utils.MakeSafeFileName(song.SongName)+"."+strings.ToLower(song.FileExt)] = song
	}
	for _, f := range files {
		songInfo, exists := songMap[f.Name()]
		if !exists {
			// 如果是不匹配的文件（比如过滤掉的封面，或者多余的临时文件），直接跳过
			utils.DebugWithFormat("{{TAG}} 跳过无需处理的文件: %s", f.Name())
			continue
		}
		musicFilePath := filepath.Join({{RECEIVER}}.tempDir, f.Name())
		remoteDir := "/" + utils.SanitizeFileName(songInfo.SongArtists) + "/" + utils.SanitizeFileName(songInfo.SongAlbum)
		if err := webdav.UploadTo(musicFilePath, remoteDir); err != nil {
			utils.WarnWithFormat("{{TAG}} ☁️ 上传失败 %s: %v", f.Name(), err)
			return err
		}
		utils.InfoWithFormat("{{TAG}} ☁️ 已上传: %s", f.Name())
	}
	// 清除临时目录
	err := processor.RemoveTempDir({{RECEIVER}}.tempDir)
	if err != nil {
		return err
	}
	return nil
}
```

## 变量替换说明

| 变量 | 说明 | 示例值 |
|------|------|--------|
| `{{PROCESSOR}}` | 处理器结构体名称 | `TidalProcessor` |
| `{{RECEIVER}}` | 接收器变量名 | `p` |
| `{{TEMP_DIR}}` | 临时目录常量名 | `TidalTempDir` |
| `{{LINK_TYPE}}` | LinkType 常量名 | `LinkTidal` |
| `{{TAG}}` | 日志标签 | `[Tidal]` |
| `{{YTDLP_FORMAT}}` | yt-dlp format 参数 | `"bestaudio"`、`"141/251/140/774/bestaudio"` |
| `{{FORMAT_PARSE_SECTION}}` | format 探测函数（可选） | 见下方 |
| `{{DECRYPTED_EXTRAS}}` | 额外解码后缀 | `""`、`",\".opus\""` |

### Format 探测函数（可选）

若需要动态探测可用 format，填入以下代码：

```go
/* ---------------------- format 解析 ---------------------- */

func ({{RECEIVER}} *{{PROCESSOR}}) getAvailableFormats(
	url string,
	cookiePath string,
) (map[string]bool, error) {

	args := []string{
		"--no-playlist",
		"--skip-download",
		"--no-warnings",
		"--no-progress",
		"--no-call-home",
		"--cookies", cookiePath,
		"-J",
		url,
	}

	cmd := exec.Command("yt-dlp", args...)

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var info ytInfo
	if err := json.Unmarshal(output, &info); err != nil {
		return nil, err
	}

	formats := make(map[string]bool)

	for _, f := range info.Formats {
		switch f.FormatID {
		case "30280", "30250", "30232", "30216":
			formats[f.FormatID] = true
		}
	}

	return formats, nil
}
```

以及需要添加的 ytInfo 结构体：

```go
type ytFormat struct {
	FormatID string `json:"format_id"`
	Ext      string `json:"ext"`
}

type ytInfo struct {
	Formats []ytFormat `json:"formats"`
}
```
