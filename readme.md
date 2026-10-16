<h1 align="center">🎵 GYMDL</h1>
<p align="center">跨平台智能音乐下载与管理工具</p>

<p align="center">
    <a href="#"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go" /></a>
    <a href="#"><img src="https://img.shields.io/badge/License-MIT-green" /></a>
    <a href="https://github.com/nichuanfang/gymdl/releases"><img src="https://img.shields.io/github/v/release/nichuanfang/gymdl?logo=github" /></a>
    <a href="https://github.com/nichuanfang/gymdl/pkgs/container/gymdl"><img src="https://img.shields.io/badge/GHCR-gymdl-blue?logo=docker" /></a>
    <a href="#"><img src="https://img.shields.io/badge/Telegram-Bot-blue?logo=telegram" /></a>
</p>

<p align="center">
    <b>中文</b> | <a href="readme_en.md">English</a>
</p>

---

## 🧭 项目简介

**GYMDL** 是一款跨平台智能音乐下载与管理工具，基于 Go 开发，支持自动识别主流音乐平台链接，并实现高效下载、解密与整理。同时提供
CookieCloud 自动同步登录、WebDAV 上传、目录监控、Telegram Bot 控制与通知，以及内置 WebUI（下载 / 搜索 / 历史 / 文件库 / 日志 / 设置 / QQ 扫码登录）等功能，让音乐管理更智能、更便捷。

- **当前版本**：[`v1.5.10`](https://github.com/nichuanfang/gymdl/releases)（多平台二进制 + Docker 镜像）
- **运行形态**：本地二进制 / Docker / NAS，均可叠加 Telegram Bot 与 WebUI

---

## ✨ 核心特性

| 功能                                                            | 状态      |
| --------------------------------------------------------------- | --------- |
| 主流音乐平台：Apple Music、Spotify、YouTube Music、SoundCloud 等 | ✅        |
| 智能链接识别与解析                                              | ✅        |
| CookieCloud 自动同步登录状态                                    | ✅        |
| WebDAV 自动上传整理后的音乐                                     | ✅        |
| Telegram Bot 控制下载、接收通知                                 | ✅        |
| 定时任务调度（gocron）                                          | ✅        |
| 目录监控 / 下载器监控                                           | ✅        |
| QQ 音乐下载                                                     | ✅        |
| 网易云音乐下载                                                  | ✅        |
| Apple Music 下载                                                | ✅        |
| YouTube Music 下载                                              | ✅        |
| SoundCloud 下载                                                 | ✅        |
| Spotify 下载                                                    | ✅        |
| AI 助手                                                         | ✅        |
| 集成 n8n                                                        | ✅        |
| Web UI（下载 / 搜索 / 历史 / 文件库 / 日志 / 设置 / QQ 登录）     | ✅        |
| 视频下载（YouTube、Bilibili、抖音等）                           | ✅        |
| 多个通知渠道                                                    | ⚠️ 规划中 |

---

## 🎧 支持的平台

### 音乐

| 平台            | 链接示例                                                        | 说明                          |
| --------------- | --------------------------------------------------------------- | ----------------------------- |
| 网易云音乐      | `music.163.com/#/song?id=...`、`163cn.tv/...`                   | 支持歌曲 / 歌单 / 专辑 / 电台 |
| QQ 音乐         | `y.qq.com/...`、`i.y.qq.com/n2/m/share/details/...`             | 需配合 QQMusicApi 使用        |
| Apple Music     | `music.apple.com/.../song/...`、`.../album/...`                 | 支持歌曲 / 专辑 / 播放列表    |
| YouTube Music   | `music.youtube.com/watch?v=...`                                 | 基于 yt-dlp                   |
| SoundCloud      | `soundcloud.com/...`、`snd.sc/...`                              | 支持曲目 / Set                |
| Spotify         | `open.spotify.com/track/...`                                    | 支持 track / album / playlist |

### 视频

| 平台     | 链接示例                            |
| -------- | ----------------------------------- |
| YouTube  | `youtube.com/watch?v=...`、`youtu.be/...` |
| Bilibili | `bilibili.com/video/BV...`、`b23.tv/...` |
| 抖音     | `douyin.com/video/...`、`v.douyin.com/...` |
| 小红书   | 站内分享链接                        |

> 💡 `additional_config.music_mode` 开启后，视频平台链接会优先转为音频下载。

---

## 🕸️ 架构图

### 五层架构

<a href="#"><img src="img/architecture.svg" /></a>

### n8n 架构

<a href="#"><img src="img/n8n_automation.svg" /></a>

---

## 🖥️ Web UI

内置 Web UI，提供以下功能：

- **仪表盘**：各服务在线状态总览
- **下载**：粘贴链接提交下载，实时查看任务进度
- **搜索**：多平台（网易云 / QQ 等）关键词搜索，支持 10/20/50/100 条合并分页，点击下载时进行重复预检
- **历史**：查看已完成/失败的下载记录（持久化到 `data/gymdl.sqlite3`）
- **文件库**：浏览整理后的音乐文件，支持在当前目录子树按歌曲名/歌手/专辑搜索，并支持试听/删除
- **设置**：在线编辑并保存 `config.yaml` + CookieCloud 手动同步 + 容器重启
- **日志**：实时 SSE 日志流，支持级别过滤 / 暂停 / 搜索
- **QQ 登录**：浏览器内扫码登录 QQ 音乐，无需 Bot

**启用方式**：`config.yaml` 中设置 `web_config.enable: true`，访问 `http://localhost:8080`。

**开发模式**：
```bash
# 终端 1: 启动后端
go run main.go -c config.yaml

# 终端 2: 启动前端（Vite 会将 /api 代理到 8080）
cd webui && npm run dev
```

**关于认证**：WebUI 默认保持兼容模式、不要求登录。启用 `web_config.auth` 后，配置、搜索、任务、日志和文件接口均需要登录，会话有效期 12 小时；密钥值不会回显，留空保持原值，需清除时使用字段旁的“清除”操作。反向代理部署请使用 HTTPS。

**关于服务重启**：Docker 部署下，Compose 示例已启用配置页的“容器重启”按钮（`GYMDL_WEBUI_RESTART_ENABLED=true`）；按钮只在容器中显示，存在进行中的下载任务时会拒绝重启。

**关于热加载**：目前仅 `tidy.dist_dir` 和 `additional_config.music_mode` 支持热加载，并应用于保存后的新任务；运行中的任务保留启动时快照，其余配置保存后需重启服务。

---

## ⚙️ 快速开始

### 1️⃣ 获取项目并编译

```bash
git clone https://github.com/nichuanfang/gymdl.git
cd gymdl
make release
```

> 💡 `make release` 会先编译 WebUI（`make webui`，输出到 `web/dist` 供 `go:embed`），再交叉编译各平台二进制到 `release/` 目录，并把 `config.yaml.example`、`requirements.txt` 等一起打包。仅编译后端时可先跳过 WebUI 步骤（但 WebUI 资源将不可用）。

也可以直接下载 [Releases](https://github.com/nichuanfang/gymdl/releases) 中对应平台的压缩包。

### 2️⃣ 配置文件 `config.yaml` 示例

<details>
<summary>点击展开 YAML 配置示例</summary>

```yaml
# GYMDL 配置文件
# 以下为详细配置项说明

# Web 服务配置
web_config:
  enable: false # 是否启用 web 服务
  app_domain: 'localhost' # web 服务域名
  https: false # 是否开启 HTTPS
  app_port: 8080 # web 服务监听端口
  gin_mode: 'debug' # Gin 运行模式: 可选 [debug, release, test]
  auth: # WebUI 管理登录；密码在配置文件中为明文，请限制文件权限
    enable: false # 是否启用 WebUI 登录门禁
    username: '' # 管理员用户名
    password: '' # 管理员密码；也可通过环境变量覆盖

# CookieCloud 配置
cookie_cloud:
  mode: 1 # cookiecloud同步模式: 1定时刷新 2webhook 默认为1 当同步模式为2时必须启用Gin Web服务
  cookiecloud_url: '' # CookieCloud 服务地址
  cookiecloud_uuid: '' # CookieCloud UUID
  cookiecloud_key: '' # CookieCloud key (多个同步端需填写同一个key，否则会解密失败)
  cookie_file_path: '' # Cookie 文件存储目录
  cookie_file: '' # Cookie 文件名
  expire_time: 180 # Cookie 文件过期时间(分钟)

# 资源整理配置
tidy:
  mode: 1 # 资源整理模式: 1=整理到 dist_dir, 2=整理到 webdav_dir
  dist_dir: 'data/dist' # 当 mode=1 时使用的本地整理目录

# WebDAV 配置
webdav:
  webdav_url: '' # WebDAV 服务地址
  webdav_user: '' # WebDAV 用户名
  webdav_pass: '' # WebDAV 密码
  webdav_dir: '' # WebDAV 目标路径
  webdav_host: '' # webdav主机名 需要容器名+frp访问才需要设置

# 日志配置
log:
  mode: 1 # 日志模式: 1=标准输出, 2=日志文件, 3=标准输出+文件
  level: 2 # 日志等级: 1=debug, 2=info, 3=warn, 4=error, 5=fatal
  file: 'data/logs/run.log' # 日志文件路径

# Telegram 配置
telegram:
  enable: true # 是否启用 Telegram 通知服务
  mode: 1 # 运行模式: 1=长轮询, 2=Webhook (推荐开发用1, 生产用2)
  chat_id: '' # 机器人 chat_id
  bot_token: '' # Telegram Bot Token
  allowed_users:
    - '' # 用户白名单列表 (Telegram 用户 ID)
  webhook_url: '' # Webhook 地址 (mode=2 时必填)
  webhook_port: 9000 # Webhook 模式下监听端口

# lrcapi 配置
lrc_api:
  enable: false # 是否启用lrcapi歌词服务
  lrc_api_url: '' # lrcapi服务url
  api_key: '' # 认证key
  lrc_api_host: '' # lrcapi服务主机名 需要容器名+frp访问才需要设置

# AI 配置
ai:
  enable: false # 是否启用 AI 功能
  base_url: '' # AI 接口的 Base URL
  model: '' # 使用的 AI 模型名称
  api_key: '' # AI 服务的 API Key
  system_prompt: '' # 默认系统提示词

# N8N配置
n8n_config:
  enable: false # 是否启用n8n
  n8n_base_url: '' # 自建n8n的地址
  auth_token: '' # 端点认证密钥
  empty_trash_endpoint: '' # 清空回收站
  ai_playlist_endpoint: '' # AI智能歌单
  tidy_playlist_endpoint: '' # 歌单整理端点
  playlist_assist: '' # 是否开启歌单分类AI增强 开启后ai会辅助歌单分类
  tidy_playlist: #歌单,名称必须与navidrome中保持一致
    - name: 中文
      desc: 歌曲的主要演唱语言为中文(含普通话、粤语、闽南语等)
    - name: 英文
      desc: 歌曲的主要演唱语言为英文(若仅有少量英文采样且本质为纯音乐，请勿勾选此项)

# qq音乐api配置
qq_music_api:
  enable: false #是否启用
  endpoint: '' #服务地址 如果需要proxy_url代理 必须填公网地址 否则无法访问
  proxy_url: '' #代理地址 国外服务器需要配置以绕过地区限制
  vip_level: vip #qq会员级别,可选vip或者svip,影响音质
  login_type: 0 #登录方式: 0未登录 1微信 2QQ
  refresh_key: '' #用于刷新已失效的musickey musickey1小时会过期
  refresh_token: '' #用于刷新已失效的musickey musickey1小时会过期
  access_token: '' #用于刷新已失效的musickey musickey1小时会过期
  music_id: '' #用于刷新已失效的musickey musickey1小时会过期
  str_music_id: '' #用于刷新已失效的musickey
  open_id: '' #用于刷新已失效的musickey musickey1小时会过期
  union_id: '' #用于刷新已失效的musickey musickey1小时会过期
  music_key: '' #用于刷新已失效的musickey musickey1小时会过期
  expired_at: '' #用于刷新已失效的musickey musickey1小时会过期

# 附加配置
additional_config:
  enable_cron: false # 是否启用定时任务功能 只有开启此配置才会尝试同步cookie文件(重要)
  enable_monitor: false # 是否启用目录监听 开启后监听下载目录使用um cli自动解密
  monitor_dirs:
    - '' # 监听的目录,下载器监控
  music_mode: true # 是否启用音乐模式 视频平台链接优先转为音频

# 代理配置
proxy:
  enable: false # 是否启用代理
  scheme: '' # 代理协议: http/https/socks5
  host: '127.0.0.1' # 代理主机
  port: 10809 # 代理端口
  user: '' # 代理用户名
  pass: '' # 代理密码
  auth: false # 是否启用代理认证
```

</details>

> ℹ️ **提示**：展开查看每个字段的详细注释说明，方便初学者直接修改配置。完整模板见仓库中的 [`config.yaml.example`](config.yaml.example)。

---

### 3️⃣ 运行 GYMDL

**方式一：源码运行**
```bash
git clone https://github.com/nichuanfang/gymdl.git
cd gymdl
go mod tidy
go run main.go
```

**方式二：二进制运行**
```bash
./gymdl -c config.yaml
```

**方式三：Docker Compose（推荐）**

```bash
# 1. 准备配置与目录
cp config.yaml.example config.yaml
mkdir -p data

# 2. 准备 Compose 文件（需先创建外部网络 mynet）
cp docker-compose.yml.example docker-compose.yml
docker network create mynet   # 若已存在可跳过

# 3. 启动
docker compose up -d
```

> 镜像：`ghcr.io/nichuanfang/gymdl:latest`。当前发布镜像目标平台为 `linux/amd64`，ARM 主机通过 Compose 示例中的 `platform: linux/amd64` 模拟运行。
> Compose 示例必须挂载 `./config.yaml` 与 `./data`（SQLite、历史、下载目录、日志、临时文件等均在其中），否则将无法启动或丢失数据。

> [!TIP]
> 推荐的三种运行模式
>
> 1. vps 运行，telegram 接收用户消息，通过 webdav 整理入库
> 2. nas 运行，telegram 接收用户消息，直接整理到 nas 目录
> 3. win/mac 运行，通过下载器监控解锁客户端应用，整理入库

---

### 4️⃣ 使用流程

1. 安装 [CookieCloud 插件](https://chrome.google.com/webstore/detail/cookiecloud/ffjiejobkoibkjlhjnlgmcnnigeelbdl)
2. 登录音乐平台并同步 Cookie（需会员）
3. 配置 `config.yaml`
4. 配置好必要的运行环境
5. 使用 `gymdl`

> ⚡ **小贴士**：确保你的 Cookie 有效，否则下载高音质音乐可能失败。

---

### 5️⃣ 高音质下载前置条件

| 条件                 | 说明     |
| -------------------- | -------- |
| 科学上网             | ✅       |
| 登录音乐平台会员账号 | ✅       |
| CookieCloud 已同步   | ✅       |

| 部署方式       | 说明                                                                                                          |
| -------------- | ------------------------------------------------------------------------------------------------------------- |
| 🐳 Docker 部署 | • 直接使用官方镜像，内置 `ffmpeg` / `ffprobe` / `MP4Box` / `N_m3u8DL-RE` / `aria2` / Playwright 等运行依赖     |
| 💻 本地部署    | 需额外安装：`Python(3.12+)`、`ffmpeg` / `ffprobe`、`N_m3u8DL-RE`、`MP4Box`                                   |

---

## 📁 数据与目录说明

| 路径                 | 说明                                                   |
| -------------------- | ------------------------------------------------------ |
| `data/gymdl.sqlite3` | WebUI 历史 / 任务持久化数据库                          |
| `data/dist`          | `tidy.mode=1` 时的本地整理目录                         |
| `data/temp`          | 下载临时目录（启动时清理）                             |
| `data/logs`          | 日志文件目录（`log.mode` 含文件时）                    |
| `data/temp/musickey.json` | QQ 音乐运行时凭证缓存（不会随每次登录改写 `config.yaml`） |

---

## 🤝 贡献指南

❤️ 欢迎提交 **Issue** 或 **Pull Request**

- 保持代码风格一致
- PR 前使用 `go fmt` 格式化代码
- PR 中详细说明改动内容

---

## 📜 许可证

MIT License ([LICENSE](LICENSE))

---

## 📬 联系方式

- GitHub：[@nichuanfang](https://github.com/nichuanfang)
- Email：[f18326186224@gmail.com](mailto:f18326186224@gmail.com)

> 💬 _“愿你的音乐，永不停歇。”_ 🎧
