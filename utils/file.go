package utils

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// IsDir 判断是否为目录
func IsDir(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// DirExist 判断目录是否存在
func DirExist(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// IsFile 判断是否为文件
func IsFile(file string) bool {
	info, err := os.Stat(file)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// FileExist 文件是否存在
func FileExist(file string) bool {
	if _, err := os.Stat(file); err == nil {
		return true
	}
	return false
}

// CopyFile 拷贝文件
func CopyFile(dstName, srcName string) (written int64, err error) {
	src, err := os.Open(srcName) // 这里修正，应该先打开源文件
	if err != nil {
		fmt.Println(err)
		return
	}
	defer src.Close()

	// 创建目标目录（如果不存在）
	if err := os.MkdirAll(filepath.Dir(dstName), os.ModePerm); err != nil {
		return 0, err
	}

	dst, err := os.OpenFile(dstName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer dst.Close()

	return io.Copy(dst, src)
}

// SanitizeFileName 处理非法字符，确保跨平台兼容性
func SanitizeFileName(name string) string {
	invalidChars := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	for _, c := range invalidChars {
		name = strings.ReplaceAll(name, c, "_")
	}
	return name
}

// contains 判断 slice 是否包含元素
func Contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// MoveFile移动文件
func MoveFile(srcPath, destPath string) error {
	// 1. 尝试使用 os.Rename（原子操作，高效）
	err := os.Rename(srcPath, destPath)
	if err == nil {
		return nil // 移动成功
	}

	// 检查目标目录是否存在。如果不存在，尝试创建它。
	if errors.Is(err, os.ErrInvalid) || strings.Contains(err.Error(), "invalid cross-device link") {
		// os.Rename 失败，继续回退到复制-删除逻辑 (跨文件系统)
	} else if os.IsNotExist(err) {
		// 如果错误是目标路径不存在，先尝试创建目标路径的父目录
		destDir := filepath.Dir(destPath)
		if mkdirErr := os.MkdirAll(destDir, 0755); mkdirErr != nil {
			// 如果创建目录失败，直接返回目录创建错误
			return fmt.Errorf("移动文件失败: 目标目录创建失败: %w", mkdirErr)
		}

		// 尝试再次使用 os.Rename
		err = os.Rename(srcPath, destPath)
		if err == nil {
			return nil // 再次尝试移动成功
		}
	}

	// 2. 如果 os.Rename 失败（通常是跨文件系统），执行复制-删除（Copy-and-Delete）

	// a. 复制文件
	if err := copyFile(srcPath, destPath); err != nil {
		return fmt.Errorf("移动文件失败: 复制文件时发生错误: %w", err)
	}

	// b. 删除原始文件
	if err := os.Remove(srcPath); err != nil {
		// 复制成功但删除源文件失败。为了保持“移动”的语义，
		// 我们应该清理目标文件并返回错误。
		_ = os.Remove(destPath) // 尝试清理目标文件，忽略清理时的错误
		return fmt.Errorf("移动文件失败: 成功复制但无法删除源文件 %s (目标文件已回滚/删除): %w", srcPath, err)
	}

	return nil
}

// copyFile 执行文件复制
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		// 复制过程中发生错误时，确保目标文件被清理
		if err != nil {
			os.Remove(dst)
		}
		out.Close()
	}()

	// 尝试获取源文件信息
	si, err := os.Stat(src)
	if err != nil {
		return err // 无法获取源文件信息，则无法复制
	}

	// 使用缓冲区进行复制 (例如 64KB)
	buffer := make([]byte, 64*1024)
	_, err = io.CopyBuffer(out, in, buffer)
	if err != nil {
		return err
	}

	// 确保所有数据都写入磁盘
	if err := out.Sync(); err != nil {
		return err
	}

	// 尝试保留源文件的权限
	if err := os.Chmod(dst, si.Mode()); err != nil {
		// 记录错误，但不返回，因为文件内容已复制成功
		// logger.Warnf("无法设置目标文件权限: %v", err)
	}

	// 尝试保留源文件的修改时间 (重要的元数据)
	if err := os.Chtimes(dst, si.ModTime(), si.ModTime()); err != nil {
		// 记录错误，但不返回
		// logger.Warnf("无法设置目标文件修改时间: %v", err)
	}

	return nil
}

// 过滤音乐文件
func FilterMusicFile(file os.DirEntry, encryptedExts []string, decryptedExts []string) bool {
	if file.IsDir() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(file.Name()))
	for _, v := range decryptedExts {
		if ext == v {
			return true
		}
	}
	for _, v := range encryptedExts {
		if ext == v {
			return true
		}
	}
	return false
}

// TruncateString 将字符串安全截断到指定长度。
// 若字符串长度超过 limit，则截断并在末尾追加 "..."。
func TruncateString(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit > 3 {
		return string(runes[:limit-3]) + "..."
	}
	return string(runes[:limit])
}

// ClearTempDirs 清理临时目录
func ClearTempDirs(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			dirPath := filepath.Join(root, entry.Name())
			err := os.RemoveAll(dirPath)
			if err != nil {
				return fmt.Errorf("failed to remove %s: %w", dirPath, err)
			}
		}
	}

	return nil
}

// downloadFile 下载网络文件到本地
func DownloadFile(url, filepath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// MakeSafeFileName 去掉非法字符，只保留字母、数字、空格和常见符号
func MakeSafeFileName(name string) string {
	invalidChars := regexp.MustCompile(`[<>:"/\\|?*]`)
	cleaned := invalidChars.ReplaceAllString(name, "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		cleaned = "unknown"
	}
	return cleaned
}
