package bot

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/musicdedupe"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

func TestQQShareTelegramReusesResolvedLinkThroughConfirmation(t *testing.T) {
	t.Chdir(t.TempDir())
	const short = "http://c6.y.qq.com/base/fcgi-bin/u?__=share-token"
	const song = "https://y.qq.com/n/ryqq/songDetail/000EqCkp3nm7Fb"
	if err := os.MkdirAll(filepath.Dir(utils.QQMusicCredentialPath), 0o700); err != nil {
		t.Fatal(err)
	}
	credential := fmt.Sprintf(`{"musicid":7,"musickey":"test","expired_at":%d}`, time.Now().Add(time.Hour).Unix())
	if err := os.WriteFile(utils.QQMusicCredentialPath, []byte(credential), 0o600); err != nil {
		t.Fatal(err)
	}

	var shares, details, downloads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/base/fcgi-bin/u":
			shares.Add(1)
			w.Header().Set("Location", song)
			w.WriteHeader(http.StatusFound)
		case "/song/000EqCkp3nm7Fb/detail":
			details.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"track":{"mid":"000EqCkp3nm7Fb","title":"Bad Day","singer":[{"name":"Daniel Powter"}],"album":{"name":"Album"},"interval":230,"file":{"size_128mp3":1}}}}`)
		case "/song/000EqCkp3nm7Fb/url":
			downloads.Add(1)
			// Stop before obtaining audio; reaching this endpoint exercises the real downloader.
			fmt.Fprint(w, `{"code":0,"data":{"data":[]}}`)
		default:
			t.Errorf("unexpected QQ request to %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	for _, hasOldRecord := range []bool{false, true} {
		t.Run(fmt.Sprintf("old record=%t", hasOldRecord), func(t *testing.T) {
			shares.Store(0)
			details.Store(0)
			downloads.Store(0)
			api, calls := maintTestBot(t)
			app := maintTestApp(t, api)
			app.cfg.AdditionalConfig = &config.AdditionalConfig{}
			app.cfg.QQMusicApiConfig = &config.QQMusicApiConfig{Enable: true, Endpoint: upstream.URL, ProxyUrl: upstream.URL}
			app.pendingMusic = make(map[string]pendingMusicDownload)
			app.activeMusicKeys = make(map[string]struct{})
			store, err := musicdedupe.Open(filepath.Join(t.TempDir(), "dedupe.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			app.musicDedupe = store
			if hasOldRecord {
				// No ID and different metadata: only the original share URL can find this record.
				err := store.RecordSuccessfulDownload(t.Context(), musicdedupe.Track{
					Platform: string(processor.LinkQQMusic), SourceURL: short, Title: "Old entry",
				})
				if err != nil {
					t.Fatal(err)
				}
			}

			api.Handle(tb.OnText, app.HandleText)
			// Exercise Telebot routing, while the simulated downloader's intentional
			// no-audio result is already shown by HandleMusic.
			api.Handle(&duplicateCallbackEndpoint, func(c tb.Context) error {
				if err := app.HandleDuplicateMusicCallback(c); err != nil && !strings.Contains(err.Error(), "无法获取歌曲下载链接") {
					t.Errorf("unexpected callback result: %v", err)
				}
				return nil
			})
			sender := &tb.User{ID: 7}
			chat := &tb.Chat{ID: 7, Type: tb.ChatPrivate}
			api.ProcessUpdate(tb.Update{Message: &tb.Message{ID: 11, Sender: sender, Chat: chat, Text: short}})

			if hasOldRecord {
				maintWait(t, calls, "editMessageText", func(call maintCall) bool {
					return strings.Contains(call.text, "同一个链接")
				})
				if shares.Load() != 1 || details.Load() != 1 || downloads.Load() != 0 {
					t.Fatalf("wrong requests before confirmation: shares=%d details=%d downloads=%d", shares.Load(), details.Load(), downloads.Load())
				}
				if len(app.pendingMusic) != 1 {
					t.Fatalf("expected one duplicate confirmation, got %d", len(app.pendingMusic))
				}
				for token, pending := range app.pendingMusic {
					if pending.link != song || pending.track.SourceURL != short {
						t.Fatal("confirmation did not retain the resolved link and original identity")
					}
					api.ProcessUpdate(tb.Update{Callback: &tb.Callback{
						ID: "continue", Sender: sender, Message: pending.origin,
						Data: "\f" + duplicateCallbackUnique + "|continue|" + token,
					}})
				}
			}

			maintWait(t, calls, "editMessageText", func(call maintCall) bool {
				return strings.Contains(call.text, "无法获取歌曲下载链接")
			})
			if shares.Load() != 1 || details.Load() != 2 || downloads.Load() != 1 {
				t.Fatalf("wrong requests: shares=%d details=%d downloads=%d", shares.Load(), details.Load(), downloads.Load())
			}
		})
	}
}

func TestQQShareTelegramShowsOneSafeOriginalCard(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(upstream.Close)
	api, calls := maintTestBot(t)
	app := maintTestApp(t, api)
	app.cfg.AdditionalConfig = &config.AdditionalConfig{}
	app.cfg.QQMusicApiConfig = &config.QQMusicApiConfig{Enable: true, ProxyUrl: upstream.URL}
	store, err := musicdedupe.Open(filepath.Join(t.TempDir(), "dedupe.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app.musicDedupe = store
	api.Handle(tb.OnText, app.HandleText)

	api.ProcessUpdate(tb.Update{Message: &tb.Message{
		ID: 11, Sender: &tb.User{ID: 7}, Chat: &tb.Chat{ID: 7, Type: tb.ChatPrivate},
		Text: "http://c6.y.qq.com/base/fcgi-bin/u?__=private-share-token",
	}})
	sent := maintWait(t, calls, "sendMessage", nil)
	edit := maintWait(t, calls, "editMessageText", nil)
	if sent.text != "🔍 正在识别链接..." || edit.messageID != "201" ||
		!strings.Contains(edit.text, "QQ 音乐分享链接暂时打不开") || !strings.Contains(edit.text, "发送歌曲详情页链接") ||
		strings.Contains(edit.text, "private-share-token") || strings.Contains(edit.text, "http") {
		t.Fatalf("unexpected Telegram feedback: sent=%+v edit=%+v", sent, edit)
	}
	if requests.Load() != 2 {
		t.Fatalf("share request count = %d, want 2", requests.Load())
	}
	select {
	case call := <-calls:
		t.Fatalf("failure card was overwritten: %+v", call)
	default:
	}
}
