package music

import (
	"bytes"
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

type YoutubeMusicProcessor struct {
	cfg     *config.Config
	tempDir string
	songs   []*SongInfo
}

// Init  初始化
func (p *YoutubeMusicProcessor) Init(cfg *config.Config) error {
	p.cfg = cfg
	p.songs = make([]*SongInfo, 0)
	p.tempDir = processor.BuildOutputDir(YoutubeTempDir)
	return nil
}

// AudioFormat 结构用来解析 yt-dlp -j 输出
type AudioFormat struct {
	FormatID string `json:"format_id"`
	Ext      string `json:"ext"`
	Acodec   string `json:"acodec"`
}

/* ---------------------- 基础接口实现 ---------------------- */

func (p *YoutubeMusicProcessor) Name() processor.LinkType {
	return processor.LinkYoutubeMusic
}

func (p *YoutubeMusicProcessor) Songs() []*SongInfo {
	return p.songs
}

/* ------------------------ 下载逻辑 ------------------------ */
func (p *YoutubeMusicProcessor) DownloadMusic(url string, callback func(string)) error {
	start := time.Now()

	utils.InfoWithFormat("[YoutubeMusic] 🎵 开始下载: %s", url)

	cmd := p.DownloadCommand(url)
	if cmd == nil {
		return errors.New("download command build failed")
	}
	utils.DebugWithFormat("[YoutubeMusic] 执行命令: %s", strings.Join(cmd.Args, " "))

	// 创建临时目录
	if err := processor.CreateOutputDir(p.tempDir); err != nil {
		utils.ErrorWithFormat("[YoutubeMusic] ❌ 创建临时目录失败: %v", err)
		return err
	}

	// 执行下载
	output, err := cmd.CombinedOutput()
	logOut := strings.TrimSpace(string(output))
	if err != nil {
		_ = processor.RemoveTempDir(p.tempDir)
		utils.ErrorWithFormat("[YoutubeMusic] ❌ 下载失败: %v\n输出:\n%s", err, logOut)
		return fmt.Errorf("yt-dlp 下载失败: %w", err)
	}

	// 输出调试信息，仅当有日志内容时
	if logOut != "" {
		utils.DebugWithFormat("[YoutubeMusic] 下载输出:\n%s", logOut)
	}

	utils.InfoWithFormat("[YoutubeMusic] ✅ 下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond))
	callback(fmt.Sprintf("下载完成（耗时 %v）", time.Since(start).Truncate(time.Millisecond)))
	return nil
}

func (p *YoutubeMusicProcessor) DownloadCommand(url string) *exec.Cmd {
	// 1️⃣ 获取可用音轨信息
	cmdInfo := exec.Command("yt-dlp", "--no-playlist", "-F", url)
	var out bytes.Buffer
	cmdInfo.Stdout = &out
	err := cmdInfo.Run()
	if err != nil {
		utils.WarnWithFormat("获取视频信息失败: %w", err)
		return nil
	}

	outputText := out.String()
	lines := strings.Split(outputText, "\n")

	var has141, has251 bool
	for _, line := range lines {
		// 每行以空格分割，第一个字段是 format ID
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		formatID := fields[0]
		ext := fields[1]

		if formatID == "141" && strings.Contains(ext, "m4a") {
			has141 = true
		}
		if formatID == "251" && strings.Contains(ext, "webm") {
			has251 = true
		}
	}

	// 2️⃣ 根据存在情况构造 yt-dlp 命令

	//cookiePath := filepath.Join(p.cfg.CookieCloud.CookieFilePath, p.cfg.CookieCloud.CookieFile)

	args := []string{
		//"--cookies", cookiePath,   // yt-dlp传递cookie文件有问题 暂时不开放
		"-x",
		"--no-playlist",
		"--embed-metadata",
		"--embed-thumbnail",
		"-o", filepath.Join(p.tempDir, "%(title)s.%(ext)s"),
	}

	if has141 {
		// 存在 141 AAC，直接下载
		args = append([]string{"-f", "141"}, args...)
	} else if has251 {
		// 不存在 141，用 251 转 AAC
		args = append([]string{"-f", "251"}, args...)
		args = append(args, "--audio-format", "aac", "--postprocessor-args", "-c:a libfdk_aac -vbr 5", "--audio-quality", "0")
	} else {
		// 其他情况，退而求其次下载 140 AAC
		args = append([]string{"-f", "140"}, args...)
	}

	args = append(args, url)
	return exec.Command("yt-dlp", args...)
}

func (p *YoutubeMusicProcessor) BeforeTidy() error {
	songs, err := ReadMusicDir(p.tempDir, processor.DetermineTidyType(p.cfg), p)
	if err != nil {
		return err
	}
	// 更新元信息列表
	p.songs = songs
	return nil
}

func (p *YoutubeMusicProcessor) NeedRemoveDRM() bool {
	return false
}

func (p *YoutubeMusicProcessor) DRMRemove() error {
	return nil
}

func (p *YoutubeMusicProcessor) TidyMusic() error {
	files, err := os.ReadDir(p.tempDir)
	if err != nil {
		return fmt.Errorf("读取临时目录失败: %w", err)
	}
	if len(files) == 0 {
		utils.WarnWithFormat("[YoutubeMusic] ⚠️ 未找到待整理的音乐文件")
		return errors.New("未找到待整理的音乐文件")
	}

	switch p.cfg.Tidy.Mode {
	case 1:
		return p.tidyToLocal(files)
	case 2:
		return p.tidyToWebDAV(files, core.GlobalWebDAV)
	default:
		return fmt.Errorf("未知整理模式: %d", p.cfg.Tidy.Mode)
	}
}

func (p *YoutubeMusicProcessor) EncryptedExts() []string {
	return make([]string, 0)
}

func (p *YoutubeMusicProcessor) DecryptedExts() []string {
	return []string{".aac", ".m4a", ".flac", ".mp3", ".ogg"}
}

/* ------------------------ 拓展方法 ------------------------ */
// 整理到本地
func (p *YoutubeMusicProcessor) tidyToLocal(files []os.DirEntry) error {
	dstDir := p.cfg.Tidy.DistDir
	if dstDir == "" {
		_ = processor.RemoveTempDir(p.tempDir)
		return errors.New("未配置输出目录")
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		_ = processor.RemoveTempDir(p.tempDir)
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	for _, f := range files {
		if !utils.FilterMusicFile(f, p.EncryptedExts(), p.DecryptedExts()) {
			utils.DebugWithFormat("[YoutubeMusic] 跳过非音乐文件: %s", f.Name())
			continue
		}
		src := filepath.Join(p.tempDir, f.Name())
		dst := filepath.Join(dstDir, utils.SanitizeFileName(f.Name()))
		err := processor.ToLocal(src, dst)
		if err != nil {
			return err
		}
		utils.InfoWithFormat("[YoutubeMusic] 📦 已整理: %s", dst)
	}
	// 清除临时目录
	err := processor.RemoveTempDir(p.tempDir)
	if err != nil {
		return err
	}
	return nil
}

// 整理到webdav
func (p *YoutubeMusicProcessor) tidyToWebDAV(files []os.DirEntry, webdav *core.WebDAV) error {
	if webdav == nil {
		_ = processor.RemoveTempDir(p.tempDir)
		return errors.New("WebDAV 未初始化")
	}

	for _, f := range files {
		if !utils.FilterMusicFile(f, p.EncryptedExts(), p.DecryptedExts()) {
			utils.DebugWithFormat("[YoutubeMusic] 跳过非音乐文件: %s", f.Name())
			continue
		}

		filePath := filepath.Join(p.tempDir, f.Name())
		if err := webdav.Upload(filePath); err != nil {
			utils.WarnWithFormat("[YoutubeMusic] ☁️ 上传失败 %s: %v", f.Name(), err)
			continue
		}
		utils.InfoWithFormat("[YoutubeMusic] ☁️ 已上传: %s", f.Name())
	}
	// 清除临时目录
	err := processor.RemoveTempDir(p.tempDir)
	if err != nil {
		return err
	}
	return nil
}
