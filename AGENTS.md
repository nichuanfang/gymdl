# Gymdl Project Guide

## Workspace and change hygiene

- Work in the repository root or an isolated Git worktree. Inspect `git status`,
  the current branch, and relevant local instructions before editing.
- Do not commit `config.yaml`, runtime databases, logs, generated assets, or
  provider-failure dumps. Keep user changes outside the current task intact.
- Use the repository's existing logger and helpers instead of introducing a new
  logging library. Logs must omit passwords, tokens, cookies, and request URLs.
- Add new English code, comments, and documentation in English. User-facing text
  and recovery instructions may remain in Chinese where the feature already uses
  that language.

## Toolchain and structure

- Use Go 1.24.3 or newer. The application entry point is `main.go`; discovery
  integration lives in `internal/discovery` and is wired from `internal/bot`.
- The WebUI is in `webui`, builds into `web/dist`, and is embedded by `web`.
- Prefer existing configuration structures and validation. When a field changes
  in `config/struct.go`, synchronize defaults in `config/config.go`, the sample in
  `config.yaml.example`, and the YAML example in `readme.md`.

## Build and test

- Format all changed Go files with `gofmt -w`.
- For a focused change, run the affected package tests first. Use the worktree
  validation script for the full local build and test pass.
- Before a full server build or test run, ensure `web/dist` contains assets built
  from the current `webui` sources. For frontend changes, run
  `npm ci --prefix webui` when dependencies are missing, then
  `npm run build --prefix webui`.
- Run `go build ./...`, `go test ./...`, and `git diff --check` before delivering
  a code change. Report checks you could not run and the reason.

## Configuration and runtime data

- Treat `config.yaml` and files under `data/` as runtime state. Do not print their
  contents or include them in fixtures, output samples, or logs.
- Preserve existing defaults and backward compatibility unless the task changes
  that behavior explicitly.
- Do not enable real downloads, external sends, scheduled jobs, or account
  synchronization as part of an offline verification.

## Release and deployment

- Local code changes do not authorize commit, push, release, or deployment. Take
  those actions only when the request explicitly includes them.
- Follow `.codex/skills/gymdl-dev-release/SKILL.md` for validation, CI/CD, release,
  deployment, rollback, and task-scoped disk cleanup.
- Confirm the current host and target server before running deployment or cleanup
  commands. Never apply server-only cleanup paths to a local Mac.
