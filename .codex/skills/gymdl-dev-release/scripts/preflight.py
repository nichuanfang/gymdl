#!/usr/bin/env python3
"""Report gymdl workspace, toolchain, resources and optional Docker state.

Read-only: no fetch/install/build, worktree creation, state-file writes or cleanup.
"""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys


def run(args, cwd=None, timeout=15):
    env = os.environ.copy()
    env.update(GOTOOLCHAIN="local", GIT_OPTIONAL_LOCKS="0")
    try:
        result = subprocess.run(
            args, cwd=cwd, env=env, text=True, capture_output=True, timeout=timeout
        )
    except (OSError, subprocess.TimeoutExpired):
        return None, ""
    # Raw stderr may include authenticated URLs; report only structured status.
    return result.returncode, result.stdout.strip()


def git(repo, *args):
    return run(["git", "-C", str(repo), *args])


def version(command, pattern):
    code, output = run(command, timeout=10)
    match = re.search(pattern, output) if code == 0 else None
    return match.group(0) if match else None


def memory_mb():
    try:
        entries = Path("/proc/meminfo").read_text().splitlines()
        values = {line.split(":", 1)[0]: int(line.split()[1]) for line in entries}
        return values.get("MemAvailable", 0) // 1024
    except (OSError, ValueError, IndexError):
        return None


def size_kib(path):
    if not shutil.which("du") or not Path(path).exists():
        return None
    code, output = run(["du", "-sk", "--", str(path)], timeout=30)
    try:
        return int(output.split()[0]) if code == 0 else None
    except (ValueError, IndexError):
        return None


def worktrees(repo, with_sizes):
    code, output = git(repo, "worktree", "list", "--porcelain")
    if code != 0:
        return None
    items = []
    for block in output.split("\n\n"):
        item = {}
        for line in block.splitlines():
            key, _, value = line.partition(" ")
            if key == "worktree":
                item["path"] = value
            elif key == "HEAD":
                item["head"] = value
            elif key == "branch":
                item["branch"] = value.removeprefix("refs/heads/")
            elif key in ("detached", "locked", "prunable", "bare"):
                item[key] = True
        if "path" not in item:
            continue
        code, status = git(item["path"], "status", "--short", "--untracked-files=normal")
        item["changes"] = status.splitlines() if code == 0 else None
        if with_sizes:
            item["size_kib"] = size_kib(item["path"])
        items.append(item)
    return items


def source_requirements(repo):
    result = {}
    try:
        text = (repo / "go.mod").read_text()
        match = re.search(r"^go\s+(\S+)", text, re.MULTILINE)
        result["go_mod_version"] = match.group(1) if match else None
    except OSError:
        pass
    try:
        text = (repo / "Dockerfile").read_text()
        match = re.search(
            r"^FROM\s+(\S+)\s+AS\s+webui-builder\s*$", text, re.MULTILINE | re.IGNORECASE
        )
        result["webui_builder_image"] = match.group(1) if match else None
    except OSError:
        pass
    try:
        lock = json.loads((repo / "webui/package-lock.json").read_text())
        result["vite_node_engines"] = (
            lock.get("packages", {}).get("node_modules/vite", {}).get("engines", {}).get("node")
        )
    except (OSError, ValueError):
        pass
    return result


def docker_state():
    if not shutil.which("docker"):
        return {"available": False}
    report = {"available": True}
    code, output = run(
        ["docker", "ps", "--filter", "name=^/gymdl$", "--format", "{{.ID}}\t{{.Image}}\t{{.Status}}\t{{.Names}}"]
    )
    report["query_succeeded"] = code == 0
    report["containers"] = [] if code == 0 else None
    if code == 0:
        for line in output.splitlines():
            fields = line.split("\t", 3)
            if len(fields) == 4:
                report["containers"].append(dict(zip(("id", "image", "status", "name"), fields)))
    code, output = run(["docker", "compose", "ls", "--all", "--format", "json"])
    try:
        stacks = json.loads(output) if code == 0 else None
        report["gymdl_stack"] = (
            [
                {"name": item.get("Name"), "config_files": item.get("ConfigFiles")}
                for item in stacks if item.get("Name") == "gymdl"
            ] if isinstance(stacks, list) else None
        )
    except ValueError:
        report["gymdl_stack"] = None
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path.cwd(), help="Gymdl checkout/worktree")
    parser.add_argument("--task", help="Task slug; reports the suggested state path without creating it")
    parser.add_argument("--docker", action="store_true", help="Read runtime container and Compose mapping")
    parser.add_argument("--sizes", action="store_true", help="Measure worktrees and shared Go caches")
    args = parser.parse_args()
    if args.task and not re.fullmatch(r"[a-z0-9][a-z0-9._-]{0,47}", args.task):
        parser.error("task must be a safe lowercase slug of at most 48 characters")
    repo = args.repo.expanduser().resolve()
    code, root = git(repo, "rev-parse", "--show-toplevel")
    if code != 0:
        parser.error("repo must be an existing Git working tree")
    repo = Path(root)
    _, head = git(repo, "rev-parse", "--verify", "HEAD")
    _, branch = git(repo, "symbolic-ref", "--quiet", "--short", "HEAD")
    usage = shutil.disk_usage(repo)
    tools = {name: shutil.which(name) for name in ("git", "go", "node", "npm", "docker", "rsync", "shellcheck", "shfmt")}
    report = {
        "read_only": True,
        "repo": str(repo),
        "head": head or None,
        "branch": branch or None,
        "worktrees": worktrees(repo, args.sizes),
        "resources": {"disk_free_mib": usage.free // (1024 * 1024), "memory_available_mib": memory_mb()},
        "tools": tools,
        "versions": {
            "python": sys.version.split()[0],
            "go": version(["go", "version"], r"go version go\S+"),
            "node": version(["node", "--version"], r"v\d+\.\d+\.\d+"),
            "npm": version(["npm", "--version"], r"\b\d+\.\d+\.\d+\b"),
        },
        "source_requirements": source_requirements(repo),
    }
    if tools["go"]:
        code, output = run(["go", "env", "GOMODCACHE", "GOCACHE"], cwd=repo)
        if code == 0 and len(output.splitlines()) == 2:
            report["shared_go_caches"] = dict(zip(("gomodcache", "gocache"), output.splitlines()))
            if args.sizes:
                report["shared_go_cache_sizes_kib"] = {
                    key: size_kib(path) for key, path in report["shared_go_caches"].items()
                }
    if args.task:
        state = Path.home() / ".local/state/gymdl-dev-release" / args.task
        report["task_state"] = {"path": str(state), "exists": state.exists()}
    if args.docker:
        report["docker"] = docker_state()
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
