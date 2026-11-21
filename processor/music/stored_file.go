package music

import (
	"path"
	"path/filepath"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/utils"
)

// StoredFilePath maps a processor's temporary output to its destination.
// In WebDAV mode it is relative to the configured WebDAV directory, even
// when the upload is still staged.
func StoredFilePath(song *SongInfo, tidy *config.TidyConfig) string {
	if song == nil || song.MusicPath == "" || tidy == nil {
		return ""
	}
	name := filepath.Base(song.MusicPath)
	switch tidy.Mode {
	case 1:
		return filepath.Join(tidy.DistDir, utils.SanitizeFileName(name))
	case 2:
		return path.Join(utils.SanitizeFileName(song.SongArtists), utils.SanitizeFileName(song.SongAlbum), name)
	}
	return ""
}
