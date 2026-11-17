package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/discovery"
	tb "gopkg.in/telebot.v4"
)

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
	_, markup := discoveryRecommendationCard(card)
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
	ctx := &discoveryCallbackContext{args: []string{"bind", c.Token}, sender: &tb.User{ID: 1}, chat: &tb.Chat{ID: 1, Type: tb.ChatPrivate}}
	if err := app.HandleDiscoveryCallback(ctx); err != nil {
		t.Fatal(err)
	}
	if ctx.responses != 1 || !strings.Contains(ctx.edited, "建立收藏基线") {
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
	args      []string
	sender    *tb.User
	chat      *tb.Chat
	responses int
	edited    string
	sent      []string
}

func (c *discoveryCallbackContext) Args() []string                        { return c.args }
func (c *discoveryCallbackContext) Sender() *tb.User                      { return c.sender }
func (c *discoveryCallbackContext) Chat() *tb.Chat                        { return c.chat }
func (c *discoveryCallbackContext) Respond(...*tb.CallbackResponse) error { c.responses++; return nil }
func (c *discoveryCallbackContext) Edit(v interface{}, _ ...interface{}) error {
	c.edited = fmt.Sprint(v)
	return nil
}

func (c *discoveryCallbackContext) Send(v interface{}, _ ...interface{}) error {
	c.sent = append(c.sent, fmt.Sprint(v))
	return nil
}

func TestRediscoverListenButtonsOpenExactPlatformLinks(t *testing.T) {
	tracks := []discovery.Track{
		{Platform: "netease", Title: "网易云歌曲", Artist: "艺人", URL: "https://music.163.com/song?id=1"},
		{Platform: "qq", Title: "QQ歌曲", Artist: "艺人", URL: "https://y.qq.com/n/ryqq/songDetail/testmid"},
		{Platform: "youtube", Title: "YouTube歌曲", Artist: "艺人", URL: "https://music.youtube.com/watch?v=dQw4w9WgXcQ"},
	}
	_, markup := discoveryRecommendationCard(discovery.Card{Token: "test-card", Tracks: tracks})
	targets := map[string]bool{}
	for _, track := range tracks {
		targets[track.URL] = false
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			raw, err := json.Marshal(button)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Text     string `json:"text"`
				URL      string `json:"url"`
				Callback string `json:"callback_data"`
			}
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.URL == "" {
				continue
			}
			if _, ok := targets[wire.URL]; !ok {
				t.Fatalf("listen button points to another song: %s", wire.URL)
			}
			if wire.Callback != "" || !strings.HasPrefix(wire.Text, "试听·") {
				t.Fatalf("listen button must open the platform rather than queue a download: %s", raw)
			}
			if targets[wire.URL] {
				t.Fatal("duplicate listen button")
			}
			targets[wire.URL] = true
		}
	}
	for target, found := range targets {
		if !found {
			t.Fatalf("missing platform listen link: %s", target)
		}
	}
}
