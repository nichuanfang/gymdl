package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

// PlaylistFileName is the sidecar document that holds the n8n tidy playlist.
// It lives next to the main configuration file and is optional for compatibility
// with deployments that still keep tidy_playlist inline.
const PlaylistFileName = "config.playlists.yaml"

// PlaylistFile is the on-disk shape of the playlist sidecar.
type PlaylistFile struct {
	TidyPlaylist []PlaylistInfo `yaml:"tidy_playlist"`
}

// PlaylistFilePath returns the sidecar path that belongs to configPath.
func PlaylistFilePath(configPath string) string {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return PlaylistFileName
	}
	return filepath.Join(filepath.Dir(configPath), PlaylistFileName)
}

// LoadPlaylistFile reads the sidecar document. present reports whether the
// document explicitly contains tidy_playlist; an explicit empty list therefore
// overrides an inline list instead of falling back to it.
func LoadPlaylistFile(path string) (entries []PlaylistInfo, present bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if info.IsDir() {
		return nil, false, fmt.Errorf("歌单配置文件 %s 是目录；请挂载文件而不是目录", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, false, nil
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("解析歌单配置文件 %s 失败: %w", path, err)
	}
	if doc == nil {
		return nil, false, nil
	}
	if _, ok := doc["tidy_playlist"]; !ok {
		return nil, false, nil
	}

	var file PlaylistFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, false, fmt.Errorf("解析歌单配置文件 %s 失败: %w", path, err)
	}
	if file.TidyPlaylist == nil {
		file.TidyPlaylist = []PlaylistInfo{}
	}
	return file.TidyPlaylist, true, nil
}

// ApplyPlaylistFile overlays the sidecar onto an already defaulted config.
// It leaves the config untouched when the sidecar is absent, empty, or invalid.
func ApplyPlaylistFile(c *Config, configPath string) error {
	if c == nil {
		return nil
	}
	entries, present, err := LoadPlaylistFile(PlaylistFilePath(configPath))
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	if c.N8NConfig == nil {
		c.N8NConfig = &N8NConfig{TidyPlaylist: []PlaylistInfo{}}
	}
	c.N8NConfig.TidyPlaylist = append([]PlaylistInfo(nil), entries...)
	return nil
}

// WritePlaylistFile atomically writes the sidecar with restrictive permissions
// and keeps the previous revision in <path>.bak.
func WritePlaylistFile(path string, entries []PlaylistInfo) error {
	if entries == nil {
		entries = []PlaylistInfo{}
	}
	data, err := yaml.Marshal(PlaylistFile{TidyPlaylist: entries})
	if err != nil {
		return fmt.Errorf("序列化歌单配置失败: %w", err)
	}
	header := []byte("# GYMDL 歌单配置\n# 由 WebUI 或手工维护；修改后需重启服务才会应用到 Bot。\n\n")
	return writePlaylistBytes(path, append(header, data...))
}

// RestorePlaylistFile atomically restores raw sidecar bytes after a failed
// multi-file save.
func RestorePlaylistFile(path string, data []byte) error {
	return writePlaylistBytes(path, data)
}

func writePlaylistBytes(path string, data []byte) error {
	return writePlaylistBytesWithBackup(path, data, true)
}

func writePlaylistBytesWithBackup(path string, data []byte, backup bool) error {
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("歌单配置文件 %s 是目录；请挂载文件而不是目录", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		if mode&0o077 != 0 {
			mode &= 0o600
		}
	}
	if old, err := os.ReadFile(path); err == nil && backup {
		if err := os.WriteFile(path+".bak", old, mode); err != nil {
			return fmt.Errorf("创建歌单配置备份失败: %w", err)
		}
		if err := os.Chmod(path+".bak", mode); err != nil {
			return fmt.Errorf("保护歌单配置备份失败: %w", err)
		}
	}

	tmp, err := os.CreateTemp(dir, ".gymdl-playlists-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		// A single-file bind mount cannot be replaced by rename (EBUSY). Keep
		// the .bak from above and rewrite the mounted file in place instead.
		if inPlaceErr := writePlaylistInPlace(path, data, mode); inPlaceErr != nil {
			return fmt.Errorf("替换歌单配置失败: %v；原地写入失败: %v", err, inPlaceErr)
		}
	}
	return nil
}

func writePlaylistInPlace(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
