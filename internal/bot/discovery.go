package bot

import (
	"context"
	"errors"
	"fmt"
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
		Notify:  func(chat int64, text string) error { _, err := app.bot.Send(tb.ChatID(chat), text); return err },
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
	return "找回遗珠\n先选平台，再选数量。只查这个平台，找到设定数量就停。", markup
}

func discoveryCountMenu(platform string) (string, *tb.ReplyMarkup) {
	markup := &tb.ReplyMarkup{}
	appendDiscoveryRows(markup,
		markup.Row(
			markup.Data("3 首", discoveryCallbackUnique, "find", platform, "3"),
			markup.Data("6 首", discoveryCallbackUnique, "find", platform, "6"),
			markup.Data("9 首", discoveryCallbackUnique, "find", platform, "9"),
		),
		markup.Row(markup.Data("换平台", discoveryCallbackUnique, "source", "menu"), markup.Data("取消", discoveryCallbackUnique, "cancel", "menu")),
	)
	return "找回遗珠 · " + aichat.PlatformLabel(platform) + "\n选择数量；结果会逐首展示，可试听后再入库。", markup
}

func rediscoverResultRecoveryMarkup(platform string, limit int) *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	markup.Inline(
		markup.Row(
			markup.Data("再找一次", discoveryCallbackUnique, "find", platform, strconv.Itoa(limit)),
			markup.Data("换平台或数量", discoveryCallbackUnique, "source", "menu"),
		),
		markup.Row(markup.Data("收藏同步", discoveryCallbackUnique, "favorites", "menu")),
	)
	return markup
}

func validRediscoverPlatform(platform string) bool {
	return platform == "netease" || platform == "qq" || platform == "youtube"
}

func validRediscoverLimit(limit int) bool {
	return limit == 3 || limit == 6 || limit == 9
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
		return c.Respond(&tb.CallbackResponse{Text: "请重新选择数量", ShowAlert: true})
	}
	limit, err := strconv.Atoi(args[2])
	if err != nil || !validRediscoverLimit(limit) {
		return c.Respond(&tb.CallbackResponse{Text: "数量只能是 3、6 或 9", ShowAlert: true})
	}
	if !app.discoveryCommandMu.TryLock() {
		return c.Respond(&tb.CallbackResponse{Text: "正在处理上一条请求", ShowAlert: true})
	}
	if err = c.Respond(&tb.CallbackResponse{Text: "正在查找…"}); err != nil {
		app.discoveryCommandMu.Unlock()
		return err
	}
	if c.Message() == nil || c.Sender() == nil || c.Chat() == nil {
		app.discoveryCommandMu.Unlock()
		return c.Send("找不到原来的消息，请重新发送 /rediscover。")
	}
	progress := fmt.Sprintf("⌛ 正在从%s查找最多 %d 首遗珠…", aichat.PlatformLabel(platform), limit)
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
			_ = editDiscoveryMessage(c, "❌ 查找失败："+discoveryPublicError(findErr)+"\n可以重试，或换个平台和数量。", rediscoverResultRecoveryMarkup(platform, limit), false)
			return
		}
		if len(batch.Tracks) == 0 {
			_ = editDiscoveryMessage(c, rediscoverEmptyCopy(platform, batch), rediscoverResultRecoveryMarkup(platform, limit), false)
			return
		}
		card, saveErr := app.discovery.Store.SaveCard(ctx, discovery.Card{User: user, Chat: chat, Kind: "recommend", Tracks: batch.Tracks, SkippedOwned: batch.SkippedOwned, Unconfirmed: batch.Unconfirmed, Requested: limit})
		if saveErr != nil {
			_ = editDiscoveryMessage(c, "❌ 保存查找结果失败。可以重试，或换个平台和数量。", rediscoverResultRecoveryMarkup(platform, limit), false)
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
		return rediscoverHeading(nil), markup
	}
	track := card.Tracks[page]
	var text strings.Builder
	fmt.Fprintf(&text, "%s\n", rediscoverHeading(card.Tracks))
	if len(card.Tracks) > 1 {
		fmt.Fprintf(&text, "%d / %d\n", page+1, len(card.Tracks))
	}
	text.WriteByte('\n')
	fmt.Fprintf(&text, "<b>%s</b>\n", discoveryListenTitle(track.Title, track.URL))
	if artist := strings.TrimSpace(track.Artist); artist != "" {
		fmt.Fprintf(&text, "%s\n", utils.EscapeHTML(utils.TruncateString(artist, 32)))
	}
	if album := strings.TrimSpace(track.Album); album != "" {
		fmt.Fprintf(&text, "《%s》\n", utils.EscapeHTML(utils.TruncateString(album, 32)))
	}
	if fact := rediscoverFactLine(track); fact != "" {
		text.WriteByte('\n')
		fmt.Fprintf(&text, "%s\n", fact)
	}
	var notes []string
	if page == 0 && strings.TrimSpace(track.URL) != "" {
		notes = append(notes, "点歌名可打开平台页面。")
	}
	if card.SkippedOwned > 0 {
		notes = append(notes, fmt.Sprintf("已避开曲库里的 %d 首", card.SkippedOwned))
	}
	if line := rediscoverShortfallLine(card); line != "" {
		notes = append(notes, strings.TrimRight(line, "\n"))
	}
	if rediscoverNeedsConfirmation(card.Tracks) {
		notes = append(notes, "有歌曲需要确认，可点下方按钮处理。")
	}
	if len(notes) > 0 {
		text.WriteByte('\n')
		text.WriteString(strings.Join(notes, "\n"))
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
		))
	}
	nav := tb.Row{}
	if page > 0 {
		nav = append(nav, markup.Data("上一首", discoveryCallbackUnique, "page", card.Token, strconv.Itoa(page-1)))
	}
	if page+1 < len(card.Tracks) {
		nav = append(nav, markup.Data("下一首", discoveryCallbackUnique, "page", card.Token, strconv.Itoa(page+1)))
	}
	if len(nav) > 0 {
		appendDiscoveryRows(markup, nav)
	}
	waiting := 0
	for _, item := range card.Tracks {
		if item.CardAction == "" {
			waiting++
		}
	}
	if waiting > 1 {
		appendDiscoveryRows(markup, markup.Row(
			markup.Data("未处理的全部入库", discoveryCallbackUnique, "batch", card.Token, "ask", strconv.Itoa(page)),
		))
	}
	if track.CardAction == "" {
		appendDiscoveryRows(markup, markup.Row(
			markup.Data("不再推荐", discoveryCallbackUnique, "ignore", card.Token, index),
		))
	}
	if rediscoverNeedsConfirmation(card.Tracks) {
		appendDiscoveryRows(markup, markup.Row(
			markup.Data("处理待确认", discoveryCallbackUnique, "pending", "0"),
		))
	}
	if allRediscoverSettled(card.Tracks) {
		appendDiscoveryRows(markup, markup.Row(
			markup.Data("再找一次", discoveryCallbackUnique, "source", "menu"),
		))
	}
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

func rediscoverFactLine(track discovery.Track) string {
	reason := "听过"
	if track.Favorite {
		reason = "收藏"
	}
	if track.Plays > 0 {
		reason += fmt.Sprintf(" %d 次", track.Plays)
	}
	if !track.LastPlayed.IsZero() {
		reason += " · " + track.LastPlayed.Local().Format("2006-01-02")
	}
	if decision := rediscoverDecision(track); decision != "" {
		return reason + " · " + decision
	}
	return reason
}

func rediscoverDecision(track discovery.Track) string {
	switch track.CardAction {
	case "":
		return ""
	case "dismissed":
		return "不再推荐"
	case discovery.Queued:
		return "排队"
	case discovery.Running:
		return "下载中"
	case discovery.PendingUpload:
		return "待上传"
	case discovery.Completed:
		return "已入库"
	case discovery.Owned:
		return "曲库已有"
	case discovery.NeedsConfirmation:
		return "待确认"
	case discovery.Ignored:
		return "已跳过"
	case discovery.Failed:
		note := strings.Join(strings.Fields(track.IngestNote), " ")
		if note == "" {
			return "失败"
		}
		return "失败 · " + utils.EscapeHTML(utils.TruncateString(note, 80))
	default:
		return ""
	}
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

func rediscoverShortfallLine(card discovery.Card) string {
	if card.Unconfirmed <= 0 || card.Requested <= 0 || len(card.Tracks) >= card.Requested {
		return ""
	}
	return fmt.Sprintf("有 %d 首对不上唯一版本，所以没有找满 %d 首。\n", card.Unconfirmed, card.Requested)
}

func rediscoverEmptyCopy(platform string, batch discovery.RediscoverBatch) string {
	name := aichat.PlatformLabel(platform)
	switch {
	case batch.SkippedOwned > 0 && batch.Unconfirmed > 0:
		return fmt.Sprintf("这批里有 %d 首曲库已经有了，另有 %d 首在%s对不上唯一版本，没有新的可入库歌曲。可以换个平台或稍后再试。", batch.SkippedOwned, batch.Unconfirmed, name)
	case batch.SkippedOwned > 0:
		return fmt.Sprintf("这批里有 %d 首曲库已经有了，没有新的可入库歌曲。可以换个平台或稍后再试。", batch.SkippedOwned)
	case batch.Unconfirmed > 0:
		return fmt.Sprintf("有 %d 首在%s对不上唯一版本，没有新的可入库歌曲。可以换个平台或稍后再试。", batch.Unconfirmed, name)
	default:
		return "在" + name + "里，这批历史和收藏暂时没有可确认的缺失歌曲。可以换个平台或稍后再试。"
	}
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
		return answerOnCard(c, err.Error(), nil, false)
	}
	if notice != "" {
		text = notice + "\n\n" + text
	}
	return answerOnCard(c, text, markup, false)
}

func answerOnCard(c tb.Context, text string, markup *tb.ReplyMarkup, html bool) error {
	if c.Callback() != nil && c.Message() != nil {
		return editDiscoveryMessage(c, text, markup, html)
	}
	if markup != nil {
		return c.Send(text, markup)
	}
	return c.Send(text)
}

func editDiscoveryMessage(c tb.Context, text string, markup *tb.ReplyMarkup, html bool) error {
	opts := []any{}
	if html {
		opts = append(opts, tb.ModeHTML)
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
	jobs, err := app.discovery.Store.Jobs(ctx, user, false)
	if err != nil {
		return "", nil, errors.New("读取入库队列失败")
	}
	counts := map[string]int{}
	for _, job := range jobs {
		counts[job.State]++
	}
	var text strings.Builder
	text.WriteString("收藏同步\n")
	if app.discovery.Enabled {
		fmt.Fprintf(&text, "\n自动检查：每 %d 分钟\n发现新收藏时会私聊通知", max(1, int(app.discovery.PollInterval/time.Minute)))
	} else {
		text.WriteString("\n自动同步未启用\n绑定账号后，新增收藏也不会自动入库")
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
			fmt.Fprintf(&text, "\n\n%s\n未绑定", name)
			appendDiscoveryRows(markup, markup.Row(markup.Data("绑定 "+name, discoveryCallbackUnique, "probe", platform)))
			continue
		}
		state := "自动同步已开启"
		switch {
		case !app.discovery.Enabled && binding.Paused:
			state = "已暂停 · 自动同步未启用"
		case !app.discovery.Enabled:
			state = "已绑定 · 自动同步未启用"
		case binding.Paused:
			state = "自动同步已暂停"
		}
		fmt.Fprintf(&text, "\n\n%s\n%s", name, state)
		if account := maskedDiscoveryAccount(binding.Account); account != "" {
			fmt.Fprintf(&text, "\n%s", account)
		}
		if binding.Error != "" {
			fmt.Fprintf(&text, "\n%s", utils.TruncateString(binding.Error, 60))
		}
		action, button := "pause", "暂停"
		if binding.Paused {
			action, button = "resume", "恢复"
		}
		appendDiscoveryRows(markup, markup.Row(
			markup.Data("重新绑定", discoveryCallbackUnique, "probe", platform),
			markup.Data(button, discoveryCallbackUnique, action, platform),
		))
	}
	var queue []string
	for _, state := range []string{discovery.Queued, discovery.Running, discovery.PendingUpload, discovery.NeedsConfirmation, discovery.Failed} {
		if counts[state] > 0 {
			label := discovery.StateLabel(state)
			if state == discovery.PendingUpload {
				label = "待上传"
			}
			queue = append(queue, fmt.Sprintf("%d %s", counts[state], label))
		}
	}
	if len(queue) > 0 {
		fmt.Fprintf(&text, "\n\n入库队列：%s", strings.Join(queue, " · "))
	} else {
		text.WriteString("\n\n入库队列：暂无待处理任务")
	}
	actions := tb.Row{markup.Data("找回遗珠", discoveryCallbackUnique, "history", "menu")}
	if app.discovery.Enabled {
		actions = append(actions, markup.Data("现在同步", discoveryCallbackUnique, "sync", "menu"))
	}
	appendDiscoveryRows(markup, actions)
	extra := tb.Row{}
	if counts[discovery.NeedsConfirmation] > 0 {
		extra = append(extra, markup.Data("待确认", discoveryCallbackUnique, "pending", "0"))
	}
	if counts[discovery.Failed] > 0 {
		extra = append(extra, markup.Data("查看失败", discoveryCallbackUnique, "failed", "0"))
		extra = append(extra, markup.Data("重试失败", discoveryCallbackUnique, "retry", "menu"))
	}
	if counts[discovery.PendingUpload] > 0 {
		extra = append(extra, markup.Data("刷新状态", discoveryCallbackUnique, "status", "menu"))
	}
	if len(extra) > 0 {
		appendDiscoveryRows(markup, extra)
	}
	return text.String(), markup, nil
}

func discoveryFavoritesShortcut() *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	markup.Inline(markup.Row(markup.Data("查看收藏同步", discoveryCallbackUnique, "favorites", "menu")))
	return markup
}

func favoritesSyncNotice(fresh []discovery.FreshFavorite) string {
	if len(fresh) == 0 {
		return "✅ 本次没有新的收藏。"
	}
	return fmt.Sprintf("✅ 发现 %d 首新收藏，已提交入库队列。", len(fresh))
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
		snap, fetchErr := app.discovery.Provider.Fetch(ctx, platform)
		if fetchErr != nil {
			_ = editDiscoveryMessage(c, "❌ 读取收藏失败："+discoveryPublicError(fetchErr)+"\n可以检查账号登录状态后重试。", favoriteBindMarkup(platform, ""), false)
			return
		}
		card, saveErr := app.discovery.Store.SaveCard(ctx, discovery.Card{User: user, Chat: chat, Kind: "binding", Snapshot: &snap})
		if saveErr != nil {
			_ = editDiscoveryMessage(c, "❌ 无法保存绑定预览，可以重试。", favoriteBindMarkup(platform, ""), false)
			return
		}
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
	snap, err := app.discovery.Provider.Fetch(ctx, platform)
	if err != nil {
		fail("❌ 读取收藏失败："+discoveryPublicError(err)+"\n可以检查账号登录状态后重试。", favoriteBindMarkup(platform, ""))
		return
	}
	card, err := app.discovery.Store.SaveCard(ctx, discovery.Card{User: user, Chat: chat, Kind: "binding", Snapshot: &snap})
	if err != nil {
		fail("❌ 无法保存绑定预览，可以重试。", favoriteBindMarkup(platform, ""))
		return
	}
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
		callbackNotice = "正在重试失败任务…"
	case "status":
		callbackNotice = "正在刷新队列状态…"
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
		text, markup := discoveryCountMenu(key)
		if err := c.Respond(&tb.CallbackResponse{Text: aichat.PlatformLabel(key)}); err != nil {
			return err
		}
		return c.Edit(text, markup)
	case "find":
		return app.startRediscover(c, key, args)
	case "cancel":
		if err := c.Respond(); err != nil {
			return err
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
				return app.presentFavorites(c, "❌ 更新暂停状态失败："+discoveryPublicError(err))
			}
			if action == "pause" {
				return app.presentFavorites(c, "✅ 已暂停该平台的自动同步。")
			}
			return app.presentFavorites(c, "✅ 已恢复该平台的自动同步。")
		case "sync":
			if !app.discoveryCommandMu.TryLock() {
				return app.presentFavorites(c, "⏳ 已有请求正在处理，请稍后查看状态。")
			}
			if err := editDiscoveryMessage(c, "⌛ 正在同步收藏…\n完成后会回到这张卡片。", nil, false); err != nil {
				app.discoveryCommandMu.Unlock()
				return err
			}
			user := c.Sender().ID
			app.goDiscovery(func() {
				defer app.discoveryCommandMu.Unlock()
				syncCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				fresh, syncErr := app.discovery.SyncQuietly(syncCtx, user)
				notice := favoritesSyncNotice(fresh)
				if syncErr != nil {
					notice = "❌ " + discoveryPublicError(syncErr)
					if len(fresh) > 0 {
						notice += "\n" + favoritesSyncNotice(fresh)
					}
				}
				text, markup, renderErr := app.renderFavorites(context.Background(), user)
				if renderErr != nil {
					_ = editDiscoveryMessage(c, notice+"\n\n无法刷新收藏卡片。", discoveryFavoritesShortcut(), false)
					return
				}
				if notice != "" {
					text = notice + "\n\n" + text
				}
				_ = editDiscoveryMessage(c, text, markup, false)
			})
			return nil
		case "history":
			text, markup := discoveryPlatformMenu()
			return answerOnCard(c, text, markup, false)
		case "favorites":
			return app.presentFavorites(c, "")
		case "retry":
			jobs, jobErr := app.discovery.Store.Jobs(ctx, c.Sender().ID, false)
			if jobErr != nil {
				return app.presentFavorites(c, "❌ 无法读取失败记录。")
			}
			failed := 0
			for _, job := range jobs {
				if job.State == discovery.Failed {
					failed++
				}
			}
			if failed == 0 {
				return app.presentFavorites(c, "✅ 没有需要重试的失败任务。")
			}
			if err := app.discovery.Store.Retry(ctx, c.Sender().ID); err != nil {
				return app.presentFavorites(c, "❌ 失败任务未能重新排队："+discoveryPublicError(err))
			}
			return app.presentFavorites(c, fmt.Sprintf("✅ %d 首失败任务已重新排队。", failed))
		case "status":
			if err := app.discovery.Reconcile(ctx, c.Sender().ID); err != nil {
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
				return answerOnCard(c, err.Error(), nil, false)
			}
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
			return app.presentFavorites(c, "❌ 无法确认收藏账号："+err.Error())
		}
		if _, _, err = app.discovery.Store.Apply(context.Background(), card.User, card.Chat, *card.Snapshot, true); err != nil {
			return app.presentFavorites(c, "❌ 建立收藏基线失败："+err.Error())
		}
		return app.presentFavorites(c, "✅ 已绑定并建立收藏基线；这批已有收藏未下载。")
	}
	if card.Kind != "recommend" {
		return c.Respond(&tb.CallbackResponse{Text: "推荐按钮无效", ShowAlert: true})
	}
	if action == "next" {
		if err = c.Respond(&tb.CallbackResponse{Text: "正在寻找下一批…"}); err != nil {
			return err
		}
		return app.RediscoverCommand(c)
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
			return answerOnCard(c, "❌ 无法保存当前进度。请查看收藏同步状态。", discoveryFavoritesShortcut(), false)
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
		return answerOnCard(c, "❌ 无法读取这批遗珠。请查看收藏同步状态。", discoveryFavoritesShortcut(), false)
	}
	watch := false
	for _, choice := range choices {
		if choice.index < 0 || choice.index >= len(card.Tracks) || card.Tracks[choice.index].CardAction != "" {
			continue
		}
		track := card.Tracks[choice.index]
		track.CardAction = ""
		track.IngestID = ""
		track.IngestNote = ""
		if action == "ignore" {
			err = app.discovery.Store.Preference(context.Background(), card.User, track, true)
			card.Tracks[choice.index].CardAction = "dismissed"
		} else {
			var id string
			id, err = app.discovery.Store.Enqueue(context.Background(), card.User, card.Chat, track)
			card.Tracks[choice.index].IngestID = id
			card.Tracks[choice.index].IngestNote = ""
			card.Tracks[choice.index].CardAction = discovery.Queued
			watch = watch || id != ""
		}
		if err != nil {
			app.rediscoverMu.Unlock()
			return answerOnCard(c, "❌ 操作未完成，暂时无法确认队列状态。请查看收藏同步状态后再决定是否重试。", discoveryFavoritesShortcut(), false)
		}
	}
	card.Page = page
	if err = app.discovery.Store.ReplaceCard(context.Background(), card); err != nil {
		app.rediscoverMu.Unlock()
		return answerOnCard(c, "❌ 操作结果暂时无法保存。请查看收藏同步状态，确认队列后再决定是否重试。", discoveryFavoritesShortcut(), false)
	}
	message := c.Message()
	text, markup := discoveryRecommendationCard(card, page)
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
	text.WriteString("待确认\n")
	if total > 1 {
		fmt.Fprintf(&text, "%d / %d\n", index+1, total)
	}
	text.WriteString("\n")
	fmt.Fprintf(&text, "<b>%s</b>\n", discoveryListenTitle(job.Track.Title, job.Track.URL))
	if artist := strings.TrimSpace(job.Track.Artist); artist != "" {
		fmt.Fprintf(&text, "%s\n", utils.EscapeHTML(utils.TruncateString(artist, 32)))
	}
	if note := strings.TrimSpace(job.Error); note != "" {
		text.WriteByte('\n')
		text.WriteString(utils.EscapeHTML(utils.TruncateString(note, 180)))
		text.WriteByte('\n')
	}
	text.WriteString("\n确认后会入库。不再处理之后，这首不会再出现。")
	markup := &tb.ReplyMarkup{}
	appendDiscoveryRows(markup, markup.Row(
		markup.Data("确认", discoveryCallbackUnique, "accept", job.ID, strconv.Itoa(index)),
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
	appendDiscoveryRows(markup, markup.Row(
		markup.Data("不再处理", discoveryCallbackUnique, "reject", job.ID, strconv.Itoa(index)),
	))
	appendDiscoveryRows(markup, markup.Row(markup.Data("收藏同步", discoveryCallbackUnique, "favorites", "menu")))
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
	text := fmt.Sprintf("未处理的全部入库\n\n还有 %d 首没处理，包含%s。\n确认后会全部加入入库队列。", waiting, title)
	markup := &tb.ReplyMarkup{}
	appendDiscoveryRows(markup,
		markup.Row(markup.Data("确认全部入库", discoveryCallbackUnique, "batch", card.Token, "all", strconv.Itoa(page))),
		markup.Row(markup.Data("返回这首", discoveryCallbackUnique, "page", card.Token, strconv.Itoa(page))),
	)
	return text, markup
}

func (app *BotApp) showDiscoveryFailed(c tb.Context, page int) error {
	jobs, err := app.discovery.Store.Jobs(context.Background(), c.Sender().ID, false)
	if err != nil {
		return answerOnCard(c, "无法读取失败记录", nil, false)
	}
	failed := make([]discovery.Job, 0)
	for _, job := range jobs {
		if job.State == discovery.Failed {
			failed = append(failed, job)
		}
	}
	if len(failed) == 0 {
		return app.presentFavorites(c, "✅ 没有失败的入库。")
	}
	if page < 0 || page >= len(failed) {
		page = 0
	}
	text, markup := discoveryFailedCard(failed[page], page, len(failed))
	return answerOnCard(c, text, markup, true)
}

func discoveryFailedCard(job discovery.Job, index, total int) (string, *tb.ReplyMarkup) {
	var text strings.Builder
	text.WriteString("失败的入库\n")
	if total > 1 {
		fmt.Fprintf(&text, "%d / %d\n", index+1, total)
	}
	text.WriteString("\n")
	fmt.Fprintf(&text, "<b>%s</b>\n", discoveryListenTitle(job.Track.Title, job.Track.URL))
	if artist := strings.TrimSpace(job.Track.Artist); artist != "" {
		fmt.Fprintf(&text, "%s\n", utils.EscapeHTML(utils.TruncateString(artist, 32)))
	}
	if platform := strings.TrimSpace(job.Track.Platform); platform != "" {
		fmt.Fprintf(&text, "%s\n", utils.EscapeHTML(aichat.PlatformLabel(platform)))
	}
	if note := strings.TrimSpace(job.Error); note != "" {
		text.WriteByte('\n')
		text.WriteString(utils.EscapeHTML(utils.TruncateString(note, 180)))
	} else {
		text.WriteString("\n没有留下失败原因。")
	}
	markup := &tb.ReplyMarkup{}
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
	appendDiscoveryRows(markup,
		markup.Row(markup.Data("全部重新排队", discoveryCallbackUnique, "retry", "menu")),
		markup.Row(markup.Data("收藏同步", discoveryCallbackUnique, "favorites", "menu")),
	)
	return strings.TrimRight(text.String(), "\n"), markup
}
