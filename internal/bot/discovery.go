package bot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/aichat"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/storage"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

const discoveryCallbackUnique = "discovery"

var discoveryEndpoint = tb.InlineButton{Unique: discoveryCallbackUnique}

func (app *BotApp) initDiscovery() {
	store, err := discovery.Open(storage.DefaultDatabasePath)
	if err != nil {
		utils.WarnWithFormat("[Discovery] 初始化失败: %v", err)
		return
	}
	web.EnsureAgentRuntime(app.cfg)
	settings := app.cfg.Favorites
	if settings == nil {
		settings = &config.FavoritesConfig{PollIntervalMinutes: 10, SummaryIntervalMinutes: 30}
	}
	app.discovery = &discovery.Service{Store: store, Provider: &discovery.Providers{Config: app.cfg}, Library: &discovery.NavidromeLibrary{Config: app.cfg.PlaylistOrganizer}, Enabled: settings.Enable, PollInterval: time.Duration(settings.PollIntervalMinutes) * time.Minute, SummaryInterval: time.Duration(settings.SummaryIntervalMinutes) * time.Minute,
		History: func(ctx context.Context) ([]discovery.Track, error) {
			if app.cfg.AI == nil {
				return nil, fmt.Errorf("尚未配置收听历史")
			}
			return discovery.LoadHistory(ctx, app.cfg.AI.HistoryDSN, app.cfg.AI.HistoryTable)
		},
		Authorized: func(user, chat int64) bool {
			if user != chat || app.cfg.Telegram == nil {
				return false
			}
			for _, id := range app.cfg.Telegram.AllowedUsers {
				if id == strconv.FormatInt(user, 10) {
					return true
				}
			}
			return false
		},
		Pending: func(ctx context.Context, t discovery.Track) (bool, error) {
			return discovery.PendingTrack(ctx, app.musicDedupe, t)
		},
		Download: func(ctx context.Context, t discovery.Track) (discovery.DownloadResult, error) {
			return discovery.Download(ctx, app.cfg, app.musicDedupe, t)
		},
		OnError: func(err error) { utils.WarnWithFormat("[Discovery] 队列处理失败，将恢复任务: %v", err) },
		Notify: func(chat int64, text string) error {
			_, err := app.bot.Send(tb.ChatID(chat), text, tb.ModeHTML)
			return err
		},
	}
}

func (app *BotApp) discoveryAuthorized(c tb.Context) bool {
	if c.Sender() == nil || c.Chat() == nil || c.Chat().Type != tb.ChatPrivate || app.cfg == nil || app.cfg.Telegram == nil {
		return false
	}
	for _, allowed := range app.cfg.Telegram.AllowedUsers {
		if allowed == strconv.FormatInt(c.Sender().ID, 10) {
			return true
		}
	}
	return false
}

func (app *BotApp) discoveryAvailable(c tb.Context) error {
	if !app.discoveryAuthorized(c) {
		return fmt.Errorf("请由授权用户在私聊中操作")
	}
	if app.discovery == nil {
		return fmt.Errorf("遗珠与收藏服务尚未就绪，请检查数据库")
	}
	return nil
}

func (app *BotApp) RediscoverCommand(c tb.Context) error {
	if err := app.discoveryAvailable(c); err != nil {
		return c.Send(err.Error())
	}
	text, markup := discoveryPlatformMenu()
	return c.Send(text, markup)
}

func discoveryPlatformMenu() (string, *tb.ReplyMarkup) {
	markup := &tb.ReplyMarkup{}
	appendDiscoveryRows(markup,
		markup.Row(markup.Data("网易云", discoveryCallbackUnique, "source", "netease"), markup.Data("QQ 音乐", discoveryCallbackUnique, "source", "qq")),
		markup.Row(markup.Data("YouTube", discoveryCallbackUnique, "source", "youtube")),
		markup.Row(markup.Data("取消", discoveryCallbackUnique, "cancel", "menu"), markup.Data("收藏同步", discoveryCallbackUnique, "favorites", "menu")),
	)
	return "🎵 找回遗珠", markup
}

func rediscoverResultRecoveryMarkup(platform string, limit int) *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	markup.Inline(
		markup.Row(
			markup.Data("再来一首", discoveryCallbackUnique, "find", platform, strconv.Itoa(limit)),
			markup.Data("换平台", discoveryCallbackUnique, "source", "menu"),
		),
		markup.Row(markup.Data("收藏同步", discoveryCallbackUnique, "favorites", "menu")),
	)
	return markup
}

func validRediscoverPlatform(platform string) bool {
	return platform == "netease" || platform == "qq" || platform == "youtube"
}

func validRediscoverLimit(limit int) bool {
	return limit == 1
}

func (app *BotApp) goDiscovery(fn func()) {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				utils.WarnWithFormat("[Discovery] 后台操作失败: %v", recovered)
			}
		}()
		fn()
	}()
}

func (app *BotApp) startRediscover(c tb.Context, platform string, args []string) error {
	if len(args) != 3 || !validRediscoverPlatform(platform) {
		return c.Respond(&tb.CallbackResponse{Text: "平台不对，重选一下吧", ShowAlert: true})
	}
	limit, err := strconv.Atoi(args[2])
	if err != nil || !validRediscoverLimit(limit) {
		return c.Respond(&tb.CallbackResponse{Text: "重新点一下找回吧", ShowAlert: true})
	}
	if !app.discoveryCommandMu.TryLock() {
		return c.Respond(&tb.CallbackResponse{Text: "上一首还在找，稍等一下", ShowAlert: true})
	}
	if err = c.Respond(&tb.CallbackResponse{Text: "正在查找…"}); err != nil {
		app.discoveryCommandMu.Unlock()
		return err
	}
	if c.Message() == nil || c.Sender() == nil || c.Chat() == nil {
		app.discoveryCommandMu.Unlock()
		return c.Send("找不到原来的消息，请重新发送 /rediscover。")
	}
	progress := fmt.Sprintf("⌛ 正在从%s帮你找歌…", aichat.PlatformLabel(platform))
	if err = editDiscoveryMessage(c, progress, nil, false); err != nil {
		app.discoveryCommandMu.Unlock()
		return err
	}
	user, chat := c.Sender().ID, c.Chat().ID
	app.goDiscovery(func() {
		defer app.discoveryCommandMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		batch, findErr := app.discovery.Rediscover(ctx, user, platform, limit)
		if findErr != nil {
			_ = editDiscoveryMessage(c, "这次没找成："+discoveryPublicError(findErr)+"\n可以再试一次，或者换个平台。", rediscoverResultRecoveryMarkup(platform, limit), false)
			return
		}
		if len(batch.Tracks) == 0 {
			_ = editDiscoveryMessage(c, rediscoverEmptyCopy(platform, batch), rediscoverResultRecoveryMarkup(platform, limit), false)
			return
		}
		card, saveErr := app.discovery.Store.SaveCard(ctx, discovery.Card{
			User: user, Chat: chat, Kind: "recommend", Tracks: batch.Tracks,
			SkippedOwned: batch.SkippedOwned, Unconfirmed: batch.Unconfirmed,
			SkippedShown: batch.SkippedShown, SkippedPending: batch.SkippedPending,
			SkippedDismissed: batch.SkippedDismissed, SkippedQueued: batch.SkippedQueued,
			SkippedArtistCap: batch.SkippedArtistCap, SkippedDuplicate: batch.SkippedDuplicate,
			SkippedOther: batch.SkippedOther, Unavailable: batch.Unavailable, Requested: limit,
		})
		if saveErr != nil {
			_ = editDiscoveryMessage(c, "结果没保存好，再试一次吧。", rediscoverResultRecoveryMarkup(platform, limit), false)
			return
		}
		text, markup := discoveryRecommendationCard(card, 0)
		if err := editDiscoveryMessage(c, text, markup, true); err != nil {
			utils.WarnWithFormat("[Discovery] 展示遗珠失败: %v", err)
			return
		}
		for _, track := range batch.Tracks {
			if err := app.discovery.Store.Preference(ctx, user, track, false); err != nil {
				utils.WarnWithFormat("[Discovery] 记录已展示的遗珠失败: %v", err)
			}
		}
	})
	return nil
}

const discoveryPageSize = 1

func discoveryRecommendationCard(card discovery.Card, page int) (string, *tb.ReplyMarkup) {
	if page < 0 || page >= len(card.Tracks) {
		page = 0
	}
	markup := &tb.ReplyMarkup{}
	if len(card.Tracks) == 0 {
		text := rediscoverHeading(nil)
		if line := rediscoverShortfallLine(card); line != "" {
			text += "\n" + line
		}
		return text, markup
	}
	track := card.Tracks[page]
	var text strings.Builder
	fmt.Fprintf(&text, "🎵 <b>%s</b>\n", utils.EscapeHTML(rediscoverHeading(card.Tracks)))
	text.WriteString("──────────────────\n")
	if len(card.Tracks) > 1 {
		fmt.Fprintf(&text, "本批 %d 首 · 第 %d 首\n", len(card.Tracks), page+1)
	}
	text.WriteByte('\n')
	fmt.Fprintf(&text, "歌曲: %s\n", discoveryListenTitle(track.Title, track.URL))
	if artist := strings.TrimSpace(track.Artist); artist != "" {
		fmt.Fprintf(&text, "艺人: %s\n", utils.EscapeHTML(utils.TruncateString(artist, 32)))
	}
	if album := strings.TrimSpace(track.Album); album != "" {
		fmt.Fprintf(&text, "专辑: %s\n", utils.EscapeHTML(utils.TruncateString(album, 32)))
	}
	if duration := rediscoverDuration(track.Duration); duration != "" {
		fmt.Fprintf(&text, "时长: %s\n", duration)
	}
	if track.Plays > 0 {
		fmt.Fprintf(&text, "次数: %d次\n", track.Plays)
	}
	if status := rediscoverDecision(track); status != "" {
		text.WriteByte('\n')
		fmt.Fprintf(&text, "%s\n", status)
	}
	discoverySongButtons(markup, card, page)
	return strings.TrimRight(text.String(), "\n"), markup
}

// discoverySongButtons keeps one song's actions on that song.
// 入库 is the primary row, paging sits under it, and 不再推荐 is alone on the last row.
func discoverySongButtons(markup *tb.ReplyMarkup, card discovery.Card, page int) {
	if page < 0 || page >= len(card.Tracks) {
		return
	}
	track := card.Tracks[page]
	index := strconv.Itoa(page)
	if track.CardAction == "" {
		appendDiscoveryRows(markup, markup.Row(
			markup.Data("入库", discoveryCallbackUnique, "download", card.Token, index),
			markup.Data("不再推荐", discoveryCallbackUnique, "ignore", card.Token, index),
		))
	}
	if track.CardAction == "dismissed" {
		appendDiscoveryRows(markup, markup.Row(
			markup.Data("恢复推荐", discoveryCallbackUnique, "restore", card.Token, index),
		))
	}
	rediscoverActionRow := tb.Row{}
	if platform := rediscoverCardPlatform(card.Tracks); validRediscoverPlatform(platform) {
		rediscoverActionRow = append(rediscoverActionRow, markup.Data("重新拾遗", discoveryCallbackUnique, "find", platform, strconv.Itoa(rediscoverCardRequested(card))))
	}
	rediscoverActionRow = append(rediscoverActionRow, markup.Data("取消", discoveryCallbackUnique, "cancel", "card"))
	appendDiscoveryRows(markup, rediscoverActionRow)
}

func rediscoverCardRequested(card discovery.Card) int {
	if validRediscoverLimit(card.Requested) {
		return card.Requested
	}
	return 1
}

func allRediscoverSettled(tracks []discovery.Track) bool {
	if len(tracks) == 0 {
		return false
	}
	for _, track := range tracks {
		if !rediscoverSettled(track.CardAction) {
			return false
		}
	}
	return true
}

func rediscoverCardPlatform(tracks []discovery.Track) string {
	for _, track := range tracks {
		if validRediscoverPlatform(track.Platform) {
			return track.Platform
		}
	}
	return ""
}

func rediscoverHeading(tracks []discovery.Track) string {
	platform := ""
	for _, track := range tracks {
		if track.Platform == "" {
			continue
		}
		if platform == "" {
			platform = track.Platform
			continue
		}
		if platform != track.Platform {
			return "遗珠"
		}
	}
	if platform == "" {
		return "遗珠"
	}
	return "遗珠 · " + aichat.PlatformLabel(platform)
}

func rediscoverDuration(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

func rediscoverDecision(track discovery.Track) string {
	switch track.CardAction {
	case "":
		return ""
	case "dismissed":
		return "🚫 不再推荐"
	case discovery.Queued:
		return "🕒 排队中..."
	case discovery.Running:
		return "⏳ 正在整理入库..."
	case discovery.PendingUpload:
		return "☁️ 等待上传..."
	case discovery.Completed:
		return "✅ 已入库"
	case discovery.Owned:
		return "✅ 曲库已有"
	case discovery.NeedsConfirmation:
		return "⚠️ 待确认"
	case discovery.Ignored:
		return "已跳过"
	case discovery.Failed:
		note := strings.Join(strings.Fields(track.IngestNote), " ")
		if note == "" {
			return "⚠️ 入库失败"
		}
		return "⚠️ 入库失败 · " + utils.EscapeHTML(utils.TruncateString(note, 50))
	default:
		return ""
	}
}

func rediscoverTrackFields(track discovery.Track) string {
	var text strings.Builder
	fmt.Fprintf(&text, "歌曲: %s\n", discoveryListenTitle(track.Title, track.URL))
	if artist := strings.TrimSpace(track.Artist); artist != "" {
		fmt.Fprintf(&text, "艺人: %s\n", utils.EscapeHTML(utils.TruncateString(artist, 32)))
	}
	if album := strings.TrimSpace(track.Album); album != "" {
		fmt.Fprintf(&text, "专辑: %s\n", utils.EscapeHTML(utils.TruncateString(album, 32)))
	}
	if platform := strings.TrimSpace(track.Platform); platform != "" {
		fmt.Fprintf(&text, "平台: %s\n", utils.EscapeHTML(aichat.PlatformLabel(platform)))
	}
	return text.String()
}

func rediscoverNeedsConfirmation(tracks []discovery.Track) bool {
	for _, track := range tracks {
		if track.CardAction == discovery.NeedsConfirmation {
			return true
		}
	}
	return false
}

func rediscoverSettled(action string) bool {
	switch action {
	case discovery.Completed, discovery.Owned, discovery.Failed, discovery.Ignored, discovery.NeedsConfirmation, "dismissed":
		return true
	default:
		return false
	}
}

type rediscoverSkipCounts struct {
	Owned, Unconfirmed, Shown, Pending, Dismissed, Queued, ArtistCap, Duplicate, Other, Unavailable int
}

func (c rediscoverSkipCounts) notes() []string {
	var notes []string
	if c.Owned > 0 {
		notes = append(notes, fmt.Sprintf("%d 首曲库已有", c.Owned))
	}
	if queued := c.Queued + c.Pending; queued > 0 {
		notes = append(notes, fmt.Sprintf("%d 首正在排队或等上传", queued))
	}
	if c.Shown > 0 {
		notes = append(notes, fmt.Sprintf("%d 首最近看过", c.Shown))
	}
	if handled := c.Dismissed + c.Other; handled > 0 {
		notes = append(notes, fmt.Sprintf("%d 首你设了不再推荐", handled))
	}
	if uncertain := c.Unconfirmed + c.Unavailable; uncertain > 0 {
		notes = append(notes, fmt.Sprintf("%d 首版本无法确认", uncertain))
	}
	if limited := c.ArtistCap + c.Duplicate; limited > 0 {
		notes = append(notes, fmt.Sprintf("%d 首因为重复或同一位歌手先留着", limited))
	}
	return notes
}

// rediscoverCardIssues only mentions things the user can understand at a
// glance. Owned songs, recent previews and dismissed songs are not worth a wall
// of counters on every card.
func (c rediscoverSkipCounts) cardIssues() []string {
	var issues []string
	if queued := c.Queued + c.Pending; queued > 0 {
		issues = append(issues, fmt.Sprintf("%d 首还在排队或等上传", queued))
	}
	if uncertain := c.Unconfirmed + c.Unavailable; uncertain > 0 {
		issues = append(issues, fmt.Sprintf("%d 首没找到确定的版本", uncertain))
	}
	if limited := c.ArtistCap + c.Duplicate; limited > 0 {
		issues = append(issues, "有几首因为重复或同一个歌手出现太多，先跳过了")
	}
	return issues
}

func (c rediscoverSkipCounts) emptyReasons() []string {
	var reasons []string
	if queued := c.Queued + c.Pending; queued > 0 {
		reasons = append(reasons, fmt.Sprintf("还有 %d 首在等上传。", queued))
	}
	if uncertain := c.Unconfirmed + c.Unavailable; uncertain > 0 {
		reasons = append(reasons, fmt.Sprintf("有 %d 首没找到确定的版本，先不推了。", uncertain))
	}
	if limited := c.ArtistCap + c.Duplicate; limited > 0 {
		reasons = append(reasons, "有几首因为重复或同一个歌手出现太多，先跳过了。")
	}
	if c.Owned+c.Shown+c.Dismissed+c.Other > 0 {
		reasons = append(reasons, "能找的基本曲库里都有了。")
	}
	return reasons
}

func rediscoverShortfallText(skippedOwned, unconfirmed, found, requested int) string {
	return rediscoverShortfallTextWithCounts(rediscoverSkipCounts{Owned: skippedOwned, Unconfirmed: unconfirmed}, found, requested)
}

func rediscoverShortfallTextWithCounts(counts rediscoverSkipCounts, found, requested int) string {
	issues := counts.cardIssues()
	if len(issues) == 0 {
		return ""
	}
	return "这首怎么样？另外" + strings.Join(issues, "，") + "。"
}

func rediscoverShortfallLine(card discovery.Card) string {
	return rediscoverShortfallTextWithCounts(rediscoverSkipCounts{
		Owned: card.SkippedOwned, Unconfirmed: card.Unconfirmed, Shown: card.SkippedShown,
		Pending: card.SkippedPending, Dismissed: card.SkippedDismissed, Queued: card.SkippedQueued,
		ArtistCap: card.SkippedArtistCap, Duplicate: card.SkippedDuplicate,
		Other: card.SkippedOther, Unavailable: card.Unavailable,
	}, len(card.Tracks), card.Requested)
}

func rediscoverEmptyCopy(platform string, batch discovery.RediscoverBatch) string {
	name := aichat.PlatformLabel(platform)
	counts := rediscoverSkipCounts{
		Owned: batch.SkippedOwned, Unconfirmed: batch.Unconfirmed, Shown: batch.SkippedShown,
		Pending: batch.SkippedPending, Dismissed: batch.SkippedDismissed, Queued: batch.SkippedQueued,
		ArtistCap: batch.SkippedArtistCap, Duplicate: batch.SkippedDuplicate,
		Other: batch.SkippedOther, Unavailable: batch.Unavailable,
	}
	if reasons := counts.emptyReasons(); len(reasons) > 0 {
		return name + "这边暂时没合适的。" + strings.Join(reasons, "") + "换个平台，或者晚点再来看看。"
	}
	return name + "这边暂时没找到合适的。换个平台，或者晚点再来看看。"
}

func discoveryListenTitle(title, rawURL string) string {
	shown := utils.EscapeHTML(utils.TruncateString(title, 42))
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return shown
	}
	href := strings.ReplaceAll(utils.EscapeHTML(rawURL), `"`, "&quot;")
	return `<a href="` + href + `">` + shown + `</a>`
}

func (app *BotApp) FavoritesCommand(c tb.Context) error {
	if err := app.discoveryAvailable(c); err != nil {
		return c.Send(err.Error())
	}
	args := c.Args()
	if len(args) == 2 && args[0] == "bind" {
		return app.previewFavoriteBinding(c, args[1])
	}
	return app.showFavorites(c)
}

func (app *BotApp) showFavorites(c tb.Context) error {
	return app.presentFavorites(c, "")
}

func (app *BotApp) presentFavorites(c tb.Context, notice string) error {
	text, markup, err := app.renderFavorites(context.Background(), c.Sender().ID)
	if err != nil {
		return answerOnCard(c, utils.EscapeHTML(err.Error()), nil, true)
	}
	if notice != "" {
		text = utils.EscapeHTML(notice) + "\n\n" + text
	}
	return answerOnCard(c, text, markup, true)
}

func answerOnCard(c tb.Context, text string, markup *tb.ReplyMarkup, html bool) error {
	if c.Callback() != nil && c.Message() != nil {
		return editDiscoveryMessage(c, text, markup, html)
	}
	opts := []any{}
	if html {
		opts = append(opts, tb.ModeHTML)
	}
	if markup != nil {
		opts = append(opts, markup)
	}
	return c.Send(text, opts...)
}

func editDiscoveryMessage(c tb.Context, text string, markup *tb.ReplyMarkup, html bool) error {
	opts := []any{}
	if html {
		opts = append(opts, tb.ModeHTML)
	} else {
		opts = append(opts, tb.NoPreview)
	}
	if markup != nil {
		opts = append(opts, markup)
	} else {
		opts = append(opts, &tb.ReplyMarkup{})
	}
	err := c.Edit(text, opts...)
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}

func (app *BotApp) renderFavorites(ctx context.Context, user int64) (string, *tb.ReplyMarkup, error) {
	bindings, err := app.discovery.Store.Bindings(ctx, user)
	if err != nil {
		return "", nil, errors.New("读取收藏状态失败")
	}
	counts, err := app.discovery.Store.JobCounts(ctx, user)
	if err != nil {
		return "", nil, errors.New("读取入库队列失败")
	}
	var text strings.Builder
	text.WriteString("🎵 <b>收藏同步</b>\n")
	text.WriteString("──────────────────\n")
	if !app.discovery.Enabled {
		text.WriteString("自动同步：未启用\n")
	}
	byPlatform := map[string]discovery.Binding{}
	for _, binding := range bindings {
		byPlatform[binding.Platform] = binding
	}
	markup := &tb.ReplyMarkup{}
	for _, platform := range []string{"netease", "qq", "youtube"} {
		binding, ok := byPlatform[platform]
		name := aichat.PlatformLabel(platform)
		if !ok {
			fmt.Fprintf(&text, "<b>%s</b> · 未绑定\n", utils.EscapeHTML(name))
			appendDiscoveryRows(markup, markup.Row(markup.Data("绑定"+strings.ReplaceAll(name, " ", ""), discoveryCallbackUnique, "probe", platform)))
			continue
		}
		fmt.Fprintf(&text, "<b>%s</b> · %s", utils.EscapeHTML(name), utils.EscapeHTML(favoriteCardState(app.discovery.Enabled, binding.PausedByUser)))
		if account := maskedDiscoveryAccount(binding.Account); account != "" {
			fmt.Fprintf(&text, " · %s", utils.EscapeHTML(account))
		}
		text.WriteByte('\n')
		if binding.LastSync > 0 {
			fmt.Fprintf(&text, "上次同步：%s\n", time.Unix(binding.LastSync, 0).Local().Format("01-02 15:04"))
		}
		if reason := favoritePlatformError(platform, binding.Error); reason != "" {
			fmt.Fprintf(&text, "异常：%s\n", utils.EscapeHTML(utils.TruncateString(reason, 60)))
		}
		action, verb := "pause", "暂停"
		if binding.PausedByUser {
			action, verb = "resume", "恢复"
		}
		appendDiscoveryRows(markup, markup.Row(
			markup.Data("重绑", discoveryCallbackUnique, "probe", platform),
			markup.Data(verb, discoveryCallbackUnique, action, platform),
		))
	}
	var queue []string
	for _, state := range []string{discovery.Queued, discovery.Running, discovery.PendingUpload, discovery.NeedsConfirmation, discovery.Failed} {
		if counts[state] > 0 {
			label := discoveryStateShortLabel(state)
			queue = append(queue, fmt.Sprintf("%d %s", counts[state], label))
		}
	}
	text.WriteByte('\n')
	if len(queue) > 0 {
		fmt.Fprintf(&text, "入库：%s\n", utils.EscapeHTML(strings.Join(queue, " · ")))
	} else {
		text.WriteString("入库：暂无任务\n")
	}
	actions := tb.Row{markup.Data("找回遗珠", discoveryCallbackUnique, "history", "menu")}
	if app.discovery.Enabled {
		actions = append(actions, markup.Data("同步全部", discoveryCallbackUnique, "sync", "menu"))
	}
	appendDiscoveryRows(markup, actions)
	if counts[discovery.NeedsConfirmation] > 0 {
		appendDiscoveryRows(markup, markup.Row(markup.Data("处理待确认", discoveryCallbackUnique, "pending", "0")))
	}
	if counts[discovery.Failed] > 0 {
		appendDiscoveryRows(markup, markup.Row(
			markup.Data("查看异常", discoveryCallbackUnique, "failed", "0"),
			markup.Data("重试", discoveryCallbackUnique, "retry", "menu"),
		))
	}
	if counts[discovery.PendingUpload] > 0 {
		appendDiscoveryRows(markup, markup.Row(
			markup.Data("查看待上传", discoveryCallbackUnique, "pending_upload", "0"),
			markup.Data("检查上传", discoveryCallbackUnique, "status", "menu"),
		))
	}
	return text.String(), markup, nil
}

func discoveryStateShortLabel(state string) string {
	switch state {
	case discovery.Queued:
		return "排队"
	case discovery.Running:
		return "下载中"
	case discovery.PendingUpload:
		return "待上传"
	case discovery.NeedsConfirmation:
		return "待确认"
	case discovery.Failed:
		return "异常"
	default:
		return discovery.StateLabel(state)
	}
}

func discoveryFavoritesShortcut() *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	markup.Inline(markup.Row(markup.Data("查看收藏同步", discoveryCallbackUnique, "favorites", "menu")))
	return markup
}

func favoritesSyncNotice(fresh []discovery.FreshFavorite) string {
	if len(fresh) == 0 {
		return "没有新收藏，曲库先不动。"
	}
	platforms := map[string]struct{}{}
	for _, item := range fresh {
		platforms[aichat.PlatformLabel(item.Platform)] = struct{}{}
	}
	names := make([]string, 0, len(platforms))
	for name := range platforms {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Sprintf("✅ 发现 %d 首新收藏（%s），已排队入库。", len(fresh), strings.Join(names, "、"))
}

func favoritesSyncResultNotice(results []discovery.FavoriteSyncResult, syncErr error) string {
	if len(results) == 0 {
		if syncErr != nil {
			return "这次没同步成：" + favoriteErrorText(discoveryPublicError(syncErr))
		}
		return "还没绑定收藏账号。"
	}
	order := map[string]int{"netease": 0, "qq": 1, "youtube": 2}
	results = append([]discovery.FavoriteSyncResult(nil), results...)
	sort.SliceStable(results, func(i, j int) bool {
		left, leftOK := order[results[i].Platform]
		right, rightOK := order[results[j].Platform]
		if leftOK && rightOK {
			return left < right
		}
		if leftOK != rightOK {
			return leftOK
		}
		return results[i].Platform < results[j].Platform
	})
	lines := make([]string, 0, len(results))
	failed := 0
	for _, result := range results {
		name := aichat.PlatformLabel(result.Platform)
		switch result.Status {
		case "synced":
			if result.Added > 0 {
				lines = append(lines, fmt.Sprintf("%s：成功，新增 %d 首", name, result.Added))
			} else {
				lines = append(lines, name+"：成功，没有新收藏")
			}
		case "paused":
			lines = append(lines, name+"：已暂停，未读取")
		case "failed":
			failed++
			reason := favoriteErrorText(result.Error)
			if reason == "" {
				reason = "读取失败"
			}
			lines = append(lines, fmt.Sprintf("%s：读取失败，基线未改（%s）", name, utils.TruncateString(reason, 60)))
		}
	}
	head := "✅ 同步完成"
	if failed > 0 {
		head = "⚠️ 部分平台同步完成"
	}
	return head + "\n" + strings.Join(lines, "\n")
}

func favoriteCardState(enabled, paused bool) string {
	switch {
	case paused:
		return "已暂停"
	case !enabled:
		return "已绑定"
	default:
		return "已开启"
	}
}

func favoriteErrorText(message string) string {
	message = strings.Join(strings.Fields(strings.TrimSpace(message)), " ")
	for _, platform := range []string{"netease", "qq", "youtube"} {
		label := aichat.PlatformLabel(platform)
		message = strings.ReplaceAll(message, platform+"：", label+"：")
		message = strings.ReplaceAll(message, platform+":", label+"：")
	}
	return message
}

func favoritePlatformError(platform, message string) string {
	message = favoriteErrorText(message)
	prefix := aichat.PlatformLabel(platform) + "："
	if strings.HasPrefix(message, prefix) {
		message = strings.TrimSpace(strings.TrimPrefix(message, prefix))
	}
	return message
}

func maskedDiscoveryAccount(s string) string {
	runes := []rune(s)
	if len(runes) <= 6 {
		return s
	}
	return string(runes[:3]) + "…" + string(runes[len(runes)-3:])
}

func discoveryPublicError(err error) string {
	if err == nil {
		return "操作失败"
	}
	return utils.TruncateString(strings.Join(strings.Fields(err.Error()), " "), 120)
}

func favoriteBindPreview(platform, account string, count int, enabled bool) string {
	text := fmt.Sprintf("%s 收藏预览\n账号：%s\n读取到 %d 首收藏。\n\n确认后会建立同步基线。这批已有收藏不会下载。\n", aichat.PlatformLabel(platform), maskedDiscoveryAccount(account), count)
	if enabled {
		return text + "之后新增的收藏会进入入库队列。"
	}
	return text + "自动同步未启用，新增收藏也不会自动入库。"
}

func favoriteBindMarkup(platform, token string) *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	back := markup.Data("返回", discoveryCallbackUnique, "favorites", "menu")
	if token == "" {
		markup.Inline(
			markup.Row(markup.Data("重新读取", discoveryCallbackUnique, "probe", platform)),
			markup.Row(back),
		)
		return markup
	}
	markup.Inline(
		markup.Row(markup.Data("确认账号并建立基线", discoveryCallbackUnique, "bind", token)),
		markup.Row(back),
	)
	return markup
}

func logFavoriteRead(platform string, count int, elapsed time.Duration, err error) {
	elapsed = elapsed.Round(time.Millisecond)
	label := aichat.PlatformLabel(platform)
	if err != nil {
		utils.WarnWithFormat("[Discovery] 读取%s收藏失败，耗时 %s: %v", label, elapsed, err)
		return
	}
	utils.InfoWithFormat("[Discovery] 已读取%s收藏 %d 首，耗时 %s", label, count, elapsed)
}

func logFavoriteBind(platform string, count int, err error) {
	label := aichat.PlatformLabel(platform)
	if err != nil {
		utils.WarnWithFormat("[Discovery] 绑定%s收藏失败: %v", label, err)
		return
	}
	utils.InfoWithFormat("[Discovery] 已绑定%s收藏 %d 首，这批已有歌曲不下载", label, count)
}

func logFavoritePause(platform string, paused bool, err error) {
	label := aichat.PlatformLabel(platform)
	action := "恢复"
	if paused {
		action = "暂停"
	}
	if err != nil {
		utils.WarnWithFormat("[Discovery] %s%s收藏同步失败: %v", action, label, err)
		return
	}
	utils.InfoWithFormat("[Discovery] 已%s%s收藏同步", action, label)
}

func logFavoriteRetry(failed int, err error) {
	if err != nil {
		utils.WarnWithFormat("[Discovery] 失败任务重新排队失败: %v", err)
		return
	}
	utils.InfoWithFormat("[Discovery] %d 首失败任务已重新排队", failed)
}

func logFavoriteResolve(accept bool, err error) {
	action := "不再处理"
	if accept {
		action = "确认入库"
	}
	if err != nil {
		utils.WarnWithFormat("[Discovery] 待确认歌曲%s失败: %v", action, err)
		return
	}
	utils.InfoWithFormat("[Discovery] 待确认歌曲已%s", action)
}

func (app *BotApp) previewFavoriteBinding(c tb.Context, platform string) error {
	if platform != "netease" && platform != "qq" && platform != "youtube" {
		return answerOnCard(c, "不支持这个平台。可选：网易云、QQ 音乐或 YouTube。", nil, false)
	}
	if c.Sender() == nil || c.Chat() == nil {
		return answerOnCard(c, "无法读取当前会话，请重新发送 /favorites。", nil, false)
	}
	if !app.discoveryCommandMu.TryLock() {
		return app.presentFavorites(c, "⏳ 另一项操作正在处理，请稍后再试。")
	}
	if err := editDiscoveryMessage(c, "⌛ 正在读取完整收藏列表…\n这一步只预览，不会下载。", nil, false); err != nil && (c.Callback() == nil || c.Message() == nil) {
		msg, sendErr := c.Bot().Send(c.Chat(), "⌛ 正在读取完整收藏列表…\n这一步只预览，不会下载。")
		if sendErr != nil {
			app.discoveryCommandMu.Unlock()
			return sendErr
		}
		user, chat := c.Sender().ID, c.Chat().ID
		enabled := app.discovery.Enabled
		bot := c.Bot()
		app.goDiscovery(func() {
			defer app.discoveryCommandMu.Unlock()
			app.finishFavoritePreview(bot, msg, user, chat, platform, enabled)
		})
		return nil
	} else if err != nil {
		app.discoveryCommandMu.Unlock()
		return err
	}
	user, chat := c.Sender().ID, c.Chat().ID
	enabled := app.discovery.Enabled
	app.goDiscovery(func() {
		defer app.discoveryCommandMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		started := time.Now()
		snap, fetchErr := app.discovery.Provider.Fetch(ctx, platform)
		if fetchErr != nil {
			logFavoriteRead(platform, 0, time.Since(started), fetchErr)
			_ = editDiscoveryMessage(c, "❌ 读取收藏失败："+discoveryPublicError(fetchErr)+"\n可以检查账号登录状态后重试。", favoriteBindMarkup(platform, ""), false)
			return
		}
		card, saveErr := app.discovery.Store.SaveCard(ctx, discovery.Card{User: user, Chat: chat, Kind: "binding", Snapshot: &snap})
		if saveErr != nil {
			utils.WarnWithFormat("[Discovery] 保存%s收藏预览失败: %v", aichat.PlatformLabel(platform), saveErr)
			_ = editDiscoveryMessage(c, "❌ 无法保存绑定预览，可以重试。", favoriteBindMarkup(platform, ""), false)
			return
		}
		logFavoriteRead(platform, len(snap.Tracks), time.Since(started), nil)
		_ = editDiscoveryMessage(c, favoriteBindPreview(platform, snap.Account, len(snap.Tracks), enabled), favoriteBindMarkup(platform, card.Token), false)
	})
	return nil
}

func (app *BotApp) finishFavoritePreview(bot tb.API, message *tb.Message, user, chat int64, platform string, enabled bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fail := func(text string, markup *tb.ReplyMarkup) {
		if bot == nil || message == nil {
			return
		}
		_, _ = bot.Edit(message, text, markup)
	}
	started := time.Now()
	snap, err := app.discovery.Provider.Fetch(ctx, platform)
	if err != nil {
		logFavoriteRead(platform, 0, time.Since(started), err)
		fail("❌ 读取收藏失败："+discoveryPublicError(err)+"\n可以检查账号登录状态后重试。", favoriteBindMarkup(platform, ""))
		return
	}
	card, err := app.discovery.Store.SaveCard(ctx, discovery.Card{User: user, Chat: chat, Kind: "binding", Snapshot: &snap})
	if err != nil {
		utils.WarnWithFormat("[Discovery] 保存%s收藏预览失败: %v", aichat.PlatformLabel(platform), err)
		fail("❌ 无法保存绑定预览，可以重试。", favoriteBindMarkup(platform, ""))
		return
	}
	logFavoriteRead(platform, len(snap.Tracks), time.Since(started), nil)
	fail(favoriteBindPreview(platform, snap.Account, len(snap.Tracks), enabled), favoriteBindMarkup(platform, card.Token))
}

func (app *BotApp) HandleDiscoveryCallback(c tb.Context) error {
	if err := app.discoveryAvailable(c); err != nil {
		return c.Respond(&tb.CallbackResponse{Text: err.Error(), ShowAlert: true})
	}
	args := c.Args()
	if len(args) < 2 {
		return c.Respond(&tb.CallbackResponse{Text: "按钮无效，请重新发送 /favorites 或 /rediscover", ShowAlert: true})
	}
	action, key := args[0], args[1]
	callbackNotice := "正在处理…"
	switch action {
	case "probe":
		callbackNotice = "正在读取收藏账号…"
	case "pause":
		callbackNotice = "正在暂停自动同步…"
	case "resume":
		callbackNotice = "正在恢复自动同步…"
	case "sync":
		callbackNotice = "开始同步收藏…"
	case "history":
		callbackNotice = "打开遗珠查找…"
	case "favorites":
		callbackNotice = "返回收藏同步…"
	case "retry":
		callbackNotice = "正在重试入库异常…"
	case "status":
		callbackNotice = "正在刷新队列状态…"
	case "pending_upload":
		callbackNotice = "正在打开待上传…"
	case "accept":
		callbackNotice = "正在确认歌曲…"
	case "reject":
		callbackNotice = "正在标为不再处理…"
	case "failed":
		callbackNotice = "正在打开失败记录…"
	}
	switch action {
	case "source":
		if key == "menu" {
			text, markup := discoveryPlatformMenu()
			if err := c.Respond(); err != nil {
				return err
			}
			return c.Edit(text, markup)
		}
		if !validRediscoverPlatform(key) {
			return c.Respond(&tb.CallbackResponse{Text: "平台无效", ShowAlert: true})
		}
		if err := c.Respond(&tb.CallbackResponse{Text: "正在帮你找歌…"}); err != nil {
			return err
		}
		return app.startRediscover(c, key, []string{"find", key, "1"})
	case "find":
		return app.startRediscover(c, key, args)
	case "cancel":
		if err := c.Respond(); err != nil {
			return err
		}
		if key == "card" {
			return c.Edit("已取消。", &tb.ReplyMarkup{})
		}
		return c.Edit("已取消。", &tb.ReplyMarkup{})
	case "pending":
		page, e := strconv.Atoi(key)
		if e != nil || page < 0 || page > 10000 {
			return c.Respond(&tb.CallbackResponse{Text: "页码无效", ShowAlert: true})
		}
		if err := c.Respond(&tb.CallbackResponse{Text: "正在打开待确认歌曲…"}); err != nil {
			return err
		}
		return app.showDiscoveryPending(c, page)
	case "failed":
		page, e := strconv.Atoi(key)
		if e != nil || page < 0 || page > 10000 {
			return c.Respond(&tb.CallbackResponse{Text: "页码无效", ShowAlert: true})
		}
		if err := c.Respond(&tb.CallbackResponse{Text: "正在打开失败记录…"}); err != nil {
			return err
		}
		return app.showDiscoveryFailed(c, page)
	case "pending_upload":
		page, e := strconv.Atoi(key)
		if e != nil || page < 0 || page > 10000 {
			return c.Respond(&tb.CallbackResponse{Text: "页码无效", ShowAlert: true})
		}
		if err := c.Respond(&tb.CallbackResponse{Text: "正在打开待上传…"}); err != nil {
			return err
		}
		return app.showDiscoveryPendingUpload(c, page)
	case "probe", "pause", "resume", "sync", "history", "favorites", "retry", "status", "accept", "reject":
		if err := c.Respond(&tb.CallbackResponse{Text: callbackNotice}); err != nil {
			return err
		}
		ctx := context.Background()
		switch action {
		case "probe":
			return app.previewFavoriteBinding(c, key)
		case "pause", "resume":
			if err := app.discovery.Store.Pause(ctx, c.Sender().ID, key, action == "pause"); err != nil {
				logFavoritePause(key, action == "pause", err)
				return app.presentFavorites(c, "❌ 更新暂停状态失败："+discoveryPublicError(err))
			}
			logFavoritePause(key, action == "pause", nil)
			name := aichat.PlatformLabel(key)
			if action == "pause" {
				return app.presentFavorites(c, "✅ 已暂停"+name+"的自动同步。")
			}
			return app.presentFavorites(c, "✅ 已恢复"+name+"的自动同步。")
		case "sync":
			if !app.discoveryCommandMu.TryLock() {
				return app.presentFavorites(c, "⏳ 已有请求正在处理，请稍后查看状态。")
			}
			if err := editDiscoveryMessage(c, "⌛ <b>正在同步全部平台…</b>\n会依次读取网易云、QQ 音乐、YouTube，完成后回到这张卡片。", nil, true); err != nil {
				app.discoveryCommandMu.Unlock()
				return err
			}
			user := c.Sender().ID
			app.goDiscovery(func() {
				defer app.discoveryCommandMu.Unlock()
				syncCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				_, results, syncErr := app.discovery.SyncWithResults(syncCtx, user)
				notice := favoritesSyncResultNotice(results, syncErr)
				text, markup, renderErr := app.renderFavorites(context.Background(), user)
				if renderErr != nil {
					_ = editDiscoveryMessage(c, utils.EscapeHTML(notice)+"\n\n无法刷新收藏卡片。", discoveryFavoritesShortcut(), true)
					return
				}
				if notice != "" {
					text = utils.EscapeHTML(notice) + "\n\n" + text
				}
				_ = editDiscoveryMessage(c, text, markup, true)
			})
			return nil
		case "history":
			text, markup := discoveryPlatformMenu()
			return answerOnCard(c, text, markup, false)
		case "favorites":
			return app.presentFavorites(c, "")
		case "retry":
			jobCounts, jobErr := app.discovery.Store.JobCounts(ctx, c.Sender().ID)
			if jobErr != nil {
				return app.presentFavorites(c, "❌ 无法读取失败记录。")
			}
			failed := jobCounts[discovery.Failed]
			if failed == 0 {
				return app.presentFavorites(c, "✅ 没有需要重试的入库异常。")
			}
			if err := app.discovery.Store.Retry(ctx, c.Sender().ID); err != nil {
				logFavoriteRetry(failed, err)
				return app.presentFavorites(c, "❌ 入库异常未能重新排队："+discoveryPublicError(err))
			}
			logFavoriteRetry(failed, nil)
			return app.presentFavorites(c, fmt.Sprintf("✅ %d 首入库异常已重新排队。", failed))
		case "status":
			if err := app.discovery.Reconcile(ctx, c.Sender().ID); err != nil {
				utils.WarnWithFormat("[Discovery] 刷新入库队列失败 user=%d: %v", c.Sender().ID, err)
				return app.presentFavorites(c, "⚠️ 刷新队列状态失败："+discoveryPublicError(err))
			}
			return app.presentFavorites(c, "✅ 已刷新队列状态。")
		case "accept", "reject":
			next := 0
			if len(args) >= 3 {
				if n, convErr := strconv.Atoi(args[2]); convErr == nil && n >= 0 && n <= 10000 {
					next = n
				}
			}
			if err := app.discovery.Store.ResolveJob(ctx, key, c.Sender().ID, action == "accept"); err != nil {
				logFavoriteResolve(action == "accept", err)
				return answerOnCard(c, utils.EscapeHTML(err.Error()), nil, true)
			}
			logFavoriteResolve(action == "accept", nil)
			return app.showDiscoveryPending(c, next)
		}
	}
	card, err := app.discovery.Store.Card(context.Background(), key, c.Sender().ID, c.Chat().ID)
	if err != nil {
		_ = c.Respond(&tb.CallbackResponse{Text: "遗珠会话已过期或不可用", ShowAlert: true})
		return c.Edit("⚠️ 这批遗珠已过期或无法读取，请重新发送 /rediscover。", &tb.ReplyMarkup{})
	}
	if action == "bind" {
		if card.Kind != "binding" || card.Snapshot == nil {
			return c.Respond(&tb.CallbackResponse{Text: "绑定按钮无效", ShowAlert: true})
		}
		if err = c.Respond(&tb.CallbackResponse{Text: "正在建立基线…"}); err != nil {
			return err
		}
		card, err = app.discovery.Store.TakeCard(context.Background(), key, c.Sender().ID, c.Chat().ID)
		if err != nil {
			return app.presentFavorites(c, "收藏账号没读出来："+err.Error())
		}
		if _, _, err = app.discovery.Store.Apply(context.Background(), card.User, card.Chat, *card.Snapshot, true); err != nil {
			logFavoriteBind(card.Snapshot.Platform, len(card.Snapshot.Tracks), err)
			return app.presentFavorites(c, "收藏没保存成功："+err.Error())
		}
		logFavoriteBind(card.Snapshot.Platform, len(card.Snapshot.Tracks), nil)
		return app.presentFavorites(c, "✅ 收藏已接上。原来这批先不下载，之后新增的再处理。")
	}
	if card.Kind != "recommend" {
		return c.Respond(&tb.CallbackResponse{Text: "这个按钮失效了，重新发 /rediscover 吧", ShowAlert: true})
	}
	if action == "next" {
		if err = c.Respond(&tb.CallbackResponse{Text: "正在寻找下一批…"}); err != nil {
			return err
		}
		return app.RediscoverCommand(c)
	}
	if action == "restore" {
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "按钮参数无效", ShowAlert: true})
		}
		index, convErr := strconv.Atoi(args[2])
		if convErr != nil || index < 0 || index >= len(card.Tracks) {
			return c.Respond(&tb.CallbackResponse{Text: "曲目编号无效", ShowAlert: true})
		}
		if err = c.Respond(&tb.CallbackResponse{Text: "已恢复推荐"}); err != nil {
			return err
		}
		app.rediscoverMu.Lock()
		fresh, loadErr := app.discovery.Store.Card(context.Background(), key, c.Sender().ID, c.Chat().ID)
		if loadErr == nil {
			track := fresh.Tracks[index]
			if loadErr = app.discovery.Store.RestorePreference(context.Background(), fresh.User, track); loadErr == nil {
				fresh.Tracks[index].CardAction = ""
				loadErr = app.discovery.Store.ReplaceCard(context.Background(), fresh)
			}
			card = fresh
		}
		var editErr error
		if loadErr == nil {
			text, markup := discoveryRecommendationCard(card, index)
			editErr = editDiscoveryMessage(c, text, markup, true)
		}
		app.rediscoverMu.Unlock()
		if loadErr != nil {
			return answerOnCard(c, "没恢复成功，过会儿再试一次。", discoveryFavoritesShortcut(), false)
		}
		return editErr
	}
	if action == "page" {
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "按钮参数无效", ShowAlert: true})
		}
		page, e := strconv.Atoi(args[2])
		if e != nil || page < 0 || page > 100 {
			return c.Respond(&tb.CallbackResponse{Text: "页码无效", ShowAlert: true})
		}
		if err = c.Respond(); err != nil {
			return err
		}
		app.rediscoverMu.Lock()
		fresh, loadErr := app.discovery.Store.Card(context.Background(), key, c.Sender().ID, c.Chat().ID)
		if loadErr == nil {
			fresh.Page = page
			loadErr = app.discovery.Store.ReplaceCard(context.Background(), fresh)
			card = fresh
		}
		var editErr error
		if loadErr == nil {
			text, markup := discoveryRecommendationCard(card, page)
			editErr = editDiscoveryMessage(c, text, markup, true)
		}
		app.rediscoverMu.Unlock()
		if loadErr != nil {
			return answerOnCard(c, "进度没存上，稍后重新打开吧。", discoveryFavoritesShortcut(), false)
		}
		return editErr
	}
	type cardChoice struct {
		index int
		track discovery.Track
	}
	var choices []cardChoice
	page := 0
	switch action {
	case "download", "ignore":
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "按钮参数无效", ShowAlert: true})
		}
		index, e := strconv.Atoi(args[2])
		if e != nil || index < 0 || index >= len(card.Tracks) {
			return c.Respond(&tb.CallbackResponse{Text: "曲目编号无效", ShowAlert: true})
		}
		choices = []cardChoice{{index: index, track: card.Tracks[index]}}
		page = index / discoveryPageSize
	case "batch":
		if len(args) < 3 || len(args) > 4 {
			return c.Respond(&tb.CallbackResponse{Text: "批量参数无效", ShowAlert: true})
		}
		if args[2] == "ask" {
			page := card.Page
			if len(args) == 4 {
				if n, convErr := strconv.Atoi(args[3]); convErr == nil && n >= 0 && n < len(card.Tracks) {
					page = n
				}
			}
			if err = c.Respond(); err != nil {
				return err
			}
			text, markup := rediscoverBatchConfirm(card, page)
			return editDiscoveryMessage(c, text, markup, false)
		}
		var picked []discovery.Track
		switch {
		case args[2] == "all":
			picked = card.Tracks
			if len(args) == 4 {
				if n, convErr := strconv.Atoi(args[3]); convErr == nil && n >= 0 && n < len(card.Tracks) {
					page = n
				}
			}
		case args[2] == "3":
			picked = card.Tracks[:min(3, len(card.Tracks))]
		case strings.HasPrefix(args[2], "p"):
			parsed, e := strconv.Atoi(strings.TrimPrefix(args[2], "p"))
			if e != nil || parsed < 0 {
				return c.Respond(&tb.CallbackResponse{Text: "批量参数无效", ShowAlert: true})
			}
			page = parsed
			start := page * discoveryPageSize
			if start >= len(card.Tracks) {
				return c.Respond(&tb.CallbackResponse{Text: "没有这一首", ShowAlert: true})
			}
			picked = card.Tracks[start:min(start+discoveryPageSize, len(card.Tracks))]
			for offset := range picked {
				choices = append(choices, cardChoice{index: start + offset, track: picked[offset]})
			}
		default:
			return c.Respond(&tb.CallbackResponse{Text: "批量参数无效", ShowAlert: true})
		}
		if !strings.HasPrefix(args[2], "p") {
			for index, track := range picked {
				choices = append(choices, cardChoice{index: index, track: track})
			}
		}
	default:
		return c.Respond(&tb.CallbackResponse{Text: "未知操作", ShowAlert: true})
	}
	ack := "已开始入库"
	if action == "ignore" {
		ack = "不再推荐"
	}
	if err = c.Respond(&tb.CallbackResponse{Text: ack}); err != nil {
		return err
	}
	app.rediscoverMu.Lock()
	card, err = app.discovery.Store.Card(context.Background(), key, c.Sender().ID, c.Chat().ID)
	if err != nil {
		app.rediscoverMu.Unlock()
		return answerOnCard(c, "这批歌打不开了，重新找一次吧。", discoveryFavoritesShortcut(), false)
	}
	watch := false
	submitted, queued, skipped, failed := 0, 0, 0, 0
	failureNote := ""
	for _, choice := range choices {
		if choice.index < 0 || choice.index >= len(card.Tracks) || card.Tracks[choice.index].CardAction != "" {
			continue
		}
		submitted++
		track := card.Tracks[choice.index]
		track.CardAction = ""
		track.IngestID = ""
		track.IngestNote = ""
		if action == "ignore" {
			err = app.discovery.Store.Preference(context.Background(), card.User, track, true)
			if err == nil {
				card.Tracks[choice.index].CardAction = "dismissed"
				skipped++
			}
		} else {
			var id string
			id, err = app.discovery.Store.Enqueue(context.Background(), card.User, card.Chat, track)
			if err == nil {
				queued++
				watch = true
				card.Tracks[choice.index].IngestID = id
				card.Tracks[choice.index].IngestNote = ""
				card.Tracks[choice.index].CardAction = discovery.Queued
			}
		}
		if err != nil {
			failed++
			failureNote = err.Error()
			if action != "ignore" {
				card.Tracks[choice.index].CardAction = discovery.Failed
				card.Tracks[choice.index].IngestNote = strings.Join(strings.Fields(err.Error()), " ")
			}
		}
	}
	card.Page = page
	if err = app.discovery.Store.ReplaceCard(context.Background(), card); err != nil {
		app.rediscoverMu.Unlock()
		return answerOnCard(c, "结果没存上，先看看收藏同步里的队列，再决定要不要重试。", discoveryFavoritesShortcut(), false)
	}
	message := c.Message()
	text, markup := discoveryRecommendationCard(card, page)
	if submitted > 0 && !(action == "ignore" && failed == 0) {
		notice := fmt.Sprintf("提交了 %d 首，排队 %d 首，跳过 %d 首", submitted, queued, skipped)
		if failed > 0 {
			notice += fmt.Sprintf("，失败 %d 首", failed)
			if failureNote != "" {
				notice += "（" + utils.TruncateString(failureNote, 80) + "）"
			}
		}
		text = utils.EscapeHTML(notice) + "\n\n" + text
	}
	editErr := editDiscoveryMessage(c, text, markup, true)
	app.rediscoverMu.Unlock()
	if watch && message != nil {
		go app.watchRediscoverCard(card.Token, card.User, card.Chat, message)
	}
	return editErr
}

func (app *BotApp) watchRediscoverCard(token string, user, chat int64, message *tb.Message) {
	if app == nil || app.bot == nil || message == nil {
		return
	}
	deadline := time.NewTimer(12 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if !app.refreshRediscoverMessage(token, user, chat, message) {
			return
		}
		select {
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

func (app *BotApp) refreshRediscoverMessage(token string, user, chat int64, message *tb.Message) bool {
	if app == nil || app.discovery == nil || app.discovery.Store == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	app.rediscoverMu.Lock()
	card, err := app.discovery.Store.Card(ctx, token, user, chat)
	if err != nil {
		app.rediscoverMu.Unlock()
		return false
	}
	updated, changed, pending, err := syncRediscoverProgress(ctx, app.discovery.Store, card)
	if err != nil {
		app.rediscoverMu.Unlock()
		return true
	}
	if changed {
		if err = app.discovery.Store.ReplaceCard(ctx, updated); err != nil {
			app.rediscoverMu.Unlock()
			return true
		}
		if app.bot != nil && message != nil {
			text, markup := discoveryRecommendationCard(updated, updated.Page)
			if _, err = app.bot.Edit(message, text, tb.ModeHTML, markup); err != nil && !strings.Contains(err.Error(), "not modified") {
				utils.WarnWithFormat("[Discovery] 更新遗珠进度失败: %v", err)
			}
		}
	}
	app.rediscoverMu.Unlock()
	return pending
}

func syncRediscoverProgress(ctx context.Context, store *discovery.Store, card discovery.Card) (discovery.Card, bool, bool, error) {
	if store == nil {
		return card, false, false, nil
	}
	changed := false
	pending := false
	for index := range card.Tracks {
		id := strings.TrimSpace(card.Tracks[index].IngestID)
		action := card.Tracks[index].CardAction
		if id == "" || action == "dismissed" || rediscoverSettled(action) {
			continue
		}
		job, err := store.Job(ctx, id)
		if err != nil {
			pending = true
			continue
		}
		note := ""
		if job.State == discovery.Failed {
			note = strings.Join(strings.Fields(job.Error), " ")
		}
		if card.Tracks[index].CardAction != job.State || card.Tracks[index].IngestNote != note {
			card.Tracks[index].CardAction = job.State
			card.Tracks[index].IngestNote = note
			changed = true
		}
		if !rediscoverSettled(job.State) {
			pending = true
		}
	}
	return card, changed, pending, nil
}

func appendDiscoveryRows(markup *tb.ReplyMarkup, rows ...tb.Row) {
	for _, row := range rows {
		var buttons []tb.InlineButton
		for _, button := range row {
			if b := button.Inline(); b != nil {
				buttons = append(buttons, *b)
			}
		}
		markup.InlineKeyboard = append(markup.InlineKeyboard, buttons)
	}
}

func (app *BotApp) showDiscoveryPending(c tb.Context, page int) error {
	if page < 0 || page > 10000 {
		return answerOnCard(c, "页码无效", nil, false)
	}
	job, index, total, err := app.discovery.Store.ConfirmationAt(context.Background(), c.Sender().ID, page)
	if err != nil {
		return answerOnCard(c, "无法读取待确认任务", nil, false)
	}
	if total == 0 {
		return answerOnCard(c, "✅ 没有待确认任务。", discoveryFavoritesShortcut(), false)
	}
	text, markup := discoveryPendingCard(job, index, total)
	return answerOnCard(c, text, markup, true)
}

func discoveryPendingCard(job discovery.Job, index, total int) (string, *tb.ReplyMarkup) {
	var text strings.Builder
	text.WriteString("⚠️ <b>待确认</b>")
	if total > 1 {
		fmt.Fprintf(&text, " · %d/%d", index+1, total)
	}
	text.WriteString("\n──────────────────\n")
	text.WriteString(rediscoverTrackFields(job.Track))
	if note := strings.TrimSpace(job.Error); note != "" {
		fmt.Fprintf(&text, "\n原因: %s", utils.EscapeHTML(utils.TruncateString(note, 120)))
	}
	markup := &tb.ReplyMarkup{}
	appendDiscoveryRows(markup, markup.Row(
		markup.Data("确认入库", discoveryCallbackUnique, "accept", job.ID, strconv.Itoa(index)),
		markup.Data("不再处理", discoveryCallbackUnique, "reject", job.ID, strconv.Itoa(index)),
	))
	nav := tb.Row{}
	if index > 0 {
		nav = append(nav, markup.Data("上一首", discoveryCallbackUnique, "pending", strconv.Itoa(index-1)))
	}
	if index+1 < total {
		nav = append(nav, markup.Data("下一首", discoveryCallbackUnique, "pending", strconv.Itoa(index+1)))
	}
	if len(nav) > 0 {
		appendDiscoveryRows(markup, nav)
	}
	appendDiscoveryRows(markup, markup.Row(markup.Data("返回收藏", discoveryCallbackUnique, "favorites", "menu")))
	return strings.TrimRight(text.String(), "\n"), markup
}

func (app *BotApp) showDiscoveryPendingUpload(c tb.Context, page int) error {
	if page < 0 {
		page = 0
	}
	jobs, err := app.discovery.Store.PendingJobs(context.Background(), c.Sender().ID)
	if err != nil {
		return answerOnCard(c, "无法读取待上传任务", nil, false)
	}
	if len(jobs) == 0 {
		return app.presentFavorites(c, "✅ 没有待上传任务。")
	}
	if page >= len(jobs) {
		page = len(jobs) - 1
	}
	text, markup := discoveryPendingUploadCard(jobs[page], page, len(jobs))
	return answerOnCard(c, text, markup, true)
}

func discoveryPendingUploadCard(job discovery.Job, index, total int) (string, *tb.ReplyMarkup) {
	var text strings.Builder
	text.WriteString("☁️ <b>待上传</b>")
	if total > 1 {
		fmt.Fprintf(&text, " · %d/%d", index+1, total)
	}
	text.WriteString("\n──────────────────\n")
	text.WriteString(rediscoverTrackFields(job.Track))
	text.WriteString("\nNAS 恢复后自动上传。")
	if note := strings.TrimSpace(job.Error); note != "" {
		fmt.Fprintf(&text, "\n原因: %s", utils.EscapeHTML(utils.TruncateString(note, 120)))
	}
	markup := &tb.ReplyMarkup{}
	appendDiscoveryRows(markup, markup.Row(
		markup.Data("检查上传", discoveryCallbackUnique, "status", "menu"),
		markup.Data("返回收藏", discoveryCallbackUnique, "favorites", "menu"),
	))
	nav := tb.Row{}
	if index > 0 {
		nav = append(nav, markup.Data("上一首", discoveryCallbackUnique, "pending_upload", strconv.Itoa(index-1)))
	}
	if index+1 < total {
		nav = append(nav, markup.Data("下一首", discoveryCallbackUnique, "pending_upload", strconv.Itoa(index+1)))
	}
	if len(nav) > 0 {
		appendDiscoveryRows(markup, nav)
	}
	return strings.TrimRight(text.String(), "\n"), markup
}

func rediscoverBatchConfirm(card discovery.Card, page int) (string, *tb.ReplyMarkup) {
	waiting := 0
	for _, track := range card.Tracks {
		if track.CardAction == "" {
			waiting++
		}
	}
	if page < 0 || page >= len(card.Tracks) {
		page = 0
	}
	title := "当前这首"
	if page >= 0 && page < len(card.Tracks) {
		if name := strings.TrimSpace(card.Tracks[page].Title); name != "" {
			title = "《" + utils.TruncateString(name, 24) + "》"
		}
	}
	text := fmt.Sprintf("剩下 %d 首都入库？\n\n包括%s。确认后我会一起放进下载队列。", waiting, title)
	markup := &tb.ReplyMarkup{}
	appendDiscoveryRows(markup,
		markup.Row(markup.Data("确认全部入库", discoveryCallbackUnique, "batch", card.Token, "all", strconv.Itoa(page))),
		markup.Row(markup.Data("返回这首", discoveryCallbackUnique, "page", card.Token, strconv.Itoa(page))),
	)
	return text, markup
}

func (app *BotApp) showDiscoveryFailed(c tb.Context, page int) error {
	if page < 0 {
		page = 0
	}
	jobs, total, err := app.discovery.Store.FailedJobs(context.Background(), c.Sender().ID, page, 1)
	if err != nil {
		return answerOnCard(c, "无法读取失败记录", nil, false)
	}
	if total == 0 {
		return app.presentFavorites(c, "✅ 没有入库异常。")
	}
	if page >= total {
		page = total - 1
		jobs, _, err = app.discovery.Store.FailedJobs(context.Background(), c.Sender().ID, page, 1)
		if err != nil || len(jobs) == 0 {
			return answerOnCard(c, "无法读取失败记录", nil, false)
		}
	}
	text, markup := discoveryFailedCard(jobs[0], page, total)
	return answerOnCard(c, text, markup, true)
}

func discoveryFailedCard(job discovery.Job, index, total int) (string, *tb.ReplyMarkup) {
	var text strings.Builder
	text.WriteString("⚠️ <b>入库异常</b>")
	if total > 1 {
		fmt.Fprintf(&text, " · %d/%d", index+1, total)
	}
	text.WriteString("\n──────────────────\n")
	text.WriteString(rediscoverTrackFields(job.Track))
	if note := strings.TrimSpace(job.Error); note != "" {
		fmt.Fprintf(&text, "\n原因: %s", utils.EscapeHTML(utils.TruncateString(note, 160)))
	} else {
		text.WriteString("\n原因: 未记录")
	}
	markup := &tb.ReplyMarkup{}
	appendDiscoveryRows(markup, markup.Row(
		markup.Data("重试", discoveryCallbackUnique, "retry", "menu"),
		markup.Data("返回收藏", discoveryCallbackUnique, "favorites", "menu"),
	))
	nav := tb.Row{}
	if index > 0 {
		nav = append(nav, markup.Data("上一首", discoveryCallbackUnique, "failed", strconv.Itoa(index-1)))
	}
	if index+1 < total {
		nav = append(nav, markup.Data("下一首", discoveryCallbackUnique, "failed", strconv.Itoa(index+1)))
	}
	if len(nav) > 0 {
		appendDiscoveryRows(markup, nav)
	}
	return strings.TrimRight(text.String(), "\n"), markup
}
