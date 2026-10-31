package web

import (
	"strings"
	"unicode/utf8"
)

// AgentResultKind separates real single tracks from compilations, mixes and
// ambient uploads. Providers happily return lifestyle videos for keyword
// searches, and without this distinction the agent would offer a 40-minute
// playlist as if it were one downloadable song.
type AgentResultKind string

const (
	AgentKindSingle   AgentResultKind = "single"
	AgentKindPlaylist AgentResultKind = "playlist"
	AgentKindMix      AgentResultKind = "mix"
	AgentKindNoise    AgentResultKind = "noise"
	AgentKindUnknown  AgentResultKind = "unknown"
)

// shortVideoSeconds is the cutoff below which a Bilibili upload is almost
// never a full song.
const shortVideoSeconds = 45

// longMixSeconds marks uploads that are compilations rather than one track.
const longMixSeconds = 15 * 60

var playlistMarkers = []string{
	"歌单", "playlist", "合集", "合辑", "混剪", "精选", "循环",
	"半小时", "1小时", "一小时", "整轨", "full album", "compilation",
	"搭车", "开车听", "通勤听", "学习用",
}

var noiseMarkers = []string{
	"白噪音", "环境音", "雨声", "助眠", "asmr", "冥想", "纯音乐合集",
	"背景音", "white noise", "rain sounds", "ambient",
	"鞋款", "穿搭", "测评", "开箱", "好物",
	"直播", "录屏", "存档", "剧社", "解说", "reaction", "对话", "访谈",
	"频率", "解压", "自然声", "音效",
	"驾驶技巧", "老司机", "干货", "教程", "攻略", "技巧", "新手",
}

// sceneWords are the generic descriptors that should never reach a provider as
// a search keyword; they reliably produce lifestyle videos and compilations.
var sceneWords = []string{
	"通勤", "写代码", "下雨天", "雨天", "深夜", "自习", "健身", "睡前",
	"放松", "治愈", "背景", "氛围", "适合", "推荐",
	"下班", "上班", "路上", "开车", "跑步", "工作", "学习", "阅读", "咖啡",
	// Time-of-day words are scene descriptors too, but a lone one ("傍晚",
	// 《阴天》) is still a plausible song title and is let through below.
	"傍晚", "黄昏", "夜晚", "晚上", "凌晨", "清晨", "早晨", "午后", "午夜",
	"阴天", "雪天", "周末", "节日", "旅行", "加班", "运动",
}

// sceneFillers are connective words that carry no searchable meaning once the
// scene descriptors around them are removed ("适合雨天通勤听的歌").
var sceneFillers = []string{
	"的歌曲", "听的歌", "听的", "的歌", "来几首", "什么歌", "什么",
	"一些", "一点", "一下", "几首", "听听", "音乐", "歌曲", "时候",
	"想听", "想", "帮我", "帮", "可以", "请", "给我", "找点", "听点",
	"的", "听", "歌", "首", "时", "点", "个", "些", "吧", "呢", "啊", "要",
	"来", "整", "弄", "搞", "放", "输", "出", "推荐", "适合", "有没有", "给我",
}

// sceneIntentMarkers are words that only make sense as a request for a
// scenario. Their presence means the term is asking for a mood, not a title.
var sceneIntentMarkers = []string{
	"推荐", "想听", "来点", "适合", "有没有", "给我", "找点", "听点", "整点",
	"听的", "听的歌", "的歌",
}

// IsSceneKeyword reports whether a search term is a scene description rather
// than a concrete song or artist.
//
// The test must not reject a real title that happens to contain a scene word:
// 《雨天》《下雨天》are songs, and "孙燕姿 雨天" is an artist-plus-title query.
// Only a term that leaves nothing searchable after removing scene words and
// fillers is treated as a scene, and a lone scene word is still allowed
// through so titles like "放松" remain searchable.
func IsSceneKeyword(keyword string) bool {
	keyword = strings.TrimSpace(strings.ToLower(keyword))
	if keyword == "" {
		return true
	}
	matched := 0
	remainder := keyword
	for _, word := range sceneWords {
		if strings.Contains(remainder, word) {
			matched++
			remainder = strings.ReplaceAll(remainder, word, " ")
		}
	}
	if matched == 0 {
		return false
	}
	intent := false
	for _, marker := range sceneIntentMarkers {
		if strings.Contains(keyword, marker) {
			intent = true
			break
		}
	}
	remainder = strings.Join(strings.Fields(remainder), " ")
	for {
		trimmed := remainder
		for _, filler := range sceneFillers {
			trimmed = strings.ReplaceAll(trimmed, filler, " ")
		}
		// Intent words ("推荐", "想听", "来点") describe what the user wants,
		// not what to search for, so they are not leftover content.
		for _, marker := range sceneIntentMarkers {
			trimmed = strings.ReplaceAll(trimmed, marker, " ")
		}
		trimmed = strings.Join(strings.Fields(trimmed), " ")
		if trimmed == remainder {
			break
		}
		remainder = trimmed
	}
	// Anything left that is not pure punctuation means the user named
	// something concrete (a song or artist); let the provider decide.
	leftover := strings.TrimFunc(remainder, func(r rune) bool {
		return r == ' ' || r == '·' || r == '-' || r == ',' || r == '，' || r == '、'
	})
	// A stray quantifier or verb ("点", "个", "些") is not a searchable term.
	leftover = strings.TrimFunc(leftover, func(r rune) bool {
		return r == ' ' || strings.ContainsRune("点个些首", r)
	})
	if leftover != "" {
		return false
	}
	return intent || matched >= 2
}

// ClassifyAgentResult labels a provider hit. Only the single/unknown kinds are
// offered as one-click downloads.
func ClassifyAgentResult(item SearchResultItem) AgentResultKind {
	name := strings.ToLower(strings.TrimSpace(item.Name))
	if strings.Contains(name, "playlist") {
		return AgentKindPlaylist
	}
	for _, marker := range noiseMarkers {
		if strings.Contains(name, marker) {
			return AgentKindNoise
		}
	}
	// Bilibili titles that stuff many separators are almost always compilation
	// or lifestyle uploads, not a single track.
	if item.Platform == "bilibili" {
		separators := strings.Count(name, "|") + strings.Count(name, "｜") + strings.Count(name, "丨")
		if separators >= 3 || utf8.RuneCountInString(name) > 60 {
			return AgentKindPlaylist
		}
	}
	for _, marker := range playlistMarkers {
		if strings.Contains(name, marker) {
			return AgentKindPlaylist
		}
	}
	switch item.Platform {
	case "bilibili":
		if item.DurationSec > 0 && item.DurationSec < shortVideoSeconds {
			return AgentKindUnknown
		}
		if item.DurationSec >= longMixSeconds {
			return AgentKindMix
		}
	case "youtube":
		if item.DurationSec >= longMixSeconds {
			return AgentKindMix
		}
	}
	return AgentKindSingle
}

// lyricsLikeName reports whether a title is probably a song performed by a
// human rather than a scene description. It is used to reject searches like
// "雨天通勤" that only ever produce compilations.
func lyricsLikeName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if utf8.RuneCountInString(name) > 40 {
		return false
	}
	return true
}
