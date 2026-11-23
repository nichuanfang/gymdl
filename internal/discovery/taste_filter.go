package discovery

import (
	"errors"
	"strings"
)

const TasteBoth = "both"

type TasteRequest struct {
	Platform     string      `json:"platform"`
	Filter       TasteFilter `json:"filter,omitempty"`
	ExploreFirst bool        `json:"explore_first,omitempty"`
	Page         int         `json:"page,omitempty"`
}

type TasteFilter struct {
	Scene    string `json:"scene,omitempty"`
	Genre    string `json:"genre,omitempty"`
	Language string `json:"language,omitempty"`
}

type tasteTerm struct {
	name    string
	aliases []string
}

var tasteScenes = []tasteTerm{
	{"通勤", []string{"通勤", "commute"}},
	{"放松", []string{"放松", "chill", "relax"}},
	{"运动", []string{"运动", "跑步", "健身", "workout"}},
	{"睡前", []string{"睡前", "助眠", "sleep"}},
}

var tasteGenres = []tasteTerm{
	{"爵士", []string{"爵士乐", "爵士", "jazz"}},
	{"摇滚", []string{"独立摇滚", "摇滚乐", "摇滚", "indie rock", "alternative rock", "rock"}},
	{"流行", []string{"流行乐", "流行", "pop", "cantopop", "mandopop", "粤语流行", "华语流行"}},
	{"民谣", []string{"民谣", "folk"}},
	{"电子", []string{"电子乐", "电子", "electronic", "electronica", "edm"}},
	{"古典", []string{"古典乐", "古典", "classical"}},
	{"嘻哈", []string{"嘻哈", "说唱", "hip-hop", "hip hop", "rap"}},
	{"节奏布鲁斯", []string{"节奏布鲁斯", "r&b", "rnb", "rhythm and blues"}},
	{"氛围音乐", []string{"氛围音乐", "ambient"}},
}

var tasteLanguages = []tasteTerm{
	{"粤语", []string{"广东话", "粤语", "粤语歌", "粤语流行", "cantonese", "cantopop"}},
	{"国语", []string{"普通话", "国语", "国语歌", "mandarin", "mandopop"}},
	{"英语", []string{"英文", "英语", "english"}},
	{"日语", []string{"日文", "日语", "japanese"}},
	{"韩语", []string{"韩文", "韩语", "korean"}},
	{"纯音乐", []string{"纯音乐", "器乐", "instrumental"}},
}

var tasteSceneCues = map[string][]string{
	"通勤": {"轻快", "节奏感", "明亮", "upbeat", "groovy", "feel good", "road trip", "commute"},
	"放松": {"柔和", "舒缓", "轻松", "平静", "安静", "calm", "relaxing", "chill", "mellow", "soft", "ambient"},
	"运动": {"运动", "节奏感", "激昂", "快节奏", "热血", "workout", "energetic", "upbeat", "dance", "high energy"},
	"睡前": {"睡前", "助眠", "安静", "舒缓", "柔和", "slow", "sleep", "calm", "peaceful", "ambient"},
}

func ValidTastePlatform(platform string) bool {
	return platform == TasteBoth || platform == "netease" || platform == "qq"
}

// ParseTasteRequest takes an optional leading platform followed by a scene,
// genre and/or language. The accepted words can be joined, as in 粤语爵士.
func ParseTasteRequest(payload string) (TasteRequest, error) {
	fields := strings.Fields(payload)
	req := TasteRequest{Platform: TasteBoth}
	if len(fields) > 0 && ValidTastePlatform(strings.ToLower(fields[0])) {
		req.Platform = strings.ToLower(fields[0])
		fields = fields[1:]
	}
	rest := Normalize(strings.Join(fields, " "))
	groups := []struct {
		terms []tasteTerm
		value *string
	}{{tasteScenes, &req.Filter.Scene}, {tasteGenres, &req.Filter.Genre}, {tasteLanguages, &req.Filter.Language}}
	for rest != "" {
		var matched, value string
		var dest *string
		for _, group := range groups {
			for _, term := range group.terms {
				// Combined catalog tags are only used as evidence, so typing
				// 粤语流行 still sets both language and genre.
				for _, alias := range append([]string{term.name}, term.aliases...) {
					key := Normalize(alias)
					switch key {
					case "粤语流行", "cantopop", "mandopop", "华语流行":
						continue
					}
					if len(key) > len(matched) && strings.HasPrefix(rest, key) {
						matched, value, dest = key, term.name, group.value
					}
				}
			}
		}
		if dest == nil || (*dest != "" && *dest != value) {
			return TasteRequest{}, errors.New("暂不支持这个条件。可试 /recommend 粤语爵士 或 /recommend 放松 纯音乐")
		}
		*dest = value
		rest = rest[len(matched):]
	}
	return req, nil
}

func (f TasteFilter) Label() string {
	parts := make([]string, 0, 3)
	for _, part := range []string{f.Scene, f.Language, f.Genre} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " · ")
}

// WithScene keeps explicitly selected genre and language when changing scene.
func (f TasteFilter) WithScene(scene string) (TasteFilter, bool) {
	switch scene {
	case "taste":
		return TasteFilter{}, true
	case "commute", "relax", "workout", "sleep":
		for _, term := range tasteScenes {
			if name := tasteLabel([]tasteTerm{term}, scene); name != "" {
				f.Scene = name
				return f, true
			}
		}
	}
	return f, false
}

func tasteLabel(terms []tasteTerm, raw string) string {
	key := Normalize(raw)
	for _, term := range terms {
		if key == Normalize(term.name) {
			return term.name
		}
		for _, alias := range term.aliases {
			if key == Normalize(alias) {
				return term.name
			}
		}
	}
	return ""
}

func tasteTagSet(tags []string) map[string]struct{} {
	set := make(map[string]struct{}, len(tags)*2)
	for _, tag := range tags {
		if key := Normalize(tag); key != "" {
			set[key] = struct{}{}
		}
		for _, terms := range [][]tasteTerm{tasteGenres, tasteLanguages} {
			if label := tasteLabel(terms, tag); label != "" {
				set[Normalize(label)] = struct{}{}
			}
		}
	}
	return set
}

func (f TasteFilter) matches(tags map[string]struct{}) bool {
	for _, term := range []string{f.Genre, f.Language} {
		if _, ok := tags[Normalize(term)]; term != "" && !ok {
			return false
		}
	}
	if f.Scene == "" {
		return true
	}
	for _, cue := range tasteSceneCues[f.Scene] {
		if _, ok := tags[Normalize(cue)]; ok {
			return true
		}
	}
	return false
}
