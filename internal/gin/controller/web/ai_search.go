package web

import (
	"context"
	"fmt"
	"strings"

	"github.com/nichuanfang/gymdl/config"
)

// defaultAgentPlatforms keeps provider fan-out small: all four providers at
// once multiplies latency and rate-limit exposure, and anonymous Netease is
// frequently rejected. QQ and Bilibili are the most reliable pair here.
var defaultAgentPlatforms = []string{"qq", "netease", "bilibili"}

// SearchAudioReference uses the existing metadata adapters without letting an
// automatic quality scan update the configured QQ credentials.
func SearchAudioReference(ctx context.Context, keyword, platform string) ([]SearchResultItem, error) {
	if platform == "qq" {
		return searchQQWithCredentialRefresh(ctx, keyword, 12, 0, false)
	}
	return SearchForAgent(ctx, keyword, []string{platform}, 12)
}

// EnsureAgentRuntime installs the config snapshot that provider searches read
// from. The Telegram bot can run without the Web server, so it must publish the
// same runtime config here instead of relying on SetupWebRoutes.
func EnsureAgentRuntime(cfg *config.Config) {
	if cfg == nil || GetWebConfig() != nil {
		return
	}
	SetWebConfig(cfg)
}

// SearchForAgent runs the same provider search used by the WebUI and returns
// merged, de-duplicated results. It exists so the Telegram agent can ground
// model suggestions in real, downloadable links.
func SearchForAgent(ctx context.Context, keyword string, platforms []string, limit int) ([]SearchResultItem, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, fmt.Errorf("搜索关键词不能为空")
	}
	if len(platforms) == 0 {
		platforms = defaultAgentPlatforms
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	valid := make([]string, 0, len(platforms))
	for _, p := range platforms {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "netease", "qq", "youtube", "bilibili":
			valid = append(valid, strings.ToLower(strings.TrimSpace(p)))
		}
	}
	if len(valid) == 0 {
		valid = defaultAgentPlatforms
	}
	return searchForAgentWindow(ctx, keyword, valid, 0, limit)
}

func searchForAgentWindow(ctx context.Context, keyword string, platforms []string, offset, limit int) ([]SearchResultItem, error) {
	if offset < 0 {
		offset = 0
	}
	search := func(ctx context.Context, platform, kw string, pageSize, pageOffset int) ([]SearchResultItem, error) {
		return cachedSearchPage(ctx, platform, kw, pageSize, pageOffset, searchProviderSearch)
	}
	items, _, _, errs := combinedSearchPageContext(ctx, platforms, keyword, offset, limit, search)
	if len(items) == 0 && len(errs) > 0 {
		parts := make([]string, 0, len(errs))
		for platform, err := range errs {
			parts = append(parts, platform+": "+err)
		}
		return nil, fmt.Errorf("平台搜索失败: %s", strings.Join(parts, "; "))
	}
	return items, nil
}

// AgentHit is a search hit annotated with how it should be presented.
type AgentHit struct {
	SearchResultItem
	Kind AgentResultKind
}

// SearchSinglesForAgent searches and returns only results that look like one
// song. Compilations and ambient uploads are returned separately so the agent
// can mention them without turning them into download buttons.
func SearchSinglesForAgent(ctx context.Context, keyword string, platforms []string, limit int) (single []AgentHit, other []AgentHit, err error) {
	return SearchSinglesForAgentPage(ctx, keyword, platforms, 0, limit)
}

// SearchSinglesForAgentPage skips offset accepted singles, so a later recommendation
// can look past the artist's most familiar tracks.
func SearchSinglesForAgentPage(ctx context.Context, keyword string, platforms []string, offset, limit int) (single []AgentHit, other []AgentHit, err error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, nil, fmt.Errorf("搜索关键词不能为空")
	}
	// Stay at or below 48. SearchForAgent turns anything above 50 into 10.
	rawLimit := limit * 3
	if rawLimit > 48 {
		rawLimit = 48
	}
	items, err := searchForAgentWindow(ctx, keyword, normalizeAgentPlatforms(platforms), offset, rawLimit)
	if err != nil {
		return nil, nil, err
	}
	for _, item := range items {
		hit := AgentHit{SearchResultItem: item, Kind: ClassifyAgentResult(item)}
		switch hit.Kind {
		case AgentKindSingle:
			if !lyricsLikeName(item.Name) {
				continue
			}
			if len(single) < limit {
				single = append(single, hit)
			}
		default:
			other = append(other, hit)
		}
	}
	return single, other, nil
}

func normalizeAgentPlatforms(platforms []string) []string {
	if len(platforms) == 0 {
		return append([]string(nil), defaultAgentPlatforms...)
	}
	valid := make([]string, 0, len(platforms))
	seen := map[string]bool{}
	for _, p := range platforms {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "netease", "qq", "youtube", "bilibili":
			name := strings.ToLower(strings.TrimSpace(p))
			if !seen[name] {
				seen[name] = true
				valid = append(valid, name)
			}
		}
	}
	if len(valid) == 0 {
		return append([]string(nil), defaultAgentPlatforms...)
	}
	return valid
}
