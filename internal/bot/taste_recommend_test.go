package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/discovery"
	tb "gopkg.in/telebot.v4"
)

type emptyTasteLibrary struct{}

func (emptyTasteLibrary) Contains(context.Context, discovery.Track) (bool, error) { return false, nil }

func TestPersonalizedRecommendationThroughTelebot(t *testing.T) {
	ctx := t.Context()
	store, err := discovery.Open(filepath.Join(t.TempDir(), "taste.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	type update struct {
		method string
		data   map[string]string
	}
	requests := make(chan update, 48)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := filepath.Base(r.URL.Path)
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decode %s: %v", method, err)
		}
		params := make(map[string]string, len(raw))
		for key, value := range raw {
			var text string
			if json.Unmarshal(value, &text) == nil {
				params[key] = text
			} else {
				params[key] = string(value)
			}
		}
		requests <- update{method, params}
		w.Header().Set("Content-Type", "application/json")
		if method == "answerCallbackQuery" {
			fmt.Fprint(w, `{"ok":true,"result":true}`)
			return
		}
		result := map[string]any{"message_id": 42, "chat": map[string]any{"id": 1, "type": "private"}}
		var replyMarkup string
		if err := json.Unmarshal(raw["reply_markup"], &replyMarkup); err == nil && replyMarkup != "" {
			result["reply_markup"] = json.RawMessage(replyMarkup)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}); err != nil {
			t.Errorf("encode %s response: %v", method, err)
		}
	}))
	defer server.Close()
	bot, err := tb.NewBot(tb.Settings{Offline: true, Synchronous: true, Token: "1:test", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	app := &BotApp{bot: bot, cfg: &config.Config{Telegram: &config.TelegramConfig{AllowedUsers: []string{"1", "2"}}},
		discovery: &discovery.Service{
			Store: store, Library: emptyTasteLibrary{},
			History: func(context.Context) ([]discovery.Track, error) {
				return []discovery.Track{{Title: "听过的歌", Artist: "常听艺人", Plays: 20}}, nil
			},
			Taste: &discovery.TasteSources{
				HistoryOwner: 1,
				Similar:      func(context.Context, string) ([]string, error) { return []string{"相近艺人"}, nil },
				Search: func(_ context.Context, platform, artist string) ([]discovery.Track, error) {
					var tracks []discovery.Track
					for i := 1; i <= 4; i++ {
						id := fmt.Sprint(i)
						if artist == "相近艺人" {
							id = "1" + id
						}
						link := "https://music.163.com/song?id=" + id
						cover := ""
						if platform == "qq" {
							link = "https://y.qq.com/n/ryqq/songDetail/" + id
							cover = "https://y.gtimg.cn/music/photo_new/T002R300x300M000002ef2VB0Qfb30_2.jpg"
						}
						tracks = append(tracks, discovery.Track{Platform: platform, ID: id, Title: "曲目" + id, Artist: artist, Album: "测试专辑", CoverURL: cover, Duration: 200, URL: link, Verified: true})
					}
					return tracks, nil
				},
				TrackTags: func(context.Context, discovery.Track) ([]string, error) {
					return []string{"jazz", "Cantonese", "calm"}, nil
				},
			},
		},
	}
	app.cfg.PlaylistOrganizer = &config.PlaylistOrganizerConfig{WebAppURL: "https://webapp.example.com"}
	bot.Handle("/recommend", app.RecommendCommand)
	bot.Handle(&discoveryEndpoint, app.HandleDiscoveryCallback)
	chat := &tb.Chat{ID: 1, Type: tb.ChatPrivate}
	songMessage := &tb.Message{ID: 42, Chat: chat}
	click := func(user int64, data string) {
		t.Helper()
		if data == "" || len(data) > 64 {
			t.Fatalf("bad Telegram callback: %q", data)
		}
		bot.ProcessUpdate(tb.Update{Callback: &tb.Callback{ID: "cb", Sender: &tb.User{ID: user}, Data: data, Message: songMessage}})
	}
	next := func(method, field, want string) map[string]string {
		t.Helper()
		timer := time.NewTimer(6 * time.Second)
		defer timer.Stop()
		var skipped []update
		for {
			select {
			case request := <-requests:
				if request.method == method && strings.Contains(request.data[field], want) {
					if strings.HasPrefix(method, "edit") && request.data["message_id"] != "42" {
						t.Fatalf("edited a different message: %v", request.data)
					}
					return request.data
				}
				skipped = append(skipped, request)
			case <-timer.C:
				t.Fatalf("missing %s with %s containing %q; saw %#v", method, field, want, skipped)
				return nil
			}
		}
	}
	waitReady := func() {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if app.discoveryCommandMu.TryLock() {
				app.discoveryCommandMu.Unlock()
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("recommendation did not finish")
	}
	button := func(data map[string]string, label string) string {
		t.Helper()
		var markup tb.ReplyMarkup
		if err := json.Unmarshal([]byte(data["reply_markup"]), &markup); err != nil {
			t.Fatalf("decode reply_markup %q from %v: %v", data["reply_markup"], data, err)
		}
		for _, row := range markup.InlineKeyboard {
			for _, btn := range row {
				if btn.Text == label {
					return btn.Data
				}
			}
		}
		t.Fatalf("missing %s: %s", label, data["reply_markup"])
		return ""
	}
	buttonRows := func(data map[string]string) [][]string {
		t.Helper()
		var markup tb.ReplyMarkup
		if err := json.Unmarshal([]byte(data["reply_markup"]), &markup); err != nil {
			t.Fatal(err)
		}
		rows := make([][]string, len(markup.InlineKeyboard))
		for i, row := range markup.InlineKeyboard {
			for _, btn := range row {
				rows[i] = append(rows[i], btn.Text)
			}
		}
		return rows
	}
	nextTasteCard := func(needle string) (map[string]string, map[string]string) {
		t.Helper()
		card := next("editMessageText", "text", needle)
		previewEdit := next("editMessageText", "link_preview_options", "url")
		var preview struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(previewEdit["link_preview_options"]), &preview); err != nil || preview.URL == "" {
			t.Fatalf("recommendation preview options = %q (%v)", previewEdit["link_preview_options"], err)
		}
		return card, previewEdit
	}
	bot.ProcessUpdate(tb.Update{Message: &tb.Message{ID: 7, Chat: chat, Sender: &tb.User{ID: 1}, Text: "/recommend 粤语爵士"}})
	next("sendMessage", "text", "找适合你的歌")
	first, previewEdit := nextTasteCard("个性化推荐 · 网易云")
	if !strings.Contains(first["text"], "粤语 · 爵士") || !strings.Contains(first["text"], `href="`+discoveryListenURL("https://music.163.com/song?id=1")+`"`) {
		t.Fatalf("first card lost typed filter or listen link: %v", first)
	}
	var preview struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(previewEdit["link_preview_options"]), &preview); err != nil || preview.URL != "https://music.163.com/song?id=1" {
		t.Fatalf("NetEase preview options = %q (%v)", previewEdit["link_preview_options"], err)
	}
	waitReady()
	if jobs, err := store.Jobs(ctx, 1, false); err != nil || len(jobs) != 0 {
		t.Fatalf("showing a recommendation created a job: %v, %v", jobs, err)
	}
	if got, want := buttonRows(first), [][]string{{"入库", "不再推荐"}, {"换一首", "更多设置"}}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("recommendation card buttons = %v, want %v", got, want)
	}
	oldNext := button(first, "换一首")
	if !strings.HasPrefix(oldNext, "\f") {
		t.Fatalf("Telebot callback namespace was not encoded: %q", oldNext)
	}
	if got := button(previewEdit, "换一首"); got != oldNext {
		t.Fatalf("preview edit changed callback data: got %q, want %q", got, oldNext)
	}
	click(2, oldNext)
	next("answerCallbackQuery", "text", "卡片已过期")
	click(1, oldNext)
	second, _ := nextTasteCard("风格相近")
	waitReady()
	click(1, oldNext)
	next("answerCallbackQuery", "text", "卡片已过期")

	click(1, button(second, "更多设置"))
	settings := next("editMessageText", "text", "选择推荐场景或来源")
	if got := buttonRows(settings); len(got) != 5 || got[0][0] != "通勤" || got[1][1] != "按口味" || got[2][0] != "网易云" || got[3][0] != "自动选择（网易云 / QQ）" || got[4][0] != "返回歌曲卡片" {
		t.Fatalf("recommendation settings buttons = %v", got)
	}
	click(1, button(settings, "返回歌曲卡片"))
	returned, _ := nextTasteCard("个性化推荐")
	if got, want := buttonRows(returned), [][]string{{"入库", "不再推荐"}, {"换一首", "更多设置"}}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("returned recommendation buttons = %v, want %v", got, want)
	}
	click(1, button(returned, "更多设置"))
	settings = next("editMessageText", "text", "选择推荐场景或来源")
	click(1, button(settings, "放松"))
	third, _ := nextTasteCard("放松 · 粤语 · 爵士")
	waitReady()
	click(1, button(third, "更多设置"))
	settings = next("editMessageText", "text", "选择推荐场景或来源")
	click(1, button(settings, "自动选择（网易云 / QQ）"))
	fourth, _ := nextTasteCard("个性化推荐 ·")
	if !strings.Contains(fourth["text"], "放松 · 粤语 · 爵士") {
		t.Fatalf("automatic platform selection dropped the filters: %v", fourth)
	}
	waitReady()
	click(1, button(fourth, "更多设置"))
	settings = next("editMessageText", "text", "选择推荐场景或来源")
	click(1, button(settings, "QQ 音乐"))
	fifth, qqPreview := nextTasteCard("个性化推荐 · QQ 音乐")
	if !strings.Contains(fifth["text"], "放松 · 粤语 · 爵士") {
		t.Fatalf("platform switch dropped the filters: %v", fifth)
	}
	var qqPreviewOptions struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(qqPreview["link_preview_options"]), &qqPreviewOptions); err != nil {
		t.Fatalf("decode QQ preview options: %v", err)
	}
	parsedQQPreview, err := url.Parse(qqPreviewOptions.URL)
	if err != nil || parsedQQPreview.Host != "webapp.example.com" || parsedQQPreview.Path != "/api/discovery/music-preview" || parsedQQPreview.Query().Get("mid") == "" {
		t.Fatalf("QQ recommendation preview URL = %q (%v)", qqPreviewOptions.URL, err)
	}
	if got, want := button(qqPreview, "换一首"), button(fifth, "换一首"); got != want {
		t.Fatalf("QQ preview edit changed callback data: got %q, want %q", got, want)
	}
	waitReady()
	download := button(fifth, "入库")
	click(1, download)
	next("editMessageText", "text", "排队中")
	click(1, download)
	jobs, err := store.Jobs(ctx, 1, false)
	if err != nil || len(jobs) != 1 || !jobs[0].Track.TasteRecommended || jobs[0].Track.Platform != "qq" {
		t.Fatalf("expected one QQ recommendation job: %v, %v", jobs, err)
	}
	if err := store.Finish(ctx, jobs[0], discovery.NeedsConfirmation, "同版时长存疑", "", 0); err != nil {
		t.Fatal(err)
	}
	confirm := next("editMessageText", "text", "同版时长存疑")
	click(1, button(confirm, "确认入库"))
	next("editMessageText", "text", "排队中")
	job, err := store.Job(ctx, jobs[0].ID)
	if err != nil || !job.Track.Approved {
		t.Fatalf("confirmation missed the job: %+v, %v", job, err)
	}
	if err = store.Finish(ctx, job, discovery.Completed, "", "", 0); err != nil {
		t.Fatal(err)
	}
	done := next("editMessageText", "text", "已入库")
	if !strings.Contains(done["text"], "个性化推荐") {
		t.Fatalf("progress changed the card type: %v", done)
	}
}
