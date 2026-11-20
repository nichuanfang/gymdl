# Gymdl CI/CD、上线与回滚

## 执行环境边界

本参考中的 VPS Compose 查询、备份、原地重建和线上验收，适用于 Codex 直接运行在 gymdl 部署机并已确认当前 Docker context 指向目标服务器的情况。Mac/Windows 开发机上的本地 Docker Desktop 和 Compose 只代表本机环境，不能用来判断线上版本，也不能执行部署机专用运维操作。

在开发机完成代码验证后，只有获得提交/推送/发版/部署授权时才推进对应阶段。应用发布使用本仓库的 GitHub Actions 标签链路；VPS 更新通过已配置的远端部署方式完成，并在目标服务器核验容器、接口和 Bot。若当前会话无法访问目标服务器，报告 CI 与镜像结果，明确部署尚未完成；不得把本地构建或 Compose 成功描述为线上成功。执行环境或 Docker context 不明确时，不部署。

## Mac 开发机发版链路

用户已确认的 Mac 发版主流程是：

1. 在 gymdl 应用仓库本地修改并完成对应验证。
2. 提交并推送应用仓库；具体默认分支、远端和提交风格以现场仓库为准。
3. 在 GitHub 为这次提交创建版本 Release/标签（当前工作流匹配 `v*.*.*`），由标签触发的 `Build&Publish` 构建并推送 `ghcr.io/nichuanfang/gymdl:<tag>` 和 `latest`，随后生成安装包与 GitHub Release。
4. 在部署仓库修改 `gymdl` 服务 Compose 中的镜像版本，例如 `ghcr.io/nichuanfang/gymdl:vX.Y.Z`。只改本次 gymdl 服务需要的版本，不带入无关服务或本地改动。
5. 提交并推送部署仓库，触发该仓库现有的部署链路；若没有适用的自动部署工作流，按现场已确认方式更新目标服务器的 gymdl 容器。
6. 在目标服务器核对容器实际使用的镜像标签/ID、启动日志、HTTP/WebUI 和 Bot 状态；镜像已推送不等于容器已更新。

这条链路的关键边界：

- 应用仓库负责代码、标签、镜像和 GitHub Release；部署仓库负责“线上运行哪个 gymdl 版本”，两者是不同提交，不能只推其中一个就报告发版完成。
- GitHub Release/标签触发的 Actions 与部署仓库 push 触发的部署要分别核验。应用工作流成功但部署仓库未推送，或部署仓库已推送但容器仍跑旧镜像，都不能算完成。
- 部署仓库路径和触发方式要现场发现。已知用户常在 `~/code/docker` 维护部署仓库，但该路径不保证存在于每台 Mac；本机可能位于其他工作区。先确认 Git 仓库、默认分支、Compose 文件、服务名和 push 后的实际路由。
- 修改版本前先查看部署仓库 `git status`、目标 Compose 差异和当前镜像；保护用户未提交改动。不要为了“干净工作树” stash、reset 或把其他服务改动一起提交。
- 若部署仓库有多个服务或路径触发其他栈，先确认本次只影响 gymdl；不要为了更新版本顺带部署无关服务。
- 服务器更新优先使用现有 Compose 流程：校验配置、拉取目标镜像、原地重建 gymdl 服务、等待可用并核验；不执行影响其他服务的 `down`。
- 发版前记录回退镜像标签/ID 和必要的持久数据备份。容器回滚不会自动回滚 SQLite 迁移，数据库兼容性单独判断。
- 纯技能/文档修改默认不创建应用版本标签，也不走镜像发版链路；只有用户明确要求发布时执行。

部署仓库示意操作（路径和文件以现场为准）：

```bash
cd <deployment-repo>
git status --short
# 只替换目标 gymdl 服务的 image 版本，审查完整 diff
git diff -- dockerfile_work/gymdl/docker-compose.yml
git add dockerfile_work/gymdl/docker-compose.yml
git commit -m "chore(gymdl): deploy vX.Y.Z"
git push
```

服务器端示意（先确认真实 Compose 文件、项目名和服务名）：

```bash
docker compose -f <gymdl-compose-file> config -q
docker compose -f <gymdl-compose-file> pull gymdl
docker compose -f <gymdl-compose-file> up -d --wait --wait-timeout 240 gymdl
docker inspect --format '{{.Config.Image}} {{.Image}} {{.State.Status}} {{.RestartCount}}' gymdl
```

若部署仓库 push 会自动触发服务器更新，仍要在目标服务器核验实际镜像和运行结果；若没有自动触发，使用当前环境中已经确认的远端部署方式执行，不能把本地 Docker Desktop 操作当线上更新。

## 两条发布链路

| 变更/动作 | 实际链路 | 需要核对 |
| --- | --- | --- |
| gymdl 应用代码 | 应用仓库版本标签 → `.github/workflows/release.yml` 的 `Build&Publish` | 标签对应提交、Actions run、镜像与安装包结果 |
| Docker 镜像 | `docker` job 构建并推送 `ghcr.io/nichuanfang/gymdl:<tag>` 和 `latest` | 新版本 manifest/digest，镜像内实际运行依赖 |
| 独立安装包 | `build` job 依赖 `docker`，执行 `make release` 并创建 GitHub Release | 前端构建、多平台产物、整个 run 结论 |
| VPS 应用部署 | 运行栈的 Compose 镜像引用 → 拉取 → 原地重建 → 运行核验 | 当前容器镜像 ID、接口、Bot、任务状态 |
| 基础设施仓库变更 | `nichuanfang/docker` 默认部署分支 push → n8n `GithubAction` 按路径路由 | 其他服务/栈是否也会被触发，部署仓库本地改动 |
| 一次性允许范围内的应用部署 | 已配置的 n8n 运维部署工具，或现有快照式 Compose 部署流程 | 当前用户的上线授权、实际执行能力、备份与最终运行结果 |

工作流、默认分支、工具、标签和 Compose 路径以现场源码/远端及 Docker 查询为准；不要沿用某次 run ID、工作流 ID、固定版本或历史容器名推断成功。

**镜像发布不等于 VPS 更新。** 运行栈通常固定版本；仅推送新标签或 `latest` 不会自动改掉 Compose 的旧版本。Docker job 成功后可部署已发布镜像，独立包仍在构建时继续跟踪，最终报告整个发布 run 的结论。

**推送部署仓库可能同时部署多个服务。** n8n 路由的兜底路径可能重置 VPS checkout。因此不能为了同步 gymdl 的镜像号顺便推一个含本地修改的部署仓库；先看其 `AGENTS.md`、工作区和真实路由。

## 发版前

1. 复核当前任务已授权上线、工作区差异和当前远端默认分支。保留原有用户改动，避免把其他任务带进发布。
2. 检查远端已有标签，选未占用的新版本；在目标提交上打标签。已发布标签不覆盖或强推，修复用新提交/新版本。
3. 代码的测试/构建证据与目标提交一致。Dockerfile 的前端构建、Go embed、运行工具安装及 CLI 复制均要纳入打包核验；程序依赖在运行镜像中实际可执行。
4. 根据用户授权提交，并按应用仓库约定推送默认分支和版本标签。适用时用 atomic push，避免标签与发布提交不一致。仅改技能/文档不另打应用发布标签。
5. 跟踪目标提交的 Actions，不把其他分支或旧 run 的绿色结果当成本次成功。GitHub 插件缺少所需查询时可用 GitHub API；认证信息从已有安全配置读取，不放命令参数、URL 输出或日志中。

## 更新 VPS

先发现真实 Compose：

```bash
docker compose ls --all --format json
docker inspect --format '{{.Config.Image}} {{.Image}} {{.RestartCount}}' gymdl
```

部署前保存原镜像引用、精确 ID、目标 Compose/配置与需要保护的持久数据快照。配置和数据库备份放权限受限的状态目录，报告路径，不展示内容。在线 SQLite 使用一致性 backup，不能只复制仍在写入且使用 WAL 的主文件。

- 优先按可用的 `docker-stack-deploy` 技能运行既有部署脚本，先 dry run，再应用；不执行 `down`，保留旧镜像。
- 使用 n8n 时先发现当前部署工具及输入，按当前用户的上线授权填写确认字段。某些子流程只有 Execute Workflow Trigger，通用 MCP 直接执行可能不支持；拒绝调用、无 execution ID 或仅返回工作流定义均不是已部署。此时用已有快照式部署能力，不为一次上线重写 n8n 流程。
- 仅将本次目标服务的镜像引用改为新版本，验证 Compose 再拉取、原地重建。不要改变其他服务或发布端口。
- 部署仓库有本地版本号修改时先审查完整状态与目标文件差异、备份原文件。不要为了通过“干净工作树”检查去提交其他人的修改、stash 或 reset。
- 如果现有脚本拒绝已审查的本地变更，先查看是否支持限定范围的选项；不能全局关闭安全检查。必要时按相同的备份、配置校验、pull、`up -d --wait`、核验与回滚流程操作本次目标服务，并保留日志与已审查差异。遇到未理解的目标文件变化，先确认含义，再执行依赖它的变更。

应用示意（先填写经查询确认的文件和服务名）：

```bash
docker compose -f "$compose_file" config -q
docker compose -f "$compose_file" pull "$service"
docker compose -f "$compose_file" up -d --wait --wait-timeout 240 "$service"
```

## 线上验收

- 记录实际容器标签与镜像 ID、运行状态、RestartCount、OOM；没有 Docker healthcheck 时以实际服务请求判断可用，不能把 running 称为健康检查通过。
- 检查实际启用的 HTTP 接口/站点，必要时通过公网验证 xray/nginx/容器链路；仅监听端口不等于服务可用。
- Telegram 变更读取 `getMyCommands` 核对新指令，按实际模式验证 webhook 监听或 polling 启动，并结合离线路由测试。菜单出现不等于已经实测用户点击全链路；不要擅自给用户发测试消息。
- 新后台功能检查配置开关、数据库初始化和任务数，保持当前用户选择；上线不自动开启收藏同步或导入历史收藏。
- 只读平台检查与真实下载分别报告。日志按需过滤；安全保存详细日志，避免输出凭据。源码、发布产物、依赖和线上环境各自的失败分别定位。

## 失败与交付

镜像尚未发布成功时保留线上原版本。重建失败或必要验收失败时按快照恢复精确镜像和部署配置；数据库是否兼容旧版要单独判断，镜像回滚不撤销数据迁移，数据库回滚遵循用户的确认要求。

报告部署的版本/提交、Actions run 结论与链接、容器和接口/Bot 证据、自动功能开关、备份/回滚路径及真实未完成项。记录到任务状态目录后，清理本任务测试产物，保留运行与回滚资源。
