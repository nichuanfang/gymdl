package music

import (
	"context"
	"errors"
	"fmt"
)

// Processors share platform temporary directories. All entry points must hold
// this gate through download, tagging and moving files out of those directories.
var workflowGate = make(chan struct{}, 1)

// RunWorkflow executes the same serialized pipeline for Bot, WebUI and watches.
// Pending WebDAV uploads remain distinguishable through SongInfo.Tidy.
func RunWorkflow(ctx context.Context, p Processor, link string, progress func(string)) ([]*SongInfo, error) {
	return RunWorkflowWithHooks(ctx, p, link, progress, WorkflowHooks{})
}

// WorkflowHooks keeps the final duplicate check and successful recording inside
// the same gate as file processing, closing races between manual and auto work.
type WorkflowHooks struct {
	Before      func() error
	BeforeStore func() error
	After       func([]*SongInfo) error
}

var ErrWorkflowCommitted = errors.New("文件已整理，需要复核成功记录")

func RunWorkflowWithHooks(ctx context.Context, p Processor, link string, progress func(string), hooks WorkflowHooks) ([]*SongInfo, error) {
	select {
	case workflowGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-workflowGate }()
	if progress == nil {
		progress = func(string) {}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if hooks.Before != nil {
		if err := hooks.Before(); err != nil {
			return nil, err
		}
	}
	if err := p.DownloadMusic(link, progress); err != nil {
		return nil, fmt.Errorf("下载失败: %w", err)
	}
	progress("整理中…")
	if err := p.BeforeTidy(); err != nil {
		return nil, fmt.Errorf("文件处理失败: %w", err)
	}
	progress("开始入库…")
	if hooks.BeforeStore != nil {
		if err := hooks.BeforeStore(); err != nil {
			return nil, err
		}
	}
	if err := p.TidyMusic(); err != nil {
		return nil, fmt.Errorf("文件入库失败: %w", err)
	}
	var songs []*SongInfo
	for _, s := range p.Songs() {
		if s != nil {
			songs = append(songs, s)
		}
	}
	if len(songs) == 0 {
		return nil, fmt.Errorf("没有检测到有效歌曲")
	}
	if hooks.After != nil {
		if err := hooks.After(songs); err != nil {
			return songs, fmt.Errorf("%w: %v", ErrWorkflowCommitted, err)
		}
	}
	return songs, nil
}
