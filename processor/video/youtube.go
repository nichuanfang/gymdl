package video

import (
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
)

// youtube下载

/* ---------------------- 结构体与构造方法 ---------------------- */

type YoutubeProcessor struct {
	cfg     *config.Config
	tempDir string
	videos  []*VideoInfo
}

// Init  初始化
func (p *YoutubeProcessor) Init(cfg *config.Config) {
	p.cfg = cfg
	p.videos = make([]*VideoInfo, 0)
	p.tempDir = processor.BuildOutputDir(YoutubeTempDir)
}

/* ---------------------- 基础接口实现 ---------------------- */

func (p *YoutubeProcessor) Name() processor.LinkType {
	return processor.LinkYoutube
}

func (p *YoutubeProcessor) Videos() []*VideoInfo {
	return p.videos
}

/* ------------------------ 下载逻辑 ------------------------ */

func (p *YoutubeProcessor) Download(url string) error {
	utils.Debugf("开始下载视频:%s", url)
	return nil
}

/* ------------------------ 拓展方法 ------------------------ */
