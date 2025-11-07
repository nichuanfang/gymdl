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

type YoutubeMusicProcessor struct {
	cfg     *config.Config
	tempDir string
	songs   []*SongInfo
}

// Init  初始化
func (p *YoutubeMusicProcessor) Init(cfg *config.Config) {
	p.cfg = cfg
	p.songs = make([]*SongInfo, 0)
	p.tempDir = processor.BuildOutputDir(YoutubeTempDir)
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

// DownloadCommand 创建yt-dlp音乐下载指令
func (p *YoutubeMusicProcessor) DownloadCommand(url string) *exec.Cmd {
	//cookiePath := filepath.Join(p.cfg.CookieCloud.CookieFilePath, p.cfg.CookieCloud.CookieFile)
	args := []string{
		//"--cookies", cookiePath,   // yt-dlp传递cookie文件有问题 暂时不开放
		//"-f", "141/251/140", //音轨质量优先级: 【141】为会员音轨256aac 【251】为中等质量opus 【140】为中等质量m4a
		"-f", "251/140",
		"-x",                    //只提取音频
		"--no-playlist",         //严格列表模式
		"--audio-format", "aac", //格式为aac
		"--audio-quality", "0", //最高质量
		"--embed-metadata",                                  //添加基本元数据 除封面外 还缺失 `专辑` `专辑艺术家` `歌词` 需配合mtw手动刮削
		"--embed-thumbnail",                                 //嵌入封面
		"-o", filepath.Join(p.tempDir, "%(title)s.%(ext)s"), // 输出路径
		url,
	}
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
