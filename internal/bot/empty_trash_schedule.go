package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/utils"
)

func ScheduledEmptyTrashConfigured(cfg *config.Config) bool {
	if cfg == nil || cfg.PlaylistOrganizer == nil || cfg.AIPlaylist == nil {
		return false
	}
	return strings.TrimSpace(cfg.PlaylistOrganizer.OliveTinURL) != "" &&
		strings.TrimSpace(cfg.PlaylistOrganizer.NavidromeURL) != "" &&
		strings.TrimSpace(cfg.AIPlaylist.PineconeHost) != "" &&
		strings.TrimSpace(cfg.AIPlaylist.PineconeAPIKey) != "" &&
		strings.TrimSpace(cfg.AIPlaylist.PineconeNamespace) != ""
}

func (app *BotApp) RunScheduledEmptyTrash(ctx context.Context, cfg *config.Config) error {
	const noChat = 0
	if !app.beginEmptyTrashRun(noChat) {
		return nil
	}
	defer app.finishEmptyTrashRun(noChat)
	service, err := newEmptyTrashService(cfg)
	if err != nil {
		return fmt.Errorf("定时清空回收站配置不可用: %w", err)
	}
	defer service.organizer.client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, emptyTrashRunTimeout)
	defer cancel()

	result := service.run(ctx, nil)
	if result.NoTracks {
		return nil
	}
	if errors.Join(result.PreflightError, result.VectorError, result.StateError, result.FileError) != nil {
		return errors.New(formatEmptyTrashResult(result))
	}
	utils.InfoWithFormat("[Job] %s", formatEmptyTrashResult(result))
	return nil
}
