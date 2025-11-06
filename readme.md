<h1 align="center">🎵 GYMDL</h1>
<p align="center">跨平台智能音乐下载与管理工具</p>

<p align="center">
    <a href="#"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go" /></a>
    <a href="#"><img src="https://img.shields.io/badge/License-MIT-green" /></a>
    <a href="#"><img src="https://img.shields.io/github/actions/workflow/status/nichuanfang/gymdl/release.yml?logo=github" /></a>
    <a href="#"><img src="https://img.shields.io/badge/Telegram-Bot-blue?logo=telegram" /></a>
</p>

---

## 🧭 项目简介

**GYMDL** 是一款跨平台智能音乐下载与管理工具，基于 Go开发，支持自动识别主流音乐平台的音乐链接，并实现高效下载、解密与整理。同时提供
CookieCloud 自动同步登录、WebDAV 上传、下载器监控、Telegram Bot 控制与通知等功能，让音乐管理更智能、更便捷。

---

## ✨ 核心特性

| 功能                                                   | 状态     |
|------------------------------------------------------|--------|
| 主流音乐平台：Apple Music、Spotify、YouTube Music、SoundCloud等 | ✅      |
| 智能链接识别与解析                                            | ✅      |
| CookieCloud 自动同步登录状态                                 | ✅      |
| WebDAV 自动上传整理后的音乐                                    | ✅      |
| Telegram Bot 控制下载、接收通知                               | ✅      |
| 定时任务调度（gocron）                                       | ✅      |
| 重构模块                                                 | ✅      |
| 下载器监控                                                | ✅      |
| 支持下载列表                                               | ✅      |
| 视频下载                                                 | 🚧 开发中 |
| YoutubeMusic下载                                       | 🚧 开发中 |
| 多个通知渠道                                               | ⚠️ 规划中 |
| AI 助手                                                | ⚠️ 规划中 |
| Web UI                                               | ⚠️ 规划中 |

---

## ⚙️ 快速开始

### 1️⃣ 获取项目并编译

```bash
git clone https://github.com/nichuanfang/gymdl.git .
make release
````

### 2️⃣ 配置文件 `config.yaml` 示例

<details>
<summary>点击展开 YAML 配置示例</summary>

```yaml
# GYMDL 配置文件
# 以下为详细配置项说明

# Web 服务配置
web_config:
  enable: false  # 是否启用 web 服务
  app_domain: "localhost"  # web 服务域名
  https: false  # 是否开启 HTTPS
  app_port: 8080  # web 服务监听端口
  gin_mode: "debug"  # Gin 运行模式: 可选 [debug, release, test]

# CookieCloud 配置
cookie_cloud:
  cookiecloud_url: ""  # CookieCloud 服务地址
  cookiecloud_uuid: ""  # CookieCloud UUID
  cookiecloud_key: ""  # CookieCloud key (多个同步端需填写同一个key，否则会解密失败)
  cookie_file_path: ""  # Cookie 文件存储目录
  cookie_file: ""  # Cookie 文件名
  expire_time: 180  # Cookie 文件过期时间(分钟)

# 资源整理配置
tidy:
  mode: 1  # 资源整理模式: 1=整理到 dist_dir, 2=整理到 webdav_dir
  dist_dir: "data/dist"  # 当 mode=1 时使用的本地整理目录

# WebDAV 配置
webdav:
  webdav_url: ""  # WebDAV 服务地址
  webdav_user: ""  # WebDAV 用户名
  webdav_pass: ""  # WebDAV 密码
  webdav_dir: ""  # WebDAV 目标路径

# 日志配置
log:
  mode: 1  # 日志模式: 1=标准输出, 2=日志文件, 3=标准输出+文件
  level: 2  # 日志等级: 1=debug, 2=info, 3=warn, 4=error, 5=fatal
  file: "data/logs/run.log"  # 日志文件路径

# Telegram 配置
telegram:
  enable: true  # 是否启用 Telegram 通知服务
  mode: 1  # 运行模式: 1=长轮询, 2=Webhook (推荐开发用1, 生产用2)
  chat_id: ""  # 机器人 chat_id
  bot_token: ""  # Telegram Bot Token
  allowed_users:
    - ""  # 用户白名单列表 (Telegram 用户 ID)
  webhook_url: ""  # Webhook 地址 (mode=2 时必填)
  webhook_port: 9000  # Webhook 模式下监听端口

# qq音乐api配置
qq_music_api:
  enable: false #是否启用
  endpoint: "" #服务地址 
  login_type: 0 #登录方式: 0未登录 1微信 2QQ
  enable_sign: false #是否启用加密接口 加密接口会多一个解密操作 但是更安全
  enable_cache: true #是否启用接口缓存

# AI 配置
ai:
  enable: false  # 是否启用 AI 功能
  base_url: ""  # AI 接口的 Base URL
  model: ""  # 使用的 AI 模型名称
  api_key: ""  # AI 服务的 API Key
  system_prompt: ""  # 默认系统提示词

# 附加配置
additional_config:
  enable_cron: false  # 是否启用定时任务功能 只有开启此配置才会尝试同步cookie文件(重要)
  enable_monitor: false  # 是否启用目录监听 开启后监听下载目录使用um cli自动解密
  monitor_dirs:
    - ""  # 监听的目录,下载器监控

# 代理配置
proxy:
  enable: false  # 是否启用代理
  scheme: ""  # 代理协议: http/https/socks5
  host: "127.0.0.1"  # 代理主机
  port: 10809  # 代理端口
  user: ""  # 代理用户名
  pass: ""  # 代理密码
  auth: false  # 是否启用代理认证
```

</details>

> ℹ️ **提示**：展开查看每个字段的详细注释说明，方便初学者直接修改配置。

---

### 3️⃣ 运行 GYMDL

```bash
git clone https://github.com/nichuanfang/gymdl.git .
go mod tidy
go run main.go
```

or

```bash
./gymdl -c config.yaml
```

> [!TIP]
> 推荐的三种运行模式
>
> 1. vps运行,telegram接收用户消息,通过webdav整理入库
> 2. nas运行,telegram接收用户消息,直接整理到nas目录
> 3. win/mac运行,通过下载器监控解锁客户端应用,整理入库

---

### 4️⃣ 使用流程

1. 安装 [CookieCloud 插件](https://chrome.google.com/webstore/detail/cookiecloud/ffjiejobkoibkjlhjnlgmcnnigeelbdl)
2. 登录音乐平台并同步 Cookie(需会员)
3. 配置 `config.yaml`
4. 配置好必要的运行环境
5. 使用 `gymdl`

> ⚡ **小贴士**：确保你的 Cookie 有效，否则下载高音质音乐可能失败。

---

### 5️⃣ 高音质下载前置条件

| 条件              | 说明   |
|-----------------|------|
| 科学上网            | ✅    |
| 登录音乐平台会员账号      | ✅    |
| CookieCloud 已同步 | ✅    |
| 部署方式            | 详见下表 |

| 部署方式         | 说明                                                                                         |
|--------------|--------------------------------------------------------------------------------------------|
| 🐳 Docker 部署 | <br>• 配置 `config.yaml`<br>                                                                 |
| 💻 本地部署      | 需额外安装：<br>• `Python(3.12+)`<br>• `ffmpeg` / `ffprobe`<br>• `N_m3u8DL-RE`<br>• `MP4Box`<br> |

---

## 🤝 贡献指南

❤️ 欢迎提交 **Issue** 或 **Pull Request**
• 保持代码风格一致
• PR 前使用 `go fmt` 格式化代码
• PR 中详细说明改动内容

---

## 📜 许可证

MIT License ([LICENSE](LICENSE))

---

## 📬 联系方式

* GitHub：[@nichuanfang](https://github.com/nichuanfang)
* Email：[f18326186224@gmail.com](mailto:f18326186224@gmail.com)

> 💬 *“愿你的音乐，永不停歇。”* 🎧
