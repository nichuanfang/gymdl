package discovery

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

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
	var lines []string
	for _, platform := range platforms {
		tracks := by[platform]
		heads = append(heads, fmt.Sprintf("%s %d 首", freshPlatformLabel(platform), len(tracks)))
		for _, track := range tracks {
			title := strings.TrimSpace(track.Title)
			if title == "" {
				title = "未命名"
			}
			line := "· " + truncateRunes(title, 28)
			if artist := strings.TrimSpace(track.Artist); artist != "" {
				line += " — " + truncateRunes(artist, 18)
			}
			lines = append(lines, line)
		}
	}
	var text strings.Builder
	text.WriteString("收藏有新歌\n")
	text.WriteString(strings.Join(heads, " · "))
	text.WriteString("\n\n")
	shown := lines
	extra := 0
	if len(lines) > 8 {
		shown = lines[:8]
		extra = len(lines) - 8
	}
	text.WriteString(strings.Join(shown, "\n"))
	if extra > 0 {
		fmt.Fprintf(&text, "\n还有 %d 首", extra)
	}
	text.WriteString("\n\n已排队入库。")
	return text.String()
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
