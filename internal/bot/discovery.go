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
		markup.Row(markup.Data("取消", discoveryCallbackUnique, "cancel", "menu")),
	)
	return "遗珠\n先选平台。只查这一个，找够就停。", markup
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
	return "遗珠 · " + aichat.PlatformLabel(platform) + "\n要找几首？找到这么多就停。", markup
}

func validRediscoverPlatform(platform string) bool {
	return platform == "netease" || platform == "qq" || platform == "youtube"
}

func validRediscoverLimit(limit int) bool {
	return limit == 3 || limit == 6 || limit == 9
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
		return c.Respond(&tb.CallbackResponse{Text: "正在处理上一条遗珠或收藏请求", ShowAlert: true})
	}
	defer app.discoveryCommandMu.Unlock()
	if err = c.Respond(&tb.CallbackResponse{Text: "开始找了"}); err != nil {
		return err
	}
	if c.Message() == nil {
		return c.Send("找不到原来的消息，请重新发送 /rediscover。")
	}
	if _, err = c.Bot().Edit(c.Message(), fmt.Sprintf("⌛ 正在从%s找 %d 首…", aichat.PlatformLabel(platform), limit)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	batch, err := app.discovery.Rediscover(ctx, c.Sender().ID, platform, limit)
	if err != nil {
		_, editErr := c.Bot().Edit(c.Message(), "❌ "+err.Error(), &tb.ReplyMarkup{})
		if editErr != nil {
			return editErr
		}
		return nil
	}
	tracks := batch.Tracks
	if len(tracks) == 0 {
		_, err = c.Bot().Edit(c.Message(), rediscoverEmptyCopy(platform, batch), &tb.ReplyMarkup{})
		return err
	}
	card, err := app.discovery.Store.SaveCard(ctx, discovery.Card{User: c.Sender().ID, Chat: c.Chat().ID, Kind: "recommend", Tracks: tracks, SkippedOwned: batch.SkippedOwned, Unconfirmed: batch.Unconfirmed, Requested: limit})
	if err != nil {
		_, editErr := c.Bot().Edit(c.Message(), "保存推荐失败，请重试。", &tb.ReplyMarkup{})
		if editErr != nil {
			return editErr
		}
		return nil
	}
	text, markup := discoveryRecommendationCard(card, 0)
	if _, err = c.Bot().Edit(c.Message(), text, tb.ModeHTML, markup); err != nil {
		return err
	}
	for _, track := range tracks {
		if err = app.discovery.Store.Preference(ctx, c.Sender().ID, track, false); err != nil {
			return err
		}
	}
	return nil
}

const discoveryPageSize = 3

func discoveryRecommendationCard(card discovery.Card, page int) (string, *tb.ReplyMarkup) {
	if page < 0 {
		page = 0
	}
	start := page * discoveryPageSize
	if start > len(card.Tracks) {
		start = 0
		page = 0
	}
	end := min(start+discoveryPageSize, len(card.Tracks))
	visible := card.Tracks[start:end]
	pages := 1
	if len(card.Tracks) > 0 {
		pages = (len(card.Tracks) + discoveryPageSize - 1) / discoveryPageSize
	}
	var text strings.Builder
	waiting := 0
	for _, t := range visible {
		if t.CardAction == "" {
			waiting++
		}
	}
	fmt.Fprintf(&text, "遗珠\n第 %d 组 · 还剩 %d 首\n", page+1, waiting)
	if card.SkippedOwned > 0 {
		fmt.Fprintf(&text, "已避开曲库里的 %d 首\n", card.SkippedOwned)
	}
	if line := rediscoverShortfallLine(card); line != "" {
		text.WriteString(line)
	}
	for _, t := range visible {
		if strings.TrimSpace(t.URL) != "" {
			text.WriteString("点歌名试听。\n")
			break
		}
	}
	markup := &tb.ReplyMarkup{}
	for offset, t := range visible {
		index := start + offset
		reason := "听过"
		if t.Favorite {
			reason = "收藏"
		}
		if t.Plays > 0 {
			reason += fmt.Sprintf(" · %d 次", t.Plays)
		}
		switch t.CardAction {
		case "queued":
			reason += " · 已提交入库"
		case "dismissed":
			reason += " · 不再推荐"
		}
		fmt.Fprintf(&text, "\n%s\n%s\n%s\n", discoveryListenTitle(t.Title, t.URL), utils.EscapeHTML(utils.TruncateString(t.Artist, 32)), reason)
		if t.CardAction == "" {
			appendDiscoveryRows(markup, markup.Row(
				markup.Data("入库", discoveryCallbackUnique, "download", card.Token, strconv.Itoa(index)),
				markup.Data("不再推荐", discoveryCallbackUnique, "ignore", card.Token, strconv.Itoa(index)),
			))
		}
	}
	footer := tb.Row{}
	if end < len(card.Tracks) {
		footer = append(footer, markup.Data("下一组", discoveryCallbackUnique, "page", card.Token, strconv.Itoa(page+1)))
	} else if page > 0 {
		footer = append(footer, markup.Data("回到第一组", discoveryCallbackUnique, "page", card.Token, "0"))
	}
	if waiting > 0 {
		footer = append(footer, markup.Data("这组入库", discoveryCallbackUnique, "batch", card.Token, "p"+strconv.Itoa(page)))
	}
	if len(footer) > 0 {
		appendDiscoveryRows(markup, footer)
	}
	if pages > 1 {
		fmt.Fprintf(&text, "\n共 %d 组，换组不重新检索。", pages)
	}
	return text.String(), markup
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
	text.WriteString("收藏\n")
	if app.discovery.Enabled {
		fmt.Fprintf(&text, "每 %d 分钟看一次新喜欢。\n", max(1, int(app.discovery.PollInterval/time.Minute)))
	} else {
		text.WriteString("自动同步还没开。绑定后，新增喜欢不会自动入库。\n")
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
			fmt.Fprintf(&text, "\n%s  未绑定", name)
			appendDiscoveryRows(markup, markup.Row(markup.Data("绑定 "+name, discoveryCallbackUnique, "probe", platform)))
			continue
		}
		state := "同步中"
		if binding.Paused {
			state = "已暂停"
		}
		if !app.discovery.Enabled {
			state = "已绑定"
		}
		fmt.Fprintf(&text, "\n%s  %s", name, state)
		if account := maskedDiscoveryAccount(binding.Account); account != "" {
			fmt.Fprintf(&text, "  %s", account)
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
			queue = append(queue, fmt.Sprintf("%d %s", counts[state], discovery.StateLabel(state)))
		}
	}
	if len(queue) > 0 {
		fmt.Fprintf(&text, "\n\n%s", strings.Join(queue, " · "))
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

func maskedDiscoveryAccount(s string) string {
	if len(s) <= 6 {
		return s
	}
	return s[:3] + "…" + s[len(s)-3:]
}

func (app *BotApp) previewFavoriteBinding(c tb.Context, platform string) error {
	if platform != "netease" && platform != "qq" && platform != "youtube" {
		return c.Send("支持的来源：netease、qq、youtube。")
	}
	if !app.discoveryCommandMu.TryLock() {
		return c.Send("⏳ 正在读取收藏或遗珠，请稍后再试。")
	}
	defer app.discoveryCommandMu.Unlock()
	if err := c.Send("⌛ 正在读取完整收藏列表，仅预览，不下载…"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	snap, err := app.discovery.Provider.Fetch(ctx, platform)
	if err != nil {
		return c.Send("❌ " + err.Error())
	}
	card, err := app.discovery.Store.SaveCard(ctx, discovery.Card{User: c.Sender().ID, Chat: c.Chat().ID, Kind: "binding", Snapshot: &snap})
	if err != nil {
		return c.Send("无法保存绑定预览，请重试。")
	}
	markup := &tb.ReplyMarkup{}
	markup.Inline(markup.Row(markup.Data("确认账号并建立基线", discoveryCallbackUnique, "bind", card.Token)))
	return c.Send(fmt.Sprintf("%s 收藏预览\n账号：%s\n读取到 %d 首收藏。\n\n请确认是你在手机上使用的账号。确认后建立基线；已有收藏不会自动下载，之后新增喜欢才自动入库。", platform, maskedDiscoveryAccount(snap.Account), len(snap.Tracks)), markup)
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
	case "probe", "pause", "resume", "sync", "history", "retry", "status", "accept", "reject", "pending":
		if err := c.Respond(&tb.CallbackResponse{Text: "已收到，正在处理…"}); err != nil {
			return err
		}
		ctx := context.Background()
		switch action {
		case "pending":
			page, e := strconv.Atoi(key)
			if e != nil {
				return c.Send("页码无效")
			}
			return app.showDiscoveryPending(c, page)
		case "probe":
			return app.previewFavoriteBinding(c, key)
		case "pause", "resume":
			if err := app.discovery.Store.Pause(ctx, c.Sender().ID, key, action == "pause"); err != nil {
				return app.presentFavorites(c, "更新暂停状态失败")
			}
			return app.presentFavorites(c, "")
		case "sync":
			if !app.discoveryCommandMu.TryLock() {
				return answerOnCard(c, "⏳ 已有请求正在处理。", nil, false)
			}
			defer app.discoveryCommandMu.Unlock()
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			if err := app.discovery.Sync(ctx, c.Sender().ID); err != nil {
				return app.presentFavorites(c, "❌ "+err.Error())
			}
			return app.presentFavorites(c, "✅ 收藏读取完成，新增歌曲已进入队列。首次接入请先绑定账号。")
		case "history":
			text, markup := discoveryPlatformMenu()
			return answerOnCard(c, text, markup, false)
		case "retry":
			if err := app.discovery.Store.Retry(ctx, c.Sender().ID); err != nil {
				return app.presentFavorites(c, "重试失败")
			}
			return app.presentFavorites(c, "✅ 失败任务已重新排队。")
		case "status":
			_ = app.discovery.Reconcile(ctx, c.Sender().ID)
			return app.presentFavorites(c, "")
		case "accept", "reject":
			if err := app.discovery.Store.ResolveJob(ctx, key, c.Sender().ID, action == "accept"); err != nil {
				return answerOnCard(c, err.Error(), nil, false)
			}
			return app.showDiscoveryPending(c, 0)
		}
	}
	card, err := app.discovery.Store.Card(context.Background(), key, c.Sender().ID, c.Chat().ID)
	if err != nil {
		_ = c.Respond(&tb.CallbackResponse{Text: err.Error(), ShowAlert: true})
		return c.Edit("⌛ "+err.Error(), &tb.ReplyMarkup{})
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
			return c.Send(err.Error())
		}
		if _, err = app.discovery.Store.Apply(context.Background(), card.User, card.Chat, *card.Snapshot, true); err != nil {
			return c.Send(err.Error())
		}
		return c.Edit("✅ 已绑定并建立收藏基线。已有收藏未下载；发送 /favorites 查看同步状态。", &tb.ReplyMarkup{})
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
			return c.Respond(&tb.CallbackResponse{Text: "分组无效", ShowAlert: true})
		}
		if err = c.Respond(&tb.CallbackResponse{Text: "已换到下一组"}); err != nil {
			return err
		}
		text, markup := discoveryRecommendationCard(card, page)
		return editDiscoveryMessage(c, text, markup, true)
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
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "批量参数无效", ShowAlert: true})
		}
		var picked []discovery.Track
		switch {
		case args[2] == "all":
			picked = card.Tracks
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
				return c.Respond(&tb.CallbackResponse{Text: "这一组已经没有歌曲", ShowAlert: true})
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
	if err = c.Respond(&tb.CallbackResponse{Text: "已收到，正在保存…"}); err != nil {
		return err
	}
	for _, choice := range choices {
		if choice.track.CardAction != "" {
			continue
		}
		track := choice.track
		track.CardAction = ""
		if action == "ignore" {
			err = app.discovery.Store.Preference(context.Background(), card.User, track, true)
			card.Tracks[choice.index].CardAction = "dismissed"
		} else {
			err = app.discovery.Store.Enqueue(context.Background(), card.User, card.Chat, track)
			card.Tracks[choice.index].CardAction = "queued"
		}
		if err != nil {
			return c.Send("保存操作失败，请重试。")
		}
	}
	if err = app.discovery.Store.ReplaceCard(context.Background(), card); err != nil {
		return c.Send("保存操作失败，请重试。")
	}
	text, markup := discoveryRecommendationCard(card, page)
	return editDiscoveryMessage(c, text, markup, true)
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
	jobs, err := app.discovery.Store.Confirmations(context.Background(), c.Sender().ID, page*8)
	if err != nil {
		return answerOnCard(c, "无法读取待确认任务", nil, false)
	}
	if len(jobs) == 0 {
		return answerOnCard(c, "没有待确认任务。", &tb.ReplyMarkup{}, false)
	}
	markup := &tb.ReplyMarkup{}
	var text strings.Builder
	fmt.Fprintf(&text, "🎵 待确认任务 · 第 %d 页\n请先核对原链接；确认后仅处理原链接音频。\n", page+1)
	for i, j := range jobs[:min(8, len(jobs))] {
		fmt.Fprintf(&text, "\n%d. %s — %s\n%s\n%s\n", i+1, utils.TruncateString(j.Track.Title, 60), utils.TruncateString(j.Track.Artist, 40), utils.TruncateString(j.Error, 100), j.Track.URL)
		appendDiscoveryRows(markup, markup.Row(markup.Data(fmt.Sprintf("确认 %d", i+1), discoveryCallbackUnique, "accept", j.ID), markup.Data("跳过", discoveryCallbackUnique, "reject", j.ID)))
	}
	row := tb.Row{}
	if page > 0 {
		row = append(row, markup.Data("上一页", discoveryCallbackUnique, "pending", strconv.Itoa(page-1)))
	}
	if len(jobs) > 8 {
		row = append(row, markup.Data("下一页", discoveryCallbackUnique, "pending", strconv.Itoa(page+1)))
	}
	if len(row) > 0 {
		appendDiscoveryRows(markup, row)
	}
	return answerOnCard(c, text.String(), markup, false)
}
