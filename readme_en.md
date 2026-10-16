<h1 align="center">🎵 GYMDL</h1>
<p align="center">Cross-platform intelligent music download & management tool</p>

<p align="center">
    <a href="#"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go" /></a>
    <a href="#"><img src="https://img.shields.io/badge/License-MIT-green" /></a>
    <a href="https://github.com/nichuanfang/gymdl/releases"><img src="https://img.shields.io/github/v/release/nichuanfang/gymdl?logo=github" /></a>
    <a href="https://github.com/nichuanfang/gymdl/pkgs/container/gymdl"><img src="https://img.shields.io/badge/GHCR-gymdl-blue?logo=docker" /></a>
    <a href="#"><img src="https://img.shields.io/badge/Telegram-Bot-blue?logo=telegram" /></a>
</p>

<p align="center">
    <a href="readme.md">中文</a> | <b>English</b>
</p>

---

## 🧭 Introduction

**GYMDL** is a cross-platform intelligent music download and management tool built with Go. It automatically recognizes links from mainstream music platforms and performs efficient downloading, decryption, and organizing. It also offers CookieCloud login auto-sync, WebDAV upload, directory monitoring, Telegram Bot control and notifications, and a built-in WebUI (Download / Search / History / Library / Logs / Settings / QQ QR login), making music management smarter and easier.

- **Current version**: [`v1.5.10`](https://github.com/nichuanfang/gymdl/releases) (multi-platform binaries + Docker images)
- **Deployment forms**: local binary / Docker / NAS, all of which can combine Telegram Bot and WebUI

---

## ✨ Core Features

| Feature                                                                              | Status      |
| ------------------------------------------------------------------------------------ | ----------- |
| Mainstream platforms: Apple Music, Spotify, YouTube Music, SoundCloud, etc.          | ✅          |
| Intelligent link recognition and parsing                                             | ✅          |
| CookieCloud auto-sync of login status                                                | ✅          |
| WebDAV auto-upload of organized music                                                | ✅          |
| Telegram Bot for controlling downloads and receiving notifications                   | ✅          |
| Scheduled tasks (gocron)                                                             | ✅          |
| Directory monitoring / downloader monitoring                                         | ✅          |
| QQ Music download                                                                    | ✅          |
| NetEase Cloud Music download                                                         | ✅          |
| Apple Music download                                                                 | ✅          |
| YouTube Music download                                                               | ✅          |
| SoundCloud download                                                                  | ✅          |
| Spotify download                                                                     | ✅          |
| AI assistant                                                                         | ✅          |
| n8n integration                                                                      | ✅          |
| Web UI (Download / Search / History / Library / Logs / Settings / QQ Login)          | ✅          |
| Video download (YouTube, Bilibili, Douyin, etc.)                                     | ✅          |
| Multiple notification channels                                                       | ⚠️ Planned  |

---

## 🎧 Supported Platforms

### Music

| Platform        | Example link                                                    | Notes                              |
| --------------- | --------------------------------------------------------------- | ---------------------------------- |
| NetEase Cloud   | `music.163.com/#/song?id=...`, `163cn.tv/...`                   | Songs / playlists / albums / radio |
| QQ Music        | `y.qq.com/...`, `i.y.qq.com/n2/m/share/details/...`             | Requires QQMusicApi                |
| Apple Music     | `music.apple.com/.../song/...`, `.../album/...`                 | Songs / albums / playlists         |
| YouTube Music   | `music.youtube.com/watch?v=...`                                 | Powered by yt-dlp                  |
| SoundCloud      | `soundcloud.com/...`, `snd.sc/...`                              | Tracks / Sets                      |
| Spotify         | `open.spotify.com/track/...`                                    | track / album / playlist           |

### Video

| Platform | Example link                                |
| -------- | ------------------------------------------- |
| YouTube  | `youtube.com/watch?v=...`, `youtu.be/...`   |
| Bilibili | `bilibili.com/video/BV...`, `b23.tv/...`    |
| Douyin   | `douyin.com/video/...`, `v.douyin.com/...`  |
| Xiaohongshu | In-app share links                       |

> 💡 With `additional_config.music_mode` enabled, links from video platforms are preferentially converted to audio downloads.

---

## 🕸️ Architecture

### Five-layer architecture

<a href="#"><img src="img/architecture.svg" /></a>

### n8n architecture

<a href="#"><img src="img/n8n_automation.svg" /></a>

---

## 🖥️ Web UI

The built-in Web UI provides:

- **Dashboard**: overview of the online status of each service
- **Download**: paste a link to submit a download and watch task progress in real time
- **Search**: keyword search across multiple platforms (NetEase / QQ, etc.), with 10/20/50/100 merged pagination and duplicate pre-check when downloading
- **History**: view completed/failed download records (persisted to `data/gymdl.sqlite3`)
- **Library**: browse organized music files, search by song / artist / album within the current directory subtree, with preview and delete support
- **Settings**: edit and save `config.yaml` online + manual CookieCloud sync + container restart
- **Logs**: real-time SSE log stream with level filtering / pause / search
- **QQ Login**: scan QR code to log in to QQ Music right in the browser, no Bot required

**Enabling**: set `web_config.enable: true` in `config.yaml`, then visit `http://localhost:8080`.

**Development mode**:
```bash
# Terminal 1: start the backend
go run main.go -c config.yaml

# Terminal 2: start the frontend (Vite proxies /api to 8080)
cd webui && npm run dev
```

**About authentication**: By default the WebUI stays in compatibility mode and does not require login. Once `web_config.auth` is enabled, the config, search, task, log, and file APIs all require login, with a session valid for 12 hours; secret values are never echoed back — leaving a field blank keeps the original value, and use the "clear" action next to a field to remove it. For reverse proxy deployments, please use HTTPS.

**About service restart**: In Docker deployments, the Compose example already enables the "container restart" button on the settings page (`GYMDL_WEBUI_RESTART_ENABLED=true`); the button only appears inside a container, and restarts are rejected while download tasks are in progress.

**About hot reload**: Currently only `tidy.dist_dir` and `additional_config.music_mode` support hot reload and apply to new tasks after saving; running tasks keep the snapshot taken at startup, and all other configs require a service restart after saving.

---

## ⚙️ Quick Start

### 1️⃣ Get the project and build

```bash
git clone https://github.com/nichuanfang/gymdl.git
cd gymdl
make release
```

> 💡 `make release` first builds the WebUI (`make webui`, output to `web/dist` for `go:embed`), then cross-compiles binaries for all platforms into the `release/` directory, bundling `config.yaml.example`, `requirements.txt`, etc. If you only need the backend, you can skip the WebUI step (but the WebUI assets will be unavailable).

You can also download the package for your platform directly from [Releases](https://github.com/nichuanfang/gymdl/releases).

### 2️⃣ Example configuration `config.yaml`

<details>
<summary>Click to expand the YAML config example</summary>

```yaml
# GYMDL configuration file
# Detailed field descriptions below

# Web service configuration
web_config:
  enable: false # Whether to enable the web service
  app_domain: 'localhost' # Web service domain
  https: false # Whether to enable HTTPS
  app_port: 8080 # Web service listening port
  gin_mode: 'debug' # Gin mode: one of [debug, release, test]
  auth: # WebUI admin login; the password is plaintext in the config file, so restrict file permissions
    enable: false # Whether to enable the WebUI login gate
    username: '' # Admin username
    password: '' # Admin password; can also be overridden by an environment variable

# CookieCloud configuration
cookie_cloud:
  mode: 1 # CookieCloud sync mode: 1 scheduled refresh, 2 webhook. Default 1; mode 2 requires the Gin web service
  cookiecloud_url: '' # CookieCloud service URL
  cookiecloud_uuid: '' # CookieCloud UUID
  cookiecloud_key: '' # CookieCloud key (all sync ends must use the same key, otherwise decryption will fail)
  cookie_file_path: '' # Cookie file storage directory
  cookie_file: '' # Cookie file name
  expire_time: 180 # Cookie file expiry time (minutes)

# Tidy configuration
tidy:
  mode: 1 # Tidy mode: 1=organize into dist_dir, 2=organize into webdav_dir
  dist_dir: 'data/dist' # Local tidy directory used when mode=1

# WebDAV configuration
webdav:
  webdav_url: '' # WebDAV service URL
  webdav_user: '' # WebDAV username
  webdav_pass: '' # WebDAV password
  webdav_dir: '' # WebDAV target path
  webdav_host: '' # WebDAV hostname; only needed when accessed via container name + frp

# Log configuration
log:
  mode: 1 # Log mode: 1=stdout, 2=log file, 3=stdout+file
  level: 2 # Log level: 1=debug, 2=info, 3=warn, 4=error, 5=fatal
  file: 'data/logs/run.log' # Log file path

# Telegram configuration
telegram:
  enable: true # Whether to enable the Telegram notification service
  mode: 1 # Run mode: 1=long polling, 2=Webhook (1 recommended for dev, 2 for production)
  chat_id: '' # Bot chat_id
  bot_token: '' # Telegram Bot Token
  allowed_users:
    - '' # User allowlist (Telegram user IDs)
  webhook_url: '' # Webhook URL (required when mode=2)
  webhook_port: 9000 # Listening port in Webhook mode

# lrcapi configuration
lrc_api:
  enable: false # Whether to enable the lrcapi lyrics service
  lrc_api_url: '' # lrcapi service URL
  api_key: '' # Auth key
  lrc_api_host: '' # lrcapi service hostname; only needed when accessed via container name + frp

# AI configuration
ai:
  enable: false # Whether to enable AI features
  base_url: '' # Base URL of the AI API
  model: '' # AI model name to use
  api_key: '' # API Key of the AI service
  system_prompt: '' # Default system prompt

# N8N configuration
n8n_config:
  enable: false # Whether to enable n8n
  n8n_base_url: '' # URL of your self-hosted n8n
  auth_token: '' # Endpoint auth token
  empty_trash_endpoint: '' # Empty trash
  ai_playlist_endpoint: '' # AI smart playlist
  tidy_playlist_endpoint: '' # Playlist tidy endpoint
  playlist_assist: '' # Whether to enable AI enhancement for playlist classification; when on, AI assists with classification
  tidy_playlist: # Playlists; names must match those in navidrome
    - name: Chinese
      desc: The main singing language of the song is Chinese (including Mandarin, Cantonese, Hokkien, etc.)
    - name: English
      desc: The main singing language of the song is English (if there is only a small amount of English sampling and it is essentially instrumental, do not check this)

# QQ Music API configuration
qq_music_api:
  enable: false # Whether to enable
  endpoint: '' # Service URL; if proxy_url is needed, fill in the public address, otherwise it cannot be accessed
  proxy_url: '' # Proxy address; servers abroad need this to bypass regional restrictions
  vip_level: vip # QQ membership level, vip or svip; affects audio quality
  login_type: 0 # Login method: 0 not logged in, 1 WeChat, 2 QQ
  refresh_key: '' # Used to refresh an expired musickey; musickey expires after 1 hour
  refresh_token: '' # Used to refresh an expired musickey; musickey expires after 1 hour
  access_token: '' # Used to refresh an expired musickey; musickey expires after 1 hour
  music_id: '' # Used to refresh an expired musickey; musickey expires after 1 hour
  str_music_id: '' # Used to refresh an expired musickey
  open_id: '' # Used to refresh an expired musickey; musickey expires after 1 hour
  union_id: '' # Used to refresh an expired musickey; musickey expires after 1 hour
  music_key: '' # Used to refresh an expired musickey; musickey expires after 1 hour
  expired_at: '' # Used to refresh an expired musickey; musickey expires after 1 hour

# Additional configuration
additional_config:
  enable_cron: false # Whether to enable scheduled tasks; only when enabled will it try to sync cookie files (important)
  enable_monitor: false # Whether to enable directory monitoring; when on, the download directory is monitored and auto-decrypted with the um cli
  monitor_dirs:
    - '' # Monitored directories, downloader monitoring
  music_mode: true # Whether to enable music mode; links from video platforms are preferentially converted to audio

# Proxy configuration
proxy:
  enable: false # Whether to enable the proxy
  scheme: '' # Proxy protocol: http/https/socks5
  host: '127.0.0.1' # Proxy host
  port: 10809 # Proxy port
  user: '' # Proxy username
  pass: '' # Proxy password
  auth: false # Whether to enable proxy auth
```

</details>

> ℹ️ **Tip**: Expand to see detailed comments for each field, handy for beginners editing the config directly. The full template is in [`config.yaml.example`](config.yaml.example) in the repo.

---

### 3️⃣ Run GYMDL

**Option 1: Run from source**
```bash
git clone https://github.com/nichuanfang/gymdl.git
cd gymdl
go mod tidy
go run main.go
```

**Option 2: Run the binary**
```bash
./gymdl -c config.yaml
```

**Option 3: Docker Compose (recommended)**

```bash
# 1. Prepare config and directories
cp config.yaml.example config.yaml
mkdir -p data

# 2. Prepare the Compose file (you need to create the external network mynet first)
cp docker-compose.yml.example docker-compose.yml
docker network create mynet   # skip if it already exists

# 3. Start
docker compose up -d
```

> Image: `ghcr.io/nichuanfang/gymdl:latest`. The current published image target platform is `linux/amd64`; ARM hosts run it via `platform: linux/amd64` emulation shown in the Compose example.
> The Compose example must mount `./config.yaml` and `./data` (SQLite, history, download directory, logs, temp files, etc. all live there), otherwise it will fail to start or lose data.

> [!TIP]
> Three recommended run modes
>
> 1. Run on a VPS: receive user messages via Telegram, organize and store via WebDAV
> 2. Run on a NAS: receive user messages via Telegram, organize directly into NAS directories
> 3. Run on Windows/Mac: unlock client apps via downloader monitoring, organize and store

---

### 4️⃣ Usage flow

1. Install the [CookieCloud extension](https://chrome.google.com/webstore/detail/cookiecloud/ffjiejobkoibkjlhjnlgmcnnigeelbdl)
2. Log in to music platforms and sync cookies (membership required)
3. Configure `config.yaml`
4. Set up the necessary runtime environment
5. Use `gymdl`

> ⚡ **Tip**: Make sure your cookies are valid, otherwise downloading high-quality audio may fail.

---

### 5️⃣ Prerequisites for high-quality downloads

| Requirement                        | Notes |
| ---------------------------------- | ----- |
| VPN / proxy access                 | ✅    |
| Logged-in music platform membership account | ✅ |
| CookieCloud synced                 | ✅    |

| Deployment         | Notes                                                                                                                       |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------- |
| 🐳 Docker          | • Use the official image directly; it bundles `ffmpeg` / `ffprobe` / `MP4Box` / `N_m3u8DL-RE` / `aria2` / Playwright, etc.    |
| 💻 Local           | Requires additionally installing: `Python (3.12+)`, `ffmpeg` / `ffprobe`, `N_m3u8DL-RE`, `MP4Box`                            |

---

## 📁 Data & Directory Reference

| Path                        | Notes                                                                    |
| --------------------------- | ------------------------------------------------------------------------ |
| `data/gymdl.sqlite3`        | WebUI history / task persistence database                                |
| `data/dist`                 | Local tidy directory when `tidy.mode=1`                                  |
| `data/temp`                 | Download temp directory (cleaned on startup)                             |
| `data/logs`                 | Log file directory (when `log.mode` includes files)                      |
| `data/temp/musickey.json`   | QQ Music runtime credential cache (does not rewrite `config.yaml` on each login) |

---

## 🤝 Contributing

❤️ **Issues** and **Pull Requests** are welcome

- Keep the code style consistent
- Run `go fmt` before submitting a PR
- Describe your changes in detail in the PR

---

## 📜 License

MIT License ([LICENSE](LICENSE))

---

## 📬 Contact

- GitHub: [@nichuanfang](https://github.com/nichuanfang)
- Email: [f18326186224@gmail.com](mailto:f18326186224@gmail.com)

> 💬 _"May your music never stop."_ 🎧
