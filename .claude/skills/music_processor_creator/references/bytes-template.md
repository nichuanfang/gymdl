# 原始字节转发模式参考模板

基于 `forword_music.go` 的模式。直接从内存中写入音频字节到文件，不调用外部工具。

## 适用场景

- 已经获取到音频字节（`[]byte`），只需写入文件
- 不需要外部下载工具
- `DownloadCommand` 返回 `nil`

## 关键区别

- 结构体多出 `FileName string` 和 `AudioBytes []byte` 字段
- `DownloadCommand` 返回 `nil`
- `DownloadMusic` 直接调用 `os.WriteFile()`
- `BeforeTidy` 调用 `ReadMusicDir`
- 无 `NeedRemoveDRM` / `DRMRemove` 逻辑

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

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
)

/* ---------------------- 结构体与构造方法 ---------------------- */

type {{PROCESSOR}} struct {
	cfg        *config.Config
	tempDir    string
	songs      []*SongInfo
	FileName   string
	AudioBytes []byte
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
	err := processor.CreateOutputDir({{RECEIVER}}.tempDir)
	if err != nil {
		return err
	}
	err = os.WriteFile(filepath.Join({{RECEIVER}}.tempDir, {{RECEIVER}}.FileName), {{RECEIVER}}.AudioBytes, 0644)
	if err != nil {
		return err
	}
	return nil
}

func ({{RECEIVER}} *{{PROCESSOR}}) DownloadCommand(url string) *exec.Cmd {
	return nil
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
	return []string{".aac", ".m4a", ".flac", ".mp3", ".ogg"}
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
| `{{PROCESSOR}}` | 处理器结构体名称 | `DeezerProcessor` |
| `{{RECEIVER}}` | 接收器变量名 | `fp` |
| `{{TEMP_DIR}}` | 临时目录常量名 | `DeezerTempDir` |
| `{{LINK_TYPE}}` | LinkType 常量名 | `LinkDeezer` |
| `{{TAG}}` | 日志标签 | `[Deezer]` |
