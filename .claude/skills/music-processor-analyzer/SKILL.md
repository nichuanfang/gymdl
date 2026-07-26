---
name: music-processor-analyzer
description: >
  分析 gymdl 项目中音乐下载报错的根源。当用户提供音乐下载链接和报错信息时，
  自动识别对应的音乐处理器（NetEase、QQ音乐、Apple Music、YouTube Music、
  SoundCloud、BiliBili音乐、Spotify、Forward），定位代码中的具体出错位置，
  分析可能原因，给出修复建议。适用于 Telegram Bot、Web API 或 CLI 方式运行
  gymdl 时产生的各类下载失败场景。核心代码位于 `processor/music/` 和 `core/` 目录。
---

# 🎵 音乐处理器错误分析器

## 概述

当用户提供一段文本，其中包含**音乐下载链接** 和 **报错信息** 时，你需要：

1. **识别平台 & 处理器** — 根据链接域名/规则匹配对应的 `music.Processor` 实现
2. **定位代码位置** — 分析报错信息，确定是哪个函数/哪行代码抛出的错误
3. **分析可能原因** — 结合代码逻辑和报错详情推断根本原因
4. **给出修复建议** — 提供具体的配置调整、代码修复或外部操作建议

## 输入格式

输入通常为如下形式（无需严格匹配，灵活提取链接和错误信息即可）：

> 链接: https://music.163.com/song?id=123456, 报错: 网易云API请求失败: ...
> 下载链接: https://y.qq.com/..., 错误信息: ...
> 链接: xxx, 报错: yyy

从文本中提取出 **链接（URL）** 和 **报错信息（error message）**。

---

## 平台识别与处理器映射

根据链接 URL 的域名和路径，对照以下规则识别处理器。域名匹配规则定义在 `core/linkparser/matcher_rules.go`，处理器代码在 `processor/music/` 下。

### 1. 网易云音乐 — `NetEaseProcessor` (`processor/music/netease.go`)
| 域名 | 路径特征 |
|------|---------|
| `music.163.com` | `/song?id=`, `/playlist?id=`, `/album?id=` |
| `y.music.163.com` | 同上 |
| `163cn.tv` | 短链格式 |
| `163cn.link` | 短链格式 |

**下载方式**: 自建 HTTP 下载（通过网易云 API 获取 URL，然后直接下载）
**Name()**: `"网易云音乐"`
**前缀标识**: `[NCM]`

### 2. QQ音乐 — `QQMusicProcessor` (`processor/music/qq_music.go`)
| 域名 | 路径特征 |
|------|---------|
| `y.qq.com` | `/n/ryqq/songDetail/`, `/n/ryqq/playlist/` |
| `c.y.qq.com`, `i.y.qq.com`, `m.y.qq.com` | 各类分享格式 |
| `c6.y.qq.com` | 重定向域名 |

**下载方式**: 通过自建 `QQMusicAPI` HTTP API 获取歌曲详情、下载链接，然后下载
**Name()**: `"QQ音乐"`
**前缀标识**: `[QQMusic]` / `[QQMusicAPI]`

### 3. Apple Music — `AppleMusicProcessor` (`processor/music/apple_music.go`)
| 域名 | 路径特征 |
|------|---------|
| `music.apple.com` | `/song/`, `/album/`, `/playlist/` |

**下载方式**: 调用外部工具 `gamdl`（通过 `exec.Command`）
**Name()**: `"AppleMusic"`
**前缀标识**: `[AppleMusic]`

### 4. YouTube Music — `YoutubeMusicProcessor` (`processor/music/youtube_music.go`)
| 域名 | 路径特征 |
|------|---------|
| `music.youtube.com` | `/watch?v=` |

**下载方式**: 调用外部工具 `yt-dlp`（通过 `exec.Command`）
**Name()**: `"YoutubeMusic"`
**前缀标识**: `[YoutubeMusic]`

### 5. SoundCloud — `SoundCloudProcessor` (`processor/music/soundcloud.go`)
| 域名 | 路径特征 |
|------|---------|
| `soundcloud.com` | `/{user}/{track}` |
| `snd.sc` | 短链 |

**下载方式**: 调用外部工具 `yt-dlp`
**Name()**: `"Soundcloud"`
**前缀标识**: `[SoundCloud]`

### 6. BiliBili音乐 — `BilibiliMusicProcessor` (`processor/music/bilibili_music.go`)
| 域名 | 路径特征 |
|------|---------|
| `www.bilibili.com`, `bilibili.com` | `/video/BV` (MusicMode 转换) |
| `b23.tv` | 短链 |
| `m.bilibili.com` | 手机端 |

**注意**: 当配置中 `MusicMode = true` 时，B站视频链接会从 `video.BiliBiliProcessor` 自动转换为 `BilibiliMusicProcessor`。映射定义在 `core/linkparser/parser.go:38`。
**Name()**: `"BiliBiliMusic"`
**前缀标识**: `[BiliBiliMusic]`

### 7. Spotify — `SpotifyProcessor` (`processor/music/spotify.go`)
| 域名 | 路径特征 |
|------|---------|
| `open.spotify.com` | `/track/`, `/album/`, `/playlist/` |
| `play.spotify.com` | 同上 |

**注意**: 当前为 **stub** 实现 — `DownloadMusic()`, `DownloadCommand()`, `BeforeTidy()`, `TidyMusic()` 等核心方法均只 `panic("implement me")`。遇到 Spotify 报错直接提示"此处理器尚未实现"。
**Name()**: `"Spotify"`

### 8. Forward音乐 — `ForwardProcessor` (`processor/music/forword_music.go`)
**Name()**: `"ForwardMusic"`
**前缀标识**: `[Forward]`
由上游推送音频数据，直接从内存写入文件。

---

## 常见错误模式与代码定位

每一类错误都标注了可能的出错函数和代码行号范围（基于当前代码库）。**分析时请实际读取目标文件确认具体行号。**

### A. HTTP/API 请求失败

| 平台 | 错误模式关键词 | 关联函数 | 文件:行号 |
|------|-------------|---------|----------|
| 网易云 | `网易云API请求失败` | `FetchSongData`, `FetchPlaylistData`, `FetchPlaylistSongData` | `netease.go:232-269, 272-296, 385-449` |
| QQ音乐 | `请求失败`, `构建请求失败`, `JSON解析失败` | `doGetRequest`, `doGetRequestWithRetry` | `qq_music.go:561-610` |
| QQ音乐 | `请求返回错误: Code=`, `Musickey` | `ensureValidMusickey`, `refreshAndSave` | `qq_music.go:680-746` |
| QQ音乐 | `重定向失败`, `无法解析最终 URL` | `parseQQMusicLink`, `getFinalURL` | `qq_music.go:790-858` |

**可能原因**: 网络不通、API 服务未启动/不可达、Cookie 过期、API 返回错误码
**修复方向**: 
- 网易云: 检查 `MUSIC_U` Cookie 是否有效（`netease.go:43`），网络连接
- QQ音乐: 检查 `QQMusicApiConfig.Endpoint` 配置、Musickey 是否过期（配置文件或 `data/temp/musickey.json`）

### B. 外部工具执行失败

| 平台 | 错误模式关键词 | 外部工具 | 关联函数 | 文件:行号 |
|------|-------------|---------|---------|----------|
| Apple Music | `gamdl 下载失败` | `gamdl` | `DownloadMusic`, `DownloadCommand` | `apple_music.go:45-97` |
| YouTube Music | `yt-dlp 下载失败` | `yt-dlp` | `DownloadMusic`, `DownloadCommand` | `youtube_music.go:54-235` |
| SoundCloud | `yt-dlp 下载失败` | `yt-dlp` | `DownloadMusic`, `DownloadCommand` | `soundcloud.go:47-193` |
| BiliBili Music | `yt-dlp 下载失败` | `yt-dlp` | `DownloadMusic`, `DownloadCommand` | `bilibili_music.go:48-228` |
| 通用 | `无法构建 yt-dlp 命令` | `yt-dlp` | `DownloadCommand` | 各文件 `DownloadCommand` 函数 |

**可能原因**:
- 外部工具未安装或不在 PATH 中
- 工具版本过旧（`yt-dlp` 需要经常更新以适配平台变更）
- Cookie 文件不存在或无效（所有 yt-dlp 处理器都依赖 Cookie 文件，检查 `cookiePath` 构建逻辑）
- Apple Music 的 `gamdl` 输出目录格式错误（`args` 中 `--output-path` / `--temp-path` / 模板参数配置）

**修复方向**:
- 确认 `yt-dlp` / `gamdl` 已安装且可执行
- 运行 `yt-dlp --version` 检查版本，过旧则 `pip install -U yt-dlp`
- 检查 CookieCloud 同步是否正常，Cookie 文件路径配置是否正确
- 对于 `gamdl`: 检查 `--cookies-path` 指向的 Cookie 文件是否包含 `music.apple.com` 的凭据

### C. 下载链接/文件获取失败

| 平台 | 错误模式关键词 | 关联函数 | 文件:行号 |
|------|-------------|---------|----------|
| 网易云 | `未获取到有效歌曲信息或歌曲无下载地址` | `downloadSingle` | `netease.go:140-143` |
| QQ音乐 | `无法获取歌曲下载链接` | `getSongUrl` | `qq_music.go:540-558` |
| QQ音乐 | `查询歌曲信息失败`, `解析文件元数据失败` | `downloadSong`, `querySong`, `ParseQQFileMetadate` | `qq_music.go:296-371` |

**可能原因**:
- 会员曲目（QQ音乐 VIP 歌曲需要对应等级的 `VipLevel` 配置）
- 歌曲下架/下架导致的 URL 为空
- API 返回的 URL 过期或无效

### D. 临时目录/文件操作失败

| 错误模式关键词 | 关联函数 | 文件:行号 |
|-------------|---------|----------|
| `创建临时目录失败` | `CreateOutputDir` | `processor/processor.go:66-81` |
| `读取临时目录失败` | `TidyMusic`, `ReadMusicDir` | 各处理器 `TidyMusic` 函数 |
| `未找到待整理的音乐文件` | `TidyMusic` | 各处理器 `TidyMusic` 函数 |
| `删除目录失败` | `RemoveTempDir` | `processor/processor.go:84-102` |

**可能原因**: 磁盘空间不足、权限问题、目录路径包含非法字符

### E. 元数据/标签写入失败

| 错误模式关键词 | 关联函数 | 文件:行号 |
|-------------|---------|----------|
| `write metadata failed` | `WriteTags`, `WriteTagsWithCoverFile`, `WriteTagsWithCoverURL` | `music_processor.go:275-361` |
| `write image failed` | `WriteTagsWithCoverFile`, `WriteTagsWithCoverURL` | `music_processor.go:293-361` |
| `fetch image failed` | `WriteTagsWithCoverURL` | `music_processor.go:319-361` |

**可能原因**: 音频文件损坏、格式不支持、封面 URL 不可访问、`taglib` 库问题

### F. 歌词 API 失败

| 错误模式关键词 | 关联函数 | 文件:行号 |
|-------------|---------|----------|
| `LrcAPI request failed` | `GetLyrics` | `core/lrcapi_helper.go:94-148` |
| `unexpected status code` | `GetLyrics` | `core/lrcapi_helper.go:128` |
| `no lyrics found` | `GetLyrics` | `core/lrcapi_helper.go:142` |
| LrcAPI `connection check failed` | `CheckConnection` | `core/lrcapi_helper.go:56-89` |

**可能原因**: LrcAPI 服务不可用、API Key 配置错误、歌词数据库中无匹配
**注意**: 歌词获取失败是**非致命**的，`FillDefaultTags` 会回退到默认歌词 `music_processor.go:254-264`

### G. 整理/归档失败

| 错误模式关键词 | 关联函数 | 文件:行号 |
|-------------|---------|----------|
| `未配置输出目录` | `tidyToLocal` | 各处理器（如 `netease.go:546-549`）|
| `WebDAV 未初始化` | `tidyToWebDAV` | 各处理器（如 `netease.go:579-582`）|
| `上传失败` | `UploadTo` | `core/webdav_helper.go` |

**可能原因**: 输出目录未配置、WebDAV 连接信息错误、远程磁盘空间不足

---

## 分析报告模板

按以下结构输出分析结果：

```markdown
## 🎯 处理器识别
- **平台**: [平台名称]
- **处理器**: [结构体名] (`processor/music/[文件名]`)
- **下载方式**: [自建HTTP下载 / 外部工具:yt-dlp/gamdl / 内存写入]

## 🔍 报错分析

### 1. 错误定位
- **错误信息**: [提取的报错原文]
- **引发函数**: [函数名] → [父函数调用链]
- **代码位置**: `[文件名]:[行号]`

### 2. 可能原因
- [原因1]: 说明
- [原因2]: 说明

### 3. 修复建议
- **配置文件检查**: [需要检查的配置项]
- **代码修复**: [如需要修改代码，给出具体建议]
- **外部操作**: [如更新工具、刷新Cookie等]

## 📋 排查清单
- [ ] 检查配置文件中相关配置项是否填写正确
- [ ] 确认 Cookie / musickey 是否有效
- [ ] 确认外部工具是否安装且版本正确
- [ ] 确认网络连接和目标 API 是否可达
- [ ] 检查磁盘空间和文件权限
```

---

## 注意事项

1. **Spotify 处理器未实现** — 如果链接匹配到 Spotify，直接指出该处理器目前是 stub，代码中所有方法均为 `panic("implement me")`
2. **通用 URL 提取** — `core/linkparser/parser.go:31` 中的正则 `genericURLRegex` 用于从文本中提取 URL；如果用户提供的链接包含中文/特殊字符，`cleanURLTrailingChars` 函数会清洗尾部符号
3. **MusicMode 转换** — 如果配置开启了 `MusicMode`，B站视频链接会被映射为 `BilibiliMusicProcessor`（`parser.go:37-41`）
4. **分析前** — 确认对应源代码文件存在且未过时，必要时先读取目标文件验证行号
5. **准确性问题** — 所有行号标记基于当前代码版本，分析时应当实际读取对应文件确认
