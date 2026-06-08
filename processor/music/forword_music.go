package music

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
    "strings"

    "github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
)

// 转发的音乐处理器

/* ---------------------- 结构体与构造方法 ---------------------- */

type ForwardProcessor struct {
	cfg        *config.Config
	tempDir    string
	songs      []*SongInfo
	FileName   string
	AudioBytes []byte
}

// Init  初始化
func (fp *ForwardProcessor) Init(cfg *config.Config) {
	fp.cfg = cfg
	fp.songs = make([]*SongInfo, 0)
	fp.tempDir = processor.BuildOutputDir(ForwardTempDir)
}

/* ---------------------- 基础接口实现 ---------------------- */

func (fp *ForwardProcessor) Name() processor.LinkType {
	return processor.LinkForwardMusic
}

func (fp *ForwardProcessor) Songs() []*SongInfo {
	return fp.songs
}

/* ------------------------ 下载逻辑 ------------------------ */

func (fp *ForwardProcessor) DownloadMusic(url string, callback func(string)) error {
	// TODO implement me
	err := processor.CreateOutputDir(fp.tempDir)
	if err != nil {
		return err
	}
	err = os.WriteFile(filepath.Join(fp.tempDir, fp.FileName), fp.AudioBytes, 0644)
	if err != nil {
		return err
	}
	return nil
}

func (fp *ForwardProcessor) DownloadCommand(url string) *exec.Cmd {
	return nil
}

func (fp *ForwardProcessor) BeforeTidy() error {
	songs, err := ReadMusicDir(fp.tempDir, processor.DetermineTidyType(fp.cfg), fp)
	if err != nil {
		return err
	}
	// 更新元信息列表
	fp.songs = songs
	return nil
}

func (fp *ForwardProcessor) NeedRemoveDRM() bool {
	return false
}

func (fp *ForwardProcessor) DRMRemove() error {
	return nil
}

func (fp *ForwardProcessor) TidyMusic() error {
	files, err := os.ReadDir(fp.tempDir)
	if err != nil {
		return fmt.Errorf("读取临时目录失败: %w", err)
	}
	if len(files) == 0 {
		utils.WarnWithFormat("[Forward] ⚠️ 未找到待整理的音乐文件")
		return errors.New("未找到待整理的音乐文件")
	}

	switch fp.cfg.Tidy.Mode {
	case 1:
		return fp.tidyToLocal(files)
	case 2:
		return fp.tidyToWebDAV(files, core.GlobalWebDAV)
	default:
		return fmt.Errorf("未知整理模式: %d", fp.cfg.Tidy.Mode)
	}
}

func (fp *ForwardProcessor) EncryptedExts() []string {
	return make([]string, 0)
}

func (fp *ForwardProcessor) DecryptedExts() []string {
	return []string{".aac", ".m4a", ".flac", ".mp3", ".ogg"}
}

/* ------------------------ 拓展方法 ------------------------ */

// 整理到本地
func (fp *ForwardProcessor) tidyToLocal(files []os.DirEntry) error {
	dstDir := fp.cfg.Tidy.DistDir
	if dstDir == "" {
		_ = processor.RemoveTempDir(fp.tempDir)
		return errors.New("未配置输出目录")
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		_ = processor.RemoveTempDir(fp.tempDir)
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	for _, f := range files {
		src := filepath.Join(fp.tempDir, f.Name())
		dst := filepath.Join(dstDir, utils.SanitizeFileName(f.Name()))
		err := processor.ToLocal(src, dst)
		if err != nil {
			return err
		}
		utils.InfoWithFormat("[Forward] 📦 已整理: %s", dst)
	}
	// 清除临时目录
	err := processor.RemoveTempDir(fp.tempDir)
	if err != nil {
		return err
	}
	return nil
}

// 整理到webdav
func (fp *ForwardProcessor) tidyToWebDAV(files []os.DirEntry, webdav *core.WebDAV) error {
	if webdav == nil {
		_ = processor.RemoveTempDir(fp.tempDir)
		return errors.New("WebDAV 未初始化")
	}
    songMap := make(map[string]*SongInfo)
    for _, song := range fp.songs {
        songMap[utils.MakeSafeFileName(song.SongName) + "."+ strings.ToLower(song.FileExt)] = song
    }
	for _, f := range files {
        songInfo, exists := songMap[f.Name()]
        if !exists {
            // 如果是不匹配的文件（比如过滤掉的封面，或者多余的临时文件），直接跳过
            utils.DebugWithFormat("[Forward] 跳过无需处理的文件: %s", f.Name())
            continue
        }
        musicFilePath := filepath.Join(fp.tempDir, f.Name())
        remoteDir := "/" + utils.SanitizeFileName(songInfo.SongArtists) + "/" + utils.SanitizeFileName(songInfo.SongAlbum)
        if err := webdav.UploadTo(musicFilePath, remoteDir); err != nil {
            utils.WarnWithFormat("[Forward] ☁️ 上传失败 %s: %v", f.Name(), err)
            return err
        }
        utils.InfoWithFormat("[Forward] ☁️ 已上传: %s", f.Name())
        
	}
	// 清除临时目录
	err := processor.RemoveTempDir(fp.tempDir)
	if err != nil {
		return err
	}
	return nil
}
