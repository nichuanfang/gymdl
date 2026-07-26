---
name: music_processor_creator
description: "Generate new music platform processors for the gymdl Go project. Creates a Go source file implementing the music.Processor interface. Use when the user asks to 'add a new music platform', 'create a music processor', 'implement a music processor', or 'add support for [platform name]'."
---

# Music Processor Creator (音乐处理器构建器)

Generate a complete Go music processor file `processor/music/<platform>_music.go` that implements the `music.Processor` interface, matching the exact code conventions of the existing processors (AppleMusic, BilibiliMusic, NetEase, QQMusic, etc.).

## Workflow

1. **Ask the user** for the platform name (e.g., "Tidal", "Deezer", "Pandora", "AmazonMusic")
2. **Determine the download pattern** by asking the user or inferring from context:
   - **yt-dlp**: "用 yt-dlp", "类似 B站/YouTube/SoundCloud" → read `references/yt-dlp-template.md`
   - **外部 CLI 工具**: "用命令行工具", "类似 AppleMusic", "用 xxx tool" → read `references/cli-tool-template.md`
   - **自定义 API**: "用 API", "调用接口", "从...拉取" → read `references/custom-api-template.md`
   - **原始字节转发**: "转发", "接收音频字节" , "类似 Forward" → read `references/bytes-template.md`
   - **TODO 骨架**: "先建骨架", "模板", "占位", "空实现" → read `references/todo-template.md`

   **提问模板**:
   ```
   请选择 {platform} 的下载方式：
   (1) yt-dlp — 类似 B站/YouTube/SoundCloud
   (2) 外部 CLI 工具 — 类似 AppleMusic (gamdl)
   (3) 自定义 API — 类似 网易云/QQ音乐
   (4) 原始字节转发 — 类似 Forward
   (5) TODO 骨架 — 仅占位，暂不实现
   ```
3. **Generate** the file at `processor/music/<platform>_music.go`
4. **Output the post-generation checklist** telling the user to register the processor

## Naming Convention

Given user-provided platform name (e.g., "Tidal", "AmazonMusic", "NetEaseCloudMusic"):

| 项目 | 规则 | 示例 |
|------|------|------|
| 文件名 | 蛇形命名 + `_music.go` | `tidal_music.go`, `amazon_music.go` |
| 结构体 | `{平台}PascalCase + Processor` | `TidalProcessor`, `AmazonMusicProcessor` |
| 临时目录常量 | `{平台}PascalCase + TempDir` | `TidalTempDir`, `AmazonMusicTempDir` |
| LinkType 常量 | `Link + {平台}PascalCase` | `LinkTidal`, `LinkAmazonMusic` |
| 日志标签 | `[{平台}]` | `[Tidal]`, `[AmazonMusic]` |
| 接收器变量 | 见下表 | `p`, `tp`, `dp` 等 |

### 接收器变量名惯例

| 模式 | 变量 | 示例 |
|------|------|------|
| yt-dlp | `p` | `func (p *TidalProcessor)` |
| 外部 CLI | 首字母缩写 | `func (tp *TidalProcessor)` |
| 自定义 API | 首字母缩写 | `func (tp *TidalProcessor)` |
| 原始字节 | `fp` | `func (fp *TidalProcessor)` |
| TODO | `p` | `func (p *TidalProcessor)` |

### 蛇形转换规则

将 PascalCase 转换为 snake_case 用于文件名：
- `Tidal` → `tidal`
- `AmazonMusic` → `amazon_music`
- `NetEaseCloudMusic` → `netease_cloud_music`
- `QQMusic` → `qq_music`

## 必需结构体与方法

每个处理器必须包含以下结构体和所有方法，按此顺序排列：

### 结构体
```go
type {Platform}Processor struct {
    cfg     *config.Config
    tempDir string
    songs   []*SongInfo
}
```

### 方法（严格按此顺序）

```go
// Init  初始化
func (r *{Platform}Processor) Init(cfg *config.Config)

func (r *{Platform}Processor) Name() processor.LinkType

func (r *{Platform}Processor) Songs() []*SongInfo

func (r *{Platform}Processor) DownloadMusic(url string, callback func(string)) error

func (r *{Platform}Processor) DownloadCommand(url string) *exec.Cmd

func (r *{Platform}Processor) BeforeTidy() error

func (r *{Platform}Processor) NeedRemoveDRM() bool

func (r *{Platform}Processor) DRMRemove() error

func (r *{Platform}Processor) TidyMusic() error

func (r *{Platform}Processor) EncryptedExts() []string

func (r *{Platform}Processor) DecryptedExts() []string
```

### 私有方法

```go
// 整理到本地
func (r *{Platform}Processor) tidyToLocal(files []os.DirEntry) error

// 整理到webdav
func (r *{Platform}Processor) tidyToWebDAV(files []os.DirEntry, webdav *core.WebDAV) error
```

## 代码惯例（必需遵守）

### 导入分组
三组用空行分隔：stdlib → 第三方包 → 项目内部包
```go
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
```

### 注释分隔区块
使用精确的注释风格匹配现有代码（注意横线数量）：
- `/* ---------------------- 结构体与构造方法 ---------------------- */`
- `/* ---------------------- 基础接口实现 ---------------------- */`
- `/* ------------------------ 下载逻辑 ------------------------ */`
- `/* ---------------------- 命令生成 ---------------------- */`（仅 yt-dlp 模式）
- `/* ---------------------- format 解析 ---------------------- */`（仅 yt-dlp 动态探测模式）
- `/* ------------------------ 拓展方法 ------------------------ */`

### 方法注释
- `Init` 方法：`// Init  初始化`（注意双空格）
- `Name` 方法：无需注释
- `tidyToLocal`: `// 整理到本地`
- `tidyToWebDAV`: `// 整理到webdav`

### 中文字段注释
所有字段注释使用中文，如 `cfg *config.Config // 配置文件`

### 表情符号日志前缀

| 场景 | 格式 |
|------|------|
| 开始下载 | `utils.InfoWithFormat("[{TAG}] 🎵 开始下载: %s", url)` |
| 下载完成 | `utils.InfoWithFormat("[{TAG}] ✅ 下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond))` |
| 下载失败 | `utils.ErrorWithFormat("[{TAG}] ❌ 下载失败: %v", err)` |
| 命令执行 | `utils.DebugWithFormat("[{TAG}] 执行命令: %s", strings.Join(cmd.Args, " "))` |
| 整理成功 | `utils.InfoWithFormat("[{TAG}] 📦 已整理: %s", dst)` |
| 上传成功 | `utils.InfoWithFormat("[{TAG}] ☁️ 已上传: %s", f.Name())` |
| 上传失败 | `utils.WarnWithFormat("[{TAG}] ☁️ 上传失败 %s: %v", f.Name(), err)` |
| 无可整理文件 | `utils.WarnWithFormat("[{TAG}] ⚠️ 未找到待整理的音乐文件")` |
| 创建目录失败 | `utils.ErrorWithFormat("[{TAG}] ❌ 创建临时目录失败: %v", err)` |
| Cookie 文件不存在 | `utils.ErrorWithFormat("[{TAG}] ❌ Cookie 文件不存在: %s", cookiePath)` |

### 回调函数
下载完成后（成功或失败）调用 `callback(msg)` 上报状态。

### tidyToLocal 模板
```go
func (r *{Platform}Processor) tidyToLocal(files []os.DirEntry) error {
    dstDir := r.cfg.Tidy.DistDir
    if dstDir == "" {
        _ = processor.RemoveTempDir(r.tempDir)
        return errors.New("未配置输出目录")
    }
    if err := os.MkdirAll(dstDir, 0755); err != nil {
        _ = processor.RemoveTempDir(r.tempDir)
        return fmt.Errorf("创建输出目录失败: %w", err)
    }
    for _, f := range files {
        if !utils.FilterMusicFile(f, r.EncryptedExts(), r.DecryptedExts()) {
            utils.DebugWithFormat("[{TAG}] 跳过非音乐文件: %s", f.Name())
            continue
        }
        src := filepath.Join(r.tempDir, f.Name())
        dst := filepath.Join(dstDir, utils.SanitizeFileName(f.Name()))
        err := processor.ToLocal(src, dst)
        if err != nil {
            return err
        }
        utils.InfoWithFormat("[{TAG}] 📦 已整理: %s", dst)
    }
    err := processor.RemoveTempDir(r.tempDir)
    if err != nil {
        return err
    }
    return nil
}
```

### tidyToWebDAV 模板
```go
func (r *{Platform}Processor) tidyToWebDAV(files []os.DirEntry, webdav *core.WebDAV) error {
    if webdav == nil {
        _ = processor.RemoveTempDir(r.tempDir)
        return errors.New("WebDAV 未初始化")
    }
    songMap := make(map[string]*SongInfo)
    for _, song := range r.songs {
        songMap[utils.MakeSafeFileName(song.SongName)+"."+strings.ToLower(song.FileExt)] = song
    }
    for _, f := range files {
        songInfo, exists := songMap[f.Name()]
        if !exists {
            utils.DebugWithFormat("[{TAG}] 跳过无需处理的文件: %s", f.Name())
            continue
        }
        musicFilePath := filepath.Join(r.tempDir, f.Name())
        remoteDir := "/" + utils.SanitizeFileName(songInfo.SongArtists) + "/" + utils.SanitizeFileName(songInfo.SongAlbum)
        if err := webdav.UploadTo(musicFilePath, remoteDir); err != nil {
            utils.WarnWithFormat("[{TAG}] ☁️ 上传失败 %s: %v", f.Name(), err)
            return err
        }
        utils.InfoWithFormat("[{TAG}] ☁️ 已上传: %s", f.Name())
    }
    err := processor.RemoveTempDir(r.tempDir)
    if err != nil {
        return err
    }
    return nil
}
```

### EncryptedExts / DecryptedExts 默认值
- yt-dlp 模式：`EncryptedExts` 返回 `make([]string, 0)`，`DecryptedExts` 返回 `[]string{".aac", ".m4a", ".flac", ".mp3", ".ogg"}`
- 如需支持 `.opus`，在 DecryptedExts 末尾追加（参考 YouTubeMusic）
- 如需加密格式，在 EncryptedExts 中返回（参考 AppleMusic 的 `.m4p`，NetEase 的 `.ncm`）

### TidyMusic 方法模板
```go
func (r *{Platform}Processor) TidyMusic() error {
    files, err := os.ReadDir(r.tempDir)
    if err != nil {
        return fmt.Errorf("读取临时目录失败: %w", err)
    }
    if len(files) == 0 {
        utils.WarnWithFormat("[{TAG}] ⚠️ 未找到待整理的音乐文件")
        return errors.New("未找到待整理的音乐文件")
    }
    switch r.cfg.Tidy.Mode {
    case 1:
        return r.tidyToLocal(files)
    case 2:
        return r.tidyToWebDAV(files, core.GlobalWebDAV)
    default:
        return fmt.Errorf("未知整理模式: %d", r.cfg.Tidy.Mode)
    }
}
```

## 生成步骤

1. 确定平台名称 → 计算所有命名
2. 确定模式 → 读取对应参考文件
3. 从参考文件复制完整 Go 代码，应用命名替换
4. 写入 `processor/music/<platform>_music.go`

## 注册步骤（输出给用户）

生成完文件后，告知用户还需要手动执行以下步骤：

1. **添加临时目录常量**：在 `processor/music/music_processor.go` 的常量区域添加：
   ```go
   var {Platform}TempDir = filepath.Join(BaseTempDir, "{platform}")
   ```

2. **添加 LinkType 常量**：在 `processor/processor.go` 的音乐平台枚举区域添加：
   ```go
   Link{Platform} LinkType = "{Platform}"
   ```

3. **添加匹配规则**：在 `core/linkparser/matcher_rules.go` 中为新平台添加 URL 匹配规则

4. **更新解析器**：在 `core/linkparser/parser.go` 中更新 `musicModeMappers`（如需新平台的音乐模式映射）

## 验证方式

1. 检查生成的 Go 文件是否包含所有必需的方法
2. 验证编译：`go vet ./processor/music/`
3. 核对日志格式、表情符号、注释风格是否与现有代码一致
