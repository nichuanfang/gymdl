package config

import (
	"fmt"
	"os"
)

// EnsurePlaylistFile creates a mountable placeholder when the sidecar is absent.
// Docker bind-mounting a missing host file may create a directory instead, and a
// placeholder also makes the first WebUI save persist to the host rather than the
// container layer. An existing sidecar is never rewritten at startup.
func EnsurePlaylistFile(configPath string) {
	path := PlaylistFilePath(configPath)
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			fmt.Println("⚠️ 歌单配置文件是目录，请改为文件挂载:", path)
		}
		return
	}
	if !os.IsNotExist(err) {
		fmt.Println("⚠️ 检查歌单配置文件失败:", err)
		return
	}
	// A comment-only placeholder deliberately omits tidy_playlist: its presence
	// keeps Docker's single-file mount persistent while leaving the inline list
	// in config.yaml authoritative until the first WebUI save migrates it.
	placeholder := []byte("# GYMDL 歌单配置\n# 该文件将在首次从 WebUI 保存设置时迁移写入。\n")
	// Do not create a .bak for a placeholder that never existed on the host.
	if err := writePlaylistBytesWithBackup(path, placeholder, false); err != nil {
		fmt.Println("⚠️ 创建歌单配置文件失败:", err)
	}
}
