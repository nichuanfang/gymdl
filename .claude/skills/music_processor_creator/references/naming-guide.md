# 命名指南

将用户提供的平台名称转换为 Go 代码中使用的各种标识符。

## 常用平台名映射

| 用户输入 | 文件名 | 结构体 | TempDir 常量 | LinkType 常量 | 日志标签 |
|----------|--------|--------|--------------|---------------|---------|
| Tidal | `tidal_music.go` | `TidalProcessor` | `TidalTempDir` | `LinkTidal` | `[Tidal]` |
| Deezer | `deezer_music.go` | `DeezerProcessor` | `DeezerTempDir` | `LinkDeezer` | `[Deezer]` |
| Pandora | `pandora_music.go` | `PandoraProcessor` | `PandoraTempDir` | `LinkPandora` | `[Pandora]` |
| AmazonMusic | `amazon_music.go` | `AmazonMusicProcessor` | `AmazonMusicTempDir` | `LinkAmazonMusic` | `[AmazonMusic]` |
| Qobuz | `qobuz_music.go` | `QobuzProcessor` | `QobuzTempDir` | `LinkQobuz` | `[Qobuz]` |
| Napster | `napster_music.go` | `NapsterProcessor` | `NapsterTempDir` | `LinkNapster` | `[Napster]` |
| YandexMusic | `yandex_music.go` | `YandexMusicProcessor` | `YandexMusicTempDir` | `LinkYandexMusic` | `[YandexMusic]` |
| KKBOX | `kkbox_music.go` | `KKBOXProcessor` | `KKBOXTempDir` | `LinkKKBOX` | `[KKBOX]` |
| Joox | `joox_music.go` | `JooxProcessor` | `JooxTempDir` | `LinkJoox` | `[Joox]` |

## 命名规则详解

### 文件名（snake_case）
1. 将 PascalCase 按大写字母分界点分解为单词
2. 单词之间用 `_` 连接
3. 全部小写
4. 追加 `_music.go`

转换示例：
- `Tidal` → `tidal` → `tidal_music.go`
- `AmazonMusic` → `Amazon Music` → `amazon` + `music` → `amazon_music.go`
- `NetEaseCloudMusic` → `Net Ease Cloud Music` → `netease_cloud_music.go`
- `KKBOX` → 全大写保留为 `kkbox_music.go`
- `YandexMusic` → `yandex_music.go`

### 结构体名（PascalCase）
- 平台名直接保持用户输入的大小写 + `Processor`
- 用户输入 `Tidal` → `TidalProcessor`
- 用户输入 `AmazonMusic` → `AmazonMusicProcessor`

### TempDir 常量（PascalCase + TempDir）
- 平台名 + `TempDir`
- `Tidal` → `TidalTempDir`
- `AmazonMusic` → `AmazonMusicTempDir`

### LinkType 常量（Link + PascalCase）
- `Link` + 平台名
- `Tidal` → `LinkTidal`
- `AmazonMusic` → `LinkAmazonMusic`
- 与现有常量风格一致：`LinkAppleMusic`, `LinkNetEase`, `LinkQQMusic`

### 日志标签（[平台名]）
- 直接在 `[` 和 `]` 之间放平台名
- `Tidal` → `[Tidal]`
- `AmazonMusic` → `[AmazonMusic]`
