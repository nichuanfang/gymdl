package aichat

import (
	"strings"
)

// skillTriggers maps a skill to the user phrasings that should load it. It is a
// deterministic backstop for the model: prompt engineering alone was measured
// to miss the right skill in roughly half of realistic turns.
var skillTriggers = map[string][]string{
	"music-recommendation":      {"推荐", "想听", "适合", "来点", "有什么歌", "想找点"},
	"music-discovery-by-artist": {"喜欢", "类似", "像", "同风格", "这个歌手", "这种风格"},
	"music-library-query":       {"我有没有", "我的库", "库里", "下载过", "多少首", "收藏", "有没有这"},
	"music-download-failure":    {"失败", "报错", "下载不了", "下载失败", "-462", "503", "风控", "401", "403"},
	"music-download-plan":       {"批量", "一次下完", "这批", "怎么下最快", "一起下"},
	"music-playlist-curation":   {"分类", "整理歌单", "分个类", "归类", "按主题", "整理成"},
	"music-lyrics-search":       {"歌词", "这句词", "唱的什么", "什么歌里"},
	"music-metadata-normalize":  {"标签", "流派", "艺人名", "专辑名", "元数据"},
}

// RouteSkills returns the skills whose trigger words appear in the user's
// message, filtered by what is actually mounted.
func RouteSkills(text string, mounted []string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil
	}
	allowed := make(map[string]bool, len(mounted))
	for _, name := range mounted {
		allowed[name] = true
	}
	out := make([]string, 0, 2)
	for name, triggers := range skillTriggers {
		if !allowed[name] {
			continue
		}
		for _, trigger := range triggers {
			if strings.Contains(text, strings.ToLower(trigger)) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// SkillHint renders the routing result as a short instruction appended to the
// system prompt for this turn only.
func SkillHint(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return "\n\n本轮相关技能：" + strings.Join(names, "、") + "。作答前先调用 load_skill 载入。"
}
