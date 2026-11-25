package bot

import (
	"context"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/aichat"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

func (app *BotApp) RecommendCommand(c tb.Context) error {
	if err := app.discoveryAvailable(c); err != nil {
		return c.Send(err.Error())
	}
	request, err := discovery.ParseTasteRequest(c.Message().Payload)
	if err != nil {
		return c.Send(err.Error())
	}
	return app.startTasteRecommendation(c, request, nil)
}

// Last.fm is a single listening history. Other authorized users still get
// recommendations from their own synchronized favorites.
func tasteHistoryOwner(cfg *config.TelegramConfig) int64 {
	if cfg == nil {
		return 0
	}
	configured, _ := strconv.ParseInt(strings.TrimSpace(cfg.ChatID), 10, 64)
	ids := map[int64]struct{}{}
	for _, raw := range cfg.AllowedUsers {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			if id == configured {
				return id
			}
			ids[id] = struct{}{}
		}
	}
	if len(ids) == 1 {
		for id := range ids {
			return id
		}
	}
	return 0
}

func appendTasteActions(markup *tb.ReplyMarkup, card discovery.Card) {
	button := func(label string, args ...string) tb.Btn {
		return markup.Data(label, discoveryCallbackUnique, append([]string{"taste", card.Token}, args...)...)
	}
	appendDiscoveryRows(markup,
		markup.Row(button("换一首", "next"), button("更多设置", "settings")),
	)
}

func tasteSettingsMarkup(token string) *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	button := func(label string, args ...string) tb.Btn {
		return markup.Data(label, discoveryCallbackUnique, append([]string{"taste", token}, args...)...)
	}
	appendDiscoveryRows(markup,
		markup.Row(button("通勤", "scene", "commute"), button("放松", "scene", "relax"), button("运动", "scene", "workout")),
		markup.Row(button("睡前", "scene", "sleep"), button("按口味", "scene", "taste")),
		markup.Row(button("网易云", "platform", "netease"), button("QQ 音乐", "platform", "qq")),
		markup.Row(button("自动选择（网易云 / QQ）", "platform", discovery.TasteBoth)),
		markup.Row(button("返回歌曲卡片", "back")),
	)
	return markup
}

func (app *BotApp) handleTasteCallback(c tb.Context, token string, args []string) error {
	if len(args) == 0 {
		return c.Respond(&tb.CallbackResponse{Text: "按钮参数无效", ShowAlert: true})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	app.rediscoverMu.Lock()
	card, err := app.discovery.Store.Card(ctx, token, c.Sender().ID, c.Chat().ID)
	if err != nil || card.Kind != discovery.TasteCardKind || card.Taste == nil {
		app.rediscoverMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "这张卡片已过期，请重新发送 /recommend", ShowAlert: true})
	}
	request := card.Taste.Request
	if args[0] == "settings" || args[0] == "scenes" || args[0] == "back" {
		defer app.rediscoverMu.Unlock()
		if err := c.Respond(); err != nil {
			return err
		}
		if args[0] == "back" {
			text, markup := app.renderDiscoveryCard(card, 0)
			return app.editDiscoveryCard(c, card, 0, text, markup, true)
		}
		text, _ := app.renderDiscoveryCard(card, 0)
		platform := aichat.PlatformLabel(request.Platform)
		if request.Platform == "" || request.Platform == discovery.TasteBoth {
			platform = "自动选择（网易云 / QQ）"
		}
		scene := request.Filter.Scene
		if scene == "" {
			scene = "按口味"
		}
		text += "\n\n<b>更多设置</b>\n选择推荐场景或来源。\n当前平台：" + utils.EscapeHTML(platform) + "\n当前场景：" + utils.EscapeHTML(scene)
		return app.editDiscoveryCard(c, card, 0, text, tasteSettingsMarkup(token), true)
	}
	app.rediscoverMu.Unlock()
	switch {
	case args[0] == "next" && len(args) == 1:
		if len(card.Tracks) == 0 {
			request.Page++
		}
	case args[0] == "platform" && len(args) == 2 && discovery.ValidTastePlatform(args[1]):
		request.Platform = args[1]
		request.Page = 0
	case args[0] == "scene" && len(args) == 2:
		filter, ok := request.Filter.WithScene(args[1])
		if !ok {
			return c.Respond(&tb.CallbackResponse{Text: "场景无效", ShowAlert: true})
		}
		request.Filter = filter
		request.Page = 0
	default:
		return c.Respond(&tb.CallbackResponse{Text: "按钮参数无效", ShowAlert: true})
	}
	return app.startTasteRecommendation(c, request, &card)
}

// An unspecified platform means one of the two catalogs, chosen once for this
// card. Later "next" clicks keep that choice; "自动选择" asks for a new draw.
func chooseUnspecifiedTastePlatform(request discovery.TasteRequest) discovery.TasteRequest {
	if request.Platform != discovery.TasteBoth {
		return request
	}
	if tasteAutoPlatform() == "qq" {
		request.Platform = "qq"
	} else {
		request.Platform = "netease"
	}
	return request
}

var tasteAutoPlatform = func() string {
	if rand.Intn(2) == 0 {
		return "netease"
	}
	return "qq"
}

func (app *BotApp) startTasteRecommendation(c tb.Context, request discovery.TasteRequest, previous *discovery.Card) error {
	request = chooseUnspecifiedTastePlatform(request)
	if !app.tasteCommandMu.TryLock() {
		if c.Callback() != nil {
			return c.Respond(&tb.CallbackResponse{Text: "上一首还在找，稍等一下"})
		}
		return c.Send("上一首还在找，稍等一下。")
	}
	message, err := app.tasteProgressMessage(c, previous)
	if err != nil || message == nil {
		app.tasteCommandMu.Unlock()
		return err
	}
	api, user, chat := c.Bot(), c.Sender().ID, c.Chat().ID
	filter := request.Filter.Label()
	if filter == "" {
		filter = "按口味"
	}
	started := time.Now()
	utils.InfoWithFormat("[Discovery] 个性化推荐开始 platform=%s filter=%s page=%d", request.Platform, filter, request.Page)
	app.goDiscovery(func() {
		defer app.tasteCommandMu.Unlock()
		query, cancel := context.WithTimeout(context.Background(), 55*time.Second)
		result, err := app.discovery.RecommendTaste(query, user, request)
		cancel()
		card := discovery.Card{User: user, Chat: chat, Kind: discovery.TasteCardKind, MessageID: message.ID, Taste: &discovery.TasteCard{Request: request}}
		if err != nil {
			utils.WarnWithFormat("[Discovery] 个性化推荐失败 platform=%s 耗时=%s: %s", request.Platform, time.Since(started).Round(time.Millisecond), err)
			card.Taste.Notice = err.Error()
		} else if result.Track != nil {
			card.Tracks = []discovery.Track{*result.Track}
			card.Taste.Reason, card.Taste.Notice = result.Reason, result.Notice
			card.Taste.Request.ExploreFirst = !result.Explore
		} else {
			filter := request.Filter.Label()
			if filter != "" {
				card.Taste.Notice = "这次没找到标签能支持「" + filter + "」的新歌。"
			} else {
				card.Taste.Notice = "这次没找到合适的新歌。"
			}
			if result.Notice != "" {
				card.Taste.Notice += "\n" + result.Notice
			} else if filter != "" {
				card.Taste.Notice += "可换个场景或回到「按口味」。"
			} else if !strings.Contains(card.Taste.Notice, "后台") {
				card.Taste.Notice += "可以再试一次或换个平台。"
			}
		}
		if result.Track != nil {
			utils.InfoWithFormat("[Discovery] 个性化推荐结束 platform=%s 找到=1 探索=%t 池=%s 耗时=%s",
				request.Platform, result.Explore, tasteResultSource(result), time.Since(started).Round(time.Millisecond))
		} else {
			utils.InfoWithFormat("[Discovery] 个性化推荐结束 platform=%s 找到=0 耗时=%s", request.Platform, time.Since(started).Round(time.Millisecond))
		}
		saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		card, err = app.discovery.Store.SaveCard(saveCtx, card)
		saveCancel()
		if err != nil {
			_, _ = api.Edit(message, "推荐结果暂时无法保存，请重新发送 /recommend。", &tb.ReplyMarkup{})
			return
		}
		text, markup := app.renderDiscoveryCard(card, 0)
		err = editDiscoveryText(api, message, text, markup, true, app.discoveryCardPreviewURL(card, 0))
		if err != nil {
			utils.WarnWithFormat("[Discovery] 个性化推荐卡片发送失败")
			return
		}
		if result.Track != nil {
			prefCtx, prefCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer prefCancel()
			if err := app.discovery.Store.Preference(prefCtx, user, *result.Track, false); err != nil {
				utils.WarnWithFormat("[Discovery] 个性化推荐展示记录保存失败")
			}
			app.discovery.WarmQQTaste(user, card.Taste.Request, *result.Track)
		}
	})
	return nil
}

func (app *BotApp) tasteProgressMessage(c tb.Context, previous *discovery.Card) (*tb.Message, error) {
	const text = "⌛ 正在从音乐平台找适合你的歌…"
	if previous == nil {
		return c.Bot().Send(c.Chat(), text)
	}
	if err := c.Respond(&tb.CallbackResponse{Text: "正在找歌…"}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	app.rediscoverMu.Lock()
	defer app.rediscoverMu.Unlock()
	card, err := app.discovery.Store.Card(ctx, previous.Token, previous.User, previous.Chat)
	if err != nil || card.Kind != discovery.TasteCardKind {
		return nil, c.Send("这张卡片已过期，请重新发送 /recommend。")
	}
	for index := range card.Tracks {
		if card.Tracks[index].IngestID == "" {
			continue
		}
		// The old message stays the only place where this download's final
		// result can appear. Remove stale action buttons before starting a new
		// recommendation so an old click cannot enqueue a second song.
		card.Page = index
		oldText, oldMarkup := app.renderDiscoveryCard(card, index)
		if _, err := c.Bot().Edit(c.Message(), oldText, tb.ModeHTML, oldMarkup); err != nil && !strings.Contains(err.Error(), "message is not modified") {
			utils.WarnWithFormat("[Discovery] 个性化推荐原卡片按钮恢复失败: %v", err)
		}
		return c.Bot().Send(c.Chat(), text)
	}
	if err := editDiscoveryMessage(c, text, nil, false); err != nil {
		return nil, err
	}
	if _, err := app.discovery.Store.TakeCard(ctx, card.Token, card.User, card.Chat); err != nil {
		return nil, c.Edit("上一张卡片已过期，请重新发送 /recommend。", &tb.ReplyMarkup{})
	}
	return c.Message(), nil
}

func tasteResultSource(result discovery.TasteResult) string {
	if result.Source == "" {
		return "unknown"
	}
	return result.Source
}
