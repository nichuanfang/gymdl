package music

import (
	"bufio"
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

type Config struct {
	AdditionalConfig struct {
		EnableYoutubeCookie bool
	}
	CookieCloud struct {
		CookieFilePath string
		CookieFile     string
	}
}

// Format JSON 解析结构
type ytFormat struct {
	FormatID string `json:"format_id"`
	Ext      string `json:"ext"`
}

type ytInfo struct {
	Formats []ytFormat `json:"formats"`
}

// Init  初始化
func (p *YoutubeMusicProcessor) Init(cfg *config.Config) {
	p.cfg = cfg
	p.songs = make([]*SongInfo, 0)
	p.tempDir = processor.BuildOutputDir(YoutubeTempDir)
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
	callback("命令构建完成，开始下载...")
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

// getAvailableFormats 获取可用格式（优化版：流式解析 + 映射表）
func (p *YoutubeMusicProcessor) getAvailableFormats(url string, cookiePath string) (map[string]bool, error) {
	args := []string{
		"--no-playlist",
		"--skip-download",
		"--no-check-certificates",
		"--no-warnings",
	}

	// 根据配置决定是否传递 cookies
	if p.cfg.YTDLPConfig.PassCookies {
		args = append(args, "--cookies", cookiePath)
	}

	args = append(args, "-F", url)

	cmd := exec.Command("yt-dlp", args...)

	// 用流式解析替代一次性读取整个输出
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	formats := make(map[string]bool)

	// 目标格式映射表
	targetExt := map[string]string{
		"141": "m4a",
		"251": "webm",
		"140": "m4a",
	}

	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 {
			if ext, ok := targetExt[fields[0]]; ok && strings.Contains(fields[1], ext) {
				formats[fields[0]] = true
			}
		}
	}

	if err = scanner.Err(); err != nil {
		return nil, err
	}

	if err = cmd.Wait(); err != nil {
		return nil, err
	}

	return formats, nil
}

// 生成下载命令
func (p *YoutubeMusicProcessor) DownloadCommand(url string) *exec.Cmd {
	cookiePath := filepath.Join(p.cfg.CookieCloud.CookieFilePath, p.cfg.CookieCloud.CookieFile)
	start := time.Now()
	// 获取可用格式

	formats, err := p.getAvailableFormats(url, cookiePath)
	utils.InfoWithFormat("[YoutubeMusic] ✅ 成功解析链接（耗时 %v）", time.Since(start).Truncate(time.Millisecond))
	if err != nil {
		// 这里可按你的 utils 警告逻辑
		return nil
	}

	// 根据优先级选择
	var formatID string
	var postArgs []string
	switch {
	case formats["141"]:
		formatID = "141" // 高质量 AAC
	case formats["251"]:
		formatID = "251" // 中等质量 opus
		postArgs = []string{
			"--audio-format", "aac",
			"--postprocessor-args", "-c:a libfdk_aac -vbr 5 -afterburner 1",
		}
	default:
		formatID = "140" // 中等质量 AAC
	}

	// 构造 yt-dlp 命令
	args := []string{
		"-x",
		"--no-playlist",
		"--embed-metadata",
		"--embed-thumbnail",
		"--no-check-certificates",
		"--no-warnings",
	}

	// 根据配置决定是否传递 cookies
	if p.cfg.YTDLPConfig.PassCookies {
		args = append(args, "--cookies", cookiePath)
	}
	args = append(args, "-o", filepath.Join(p.tempDir, "%(title)s.%(ext)s"))
	args = append(args, postArgs...)
	args = append([]string{"-f", formatID}, args...)
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
