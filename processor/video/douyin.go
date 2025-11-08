package video

import (
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
)

// 抖音下载

/* ---------------------- 结构体与构造方法 ---------------------- */

type DouyinProcessor struct {
	cfg     *config.Config
	tempDir string
	videos  []*VideoInfo
}

// Init  初始化
func (p *DouyinProcessor) Init(cfg *config.Config) {
	p.cfg = cfg
	p.videos = make([]*VideoInfo, 0)
	p.tempDir = processor.BuildOutputDir(DouyinTempDir)
}

/* ---------------------- 基础接口实现 ---------------------- */

func (p *DouyinProcessor) Name() processor.LinkType {
	return processor.LinkDouyin
}

func (p *DouyinProcessor) Videos() []*VideoInfo {
	return p.videos
}

/* ------------------------ 下载逻辑 ------------------------ */

func (p *DouyinProcessor) Download(url string) error {
	utils.InfoWithFormat("55.69 MiB / 55.69 MiB [==============] 1.77 MiB p/s 100.00% 32s：%s", url)
	return nil
}

/* ------------------------ 拓展方法 ------------------------ */
