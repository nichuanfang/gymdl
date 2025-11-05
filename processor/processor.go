package processor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"

	"github.com/nichuanfang/gymdl/utils"
)

// 顶级接口定义

type Processor interface {
	//构造方法
	Init(cfg *config.Config)
	//处理器名称
	Name() LinkType
}

/* ---------------------- 常量 ---------------------- */

// LinkType 是所有解析出来的类型枚举
type LinkType string

const (
	/* ------------------------ 音乐平台枚举 ---------------------- */
	LinkUnknown      LinkType = ""
	LinkAppleMusic   LinkType = "AppleMusic"
	LinkNetEase      LinkType = "网易云音乐"
	LinkQQMusic      LinkType = "QQ音乐"
	LinkSoundcloud   LinkType = "Soundcloud"
	LinkSpotify      LinkType = "Spotify"
	LinkYoutubeMusic LinkType = "YoutubeMusic"
    LinkForwardMusic LinkType = "ForwardMusic"

	/* -------------------------视频平台枚举 ---------------------- */

	LinkBilibili    LinkType = "B站"
	LinkDouyin      LinkType = "抖音"
	LinkXiaohongshu LinkType = "小红书"
	LinkYoutube     LinkType = "Youtube"
)

/* ---------------------- 通用业务工具 ---------------------- */

// BuildOutputDir 构建输出目录
// 规则: baseTempDir + 时间戳（例如：temp/20251030153045）
func BuildOutputDir(baseTempDir string) string {
	// 1. 获取当前时间戳（格式：YYYYMMDDHHMMSS）
	timestamp := time.Now().Format("20060102150405")
	// 2. 构建输出目录路径
	return filepath.Join(baseTempDir, timestamp)
}

// CreateOutputDir 创建临时目录
func CreateOutputDir(outputDir string) error {
	// 判断目录是否存在
	if _, err := os.Stat(outputDir); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		// 其他错误
		return fmt.Errorf("检查目录失败: %v", err)
	}

	// 目录不存在，创建
	if err := os.MkdirAll(outputDir, os.ModePerm); err != nil {
		return fmt.Errorf("创建目录失败: %v", err)
	}
	utils.DebugWithFormat("🧹 已创建临时目录: %s\n", outputDir)
	return nil
}

// RemoveTempDir 用于清理临时目录
func RemoveTempDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("目录路径为空")
	}

	// 判断目录是否存在
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("检查目录失败: %v", err)
	}

	// 删除整个目录树
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("删除目录失败: %v", err)
	}
	utils.DebugWithFormat("🧹 已删除临时目录: %s\n", dir)
	return nil
}

// DetermineTidyType 获取整理类型
func DetermineTidyType(cfg *config.Config) string {
	return map[int]string{1: "LOCAL", 2: "WEBDAV"}[cfg.Tidy.Mode]
}

// copyFile 使用缓冲区复制文件，确保文件句柄关闭
func copyFile(src, dst string) error {
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close() // 手动关闭源文件

	output, err := os.Create(dst)
	if err != nil {
		return err
	}

	// 使用 1MB 缓冲区
	buf := make([]byte, 1024*1024)
	_, err = io.CopyBuffer(output, input, buf)

	// 立即关闭目标文件
	if closeErr := output.Close(); closeErr != nil {
		utils.WarnWithFormat("[Um] ⚠️ 关闭目标文件失败: %v", closeErr)
	}

	return err
}

// ToLocal 整理到本地目录
func ToLocal(src string, dst string) error {
	if sameDrive(src, dst) {
		// 同盘直接重命名
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	} else {
		// 跨盘复制 + 删除
		if err := copyFile(src, dst); err != nil {
			return err
		}

		// 删除源文件
		if err := os.Remove(src); err != nil {
			return err
		}
	}
	return nil
}

// sameDrive 判断两个路径是否在同一个磁盘或 UNC 网络共享
func sameDrive(path1, path2 string) bool {
	abs1, err1 := filepath.Abs(path1)
	abs2, err2 := filepath.Abs(path2)
	if err1 != nil || err2 != nil {
		return false
	}

	// 本地盘符比较 (C:, D:...)
	if len(abs1) >= 2 && len(abs2) >= 2 && abs1[1] == ':' && abs2[1] == ':' {
		return strings.EqualFold(abs1[:2], abs2[:2])
	}

	// UNC 网络路径比较 (\\NAS\share)
	if strings.HasPrefix(abs1, `\\`) && strings.HasPrefix(abs2, `\\`) {
		parts1 := strings.SplitN(abs1, `\`, 4)
		parts2 := strings.SplitN(abs2, `\`, 4)
		if len(parts1) >= 3 && len(parts2) >= 3 {
			return strings.EqualFold(parts1[1], parts2[1]) && strings.EqualFold(parts1[2], parts2[2])
		}
	}

	return false
}
