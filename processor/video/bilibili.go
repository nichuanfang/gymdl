package video

// bilibili下载

import (
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
)

/* ---------------------- 结构体与构造方法 ---------------------- */

type BiliBiliProcessor struct {
	cfg     *config.Config
	tempDir string
	videos  []*VideoInfo
}

// Init  初始化
func (p *BiliBiliProcessor) Init(cfg *config.Config) {
	p.cfg = cfg
	p.videos = make([]*VideoInfo, 0)
	p.tempDir = processor.BuildOutputDir(BilibiliTempDir)
}

/* ---------------------- 基础接口实现 ---------------------- */

func (p *BiliBiliProcessor) Name() processor.LinkType {
	return processor.LinkBilibili
}

func (p *BiliBiliProcessor) Videos() []*VideoInfo {
	return p.videos
}

/* ------------------------ 下载逻辑 ------------------------ */

func (p *BiliBiliProcessor) Download(url string) error {
	utils.Debugf("开始下载视频:%s", url)
	return nil
}

/* ------------------------ 拓展方法 ------------------------ */
