package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/discovery"
	tb "gopkg.in/telebot.v4"
)

type snapshotOnlyProvider struct {
	fetch func(context.Context, string) (discovery.Snapshot, error)
}

func (p snapshotOnlyProvider) Fetch(ctx context.Context, platform string) (discovery.Snapshot, error) {
	return p.fetch(ctx, platform)
}
func (snapshotOnlyProvider) Resolve(context.Context, discovery.Track, string) (discovery.Track, error) {
	return discovery.Track{}, errors.New("no alternate")
}
func (snapshotOnlyProvider) ResolveOn(context.Context, discovery.Track, string) (discovery.Track, error) {
	return discovery.Track{}, errors.New("no alternate")
}
func (snapshotOnlyProvider) Enrich(_ context.Context, t discovery.Track) (discovery.Track, bool, error) {
	return t, false, nil
}

// Exercise the real Telebot dispatch/parser and API feedback, not just a
// callback helper with pre-split arguments.
func TestDiscoveryButtonsThroughTelebotRoute(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	responses := 0
	edits := 0
	var sent []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "answerCallbackQuery") {
			responses++
			fmt.Fprint(w, `{"ok":true,"result":true}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "editMessageText") {
			edits++
		}
		var text string
		_ = json.Unmarshal(payload["text"], &text)
		sent = append(sent, text)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":10,"type":"private"}}}`)
	}))
	defer server.Close()
	bot, err := tb.NewBot(tb.Settings{Offline: true, Synchronous: true, Token: "1:test", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	app := &BotApp{bot: bot, cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"10", "20"}}}, discovery: &discovery.Service{Store: store}}
	bot.Handle(&discoveryEndpoint, app.HandleDiscoveryCallback)
	tracks := []discovery.Track{{Platform: "netease", ID: "1", Title: "一", Artist: "A", URL: "https://music.163.com/song?id=1", Verified: true}, {Platform: "qq", ID: "2", Title: "二", Artist: "B", URL: "https://y.qq.com/n/ryqq/songDetail/2", Verified: true}}
	card, err := store.SaveCard(context.Background(), discovery.Card{User: 10, Chat: 10, Kind: "recommend", Tracks: tracks})
	if err != nil {
		t.Fatal(err)
	}
	click := func(user int64, action, key, extra string) {
		data := "\fdiscovery|" + action + "|" + key
		if extra != "" {
			data += "|" + extra
		}
		bot.ProcessUpdate(tb.Update{Callback: &tb.Callback{ID: "callback", Sender: &tb.User{ID: user}, Data: data, Message: &tb.Message{ID: 1, Chat: &tb.Chat{ID: 10, Type: tb.ChatPrivate}}}})
	}
	click(10, "download", card.Token, "0")
	click(10, "download", card.Token, "0")
	jobs, _ := store.Jobs(context.Background(), 10, false)
	if responses != 2 || len(jobs) != 1 || len(sent) != 2 {
		t.Fatalf("silent or duplicate click responses=%d jobs=%d sent=%v", responses, len(jobs), sent)
	}
	click(10, "batch", card.Token, "all")
	jobs, _ = store.Jobs(context.Background(), 10, false)
	if len(jobs) != 2 {
		t.Fatal("batch did not enqueue second track")
	}
	click(10, "ignore", card.Token, "1")
	excluded, _ := store.Excluded(context.Background(), 10)
	if !excluded[discovery.Identity(tracks[1])] {
		t.Fatal("ignore not persisted")
	}
	click(20, "download", card.Token, "0")
	if edits == 0 {
		t.Fatal("wrong-owner callback did not give visible expiry feedback")
	}
	before := responses
	click(10, "download", "expired", "0")
	if responses != before+1 || edits < 2 {
		t.Fatal("expired callback silent")
	}
	if jobs, _ = store.Jobs(context.Background(), 20, false); len(jobs) != 0 {
		t.Fatal("cross-user queue modified")
	}
	_, markup := discoveryRecommendationCard(card, 0)
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			raw, err := json.Marshal(button)
			if err != nil {
				t.Fatal(err)
			}
			var data struct {
				Data string `json:"callback_data"`
			}
			json.Unmarshal(raw, &data)
			if len(data.Data) > 64 {
				t.Fatal("callback exceeds Telegram limit")
			}
		}
	}
}

func TestDiscoveryBaselineConfirmationIsSingleUse(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c, err := store.SaveCard(context.Background(), discovery.Card{User: 1, Chat: 1, Kind: "binding", Snapshot: &discovery.Snapshot{Platform: "netease", Account: "account", Complete: true, Tracks: []discovery.Track{{Platform: "netease", ID: "1", Title: "Song", Artist: "Artist", Verified: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	app := &BotApp{cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"1"}}}, discovery: &discovery.Service{Store: store}}
	ctx := &discoveryCallbackContext{
		args:     []string{"bind", c.Token},
		sender:   &tb.User{ID: 1},
		chat:     &tb.Chat{ID: 1, Type: tb.ChatPrivate},
		callback: &tb.Callback{},
		message:  &tb.Message{},
	}
	if err := app.HandleDiscoveryCallback(ctx); err != nil {
		t.Fatal(err)
	}
	if ctx.responses != 1 || !strings.Contains(ctx.edited, "收藏已接上") {
		t.Fatal("baseline confirmation silent", ctx)
	}
	// An old confirmation must not reset the baseline after new favourites arrive.
	snap := *c.Snapshot
	snap.Tracks = append(snap.Tracks, discovery.Track{Platform: "netease", ID: "2", Title: "Two", Artist: "Artist", Verified: true})
	store.Apply(context.Background(), 1, 1, snap, false)
	if err := app.HandleDiscoveryCallback(ctx); err != nil {
		t.Fatal(err)
	}
	favorites, _ := store.Favorites(context.Background(), 1)
	if len(favorites) != 2 {
		t.Fatal("repeated confirmation reverted baseline")
	}
}

type discoveryCallbackContext struct {
	tb.Context
	mu        sync.Mutex
	args      []string
	sender    *tb.User
	chat      *tb.Chat
	callback  *tb.Callback
	message   *tb.Message
	responses int
	edited    string
	sent      []string
	sendOpts  [][]any
}

func (c *discoveryCallbackContext) Callback() *tb.Callback { return c.callback }
func (c *discoveryCallbackContext) Message() *tb.Message   { return c.message }

func (c *discoveryCallbackContext) Args() []string                        { return c.args }
func (c *discoveryCallbackContext) Sender() *tb.User                      { return c.sender }
func (c *discoveryCallbackContext) Chat() *tb.Chat                        { return c.chat }
func (c *discoveryCallbackContext) Respond(...*tb.CallbackResponse) error { c.responses++; return nil }
func (c *discoveryCallbackContext) Edit(v interface{}, _ ...interface{}) error {
	c.mu.Lock()
	c.edited = fmt.Sprint(v)
	c.mu.Unlock()
	return nil
}

func (c *discoveryCallbackContext) waitEdited(t *testing.T, part string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		edited := c.edited
		c.mu.Unlock()
		if strings.Contains(edited, part) {
			return edited
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t.Fatalf("timed out waiting for %q, last edit: %s", part, c.edited)
	return ""
}

func (c *discoveryCallbackContext) Send(v interface{}, opts ...interface{}) error {
	c.sent = append(c.sent, fmt.Sprint(v))
	c.sendOpts = append(c.sendOpts, append([]any(nil), opts...))
	return nil
}

func TestDiscoveryPendingCardShowsOneSong(t *testing.T) {
	job := discovery.Job{
		ID: "job-1",
		Track: discovery.Track{
			Title: "至少还有你", Artist: "林忆莲", URL: "https://music.163.com/song?id=1",
		},
		Error: "对不上唯一版本 <原版>",
	}
	text, markup := discoveryPendingCard(job, 0, 3)
	for _, want := range []string{"⚠️ <b>待确认</b> · 1/3", "歌曲: <a href=\"https://music.163.com/song?id=1\">至少还有你</a>", "艺人: 林忆莲", "原因: 对不上唯一版本 &lt;原版&gt;"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if got := buttonRowTexts(markup); len(got) != 3 || len(got[0]) != 2 || got[0][0] != "确认入库" || got[0][1] != "不再处理" || got[1][0] != "下一首" || got[2][0] != "返回收藏" {
		t.Fatalf("pending rows = %#v", got)
	}
	if markup.InlineKeyboard[0][0].Data != "accept|job-1|0" || markup.InlineKeyboard[0][1].Data != "reject|job-1|0" {
		t.Fatalf("pending callbacks = %s / %s", markup.InlineKeyboard[0][0].Data, markup.InlineKeyboard[0][1].Data)
	}
	last, lastMarkup := discoveryPendingCard(job, 2, 3)
	if !strings.Contains(last, "3/3") || buttonTexts(lastMarkup)["下一首"] || !buttonTexts(lastMarkup)["上一首"] {
		t.Fatalf("last pending card = %s %#v", last, buttonTexts(lastMarkup))
	}
	only, onlyMarkup := discoveryPendingCard(job, 0, 1)
	if strings.Contains(only, "1/1") || buttonTexts(onlyMarkup)["下一首"] || !buttonTexts(onlyMarkup)["确认入库"] || !buttonTexts(onlyMarkup)["不再处理"] {
		t.Fatalf("single pending card = %s %#v", only, buttonTexts(onlyMarkup))
	}
}

func TestDiscoveryCardShowsOneSongAtATime(t *testing.T) {
	tracks := []discovery.Track{
		{Platform: "netease", ID: "1", Title: "第一首", Artist: "甲", Album: "专辑甲", URL: "https://music.163.com/song?id=1"},
		{Platform: "netease", ID: "2", Title: "第二首", Artist: "乙", URL: "https://music.163.com/song?id=2"},
		{Platform: "qq", ID: "3", Title: "第三首", Artist: "丙", URL: "https://y.qq.com/n/ryqq/songDetail/3"},
		{Platform: "qq", ID: "4", Title: "第四首", Artist: "丁", URL: "https://y.qq.com/n/ryqq/songDetail/4"},
	}
	text, markup := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: tracks}, 0)
	if strings.Contains(text, "第二首") || strings.Contains(text, "第四首") || !strings.Contains(text, "第一首") || !strings.Contains(text, "本批 4 首 · 第 1 首") || strings.Contains(text, "已避开") || strings.Contains(text, "点歌名") || strings.Contains(text, "\n1. ") {
		t.Fatalf("first song was not alone: %s", text)
	}
	if !strings.Contains(text, "🎵 <b>遗珠</b>\n──────────────────\n") {
		t.Fatalf("heading lost the shared card frame: %s", text)
	}
	if !strings.Contains(text, `歌曲: <a href="https://music.163.com/song?id=1">第一首</a>`) || !strings.Contains(text, "艺人: 甲") || !strings.Contains(text, "专辑: 专辑甲") || strings.Contains(text, "听过") || strings.Contains(text, "收藏过") {
		t.Fatalf("card should show only title, artist and album: %s", text)
	}
	labels := buttonTexts(markup)
	for _, unwanted := range []string{"下载前 3 首", "全部下载", "换一批", "下一组", "这组入库", "1 入库", "上一首", "跳过", "试听"} {
		if labels[unwanted] {
			t.Fatalf("old button still present: %s", unwanted)
		}
	}
	if !labels["入库"] || !labels["不再推荐"] || !labels["重新拾遗"] || !labels["取消"] {
		t.Fatalf("missing song actions: %#v", labels)
	}
	if got := buttonRowTexts(markup); len(got) != 2 || got[0][0] != "入库" || got[0][1] != "不再推荐" || got[1][0] != "重新拾遗" || got[1][1] != "取消" {
		t.Fatalf("button rows = %#v", got)
	}
	if markup.InlineKeyboard[0][0].Data != "download|card|0" || markup.InlineKeyboard[1][0].Data != "find|netease|1" || markup.InlineKeyboard[1][1].Data != "cancel|card" {
		t.Fatalf("callbacks = %s / %s / %s", markup.InlineKeyboard[0][0].Data, markup.InlineKeyboard[1][0].Data, markup.InlineKeyboard[1][1].Data)
	}
	second, secondMarkup := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: tracks}, 1)
	if strings.Contains(second, "第一首") || !strings.Contains(second, "第二首") || !strings.Contains(second, "本批 4 首 · 第 2 首") {
		t.Fatalf("second song did not stay on the saved card: %s", second)
	}
	if got := buttonRowTexts(secondMarkup); len(got) != 2 || got[0][0] != "入库" || got[0][1] != "不再推荐" || got[1][0] != "重新拾遗" || got[1][1] != "取消" {
		t.Fatalf("middle action rows = %#v", got)
	}
	last, lastMarkup := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: tracks}, 3)
	if strings.Contains(last, "第一首") || !strings.Contains(last, "第四首") || !buttonTexts(lastMarkup)["重新拾遗"] || !buttonTexts(lastMarkup)["取消"] {
		t.Fatalf("last song lost its action row: %s %#v", last, buttonTexts(lastMarkup))
	}
	skipped, _ := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: tracks, SkippedOwned: 4}, 0)
	if strings.Contains(skipped, "已避开") || strings.Contains(skipped, "点歌名") {
		t.Fatalf("owned skips or listen hint returned to the song card: %s", skipped)
	}
	short, shortMarkup := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: tracks[:1], SkippedOwned: 1, Unconfirmed: 2, Requested: 1}, 0)
	if strings.Contains(short, "没找到确定的版本") || strings.Contains(short, "曲库已有") || strings.Contains(short, "这首怎么样") {
		t.Fatalf("card should not explain skipped candidates: %s", short)
	}
	if buttonTexts(shortMarkup)["未处理的全部入库"] || buttonTexts(shortMarkup)["下一首"] || !buttonTexts(shortMarkup)["入库"] || !buttonTexts(shortMarkup)["重新拾遗"] {
		t.Fatalf("single song action row = %#v", buttonTexts(shortMarkup))
	}
	full, _ := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: tracks[:3], Unconfirmed: 2, Requested: 3}, 0)
	if strings.Contains(full, "没有找满") {
		t.Fatalf("a full result still explained a shortfall: %s", full)
	}
	acted := append([]discovery.Track(nil), tracks[:2]...)
	acted[0].CardAction = "queued"
	acted[1].CardAction = "dismissed"
	actedText, actedMarkup := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: acted}, 0)
	if !strings.Contains(actedText, "排队") || strings.Contains(actedText, "不再推荐") || strings.Contains(actedText, "第二首") {
		t.Fatalf("the queued song was not alone: %s", actedText)
	}
	if buttonTexts(actedMarkup)["入库"] || buttonTexts(actedMarkup)["不再推荐"] || buttonTexts(actedMarkup)["这组入库"] || buttonTexts(actedMarkup)["未处理的全部入库"] || !buttonTexts(actedMarkup)["重新拾遗"] {
		t.Fatalf("finished song kept action buttons: %#v", buttonTexts(actedMarkup))
	}
	dismissedText, dismissedMarkup := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: acted}, 1)
	if !strings.Contains(dismissedText, "不再推荐") || !strings.Contains(dismissedText, "第二首") {
		t.Fatalf("dismissed song lost its status: %s", dismissedText)
	}
	if buttonTexts(dismissedMarkup)["入库"] || buttonTexts(dismissedMarkup)["不再推荐"] || !buttonTexts(dismissedMarkup)["重新拾遗"] || !buttonTexts(dismissedMarkup)["取消"] {
		t.Fatalf("dismissed song lost cancel/redraw: %#v", buttonTexts(dismissedMarkup))
	}
	if shortMarkup == nil {
		t.Fatal("short card lost its keyboard")
	}
}

func TestRediscoverCardKeepsTheOriginalFrameAndAddsCompactFacts(t *testing.T) {
	track := discovery.Track{
		Platform: "netease", ID: "1", Title: "东方之珠 (Live)", Artist: "罗大佑", Album: "北京-香港演唱会",
		URL: "https://music.163.com/song?id=1", Duration: 201, Favorite: true, Plays: 12,
		LastPlayed: time.Now().AddDate(0, 0, -40), CardAction: discovery.Running,
	}
	text, markup := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: []discovery.Track{track}}, 0)
	want := "🎵 <b>遗珠 · 网易云</b>\n──────────────────\n歌曲: <a href=\"https://music.163.com/song?id=1\">东方之珠 (Live)</a>\n艺人: 罗大佑\n专辑: 北京-香港演唱会\n时长: 3:21\n次数: 12次\n\n⏳ 正在整理入库..."
	if text != want {
		t.Fatalf("card = %q, want %q", text, want)
	}
	if got := buttonRowTexts(markup); len(got) != 1 || len(got[0]) != 2 || got[0][0] != "重新拾遗" || got[0][1] != "取消" {
		t.Fatalf("button rows = %#v", got)
	}
}

func TestRediscoverCardTruncatesAndEscapesLongArtistAlbum(t *testing.T) {
	track := discovery.Track{
		Platform: "qq", ID: "1", Title: "标题", Artist: strings.Repeat("歌", 30) + "<&>",
		Album: strings.Repeat("专", 40) + `"`, URL: "https://y.qq.com/n/ryqq/songDetail/1",
	}
	text, _ := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: []discovery.Track{track}}, 0)
	if strings.Contains(text, "<&>") || !strings.Contains(text, "艺人: "+strings.Repeat("歌", 29)+"...") || !strings.Contains(text, "专辑: "+strings.Repeat("专", 29)+"...") {
		t.Fatalf("long artist or album was not escaped and truncated: %s", text)
	}
	if strings.Contains(text, "时长:") || strings.Contains(text, "次数:") || strings.Contains(text, "⏳") {
		t.Fatalf("missing facts left extra fields: %s", text)
	}
}

func TestRediscoverCardOmitsMissingFields(t *testing.T) {
	track := discovery.Track{Platform: "youtube", ID: "1", Title: "只有歌名", URL: "https://music.youtube.com/watch?v=1"}
	text, _ := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: []discovery.Track{track}}, 0)
	want := "🎵 <b>遗珠 · YouTube</b>\n──────────────────\n歌曲: <a href=\"https://music.youtube.com/watch?v=1\">只有歌名</a>"
	if text != want {
		t.Fatalf("card = %q, want %q", text, want)
	}
}

func TestRediscoverEmptyCopyExplainsSeparateSkipReasons(t *testing.T) {
	owned := rediscoverEmptyCopy("netease", discovery.RediscoverBatch{SkippedOwned: 2})
	if !strings.Contains(owned, "曲库里都有了") || strings.Contains(owned, "没找到确定的版本") {
		t.Fatal(owned)
	}
	both := rediscoverEmptyCopy("qq", discovery.RediscoverBatch{SkippedOwned: 1, Unconfirmed: 3})
	if !strings.Contains(both, "有 3 首没找到确定的版本") || !strings.Contains(both, "曲库里都有了") {
		t.Fatal(both)
	}
	only := rediscoverEmptyCopy("youtube", discovery.RediscoverBatch{Unconfirmed: 4})
	if !strings.Contains(only, "有 4 首没找到确定的版本") {
		t.Fatal(only)
	}
	reasons := rediscoverEmptyCopy("netease", discovery.RediscoverBatch{SkippedDismissed: 1, SkippedQueued: 2, SkippedArtistCap: 3, SkippedDuplicate: 4})
	for _, want := range []string{"还有 2 首在等上传", "有几首因为重复或同一个歌手出现太多", "曲库里都有了"} {
		if !strings.Contains(reasons, want) {
			t.Fatalf("missing %q: %s", want, reasons)
		}
	}
}

func TestRediscoverRedrawAndCancelButtons(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	track := discovery.Track{Platform: "netease", ID: "1", Title: "一", Artist: "甲", URL: "https://music.163.com/song?id=1", Verified: true}
	card, err := store.SaveCard(context.Background(), discovery.Card{User: 1, Chat: 1, Kind: "recommend", Tracks: []discovery.Track{track}, Requested: 1})
	if err != nil {
		t.Fatal(err)
	}
	app := &BotApp{cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"1"}}}, discovery: &discovery.Service{Store: store}}
	ctx := &discoveryCallbackContext{sender: &tb.User{ID: 1}, chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate}, callback: &tb.Callback{}, message: &tb.Message{ID: 1, Chat: &tb.Chat{ID: 1}}, args: []string{"cancel", "card"}}
	ctx.callback.Message = ctx.message
	if err = app.HandleDiscoveryCallback(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx.edited, "已取消") {
		t.Fatalf("cancel copy = %q", ctx.edited)
	}
	if _, err = store.Card(context.Background(), card.Token, 1, 1); err != nil {
		t.Fatalf("cancel unexpectedly removed the saved card: %v", err)
	}
}

func TestFavoritesActionsRewriteTheSameCard(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &BotApp{
		cfg:       &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"1"}}},
		discovery: &discovery.Service{Store: store, Enabled: true},
	}
	msg := &tb.Message{ID: 7, Chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate}}
	click := func(action, key string) *discoveryCallbackContext {
		ctx := &discoveryCallbackContext{
			args:     []string{action, key},
			sender:   &tb.User{ID: 1},
			chat:     &tb.Chat{ID: 1, Type: tb.ChatPrivate},
			callback: &tb.Callback{ID: "cb", Message: msg},
			message:  msg,
		}
		if err := app.HandleDiscoveryCallback(ctx); err != nil {
			t.Fatal(err)
		}
		if len(ctx.sent) != 0 {
			t.Fatalf("%s opened a new message: %v", action, ctx.sent)
		}
		return ctx
	}
	if edited := click("history", "menu").edited; !strings.Contains(edited, "找回遗珠") {
		t.Fatalf("history did not become the platform menu: %s", edited)
	}
	if edited := click("pause", "netease").edited; !strings.Contains(edited, "收藏") {
		t.Fatalf("pause did not refresh the favorites card: %s", edited)
	}
	syncCard := click("sync", "menu")
	if edited := syncCard.waitEdited(t, "还没绑定收藏账号"); !strings.Contains(edited, "收藏") {
		t.Fatalf("sync did not stay on the favorites card: %s", edited)
	}
	if edited := click("retry", "menu").edited; !strings.Contains(edited, "没有需要重试的入库异常") {
		t.Fatalf("retry did not stay on the favorites card: %s", edited)
	}
}

func TestRediscoverDecisionStaysOnTheCard(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tracks := []discovery.Track{
		{Platform: "netease", ID: "1", Title: "一", Artist: "甲", URL: "https://music.163.com/song?id=1", Verified: true},
		{Platform: "netease", ID: "2", Title: "二", Artist: "乙", URL: "https://music.163.com/song?id=2", Verified: true},
	}
	card, err := store.SaveCard(context.Background(), discovery.Card{User: 1, Chat: 1, Kind: "recommend", Tracks: tracks})
	if err != nil {
		t.Fatal(err)
	}
	app := &BotApp{
		cfg:       &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"1"}}},
		discovery: &discovery.Service{Store: store},
	}
	msg := &tb.Message{ID: 3, Chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate}}
	click := func(action, extra string) *discoveryCallbackContext {
		ctx := &discoveryCallbackContext{
			args:     []string{action, card.Token, extra},
			sender:   &tb.User{ID: 1},
			chat:     &tb.Chat{ID: 1, Type: tb.ChatPrivate},
			callback: &tb.Callback{ID: "cb", Message: msg},
			message:  msg,
		}
		if err := app.HandleDiscoveryCallback(ctx); err != nil {
			t.Fatal(err)
		}
		if len(ctx.sent) != 0 {
			t.Fatalf("%s opened a new message: %v", action, ctx.sent)
		}
		return ctx
	}
	if edited := click("ignore", "0").edited; !strings.Contains(edited, "不再推荐") || strings.Contains(edited, "已提交入库") {
		t.Fatalf("dismiss did not stay on the card: %s", edited)
	}
	saved, err := store.Card(context.Background(), card.Token, 1, 1)
	if err != nil || saved.Tracks[0].CardAction != "dismissed" {
		t.Fatalf("card action = %#v err=%v", saved.Tracks, err)
	}
	click("download", "0")
	jobs, _ := store.Jobs(context.Background(), 1, false)
	if len(jobs) != 0 {
		t.Fatal("dismissed track was queued")
	}
	if edited := click("download", "1").edited; !strings.Contains(edited, "排队") || !strings.Contains(edited, "二") || strings.Contains(edited, "一") {
		t.Fatalf("queue did not stay on the card: %s", edited)
	}
	click("download", "1")
	jobs, _ = store.Jobs(context.Background(), 1, false)
	if len(jobs) != 1 {
		t.Fatalf("second click queued again: %d", len(jobs))
	}
}

func TestRediscoverBatchKeepsSuccessfulRowsWhenOneEnqueueFails(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tracks := []discovery.Track{
		{Platform: "netease", ID: "1", Title: "一", Artist: "甲", URL: "https://music.163.com/song?id=1", Verified: true},
		{Platform: "netease", ID: "2", Title: "二", Artist: "乙", URL: "https://music.163.com/song?id=2", Verified: true},
	}
	card, err := store.SaveCard(context.Background(), discovery.Card{User: 1, Chat: 1, Kind: "recommend", Tracks: tracks})
	if err != nil {
		t.Fatal(err)
	}
	app := &BotApp{cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"1"}}}, discovery: &discovery.Service{Store: store}}
	msg := &tb.Message{ID: 3, Chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate}}
	ctx := &discoveryCallbackContext{
		args: []string{"batch", card.Token, "all", "0"}, sender: &tb.User{ID: 1},
		chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate}, callback: &tb.Callback{ID: "cb", Message: msg}, message: msg,
	}
	if err = app.HandleDiscoveryCallback(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx.edited, "提交了 2 首，排队 2 首，跳过 0 首") {
		t.Fatalf("batch result = %s", ctx.edited)
	}
	saved, err := store.Card(context.Background(), card.Token, 1, 1)
	if err != nil || saved.Tracks[0].CardAction != discovery.Queued || saved.Tracks[1].CardAction != discovery.Queued {
		t.Fatalf("saved card = %+v err=%v", saved.Tracks, err)
	}
}

func TestRediscoverPagerMovesAcrossTheSavedCard(t *testing.T) {
	tracks := []discovery.Track{
		{Platform: "netease", ID: "1", Title: "第一首", Artist: "甲", Verified: true},
		{Platform: "netease", ID: "2", Title: "第二首", Artist: "乙", Verified: true},
		{Platform: "netease", ID: "3", Title: "第三首", Artist: "丙", Verified: true},
	}
	text, markup := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: tracks}, 1)
	if !strings.Contains(text, "第二首") || !strings.Contains(text, "本批 3 首 · 第 2 首") {
		t.Fatalf("middle card = %s", text)
	}
	if got := buttonRowTexts(markup); len(got) != 2 || got[0][0] != "入库" || got[0][1] != "不再推荐" || got[1][0] != "重新拾遗" || got[1][1] != "取消" {
		t.Fatalf("middle action rows = %#v", got)
	}
}

func TestRediscoverRestorePreferenceMakesTheSongAvailableAgain(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	track := discovery.Track{Platform: "netease", ID: "1", Title: "一", Artist: "甲", URL: "https://music.163.com/song?id=1", Verified: true}
	card, err := store.SaveCard(context.Background(), discovery.Card{User: 1, Chat: 1, Kind: "recommend", Tracks: []discovery.Track{track}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Preference(context.Background(), 1, track, true); err != nil {
		t.Fatal(err)
	}
	app := &BotApp{cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"1"}}}, discovery: &discovery.Service{Store: store}}
	msg := &tb.Message{ID: 9, Chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate}}
	ctx := &discoveryCallbackContext{
		args: []string{"restore", card.Token, "0"}, sender: &tb.User{ID: 1},
		chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate}, callback: &tb.Callback{ID: "cb", Message: msg}, message: msg,
	}
	if err = app.HandleDiscoveryCallback(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ctx.edited, "恢复推荐") {
		t.Fatalf("restored card kept restore button: %s", ctx.edited)
	}
	saved, err := store.Card(context.Background(), card.Token, 1, 1)
	if err != nil || saved.Tracks[0].CardAction != "" {
		t.Fatalf("stored card = %+v err=%v", saved.Tracks, err)
	}
	excluded, err := store.Excluded(context.Background(), 1)
	if err != nil || excluded[discovery.Identity(track)] {
		t.Fatalf("preference was not restored: %+v err=%v", excluded, err)
	}
}

func TestFavoritesCommandRendersHTMLOnFirstSend(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &BotApp{cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"7"}}}, discovery: &discovery.Service{Store: store}}
	ctx := &discoveryCallbackContext{sender: &tb.User{ID: 7}, chat: &tb.Chat{ID: 7, Type: tb.ChatPrivate}}
	if err := app.FavoritesCommand(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ctx.sent) != 1 || !strings.Contains(ctx.sent[0], "<b>收藏同步</b>") || strings.Contains(ctx.sent[0], "<b>") && !strings.Contains(ctx.sent[0], "</b>") {
		t.Fatalf("favorites command sent raw HTML: %#v", ctx.sent)
	}
	htmlMode := false
	for _, opt := range ctx.sendOpts[0] {
		if mode, ok := opt.(string); ok && mode == tb.ModeHTML {
			htmlMode = true
		}
	}
	if !htmlMode {
		t.Fatalf("favorites command did not send with HTML parse mode: %#v", ctx.sendOpts)
	}
}

func TestRediscoverAllSettledOffersTheSameBatchAgain(t *testing.T) {
	tracks := []discovery.Track{
		{Platform: "netease", ID: "1", Title: "一", Artist: "甲", CardAction: discovery.Completed},
		{Platform: "netease", ID: "2", Title: "二", Artist: "乙", CardAction: "dismissed"},
	}
	_, markup := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: tracks, Requested: 1}, 0)
	if markup.InlineKeyboard[len(markup.InlineKeyboard)-1][0].Data != "find|netease|1" {
		t.Fatalf("settled card did not offer the same batch: %#v", markup.InlineKeyboard)
	}
}

func TestRediscoverShortfallExplainsTheCandidatePool(t *testing.T) {
	tracks := []discovery.Track{{Platform: "netease", Title: "一首", Artist: "甲"}}
	text, _ := discoveryRecommendationCard(discovery.Card{Token: "card", Tracks: tracks, SkippedOwned: 2, Unconfirmed: 1, Requested: 1}, 0)
	for _, unwanted := range []string{"这首怎么样", "没找到确定的版本", "曲库已有", "候选池"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("card should not explain skipped candidates: %s", text)
		}
	}
}

func TestFavoritesCardSeparatesSourceAndIngestFailures(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &BotApp{discovery: &discovery.Service{Store: store}}
	ctx := context.Background()
	for i, title := range []string{"下载失败一", "下载失败二"} {
		if _, err = store.Enqueue(ctx, 7, 7, discovery.Track{Platform: "netease", ID: strconv.Itoa(i + 1), Title: title, Artist: "甲", URL: "https://music.163.com/song?id=" + strconv.Itoa(i+1), Verified: true}); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := store.Jobs(ctx, 7, false)
	if err != nil || len(jobs) != 2 {
		t.Fatal(err, jobs)
	}
	for _, job := range jobs {
		if err = store.Finish(ctx, job, discovery.Failed, "下载失败", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	text, markup, err := app.renderFavorites(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "2 异常") || !buttonTexts(markup)["重试"] || !buttonTexts(markup)["查看异常"] {
		t.Fatalf("ingest failures = %s %#v", text, buttonTexts(markup))
	}
	if strings.Contains(text, "同步异常") {
		t.Fatalf("ingest failure leaked into source status: %s", text)
	}
}

func TestFavoritesCardOpensPendingUploadsAndShowsRecoveryHint(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &BotApp{discovery: &discovery.Service{Store: store}}
	ctx := context.Background()
	id, err := store.Enqueue(ctx, 7, 7, discovery.Track{Platform: "netease", ID: "pending-1", Title: "待上传歌", Artist: "甲", URL: "https://music.163.com/song?id=pending-1", Verified: true})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Job(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Finish(ctx, job, discovery.PendingUpload, "WebDAV 423", "saved locally", 0); err != nil {
		t.Fatal(err)
	}
	text, markup, err := app.renderFavorites(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "1 待上传") || !buttonTexts(markup)["查看待上传"] || !buttonTexts(markup)["检查上传"] {
		t.Fatalf("favorites pending entry = %s %#v", text, buttonTexts(markup))
	}
	jobs, err := store.PendingJobs(ctx, 7)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("pending jobs = %+v err=%v", jobs, err)
	}
	cardText, cardMarkup := discoveryPendingUploadCard(jobs[0], 0, 1)
	if !strings.Contains(cardText, "等待曲库确认") || !buttonTexts(cardMarkup)["检查上传"] {
		t.Fatalf("pending upload card = %s %#v", cardText, buttonTexts(cardMarkup))
	}
}

func TestFavoritesCardKeepsPlatformRowsCompact(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, _, err = store.Apply(ctx, 7, 7, discovery.Snapshot{Platform: "qq", Account: "99887766", Complete: true, Tracks: []discovery.Track{{Platform: "qq", ID: "1", Title: "歌", Artist: "甲", Verified: true}}}, true); err != nil {
		t.Fatal(err)
	}
	app := &BotApp{discovery: &discovery.Service{Store: store, Enabled: true}}
	text, markup, err := app.renderFavorites(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<b>QQ 音乐</b> · 已开启", "账号 998…766", "同步 ", "<b>网易云</b> · 未绑定", "<b>YouTube</b> · 未绑定", "<b>入库</b>", "暂无任务"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "\n\n\n") {
		t.Fatalf("favorites card has extra blank spacing: %s", text)
	}
	if got := buttonRowTexts(markup); len(got) < 4 || !slices.Equal(got[1], []string{"重绑", "暂停", "同步 QQ 音乐"}) || got[len(got)-1][0] != "找回遗珠" || got[len(got)-1][1] != "同步全部" {
		t.Fatalf("favorites buttons = %#v", got)
	}
}

func TestDiscoveryPendingUploadAndFailedCardsUseTrackFields(t *testing.T) {
	job := discovery.Job{Track: discovery.Track{
		Platform: "netease", Title: "待上传歌", Artist: "甲", Album: "专辑", URL: "https://music.163.com/song?id=pending-1",
	}, Error: "WebDAV 423 <锁定>"}
	upload, uploadMarkup := discoveryPendingUploadCard(job, 0, 2)
	for _, want := range []string{"☁️ <b>待上传</b> · 1/2", "歌曲: <a href=\"https://music.163.com/song?id=pending-1\">待上传歌</a>", "艺人: 甲", "专辑: 专辑", "平台: 网易云", "等待曲库确认", "原因: WebDAV 423 &lt;锁定&gt;"} {
		if !strings.Contains(upload, want) {
			t.Fatalf("upload card missing %q: %s", want, upload)
		}
	}
	if got := buttonRowTexts(uploadMarkup); len(got) != 2 || got[0][0] != "检查上传" || got[0][1] != "返回收藏" || got[1][0] != "下一首" {
		t.Fatalf("upload buttons = %#v", got)
	}
	failed, failedMarkup := discoveryFailedCard(discovery.Job{Track: job.Track, Error: "下载失败"}, 1, 2)
	for _, want := range []string{"⚠️ <b>入库异常</b> · 2/2", "歌曲: <a href=", "原因: 下载失败"} {
		if !strings.Contains(failed, want) {
			t.Fatalf("failed card missing %q: %s", want, failed)
		}
	}
	if got := buttonRowTexts(failedMarkup); len(got) != 3 || got[0][0] != "重试" || got[0][1] != "换平台" || got[1][0] != "不再处理" || got[1][1] != "返回收藏" || got[2][0] != "上一首" {
		t.Fatalf("failed buttons = %#v", got)
	}
}

func TestFailedJobCardActionsRetryAndDismissOnlyThatSong(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	ids := make([]string, 0, 2)
	for i, title := range []string{"First", "Second"} {
		job := discovery.Track{Platform: "netease", ID: strconv.Itoa(i + 1), Title: title, Artist: "Artist", URL: "https://music.163.com/song?id=" + strconv.Itoa(i+1), Verified: true}
		id, err := store.Enqueue(ctx, 1, 1, job)
		if err != nil {
			t.Fatal(err)
		}
		j, err := store.Job(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Finish(ctx, j, discovery.Failed, "下载失败", "", 0); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	app := &BotApp{cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"1"}}}, discovery: &discovery.Service{Store: store}}
	msg := &tb.Message{ID: 7, Chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate}}
	click := func(action string, extra ...string) *discoveryCallbackContext {
		args := append([]string{action, ids[0]}, extra...)
		ctx := &discoveryCallbackContext{
			args: args, sender: &tb.User{ID: 1}, chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate},
			callback: &tb.Callback{ID: "cb", Message: msg}, message: msg,
		}
		if err := app.HandleDiscoveryCallback(ctx); err != nil {
			t.Fatal(err)
		}
		return ctx
	}
	click("retry", "0")
	retriedJob, _ := store.Job(ctx, ids[0])
	untouchedJob, _ := store.Job(ctx, ids[1])
	if retriedJob.State != discovery.Queued || untouchedJob.State != discovery.Failed {
		t.Fatalf("single retry changed the wrong job: retried=%+v untouched=%+v", retriedJob, untouchedJob)
	}
	refailed, err := store.Job(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Finish(ctx, refailed, discovery.Failed, "再次失败", "", 0); err != nil {
		t.Fatal(err)
	}
	if edited := click("dismiss_job", "0").edited; !strings.Contains(edited, "入库异常") {
		t.Fatalf("dismiss did not move to the remaining failed card: %s", edited)
	}
	job, err := store.Job(ctx, ids[0])
	if err != nil || job.State != discovery.Ignored {
		t.Fatalf("dismissed state=%+v err=%v", job, err)
	}
}

func TestFailedJobCrossPlatformLookupDoesNotMutateOnFailure(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	track := discovery.Track{Platform: "netease", ID: "1", Title: "Song", Artist: "Artist", URL: "https://music.163.com/song?id=1", Verified: true}
	id, err := store.Enqueue(ctx, 1, 1, track)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := store.Job(ctx, id)
	if err = store.Finish(ctx, job, discovery.Failed, "下载失败", "", 0); err != nil {
		t.Fatal(err)
	}
	var clicks int
	app := &BotApp{
		cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"1"}}},
		discovery: &discovery.Service{Store: store, Provider: snapshotOnlyProvider{fetch: func(context.Context, string) (discovery.Snapshot, error) {
			clicks++
			return discovery.Snapshot{}, errors.New("source unavailable")
		}}},
	}
	msg := &tb.Message{ID: 7, Chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate}}
	ctxCB := &discoveryCallbackContext{
		args: []string{"switch_source", id, "0"}, sender: &tb.User{ID: 1}, chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate},
		callback: &tb.Callback{ID: "cb", Message: msg}, message: msg,
	}
	if err = app.HandleDiscoveryCallback(ctxCB); err != nil {
		t.Fatal(err)
	}
	edited := ctxCB.waitEdited(t, "没能换平台")
	if !strings.Contains(edited, "原任务未改") {
		t.Fatalf("failure copy = %s", edited)
	}
	job, _ = store.Job(ctx, id)
	if job.State != discovery.Failed || job.Error != "下载失败" || clicks != 0 {
		t.Fatalf("failed lookup mutated job=%+v provider calls=%d", job, clicks)
	}
}

func TestFailedJobCrossPlatformRetryReplacesTheJobWithVerifiedAlternate(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	original := discovery.Track{Platform: "netease", ID: "1", Title: "Song", Artist: "Artist", URL: "https://music.163.com/song?id=1", Verified: true}
	id, err := store.Enqueue(ctx, 1, 1, original)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := store.Job(ctx, id)
	if err = store.Finish(ctx, job, discovery.Failed, "原平台失败", "", 0); err != nil {
		t.Fatal(err)
	}
	alternate := discovery.Track{Platform: "qq", ID: "q1", Title: "Song", Artist: "Artist", URL: "https://y.qq.com/n/ryqq/songDetail/q1", Verified: true}
	app := &BotApp{
		cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"1"}}},
		discovery: &discovery.Service{Store: store, Provider: snapshotOnlyProvider{fetch: func(context.Context, string) (discovery.Snapshot, error) {
			return discovery.Snapshot{}, errors.New("source unavailable")
		}}},
	}
	// Use a provider that resolves the alternate so the callback follows the
	// same production path as a safe cross-platform replacement.
	app.discovery.Provider = alternateProvider{alternate: alternate}
	msg := &tb.Message{ID: 7, Chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate}}
	ctxCB := &discoveryCallbackContext{
		args: []string{"switch_source", id, "0"}, sender: &tb.User{ID: 1}, chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate},
		callback: &tb.Callback{ID: "cb", Message: msg}, message: msg,
	}
	if err = app.HandleDiscoveryCallback(ctxCB); err != nil {
		t.Fatal(err)
	}
	_ = ctxCB.waitEdited(t, "入库异常")
	job, err = store.Job(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != discovery.Queued || job.Track.Platform != "qq" || job.Track.ID != "q1" || job.Track.URL != alternate.URL {
		t.Fatalf("alternate was not queued in place: %+v", job)
	}
}

type alternateProvider struct{ alternate discovery.Track }

func (alternateProvider) Fetch(context.Context, string) (discovery.Snapshot, error) {
	return discovery.Snapshot{}, errors.New("not used")
}
func (p alternateProvider) Resolve(context.Context, discovery.Track, string) (discovery.Track, error) {
	return p.alternate, nil
}
func (p alternateProvider) ResolveOn(context.Context, discovery.Track, string) (discovery.Track, error) {
	return p.alternate, nil
}
func (alternateProvider) Enrich(_ context.Context, t discovery.Track) (discovery.Track, bool, error) {
	return t, false, nil
}

func TestFavoritesSyncResultNoticeKeepsSuccessfulPlatformsSuccessful(t *testing.T) {
	notice := favoritesSyncResultNotice([]discovery.FavoriteSyncResult{
		{Platform: "netease", Status: "synced", Added: 2},
		{Platform: "qq", Status: "failed", Error: "登录失效"},
		{Platform: "youtube", Status: "paused"},
	}, errors.New("qq：登录失效"))
	for _, want := range []string{"部分平台同步完成", "网易云：成功，新增 2 首", "QQ 音乐：读取失败，基线未改", "YouTube：已暂停，未读取"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice missing %q: %s", want, notice)
		}
	}
	if strings.Contains(notice, "收藏基线未改\n\n") || strings.Contains(notice, "收藏基线未改。") {
		t.Fatalf("partial success reported as total baseline failure: %s", notice)
	}
}

func TestFavoritesCardShowsNamedSyncState(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, _, err = store.Apply(ctx, 7, 7, discovery.Snapshot{Platform: "netease", Account: "112345170", Complete: true, Tracks: []discovery.Track{{Platform: "netease", ID: "1", Title: "歌", Artist: "甲", Verified: true}}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SourceError(ctx, discovery.Binding{User: 7, Platform: "netease"}, "netease：网易云收藏暂时读不到，本轮跳过"); err != nil {
		t.Fatal(err)
	}
	app := &BotApp{discovery: &discovery.Service{Store: store, Enabled: true, PollInterval: 10 * time.Minute}}
	text, markup, err := app.renderFavorites(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<b>收藏同步</b>", "<b>网易云</b> · 已开启", "账号 112…170", "同步 ", "⚠️ 网易云收藏暂时读不到", "<b>QQ 音乐</b> · 未绑定", "<b>入库</b>", "暂无任务"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "每 10 分钟") || strings.Contains(text, "私聊通知") || strings.Contains(text, "netease：") {
		t.Fatal(text)
	}
	labels := buttonTexts(markup)
	if !labels["重绑"] || !labels["暂停"] || !labels["绑定QQ音乐"] || !labels["绑定YouTube"] || labels["重新绑定"] {
		t.Fatalf("buttons = %#v", labels)
	}
}

func TestFavoritesCardNamesTheAllPlatformSyncAction(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &BotApp{discovery: &discovery.Service{Store: store, Enabled: true}}
	_, markup, err := app.renderFavorites(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !buttonTexts(markup)["同步全部"] || buttonTexts(markup)["现在同步"] {
		t.Fatalf("sync action = %#v", buttonTexts(markup))
	}
}

func TestFavoritesCardOmitsActionsThatHaveNothingToDo(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &BotApp{discovery: &discovery.Service{Store: store}}
	text, markup, err := app.renderFavorites(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	labels := buttonTexts(markup)
	if labels["同步全部"] || labels["重试"] || labels["处理待确认"] || labels["刷新状态"] {
		t.Fatalf("idle favorites card offered empty actions: %#v\n%s", labels, text)
	}
	if !labels["绑定网易云"] || !labels["找回遗珠"] {
		t.Fatalf("useful actions missing: %#v", labels)
	}
	if _, err = store.Enqueue(context.Background(), 7, 7, discovery.Track{Platform: "netease", ID: "9", Title: "失败的歌", Artist: "甲", URL: "https://music.163.com/song?id=9", Verified: true}); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.Jobs(context.Background(), 7, false)
	if err != nil || len(jobs) != 1 {
		t.Fatal(err, jobs)
	}
	if err = store.Finish(context.Background(), jobs[0], discovery.Failed, "暂时失败", "", 0); err != nil {
		t.Fatal(err)
	}
	_, markup, err = app.renderFavorites(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !buttonTexts(markup)["重试"] || !buttonTexts(markup)["查看异常"] {
		t.Fatal("failed job did not offer retry and inspection")
	}
}

func buttonTexts(markup *tb.ReplyMarkup) map[string]bool {
	out := map[string]bool{}
	for _, row := range buttonRowTexts(markup) {
		for _, text := range row {
			out[text] = true
		}
	}
	return out
}

func buttonRowTexts(markup *tb.ReplyMarkup) [][]string {
	if markup == nil {
		return nil
	}
	rows := make([][]string, 0, len(markup.InlineKeyboard))
	for _, row := range markup.InlineKeyboard {
		labels := make([]string, 0, len(row))
		for _, button := range row {
			raw, err := json.Marshal(button)
			if err != nil {
				continue
			}
			var wire struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(raw, &wire)
			if wire.Text != "" {
				labels = append(labels, wire.Text)
			}
		}
		rows = append(rows, labels)
	}
	return rows
}

func TestRediscoverMenuAsksOnlyForPlatform(t *testing.T) {
	text, markup := discoveryPlatformMenu()
	if text != "🎵 找回遗珠" || buttonTexts(markup)["下载前 3 首"] {
		t.Fatalf("platform menu = %q", text)
	}
	labels := buttonTexts(markup)
	for _, unwanted := range []string{"3 首", "6 首", "9 首"} {
		if labels[unwanted] {
			t.Fatalf("count selector still present: %s", unwanted)
		}
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			raw, err := json.Marshal(button)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Text string `json:"text"`
				Data string `json:"callback_data"`
			}
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Data) > 64 || wire.Text == "" || len([]rune(wire.Text)) > 64 {
				t.Fatalf("button does not fit Telegram: %s", raw)
			}
		}
	}
}

func assertNoListenButton(t *testing.T, markup *tb.ReplyMarkup) {
	t.Helper()
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			raw, err := json.Marshal(button)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Text string `json:"text"`
				URL  string `json:"url"`
			}
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.URL != "" || strings.Contains(wire.Text, "试听") {
				t.Fatalf("listen button still present: %s", raw)
			}
		}
	}
}

func TestRediscoverSongNameOpensTheListenLink(t *testing.T) {
	tracks := []discovery.Track{
		{Platform: "netease", Title: "网易云歌曲", Artist: "艺人", URL: "https://music.163.com/song?id=1"},
		{Platform: "qq", Title: "QQ歌曲", Artist: "艺人", URL: "https://y.qq.com/n/ryqq/songDetail/testmid"},
		{Platform: "youtube", Title: "A & B", Artist: "X <Y>", URL: "https://music.youtube.com/watch?v=dQw4w9WgXcQ&list=1"},
	}
	for index, track := range tracks[:2] {
		text, markup := discoveryRecommendationCard(discovery.Card{Token: "test-card", Tracks: tracks}, index)
		want := fmt.Sprintf(`歌曲: <a href="%s">%s</a>`, track.URL, track.Title)
		if !strings.Contains(text, want) || strings.Contains(text, "试听·") {
			t.Fatalf("title is not the listen link: %s", text)
		}
		assertNoListenButton(t, markup)
	}
	youtube, youtubeMarkup := discoveryRecommendationCard(discovery.Card{Token: "test-card", Tracks: tracks}, 2)
	if !strings.Contains(youtube, `歌曲: <a href="https://music.youtube.com/watch?v=dQw4w9WgXcQ&amp;list=1">A &amp; B</a>`) || !strings.Contains(youtube, "X &lt;Y&gt;") || strings.Contains(youtube, "网易云歌曲") {
		t.Fatalf("link or artist was not escaped: %s", youtube)
	}
	assertNoListenButton(t, youtubeMarkup)
	plain, plainMarkup := discoveryRecommendationCard(discovery.Card{Token: "plain", Tracks: []discovery.Track{{Title: "无链接", Artist: "甲"}}}, 0)
	if strings.Contains(plain, "<a ") || strings.Contains(plain, "点歌名试听") || !strings.Contains(plain, "无链接") {
		t.Fatalf("missing url should stay plain text: %s", plain)
	}
	for _, row := range plainMarkup.InlineKeyboard {
		for _, button := range row {
			raw, _ := json.Marshal(button)
			if strings.Contains(string(raw), `"url"`) {
				t.Fatalf("plain track grew a link button: %s", raw)
			}
		}
	}
}

func TestRediscoverProgressFollowsTheDownload(t *testing.T) {
	store, err := discovery.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	track := discovery.Track{Platform: "netease", ID: "1", Title: "发如雪", Artist: "周杰伦", URL: "https://music.163.com/song?id=1", Verified: true, Plays: 4}
	id, err := store.Enqueue(context.Background(), 1, 1, track)
	if err != nil || id == "" {
		t.Fatal(err, id)
	}
	track.IngestID = id
	track.CardAction = discovery.Queued
	card := discovery.Card{Token: "card", User: 1, Chat: 1, Tracks: []discovery.Track{track}, Page: 0}
	text, markup := discoveryRecommendationCard(card, 0)
	if !strings.Contains(text, "遗珠 · 网易云") || !strings.Contains(text, "排队") || buttonTexts(markup)["入库"] {
		t.Fatalf("queued card = %s %#v", text, buttonTexts(markup))
	}
	updated, changed, pending, err := syncRediscoverProgress(context.Background(), store, card)
	if err != nil || changed || !pending || updated.Tracks[0].CardAction != discovery.Queued {
		t.Fatalf("queued progress = %#v changed=%t pending=%t err=%v", updated.Tracks, changed, pending, err)
	}
	job, err := store.Job(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Finish(context.Background(), job, discovery.Running, "", "", 0); err != nil {
		t.Fatal(err)
	}
	updated, changed, pending, err = syncRediscoverProgress(context.Background(), store, updated)
	if err != nil || !changed || !pending || updated.Tracks[0].CardAction != discovery.Running {
		t.Fatalf("running progress = %#v changed=%t pending=%t err=%v", updated.Tracks, changed, pending, err)
	}
	runningText, _ := discoveryRecommendationCard(updated, 0)
	if !strings.Contains(runningText, "正在整理入库") {
		t.Fatal(runningText)
	}
	job, err = store.Job(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Finish(context.Background(), job, discovery.Completed, "", "ok", 0); err != nil {
		t.Fatal(err)
	}
	updated, changed, pending, err = syncRediscoverProgress(context.Background(), store, updated)
	if err != nil || !changed || pending || updated.Tracks[0].CardAction != discovery.Completed {
		t.Fatalf("done progress = %#v changed=%t pending=%t err=%v", updated.Tracks, changed, pending, err)
	}
	doneText, doneMarkup := discoveryRecommendationCard(updated, 0)
	if !strings.Contains(doneText, "已入库") || buttonTexts(doneMarkup)["入库"] || buttonTexts(doneMarkup)["这组入库"] || buttonTexts(doneMarkup)["未处理的全部入库"] {
		t.Fatalf("done card = %s %#v", doneText, buttonTexts(doneMarkup))
	}
	job, _ = store.Job(context.Background(), id)
	if err = store.Finish(context.Background(), job, discovery.Failed, "暂时失败", "", 0); err != nil {
		t.Fatal(err)
	}
	updated.Tracks[0].CardAction = discovery.Running
	failed, changed, pending, err := syncRediscoverProgress(context.Background(), store, updated)
	if err != nil || !changed || pending || !strings.Contains(rediscoverDecision(failed.Tracks[0]), "失败") || !strings.Contains(rediscoverDecision(failed.Tracks[0]), "暂时失败") {
		t.Fatalf("failure = %#v changed=%t pending=%t err=%v", failed.Tracks, changed, pending, err)
	}
}
