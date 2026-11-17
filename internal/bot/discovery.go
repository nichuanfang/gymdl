package bot

import (
	"context"
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
	if !app.discoveryCommandMu.TryLock() {
		return c.Send("⏳ 正在处理上一条遗珠或收藏请求，请稍后再试。")
	}
	defer app.discoveryCommandMu.Unlock()
	status, err := c.Bot().Send(c.Chat(), "⌛ 正在核对历史、收藏和实际曲库…")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tracks, err := app.discovery.Recommend(ctx, c.Sender().ID)
	if err != nil {
		_, err = c.Bot().Edit(status, "❌ "+err.Error())
		return err
	}
	if len(tracks) == 0 {
		_, err = c.Bot().Edit(status, "这批可读取的历史与收藏中，暂未找到可确认的缺失歌曲。可稍后再试或先绑定平台收藏。", &tb.ReplyMarkup{})
		return err
	}
	card, err := app.discovery.Store.SaveCard(ctx, discovery.Card{User: c.Sender().ID, Chat: c.Chat().ID, Kind: "recommend", Tracks: tracks})
	if err != nil {
		return c.Send("保存推荐失败，请重试。")
	}
	text, markup := discoveryRecommendationCard(card)
	if _, err = c.Bot().Edit(status, text, markup); err != nil {
		return err
	}
	for _, t := range tracks {
		if err = app.discovery.Store.Preference(ctx, c.Sender().ID, t, false); err != nil {
			return err
		}
	}
	return nil
}

func discoveryRecommendationCard(card discovery.Card) (string, *tb.ReplyMarkup) {
	var text strings.Builder
	text.WriteString("🎵 找回遗珠\n以下歌曲目前未在实际曲库中找到：\n")
	markup := &tb.ReplyMarkup{}
	for i, t := range card.Tracks {
		reason := ""
		if t.Favorite {
			reason = "平台收藏"
		}
		if t.Plays > 0 {
			if reason != "" {
				reason += "；"
			}
			reason += fmt.Sprintf("听过 %d 次", t.Plays)
		}
		if !t.LastPlayed.IsZero() {
			reason += "；上次 " + t.LastPlayed.Format("2006-01-02")
		}
		fmt.Fprintf(&text, "\n%d. %s — %s\n%s\n", i+1, utils.TruncateString(t.Title, 65), utils.TruncateString(t.Artist, 45), reason)
		row := tb.Row{}
		if t.URL != "" {
			row = append(row, markup.URL("试听·"+aichat.PlatformLabel(t.Platform), t.URL))
		}
		row = append(row, markup.Data(fmt.Sprintf("下载 %d", i+1), discoveryCallbackUnique, "download", card.Token, strconv.Itoa(i)), markup.Data("不再推荐", discoveryCallbackUnique, "ignore", card.Token, strconv.Itoa(i)))
		appendDiscoveryRows(markup, row)
	}
	appendDiscoveryRows(markup, markup.Row(markup.Data("下载前 3 首", discoveryCallbackUnique, "batch", card.Token, "3"), markup.Data("全部下载", discoveryCallbackUnique, "batch", card.Token, "all")), markup.Row(markup.Data("换一批", discoveryCallbackUnique, "next", card.Token)))
	text.WriteString("\n先点试听确认喜欢，再选择下载。下载前会再次查重；下载等操作按钮 30 分钟内有效。")
	return text.String(), markup
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
	ctx := context.Background()
	bindings, err := app.discovery.Store.Bindings(ctx, c.Sender().ID)
	if err != nil {
		return c.Send("读取收藏状态失败")
	}
	var text strings.Builder
	text.WriteString("🎵 收藏同步\n")
	if app.discovery.Enabled {
		fmt.Fprintf(&text, "已绑定来源每 %d 分钟检查新增喜欢。\n", max(1, int(app.discovery.PollInterval/time.Minute)))
	} else {
		text.WriteString("自动同步尚未开启：先预览、绑定，再配置 favorites.enable: true 并重启。\n")
	}
	text.WriteString("首次绑定只建立基线，历史收藏通过“预览历史收藏”选择下载。\n")
	byPlatform := map[string]discovery.Binding{}
	for _, b := range bindings {
		byPlatform[b.Platform] = b
	}
	markup := &tb.ReplyMarkup{}
	for _, platform := range []string{"netease", "qq", "youtube"} {
		b, ok := byPlatform[platform]
		label := platform
		if ok {
			state := "同步中"
			if b.Paused {
				state = "已暂停"
			}
			if !app.discovery.Enabled {
				state = "等待全局开启"
			}
			fmt.Fprintf(&text, "\n%s：%s · 账号 %s", platform, state, maskedDiscoveryAccount(b.Account))
			if b.LastSync > 0 {
				text.WriteString("\n上次读取：" + time.Unix(b.LastSync, 0).Format("01-02 15:04"))
			}
			if b.Error != "" {
				text.WriteString("\n" + utils.TruncateString(b.Error, 120))
			}
			label += " 重新绑定"
		} else {
			fmt.Fprintf(&text, "\n%s：未绑定", platform)
			label += " 绑定"
		}
		row := markup.Row(markup.Data(label, discoveryCallbackUnique, "probe", platform))
		if ok {
			action, button := "pause", "暂停"
			if b.Paused {
				action, button = "resume", "恢复"
			}
			row = append(row, markup.Data(button, discoveryCallbackUnique, action, platform))
		}
		appendDiscoveryRows(markup, row)
	}
	jobs, err := app.discovery.Store.Jobs(ctx, c.Sender().ID, false)
	if err != nil {
		return c.Send("读取入库队列失败")
	}
	counts := map[string]int{}
	for _, j := range jobs {
		counts[discovery.StateLabel(j.State)]++
	}
	if len(jobs) > 0 {
		fmt.Fprintf(&text, "\n\n最近 %d 个任务：", len(jobs))
		for _, state := range []string{"queued", "running", "pending_upload", "completed", "owned", "needs_confirmation", "failed", "ignored"} {
			label := discovery.StateLabel(state)
			if counts[label] > 0 {
				fmt.Fprintf(&text, "\n%s：%d", label, counts[label])
			}
		}
	}
	shown := 0
	for _, j := range jobs {
		if (j.State == discovery.Failed || j.State == discovery.NeedsConfirmation) && shown < 8 {
			fmt.Fprintf(&text, "\n\n%s — %s\n%s：%s", utils.TruncateString(j.Track.Title, 60), utils.TruncateString(j.Track.Artist, 40), discovery.StateLabel(j.State), utils.TruncateString(j.Error, 100))
			if j.State == discovery.NeedsConfirmation {
				fmt.Fprintf(&text, "\n%s", j.Track.URL)
				appendDiscoveryRows(markup, markup.Row(markup.Data(fmt.Sprintf("确认此原链接 %d", shown+1), discoveryCallbackUnique, "accept", j.ID), markup.Data("跳过", discoveryCallbackUnique, "reject", j.ID)))
			}
			shown++
		}
	}
	appendDiscoveryRows(markup, markup.Row(markup.Data("立即同步", discoveryCallbackUnique, "sync", "menu"), markup.Data("预览历史收藏", discoveryCallbackUnique, "history", "menu")), markup.Row(markup.Data("重试失败", discoveryCallbackUnique, "retry", "menu"), markup.Data("刷新状态", discoveryCallbackUnique, "status", "menu")))
	appendDiscoveryRows(markup, markup.Row(markup.Data("查看全部待确认", discoveryCallbackUnique, "pending", "0")))
	return c.Send(text.String(), markup)
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
				return c.Send("更新暂停状态失败")
			}
			return app.showFavorites(c)
		case "sync":
			if !app.discoveryCommandMu.TryLock() {
				return c.Send("⏳ 已有请求正在处理。")
			}
			defer app.discoveryCommandMu.Unlock()
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			if err := app.discovery.Sync(ctx, c.Sender().ID); err != nil {
				return c.Send(err.Error())
			}
			return c.Send("✅ 收藏读取完成，新增歌曲已进入队列。首次接入请先绑定账号。")
		case "history":
			return app.RediscoverCommand(c)
		case "retry":
			if err := app.discovery.Store.Retry(ctx, c.Sender().ID); err != nil {
				return c.Send("重试失败")
			}
			return c.Send("✅ 失败任务已重新排队。")
		case "status":
			_ = app.discovery.Reconcile(ctx, c.Sender().ID)
			return app.showFavorites(c)
		case "accept", "reject":
			if err := app.discovery.Store.ResolveJob(ctx, key, c.Sender().ID, action == "accept"); err != nil {
				return c.Send(err.Error())
			}
			return c.Send("✅ 已更新待确认任务。")
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
	var selected []discovery.Track
	switch action {
	case "download", "ignore":
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "按钮参数无效", ShowAlert: true})
		}
		index, e := strconv.Atoi(args[2])
		if e != nil || index < 0 || index >= len(card.Tracks) {
			return c.Respond(&tb.CallbackResponse{Text: "曲目编号无效", ShowAlert: true})
		}
		selected = []discovery.Track{card.Tracks[index]}
	case "batch":
		if len(args) != 3 {
			return c.Respond(&tb.CallbackResponse{Text: "批量参数无效", ShowAlert: true})
		}
		n := len(card.Tracks)
		if args[2] == "3" {
			n = min(n, 3)
		} else if args[2] != "all" {
			return c.Respond(&tb.CallbackResponse{Text: "批量参数无效", ShowAlert: true})
		}
		selected = card.Tracks[:n]
	default:
		return c.Respond(&tb.CallbackResponse{Text: "未知操作", ShowAlert: true})
	}
	if err = c.Respond(&tb.CallbackResponse{Text: "已收到，正在保存…"}); err != nil {
		return err
	}
	for _, t := range selected {
		if action == "ignore" {
			err = app.discovery.Store.Preference(context.Background(), card.User, t, true)
		} else {
			err = app.discovery.Store.Enqueue(context.Background(), card.User, card.Chat, t)
		}
		if err != nil {
			return c.Send("保存操作失败，请重试。")
		}
	}
	if action == "ignore" {
		return c.Send("✅ 已加入不再推荐列表。")
	}
	return c.Send(fmt.Sprintf("✅ 已提交 %d 首到入库队列；重复提交会自动合并。发送 /favorites 查看进度。", len(selected)))
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
		return c.Send("页码无效")
	}
	jobs, err := app.discovery.Store.Confirmations(context.Background(), c.Sender().ID, page*8)
	if err != nil {
		return c.Send("无法读取待确认任务")
	}
	if len(jobs) == 0 {
		return c.Send("没有待确认任务。")
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
	return c.Send(text.String(), markup)
}
