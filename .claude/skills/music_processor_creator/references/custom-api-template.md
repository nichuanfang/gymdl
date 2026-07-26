# 自定义 API 模式参考模板

基于 `netease.go` 的模式。通过 HTTP API 自行实现歌曲信息获取和下载。

## 适用场景

- 有可用的音乐 API（官方或第三方）
- 需要自己处理下载逻辑、元数据构建
- 通常涉及 URL 解析（单曲 vs 歌单）、文件下载、封面下载

## 重要说明

自定义 API 的下载逻辑高度依赖具体平台。本模板提供骨架结构，下载部分的逻辑需要用户填充。

## 完整代码模板

```go
package music

import (
	"errors"
	"fmt"
	"net/http"
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
	cfg     *config.Config // 配置文件
	songs   []*SongInfo    // 歌曲元信息列表
	tempDir string         // 临时目录
}

// Init  初始化
func ({{RECEIVER}} *{{PROCESSOR}}) Init(cfg *config.Config) {
	{{RECEIVER}}.cfg = cfg
	{{RECEIVER}}.songs = make([]*SongInfo, 0)
	{{RECEIVER}}.tempDir = processor.BuildOutputDir({{TEMP_DIR}})
	// TODO: 初始化 HTTP 客户端、加载 Cookie、初始化 API 服务等
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

	// TODO: 解析 URL 获取音乐 ID 和类型（单曲/歌单）
	// TODO: 根据类型执行对应下载逻辑

	// TODO: 创建临时目录
	// if err := processor.CreateOutputDir({{RECEIVER}}.tempDir); err != nil { return err }

	// TODO: 获取歌曲信息、下载地址、歌词、封面
	// TODO: 下载音乐文件和封面
	// TODO: 构建 SongInfo 并添加到 {{RECEIVER}}.songs

	utils.InfoWithFormat("{{TAG}} ✅ 下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond))
	callback(fmt.Sprintf("下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond)))
	return nil
}

func ({{RECEIVER}} *{{PROCESSOR}}) DownloadCommand(url string) *exec.Cmd {
	return nil
}

func ({{RECEIVER}} *{{PROCESSOR}}) BeforeTidy() error {
	// 如果直接下载的文件自带元数据，使用 ReadMusicDir
	// 如果需要在 BeforeTidy 中写入封面/歌词，参考 netease.go 的 BeforeTidy 实现

	var fileName string
	var coverFileName string
	for _, song := range {{RECEIVER}}.songs {
		fileName = filepath.Join({{RECEIVER}}.tempDir, {{RECEIVER}}.safeFileName(song))
		coverFileName = filepath.Join({{RECEIVER}}.tempDir, {{RECEIVER}}.safeCoverFileName(song))
		err := WriteTagsWithCoverFile(song, fileName, coverFileName)
		if err != nil {
			return err
		}
	}
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
	return []string{".flac", ".mp3", ".aac", ".m4a", ".ogg"}
}

/* ------------------------ 拓展方法 ------------------------ */

// safeFileName 合法的文件名
func ({{RECEIVER}} *{{PROCESSOR}}) safeFileName(info *SongInfo) string {
	replacer := strings.NewReplacer("/", " ", "?", " ", "*", " ", ":", " ",
		"|", " ", "\\", " ", "<", " ", ">", " ", "\"", " ")

	baseName := fmt.Sprintf("%s - %s", strings.ReplaceAll(info.SongArtists, "/", ","), info.SongName)
	baseName = truncateString(baseName, 120)

	return replacer.Replace(fmt.Sprintf("%s.%s", baseName, info.FileExt))
}

// safeCoverFileName 合法的封面文件名
func ({{RECEIVER}} *{{PROCESSOR}}) safeCoverFileName(info *SongInfo) string {
	replacer := strings.NewReplacer("/", " ", "?", " ", "*", " ", ":", " ",
		"|", " ", "\\", " ", "<", " ", ">", " ", "\"", " ")

	baseName := fmt.Sprintf("%s - %s_cover", strings.ReplaceAll(info.SongArtists, "/", ","), info.SongName)
	baseName = truncateString(baseName, 120)

	return replacer.Replace(fmt.Sprintf("%s.jpg", baseName))
}

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
		songMap[{{RECEIVER}}.safeFileName(song)] = song
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

// truncateString 确保字符串不超过指定长度
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return s
}
```

## 变量替换说明

| 变量 | 说明 | 示例值 |
|------|------|--------|
| `{{PROCESSOR}}` | 处理器结构体名称 | `TidalProcessor` |
| `{{RECEIVER}}` | 接收器变量名 | `tp` |
| `{{TEMP_DIR}}` | 临时目录常量名 | `TidalTempDir` |
| `{{LINK_TYPE}}` | LinkType 常量名 | `LinkTidal` |
| `{{TAG}}` | 日志标签 | `[Tidal]` |

## 自定义 API 类处理器常见模式

参考 `netease.go` 和 `qq_music.go`，以下是一些常见实现模式：

### URL 解析
解析分享链接，提取音乐 ID 和类型（单曲/歌单）：
```go
type MusicLink struct {
    id     string
    isSong bool
}

func (r *{{PROCESSOR}}) parseMusicLink(raw string) (MusicLink, error) {
    // TODO: 解析各类分享链接格式
}
```

### 并发下载
同时下载音乐文件和封面：
```go
var wg sync.WaitGroup
wg.Add(2)

var coverErr error
go func() {
    defer wg.Done()
    coverErr = utils.DownloadFile(client, coverUrl, coverPath)
}()

// 下载音乐文件（主任务）
if err := utils.DownloadFile(client, songUrl, tempPath); err != nil {
    return err
}

wg.Wait()
if coverErr != nil {
    return coverErr
}
```

### 元数据嵌入
```go
func (r *{{PROCESSOR}}) BeforeTidy() error {
    for _, song := range r.songs {
        fileName := filepath.Join(r.tempDir, r.safeFileName(song))
        coverFileName := filepath.Join(r.tempDir, r.safeCoverFileName(song))
        err := WriteTagsWithCoverFile(song, fileName, coverFileName)
        if err != nil {
            return err
        }
    }
    return nil
}
```
