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
	if s == nil || s.Notify == nil || len(items) == 0 {
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
		if err := s.Notify(chat, notices[chat]); err != nil {
			utils.WarnWithFormat("[Discovery] 新收藏通知发送失败: %v", err)
		}
	}
}

func formatFreshFavoriteNotices(items []FreshFavorite) map[int64]string {
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
	out := make(map[int64]string, len(buckets))
	for chat, b := range buckets {
		out[chat] = renderFreshFavorites(orderFreshPlatforms(b.order), b.by)
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

func renderFreshFavorites(platforms []string, by map[string][]Track) string {
	var heads []string
	var sections []string
	shown := 0
	extra := 0
	for _, platform := range platforms {
		tracks := by[platform]
		label := freshPlatformLabel(platform)
		heads = append(heads, fmt.Sprintf("%s %d 首", label, len(tracks)))
		lines := []string{"<b>" + utils.EscapeHTML(label) + "</b>"}
		for _, track := range tracks {
			if shown >= 8 {
				extra++
				continue
			}
			shown++
			line := "· " + freshListenTitle(track.Title, track.URL)
			if artist := strings.TrimSpace(track.Artist); artist != "" {
				line += " — " + utils.EscapeHTML(truncateRunes(artist, 18))
			}
			lines = append(lines, line)
		}
		if len(lines) > 1 {
			sections = append(sections, strings.Join(lines, "\n"))
		}
	}
	var text strings.Builder
	text.WriteString("🎵 <b>收藏有新歌</b>\n")
	text.WriteString("──────────────────\n")
	text.WriteString(strings.Join(heads, " · "))
	if len(sections) > 0 {
		text.WriteString("\n\n")
		text.WriteString(strings.Join(sections, "\n\n"))
	}
	if extra > 0 {
		fmt.Fprintf(&text, "\n\n还有 %d 首", extra)
	}
	text.WriteString("\n\n已排队入库。")
	return text.String()
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
