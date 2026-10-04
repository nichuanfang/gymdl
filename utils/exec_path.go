package utils

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	execPathOnce sync.Once
	cachedPath   string
)

// ExtendedPATH 返回扩展过的 PATH，包含常见的 yt-dlp 安装目录
// 解决 launchctl / systemd / Docker 等环境下 PATH 不完整导致找不到 yt-dlp 的问题
func ExtendedPATH() string {
	execPathOnce.Do(func() {
		home, _ := os.UserHomeDir()
		extra := []string{
			"/usr/local/bin",
			"/opt/homebrew/bin",
			"/opt/homebrew/sbin",
			filepath.Join(home, ".local/bin"),
			filepath.Join(home, "go/bin"),
		}
		if runtime.GOOS == "windows" {
			extra = []string{
				filepath.Join(home, "AppData\\Local\\Programs\\Python\\Scripts"),
				filepath.Join(home, "AppData\\Roaming\\Python\\Scripts"),
			}
		}
		current := os.Getenv("PATH")
		seen := make(map[string]bool)
		parts := make([]string, 0)
		for _, p := range append(extra, strings.Split(current, string(os.PathListSeparator))...) {
			if p != "" && !seen[p] {
				parts = append(parts, p)
				seen[p] = true
			}
		}
		cachedPath = strings.Join(parts, string(os.PathListSeparator))
	})
	return cachedPath
}

// Command 创建一个带扩展 PATH 的 exec.Cmd
// 如果 LookPath(name) 在默认 PATH 中找不到，会尝试在扩展 PATH 中查找并把 cmd.Path 设为绝对路径
func Command(name string, args ...string) *exec.Cmd {
	return commandWithContext(nil, name, args...)
}

// CommandContext creates a cancellable command with the same extended PATH behavior as Command.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return commandWithContext(ctx, name, args...)
}

func commandWithContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if ctx == nil {
		cmd = exec.Command(name, args...)
	} else {
		cmd = exec.CommandContext(ctx, name, args...)
	}
	cmd.Env = append(os.Environ(), "PATH="+ExtendedPATH())
	// 如果默认 LookPath 失败，手动在扩展 PATH 中查找
	if cmd.Err != nil || cmd.Path == "" || cmd.Path == name {
		for _, dir := range strings.Split(ExtendedPATH(), string(os.PathListSeparator)) {
			if dir == "" {
				continue
			}
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				cmd.Path = candidate
				cmd.Err = nil
				break
			}
		}
	}
	return cmd
}

// LookPath 在扩展 PATH 中查找可执行文件
func LookPath(name string) (string, error) {
	return exec.LookPath(name)
}
