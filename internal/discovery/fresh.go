package discovery

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nichuanfang/gymdl/internal/musiclink"
	"github.com/nichuanfang/gymdl/utils"
)

func (s *Service) notifyFresh(items []FreshFavorite) {
	if s == nil || len(items) == 0 || (s.Notify == nil && s.NotifySong == nil) {
		return
	}
	users := map[int64]int64{}
	for _, item := range items {
		users[item.Chat] = item.User
	}
	notices := formatFreshFavoriteNotices(items)
	chats := make([]int64, 0, len(notices))
	for chat := range notices {
		chats = append(chats, chat)
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
	for _, chat := range chats {
		if s.Authorized != nil && !s.Authorized(users[chat], chat) {
			continue
		}
		for _, notice := range notices[chat] {
			var err error
			if s.NotifySong != nil {
				err = s.NotifySong(chat, notice.Text, notice.Track)
			} else {
				err = s.Notify(chat, notice.Text)
			}
			if err != nil {
				utils.WarnWithFormat("[Discovery] 新收藏通知发送失败: %v", err)
			}
		}
	}
}

type freshNotice struct {
	Text  string
	Track Track
}

func formatFreshFavoriteNotices(items []FreshFavorite) map[int64][]freshNotice {
	type bucket struct {
		order []string
		by    map[string][]Track
	}
	buckets := map[int64]*bucket{}
	for _, item := range items {
		b := buckets[item.Chat]
		if b == nil {
			b = &bucket{by: map[string][]Track{}}
			buckets[item.Chat] = b
		}
		if _, ok := b.by[item.Platform]; !ok {
			b.order = append(b.order, item.Platform)
		}
		b.by[item.Platform] = append(b.by[item.Platform], item.Track)
	}
	out := make(map[int64][]freshNotice, len(buckets))
	for chat, b := range buckets {
		var cards []freshNotice
		for _, platform := range orderFreshPlatforms(b.order) {
			for _, track := range b.by[platform] {
				cards = append(cards, freshNotice{Text: renderFreshFavoriteCard(platform, track), Track: track})
			}
		}
		out[chat] = cards
	}
	return out
}

func orderFreshPlatforms(platforms []string) []string {
	rank := map[string]int{"netease": 0, "qq": 1, "youtube": 2}
	ordered := append([]string(nil), platforms...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, leftOK := rank[ordered[i]]
		right, rightOK := rank[ordered[j]]
		if leftOK && rightOK {
			return left < right
		}
		if leftOK != rightOK {
			return leftOK
		}
		return ordered[i] < ordered[j]
	})
	return ordered
}

func renderFreshFavoriteCard(platform string, track Track) string {
	var text strings.Builder
	fmt.Fprintf(&text, "🎵 <b>收藏有新歌 · %s</b>\n", utils.EscapeHTML(freshPlatformLabel(platform)))
	text.WriteString("──────────────────\n")
	fmt.Fprintf(&text, "歌曲: %s\n", freshListenTitle(track.Title, track.URL))
	if artist := strings.TrimSpace(track.Artist); artist != "" {
		fmt.Fprintf(&text, "艺人: %s\n", utils.EscapeHTML(truncateRunes(artist, 32)))
	}
	if album := strings.TrimSpace(track.Album); album != "" {
		fmt.Fprintf(&text, "专辑: %s\n", utils.EscapeHTML(truncateRunes(album, 32)))
	}
	if track.Duration > 0 {
		fmt.Fprintf(&text, "时长: %d:%02d\n", track.Duration/60, track.Duration%60)
	}
	text.WriteString("\n已排队入库。")
	return strings.TrimRight(text.String(), "\n")
}

func freshListenTitle(title, rawURL string) string {
	shown := utils.EscapeHTML(truncateRunes(strings.TrimSpace(title), 32))
	if shown == "" {
		shown = "未命名"
	}
	rawURL = strings.TrimSpace(musiclink.DirectAppLink(rawURL))
	if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "http://") {
		return shown
	}
	href := strings.ReplaceAll(utils.EscapeHTML(rawURL), `"`, "&quot;")
	return `<a href="` + href + `">` + shown + `</a>`
}

func freshPlatformLabel(platform string) string {
	switch platform {
	case "netease":
		return "网易云"
	case "qq":
		return "QQ 音乐"
	case "youtube":
		return "YouTube"
	default:
		platform = strings.TrimSpace(platform)
		if platform == "" {
			return "收藏"
		}
		return platform
	}
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}
