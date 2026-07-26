# 外部 CLI 工具模式参考模板

基于 `apple_music.go` 的模式。使用外部命令行工具下载（如 gamdl、sclib、pandl 等）。

## 适用场景

- 使用某特定 CLI 工具下载（非 yt-dlp）
- 下载完成后通过 `cmd.CombinedOutput()` 获取输出
- 不需要流式输出管道

## 关键差异点

与 yt-dlp 模式的区别：
- `DownloadMusic` 使用 `cmd.CombinedOutput()` 而非流式管道
- 无 `streamPipe()` 方法
- `DownloadCommand` 参数结构完全不同
- `EncryptedExts()` 可能返回加密扩展名（如 `.m4p`）

## 完整代码模板

```go
package music

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func ({{RECEIVER}} *{{PROCESSOR}}) DownloadMusic(url string, callback func(string)) error {
	start := time.Now()

	utils.InfoWithFormat("{{TAG}} 🎵 开始下载: %s", url)

	cmd := {{RECEIVER}}.DownloadCommand(url)
	if cmd == nil {
		return errors.New("无法构建下载命令")
	}
	utils.DebugWithFormat("{{TAG}} 执行命令: %s", strings.Join(cmd.Args, " "))

	// 创建临时目录
	if err := processor.CreateOutputDir({{RECEIVER}}.tempDir); err != nil {
		utils.ErrorWithFormat("{{TAG}} ❌ 创建临时目录失败: %v", err)
		return err
	}

	// 执行下载
	output, err := cmd.CombinedOutput()
	logOut := strings.TrimSpace(string(output))
	if err != nil {
		_ = processor.RemoveTempDir({{RECEIVER}}.tempDir)
		utils.ErrorWithFormat("{{TAG}} ❌ 下载失败: %v\n输出:\n%s", err, logOut)
		return fmt.Errorf("{{CLI_TOOL}} 下载失败: %w", err)
	}

	// 输出调试信息，仅当有日志内容时
	if logOut != "" {
		utils.DebugWithFormat("{{TAG}} 下载输出:\n%s", logOut)
	}

	utils.InfoWithFormat("{{TAG}} ✅ 下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond))
	callback(fmt.Sprintf("下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond)))
	return nil
}

func ({{RECEIVER}} *{{PROCESSOR}}) DownloadCommand(url string) *exec.Cmd {
	cookiePath := filepath.Join({{RECEIVER}}.cfg.CookieCloud.CookieFilePath, {{RECEIVER}}.cfg.CookieCloud.CookieFile)
	rootDir := filepath.Dir({{RECEIVER}}.tempDir)
	baseDir := filepath.Base({{RECEIVER}}.tempDir)

	args := []string{
		{{CLI_ARGS}}
	}
	return exec.Command("{{CLI_TOOL}}", args...)
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
	return {{ENC_EXTS}}
}

func ({{RECEIVER}} *{{PROCESSOR}}) DecryptedExts() []string {
	return {{DEC_EXTS}}
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
| `{{PROCESSOR}}` | 处理器结构体名称 | `PandoraProcessor` |
| `{{RECEIVER}}` | 接收器变量名 | `pp` |
| `{{TEMP_DIR}}` | 临时目录常量名 | `PandoraTempDir` |
| `{{LINK_TYPE}}` | LinkType 常量名 | `LinkPandora` |
| `{{TAG}}` | 日志标签 | `[Pandora]` |
| `{{CLI_TOOL}}` | CLI 工具二进制名 | `"pandl"`、`"gamdl"` |
| `{{CLI_ARGS}}` | 工具参数（用 `\n` 连接） | `"--cookies-path", cookiePath, "--output", ...` |
| `{{ENC_EXTS}}` | 加密扩展名 Go 数组 | `[]string{".m4p"}` 或 `make([]string, 0)` |
| `{{DEC_EXTS}}` | 解码扩展名 Go 数组 | `[]string{".aac", ".m4a", ".alac"}` |

### {{CLI_ARGS}} 格式示例

```go
// 参考 AppleMusic (gamdl):
"--cookies-path", cookiePath,
"--download-mode", "nm3u8dlre",
"--output-path", rootDir,
"--temp-path", rootDir,
"--album-folder-template", baseDir,
"--compilation-folder-template", baseDir,
"--no-album-folder-template", baseDir,
"--single-disc-file-template", "{title}",
"--multi-disc-file-template", "{title}",
"--no-synced-lyrics",
url,
```
