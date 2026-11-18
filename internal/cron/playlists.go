package cron

import (
	"context"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/playlistcatalog"
	"github.com/nichuanfang/gymdl/internal/storage"
)

func syncPlaylists(c *config.Config) error {
	if c == nil || c.PlaylistOrganizer == nil || strings.TrimSpace(c.PlaylistOrganizer.NavidromeURL) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err := playlistcatalog.Sync(ctx, c.PlaylistOrganizer, storage.DefaultDatabasePath, nil)
	return err
}
