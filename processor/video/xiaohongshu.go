package video

import (
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
)

// 小红书下载

/* ---------------------- 结构体与构造方法 ---------------------- */

type XiaohongshuProcessor struct {
	cfg     *config.Config
	tempDir string
	videos  []*VideoInfo
}

// Init  初始化
func (p *XiaohongshuProcessor) Init(cfg *config.Config) {
	p.cfg = cfg
	p.videos = make([]*VideoInfo, 0)
	p.tempDir = processor.BuildOutputDir(XHSTempDir)
}

/* ---------------------- 基础接口实现 ---------------------- */

func (p *XiaohongshuProcessor) Name() processor.LinkType {
	return processor.LinkXiaohongshu
}

func (p *XiaohongshuProcessor) Videos() []*VideoInfo {
	return p.videos
}

/* ------------------------ 下载逻辑 ------------------------ */

func (p *XiaohongshuProcessor) Download(url string) error {
	utils.Debugf("开始下载视频:%s", url)
	return nil
}

/* ------------------------ 拓展方法 ------------------------ */
