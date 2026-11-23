package bot

import (
	"github.com/nichuanfang/gymdl/internal/discovery"
	tb "gopkg.in/telebot.v4"
)

func freshFavoritePreviewURL(app *BotApp, tracks []discovery.Track) string {
	for _, track := range tracks {
		card := discovery.Card{Kind: discovery.TasteCardKind, Tracks: []discovery.Track{track}}
		if previewURL := app.discoveryCardPreviewURL(card, 0); previewURL != "" {
			return previewURL
		}
	}
	return ""
}

func sendDiscoveryFreshNotice(bot tb.API, chat int64, text, previewURL string) error {
	if previewURL == "" {
		_, err := bot.Send(tb.ChatID(chat), text, tb.ModeHTML)
		return err
	}
	_, err := bot.Raw("sendMessage", map[string]any{
		"chat_id":    chat,
		"text":       text,
		"parse_mode": string(tb.ModeHTML),
		"link_preview_options": map[string]any{
			"url":                previewURL,
			"prefer_large_media": true,
		},
	})
	return err
}
