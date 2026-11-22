package bot

import (
	"github.com/nichuanfang/gymdl/internal/musiclink"
)

func discoveryListenURL(rawURL string) string {
	return musiclink.DirectAppLink(rawURL)
}
