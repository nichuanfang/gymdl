package music

import (
	"os/exec"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor"
)

// 转发的音乐处理器

/* ---------------------- 结构体与构造方法 ---------------------- */

type ForwardProcessor struct {
	cfg        *config.Config
	tempDir    string
	songs      []*SongInfo
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
	panic("implement me")
}

func (fp *ForwardProcessor) DownloadCommand(url string) *exec.Cmd {
	// TODO implement me
	panic("implement me")
}

func (fp *ForwardProcessor) BeforeTidy() error {
	// TODO implement me
	panic("implement me")
}

func (fp *ForwardProcessor) NeedRemoveDRM() bool {
	// TODO implement me
	panic("implement me")
}

func (fp *ForwardProcessor) DRMRemove() error {
	// TODO implement me
	panic("implement me")
}

func (fp *ForwardProcessor) TidyMusic() error {
	// TODO implement me
	panic("implement me")
}

func (fp *ForwardProcessor) EncryptedExts() []string {
	// TODO implement me
	panic("implement me")
}

func (fp *ForwardProcessor) DecryptedExts() []string {
	// TODO implement me
	panic("implement me")
}

/* ------------------------ 拓展方法 ------------------------ */
