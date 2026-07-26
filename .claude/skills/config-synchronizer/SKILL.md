---
name: config-synchronizer
description: >
  Gymdl项目配置同步器。当用户修改了 `config/struct.go` 里的配置结构体（新增/删除/修改字段或子结构体），需要同步更新 3 个文件：
  `config/config.go` 的 setDefaults() 方法、`config.yaml.example` 的 YAML 示例、以及 `readme.md`
  中的 YAML 示例。使用此技能确保这 4 个文件始终保持一致。当用户提到"同步配置"、"更新配置"、"配置不同步了"、
  "struct.go 改了但 xxx 没改"、"同步 struct.go 的变更"、"配置对不上"、"帮我同步配置变更"等场景时应自动触发。
  不要在未读 `config/struct.go` 的情况下就执行任何操作。
  此技能只涉及 gymdl 项目的配置同步，不涉及其他项目。
  适用范围：仅 gymdl 项目。
---

# 配置同步器 (Config Synchronizer)

保持 gymdl 项目 4 个配置相关文件的一致性。源数据文件为 `config/struct.go`（Go 结构体定义），据此同步到其余 3 个文件。

## 文件列表

| 文件 | 角色 | 注意事项 |
|------|------|----------|
| `config/struct.go` | **源数据**—Go 结构体 + yaml tags + 注释 | 以此为基准，**不要修改** |
| `config/config.go` | 目标 1—`setDefaults()` 方法 | 初始化默认值 |
| `config.yaml.example` | 目标 2—YAML 配置示例 | 字符串用双引号 `""` |
| `readme.md` | 目标 3—文档中的 YAML 示例 | 字符串用单引号 `''` |

## 工作流

### 第1步：检测变更

执行两条命令并行检测：

```bash
# 检测 struct.go 未提交的变更
cd /Users/nichuanfang/workspace/go/gymdl
git diff HEAD -- config/struct.go
git diff HEAD -- config/config.go
git diff HEAD -- config.yaml.example
git diff HEAD -- readme.md
```

同时**从头比对** `config/struct.go` 与 3 个目标文件，找出所有不一致之处（不仅是 git diff 涉及的），因为可能存在历史遗留的不同步问题。比对逻辑见第3步。

如果没有任何不一致，输出 `✅ 所有配置文件均已同步，无需操作。` 并结束。

### 第2步：解析源数据 (`config/struct.go`)

读取 `config/struct.go`，解析出以下信息结构。示例数据基于当前代码：

#### 2.1 `Config` 顶层结构体

遍历 `Config` 结构体的所有字段，每个字段包含：
- **字段名**（Go 名）
- **yaml tag**（配置段名）
- **子结构体类型**（`*XxxConfig`）
- **注释**（中文，用于 YAML 注释）

当前所有子配置：

| 字段名 | yaml tag | 结构体类型 | 注释 |
|--------|----------|-----------|------|
| WebConfig | `web_config` | `*WebConfig` | web配置 |
| CookieCloud | `cookie_cloud` | `*CookieCloudConfig` | cookiecloud配置 |
| Tidy | `tidy` | `*TidyConfig` | 资源整理配置 |
| WebDAV | `webdav` | `*WebDAVConfig` | webdav配置 |
| Log | `log` | `*LogConfig` | 日志配置 |
| Telegram | `telegram` | `*TelegramConfig` | telegram配置 |
| LrcAPI | `lrc_api` | `*LrcAPIConfig` | lrcapi配置 |
| AI | `ai` | `*AIConfig` | AI配置 |
| N8NConfig | `n8n_config` | `*N8NConfig` | n8n配置 |
| QQMusicApiConfig | `qq_music_api` | `*QQMusicApiConfig` | qq-music-api服务配置 |
| AdditionalConfig | `additional_config` | `*AdditionalConfig` | 附属配置 |
| ProxyConfig | `proxy` | `*ProxyConfig` | 代理配置 |

#### 2.2 每个子结构体的字段

遍历每个子结构体的所有字段，每个字段包含：
- **字段名**（Go 名）
- **类型**（`string`, `int`, `bool`, `[]string`, `[]PlaylistInfo` 等）
- **yaml tag**（YAML 键名）
- **注释**（中文，用于 YAML 行尾注释）

### 第3步：比对差异

对于每个目标文件，逐项比对：

#### 3.1 比对 `config/config.go` 的 `setDefaults()` 方法

`setDefaults()` 位于约第 100~194 行。每个子结构体对应一个初始块：

```go
if c.Xxx == nil {
    c.Xxx = &XxxConfig{
        Field1: defaultValue1,
        Field2: defaultValue2,
    }
}
```

比对逻辑：
- 遍历 `Config` 的所有 `*XxxConfig` 指针字段
- 检查 `setDefaults()` 中是否有对应的 nil 检查块
- **缺少则添加**（在第 2 步解析的所有子结构体之后追加，在 `if c.ProxyConfig == nil` 块之后、方法结尾 `}` 之前）
- **多余则删除**（`setDefaults()` 中有但 `Config` 中已不存在的字段）
- 已有块则检查其内部字段是否与子结构体定义一致
  - **新增字段**：在对应的 `setDefaults()` 默认值中补全（按字母或原有顺序追加到该结构体的初始化块中）
  - **移除字段**：从该结构体的初始化块中移除对应行
  - **类型变更**：调整默认值（如 `int`→`string` 则 `0` 改为 `""`）
- `PlaylistInfo` 和 `[]PlaylistInfo` 等辅助类型不直接出现在 `setDefaults()` 中，但在 `N8NConfig` 中可能会出现 `make([]PlaylistInfo, 0)` 之类的初始化

**默认值规则**（按类型）：

| 类型 | 默认值 |
|------|--------|
| `bool` | `false` |
| `string` | `""` |
| `int` | `0` |
| `[]string` | `make([]string, 0)` |
| `[]PlaylistInfo` | `make([]PlaylistInfo, 0)` |

例外：如果该字段有合理的非零默认值（如端口号 `8080`、模式 `1` 等），使用现有代码中的值，**不要**改为 `0` 或 `""`。

**保持** `setDefaults()` 现有注释和缩进风格：
```go
    if c.WebDAV == nil {
        c.WebDAV = &WebDAVConfig{
            WebDAVUrl:  "",
            WebDAVUser: "",
            WebDAVPass: "",
            WebDAVDir:  "",
            WebDavHost: "",
        }
    }
```

注意该块中使用的是 Tab 缩进（Go 标准），且字段之间有空行分割（实际代码中不一定有）。保持现有实际代码的格式。

**重要**：只修改 `setDefaults()` 方法内部（从 `func (c *Config) setDefaults() {` 到对应的 `}`），**不要**修改该方法的签名或文件中的其他函数。

#### 3.2 比对 `config.yaml.example`

这是一个完整的 YAML 配置文件，每个子结构体对应一个 YAML section：

```yaml
# Web 服务配置
web_config:
  enable: false  # 是否启用 web 服务
  app_domain: "localhost"  # web 服务域名
```

比对逻辑：
- 遍历 `Config` 的所有 `*XxxConfig` 指针字段，确保每个都有对应的 YAML section
- **缺少 section**：在文件末尾（或最后一个配置段之后、`# 代理配置` 之前按合理顺序）添加新的 section
- **多余 section**：删除（YAML 中有但 `Config` 中已不存在的 section）
- 每个 section 内部逐字段比对
  - **新增字段**：在 section 末尾添加新的 YAML 键值对
  - **移除字段**：删除对应行
  - **类型变更**：调整值格式

**格式规则（config.yaml.example）：**
- 2 空格缩进
- 字符串值用双引号 `""`
- 布尔值/数字不加引号
- 切片（如 `allowed_users`、`monitor_dirs`）用 `- ""` 列表格式
- `tidy_playlist` 的 `[]PlaylistInfo` 用：
  ```yaml
  tidy_playlist: #歌单,名称必须与navidrome中保持一致
    - name: 中文
      desc: 歌曲的主要演唱语言为中文(含普通话、粤语、闽南语等)
  ```
- 每行末尾的注释来自 Go 结构体的注释，加上 `# ` 前缀
- section 标题注释：`# xxx配置` 或 `# xxx 配置`
- 保持现有文件中每行注释的精确措辞风格，不要随意改写中文注释
- 保持现有文件中各 section 出现的**顺序**与 `Config` 结构体字段顺序一致
- 只修改 YAML body 区域，**不要**修改文件头（`# GYMDL 配置文件` 等）

#### 3.3 比对 `readme.md`

定位 `### 2️⃣ 配置文件 \`config.yaml\` 示例` 部分，找到 `<details>` 块内的 YAML 代码 fence：

````markdown
### 2️⃣ 配置文件 `config.yaml` 示例

<details>
<summary>点击展开 YAML 配置示例</summary>

```yaml
...
```

</details>
````

比对逻辑同 `config.yaml.example`，关键区别：

**格式规则（readme.md YAML）：**
- 2 空格缩进
- 字符串值用**单引号** `''`
- 布尔值/数字不加引号
- 切片用 `- ''` 列表格式
- 只修改 YAML 代码 fence（` ```yaml ` 和 ` ``` ` 之间的内容）
- **不要**修改文档的其他部分（标题、HTML 标签、fence 外的文本、提示等）

### 第4步：应用修改

使用 Edit 工具进行精确替换，每次只修改一个目标文件的一个部分。

**修改策略：**
- 对于 `config.go`，定位 `setDefaults()` 方法，逐块修改
- 对于 `config.yaml.example` 和 `readme.md`，定位 YAML 区域，逐 section 修改
- 每次修改前先 Read 目标文件确认当前内容
- 使用 Edit 工具，确保 `old_string` 精确匹配

### 第5步：验证

修改完成后执行以下验证：

```bash
cd /Users/nichuanfang/workspace/go/gymdl

# 1. Go 编译检查
go vet ./config/

# 2. YAML 解析检查
go run -e "package main; import (\"fmt\"; \"github.com/goccy/go-yaml\"); type Config struct { ... }" 2>/dev/null || echo "需手动验证 YAML"

# 3. 确认 4 个文件都已同步
git diff HEAD -- config/struct.go config/config.go config.yaml.example readme.md
```

**如果 Go vet 失败**：修复编译错误后再提交。
**如果 git diff 显示有未预期的变更**：回退并检查逻辑。

## 边界情况处理

| 场景 | 处理方式 |
|------|----------|
| struct.go 无变更 | 提示"✅ 无需同步"，结束 |
| 新增 `*XxxConfig` 字段 | 三目标文件都添加新配置段 |
| 删除 `*XxxConfig` 字段 | 从三目标文件移除对应段 |
| 新增单字段 | 在现有配置段末尾添加 |
| 删除单字段 | 从配置段移除对应行 |
| 字段重命名 | 旧名删除 + 新名添加 |
| 字段类型变更（如 int→string） | `0` → `""`（默认值） |
| 注释修改 | 同步 YAML 行内注释 |
| 目标文件不存在 | 提示手动创建后重试 |
| setDefaults() 被修改过（有未提交变更） | 提示手动确认后再执行 |

## 重要提醒

1. **只修改目标行，不碰无关代码** — 不要格式化或整理整个文件
2. **保持现有代码风格** — 缩进、空格、换行、注释位置都与原来一致
3. **不要修改 struct.go** — 它是源数据
4. **先比较再修改** — 先输出变更清单，确认后再执行修改
5. **每次修改一个小单元** — 避免大块替换导致 Edit 失败
6. **如果遇到复杂场景（如结构体大幅重构）**，输出变更建议清单让用户确认后再执行
