package bot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/aichat"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/musicdedupe"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

var musicSourceEditionSuffix = regexp.MustCompile(`\s*(?:\([^()]+\)|（[^（）]+）|\[[^\[\]]+\]|\s+-\s+.+)$`)
var musicSourceEditionLabel = regexp.MustCompile(`(?i)\b(?:live|remix|remaster(?:ed)?|acoustic|instrumental|demo|version|feat\.?|ft\.?)\b|现场|現場|演唱会|演唱會|混音|伴奏|重制|重製|不插电|不插電|完整版|专辑版|專輯版`)

func (app *BotApp) presentOtherMusicSources(ctx context.Context, c tb.Context, message *tb.Message, action pendingMusicAction) error {
	return app.presentMusicSourceChoices(ctx, c, message, action, false)
}

func (app *BotApp) presentMusicSourceChoices(ctx context.Context, c tb.Context, message *tb.Message, action pendingMusicAction, confirm bool) error {
	web.EnsureAgentRuntime(app.cfg)
	platforms := []string{"qq", "netease", "youtube", "bilibili"}
	if app.cfg == nil || app.cfg.QQMusicApiConfig == nil || !app.cfg.QQMusicApiConfig.Enable {
		platforms = platforms[1:]
	}
	items, failed := findOtherMusicSources(ctx, action, platforms, web.SearchMusicSourceCandidates)
	if len(items) == 0 {
		markup := &tb.ReplyMarkup{}
		text := "没找到匹配《" + utils.TruncateString(displaySongValue(action.title), 100) + "》的其他音源。"
		if failed {
			text += "\n部分平台搜索未完成，可以稍后重试。"
		}
		if action.checkToken != "" {
			markup.Inline(markup.Row(
				markup.Data("删除…", audioCheckCallback, "source_trash", action.checkToken),
				markup.Data("返回体检", audioCheckCallback, "resume", action.checkToken),
			))
		}
		_, err := c.Bot().Edit(message, text, markup)
		return err
	}

	var text strings.Builder
	fmt.Fprintf(&text, "<b>《%s》的其他音源</b>", utils.EscapeHTML(utils.TruncateString(displaySongValue(action.title), 80)))
	markup := &tb.ReplyMarkup{}
	rows := make([]tb.Row, 0, len(items))
	tokens := make([]string, 0, len(items))
	for i, item := range items {
		fmt.Fprintf(&text, "\n\n%d. <b>%s</b> · %s", i+1, musicSourceLabel(item.Name), aichat.PlatformLabel(item.Platform))
		details := musicSourceLabel(item.Artists)
		if item.Album != "" {
			details += " · " + musicSourceLabel(item.Album)
		}
		if item.DurationSec > 0 {
			details += fmt.Sprintf(" · %d:%02d", item.DurationSec/60, item.DurationSec%60)
		}
		fmt.Fprintf(&text, "\n%s", details)
		token := strings.ReplaceAll(uuid.NewString(), "-", "")
		tokens = append(tokens, token)
		app.storeMusicAction(token, pendingMusicAction{
			title: item.Name, choice: item.URL, confirmSource: confirm, expiresAt: time.Now().Add(musicActionTTL),
			checkToken: action.checkToken,
		})
		rows = append(rows, markup.Row(
			markup.URL(fmt.Sprintf("打开 %d", i+1), discoveryListenURL(item.URL)),
			markup.Data(fmt.Sprintf("下载 %d", i+1), musicActionCallbackUnique, "download", token),
		))
	}
	text.WriteString("\n\n实际码率以下载结果为准。下载同名同格式的版本可能覆盖原文件。")
	if action.checkToken != "" {
		rows = append(rows, markup.Row(markup.Data("⬅️ 返回体检", audioCheckCallback, "resume", action.checkToken)))
	}
	markup.Inline(rows...)
	app.setMusicActionMessages(tokens, message, nil, c.Sender().ID, c.Chat().ID)
	_, err := c.Bot().Edit(message, text.String(), tb.ModeHTML, markup)
	return err
}

func findOtherMusicSources(ctx context.Context, action pendingMusicAction, platforms []string, search func(context.Context, string, []string) ([]web.SearchResultItem, web.SearchPlatformErrors)) ([]web.SearchResultItem, bool) {
	title := musicSourceBaseTitle(action.title)
	if discovery.Normalize(title) == "" {
		return nil, false
	}
	query := strings.TrimSpace(title + " " + discovery.PrimaryArtist(action.artist))
	raw, failures := search(ctx, query, platforms)
	matches := otherMusicSourceMatches(action, raw)
	if len(matches) == 0 && query != title && ctx.Err() == nil {
		retry, retryFailures := search(ctx, title, platforms)
		matches = otherMusicSourceMatches(action, retry)
		failures = retryFailures
	}
	return matches, len(failures) > 0
}

func (app *BotApp) confirmMusicSourceChoice(c tb.Context, action pendingMusicAction) error {
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	markup := &tb.ReplyMarkup{}
	row := tb.Row{markup.Data("确认下载", musicActionCallbackUnique, "download", token)}
	if action.checkToken != "" {
		row = append(row, markup.Data("⬅️ 返回体检", audioCheckCallback, "resume", action.checkToken))
	}
	markup.Inline(row)
	message, err := c.Bot().Send(c.Chat(),
		"下载《"+utils.TruncateString(displaySongValue(action.title), 100)+"》？如果新音源与原文件同名同格式，原文件可能被覆盖。", markup)
	if err != nil {
		return err
	}
	app.storeMusicAction(token, pendingMusicAction{
		title: action.title, choice: action.choice, expiresAt: time.Now().Add(10 * time.Minute),
		checkToken: action.checkToken,
	})
	app.setMusicActionMessages([]string{token}, message, markup, c.Sender().ID, c.Chat().ID)
	return nil
}

func otherMusicSourceMatches(action pendingMusicAction, items []web.SearchResultItem) []web.SearchResultItem {
	title := discovery.Normalize(musicSourceBaseTitle(action.title))
	seen := map[string]bool{musicdedupe.CanonicalURL(action.sourceURL): true}
	matches := make([]web.SearchResultItem, 0, 8)
	for _, item := range items {
		link := musicdedupe.CanonicalURL(item.URL)
		if seen[link] || sameMusicSource(item, action.sourceURL) ||
			((item.Platform == "youtube" || item.Platform == "bilibili") && web.ClassifyAgentResult(item) != web.AgentKindSingle) {
			continue
		}
		name := discovery.Normalize(item.Name)
		if title == "" || !strings.Contains(name, title) {
			continue
		}
		if !musicSourceArtistMatches(action.artist, item) {
			continue
		}
		seen[link] = true
		matches = append(matches, item)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return (discovery.Normalize(matches[i].Name) == title) && discovery.Normalize(matches[j].Name) != title
	})
	return matches[:min(len(matches), 8)]
}

func musicSourceBaseTitle(title string) string {
	title = strings.TrimSpace(title)
	suffix := musicSourceEditionSuffix.FindStringIndex(title)
	if suffix != nil && suffix[0] > 0 && musicSourceEditionLabel.MatchString(title[suffix[0]:]) {
		return strings.TrimSpace(title[:suffix[0]])
	}
	return title
}

func musicSourceArtistMatches(artist string, item web.SearchResultItem) bool {
	if discovery.Normalize(artist) == "" || discovery.ArtistCreditsOverlap(artist, item.Artists) {
		return true
	}
	if item.Platform != "youtube" && item.Platform != "bilibili" {
		return false
	}
	credit := discovery.Normalize(discovery.PrimaryArtist(artist))
	return credit != "" && (strings.Contains(discovery.Normalize(item.Artists), credit) || strings.Contains(discovery.Normalize(item.Name), credit))
}

func sameMusicSource(item web.SearchResultItem, sourceURL string) bool {
	// The same platform track may have multiple public URL shapes.
	var kind processor.LinkType
	switch item.Platform {
	case "netease":
		kind = processor.LinkNetEase
	case "qq":
		kind = processor.LinkQQMusic
	case "bilibili":
		kind = processor.LinkBiliBiliMusic
	default:
		return false
	}
	id := musicdedupe.IdentityFromLink(kind, sourceURL).PlatformTrackID
	return id != "" && id == musicdedupe.IdentityFromLink(kind, item.URL).PlatformTrackID
}

func musicSourceLabel(value string) string {
	return utils.EscapeHTML(utils.TruncateString(displaySongValue(value), 72))
}

func (app *BotApp) downloadOtherMusicSource(c tb.Context, action pendingMusicAction) error {
	taskConfig := config.WithLiveTaskOverrides(app.cfg)
	if taskConfig.AdditionalConfig == nil {
		taskConfig.AdditionalConfig = &config.AdditionalConfig{}
	}
	taskConfig.AdditionalConfig.MusicMode = true
	err := app.downloadMusicCandidate(c, aichat.Candidate{Name: action.title, URL: action.choice}, taskConfig)
	if music.IsQQShareLinkError(err) || errors.Is(err, errDuplicateConfirmationPending) || errors.Is(err, errMusicDownloadAlreadyProcessing) ||
		errors.Is(err, errMusicDedupeUnavailable) || errors.Is(err, errCandidateNotDownloadable) || errors.Is(err, errCandidateDownloadFailed) {
		return nil // The download card already shows the outcome.
	}
	return err
}
