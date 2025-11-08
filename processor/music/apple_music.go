package music

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
)

/* ---------------------- 结构体与构造方法 ---------------------- */

type AppleMusicProcessor struct {
	cfg     *config.Config
	tempDir string
	songs   []*SongInfo
}

// Init  初始化
func (am *AppleMusicProcessor) Init(cfg *config.Config) {
	am.songs = make([]*SongInfo, 0)
	am.cfg = cfg
	am.tempDir = processor.BuildOutputDir(AppleMusicTempDir)
}

/* ---------------------- 基础接口实现 ---------------------- */

func (am *AppleMusicProcessor) Name() processor.LinkType {
	return processor.LinkAppleMusic
}

func (am *AppleMusicProcessor) Songs() []*SongInfo {
	return am.songs
}

/* ------------------------ 下载逻辑 ------------------------ */

func (am *AppleMusicProcessor) DownloadMusic(url string, callback func(string)) error {
	start := time.Now()

	utils.InfoWithFormat("[AppleMusic] 🎵 开始下载: %s", url)

	cmd := am.DownloadCommand(url)
	utils.DebugWithFormat("[AppleMusic] 执行命令: %s", strings.Join(cmd.Args, " "))

	// 创建临时目录
	if err := processor.CreateOutputDir(am.tempDir); err != nil {
		utils.ErrorWithFormat("[AppleMusic] ❌ 创建临时目录失败: %v", err)
		return err
	}

	// 执行下载
	output, err := cmd.CombinedOutput()
	logOut := strings.TrimSpace(string(output))
	if err != nil {
		_ = processor.RemoveTempDir(am.tempDir)
		utils.ErrorWithFormat("[AppleMusic] ❌ 下载失败: %v\n输出:\n%s", err, logOut)
		return fmt.Errorf("gamdl 下载失败: %w", err)
	}

	// 输出调试信息，仅当有日志内容时
	if logOut != "" {
		utils.DebugWithFormat("[AppleMusic] 下载输出:\n%s", logOut)
	}

	utils.InfoWithFormat("[AppleMusic] ✅ 下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond))
	callback(fmt.Sprintf("下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond)))
	return nil
}

func (am *AppleMusicProcessor) DownloadCommand(url string) *exec.Cmd {
	cookiePath := filepath.Join(am.cfg.CookieCloud.CookieFilePath, am.cfg.CookieCloud.CookieFile)
	// https://github.com/glomatico/gamdl/commit/fdab6481ea246c2cf3415565c39da62a3b9dbd52 部分options改动
	rootDir := filepath.Dir(am.tempDir)
	baseDir := filepath.Base(am.tempDir)
	args := []string{
		"--cookies-path", cookiePath,
		"--download-mode", "nm3u8dlre",
		"--output-path", rootDir,
		"--temp-path", rootDir,
		"--album-folder-template", baseDir,
		"--compilation-folder-template", baseDir,
		"--no-album-folder-template", baseDir,
		"--single-disc-file-template", "{title}",
		"--multi-disc-file-template", "{title}",
		"--no-synced-lyrics",
		url,
	}
	return exec.Command("gamdl", args...)
}

func (am *AppleMusicProcessor) BeforeTidy() error {
	songs, err := ReadMusicDir(am.tempDir, processor.DetermineTidyType(am.cfg), am)
	if err != nil {
		return err
	}
	// 更新元信息列表
	am.songs = songs
	return nil
}

func (am *AppleMusicProcessor) NeedRemoveDRM() bool {
	return false
}

func (am *AppleMusicProcessor) DRMRemove() error {
	return nil
}

func (am *AppleMusicProcessor) TidyMusic() error {
	files, err := os.ReadDir(am.tempDir)
	if err != nil {
		return fmt.Errorf("读取临时目录失败: %w", err)
	}
	if len(files) == 0 {
		utils.WarnWithFormat("[AppleMusic] ⚠️ 未找到待整理的音乐文件")
		return errors.New("未找到待整理的音乐文件")
	}

	switch am.cfg.Tidy.Mode {
	case 1:
		return am.tidyToLocal(files)
	case 2:
		return am.tidyToWebDAV(files, core.GlobalWebDAV)
	default:
		return fmt.Errorf("未知整理模式: %d", am.cfg.Tidy.Mode)
	}
}

func (am *AppleMusicProcessor) EncryptedExts() []string {
	return []string{".m4p"}
}

func (am *AppleMusicProcessor) DecryptedExts() []string {
	return []string{".aac", ".m4a", ".alac"}
}

/* ------------------------ 拓展方法 ------------------------ */

// 整理到本地
func (am *AppleMusicProcessor) tidyToLocal(files []os.DirEntry) error {
	dstDir := am.cfg.Tidy.DistDir
	if dstDir == "" {
		_ = processor.RemoveTempDir(am.tempDir)
		return errors.New("未配置输出目录")
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		_ = processor.RemoveTempDir(am.tempDir)
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	for _, f := range files {
		if !utils.FilterMusicFile(f, am.EncryptedExts(), am.DecryptedExts()) {
			utils.DebugWithFormat("[AppleMusic] 跳过非音乐文件: %s", f.Name())
			continue
		}
		src := filepath.Join(am.tempDir, f.Name())
		dst := filepath.Join(dstDir, utils.SanitizeFileName(f.Name()))
		err := processor.ToLocal(src, dst)
		if err != nil {
			return err
		}
		utils.InfoWithFormat("[AppleMusic] 📦 已整理: %s", dst)
	}
	// 清除临时目录
	err := processor.RemoveTempDir(am.tempDir)
	if err != nil {
		return err
	}
	return nil
}

// 整理到webdav
func (am *AppleMusicProcessor) tidyToWebDAV(files []os.DirEntry, webdav *core.WebDAV) error {
	if webdav == nil {
		_ = processor.RemoveTempDir(am.tempDir)
		return errors.New("WebDAV 未初始化")
	}

	for _, f := range files {
		if !utils.FilterMusicFile(f, am.EncryptedExts(), am.DecryptedExts()) {
			utils.DebugWithFormat("[AppleMusic] 跳过非音乐文件: %s", f.Name())
			continue
		}

		filePath := filepath.Join(am.tempDir, f.Name())
		if err := webdav.Upload(filePath); err != nil {
			utils.WarnWithFormat("[AppleMusic] ☁️ 上传失败 %s: %v", f.Name(), err)
			continue
		}
		utils.InfoWithFormat("[AppleMusic] ☁️ 已上传: %s", f.Name())
	}
	// 清除临时目录
	err := processor.RemoveTempDir(am.tempDir)
	if err != nil {
		return err
	}
	return nil
}
