package web

import (
	"context"
	"fmt"
	"math/rand"
	"strings"

	"github.com/nichuanfang/gymdl/config"
)

// defaultAgentPlatforms is only the validation fallback for explicitly
// requested batches. An omitted platform is resolved by agentPlatforms.
var defaultAgentPlatforms = []string{"qq", "netease", "bilibili"}

// agentPlatformPicker is a test seam for the random platform choice.
var agentPlatformPicker = func() string {
	if rand.Intn(2) == 0 {
		return "netease"
	}
	return "qq"
}

// agentPlatforms turns "no platform" into one random source instead of
// fanning out across every provider. The user sees that platform on each hit.
func agentPlatforms(platforms []string) []string {
	if len(platforms) != 0 {
		return platforms
	}
	return []string{agentPlatformPicker()}
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
	platforms = agentPlatforms(platforms)
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
		valid = agentPlatforms(nil)
	}
	search := func(ctx context.Context, platform, kw string, pageSize, offset int) ([]SearchResultItem, error) {
		return cachedSearchPage(ctx, platform, kw, pageSize, offset, searchProviderSearch)
	}
	items, _, _, errs := combinedSearchPageContext(ctx, valid, keyword, 0, limit, search)
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
	items, err := SearchForAgent(ctx, keyword, platforms, max(limit*3, limit))
	if err != nil {
		return nil, nil, err
	}
	for _, item := range items {
		hit := AgentHit{SearchResultItem: item, Kind: ClassifyAgentResult(item)}
		switch hit.Kind {
		case AgentKindSingle:
			if len(single) < limit && lyricsLikeName(item.Name) {
				single = append(single, hit)
			}
		default:
			other = append(other, hit)
		}
	}
	return single, other, nil
}
