# TODO 骨架模式参考模板

基于 `spotify.go` 的模式。所有方法实现为 `panic("implement me")` / `TODO implement me`，仅提供结构体和 Init/Name/Songs 的实现。用于先建立文件结构，后续再填充实际逻辑。

## 适用场景

- 用户暂时不想实现下载逻辑，只需要占位
- 先建立骨架，后续再实现
- 用户说"模板"、"骨架"、"先建空文件"

## 完整代码模板

```go
package music

import (
	"os/exec"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor"
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
	// TODO implement me
	panic("implement me")
}

func ({{RECEIVER}} *{{PROCESSOR}}) DownloadCommand(url string) *exec.Cmd {
	// TODO implement me
	panic("implement me")
}

func ({{RECEIVER}} *{{PROCESSOR}}) BeforeTidy() error {
	// TODO implement me
	panic("implement me")
}

func ({{RECEIVER}} *{{PROCESSOR}}) NeedRemoveDRM() bool {
	// TODO implement me
	panic("implement me")
}

func ({{RECEIVER}} *{{PROCESSOR}}) DRMRemove() error {
	// TODO implement me
	panic("implement me")
}

func ({{RECEIVER}} *{{PROCESSOR}}) TidyMusic() error {
	// TODO implement me
	panic("implement me")
}

func ({{RECEIVER}} *{{PROCESSOR}}) EncryptedExts() []string {
	// TODO implement me
	panic("implement me")
}

func ({{RECEIVER}} *{{PROCESSOR}}) DecryptedExts() []string {
	// TODO implement me
	panic("implement me")
}

/* ------------------------ 拓展方法 ------------------------ */
```

## 变量替换说明

| 变量 | 说明 | 示例值 |
|------|------|--------|
| `{{PROCESSOR}}` | 处理器结构体名称 | `SpotifyProcessor` |
| `{{RECEIVER}}` | 接收器变量名 | `p` |
| `{{TEMP_DIR}}` | 临时目录常量名 | `SpotifyTempDir` |
| `{{LINK_TYPE}}` | LinkType 常量名 | `LinkSpotify` |
