package linkparser

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/processor/video"
)

// 链接解析器

// linkTypeMatcher 处理器匹配规则
type linkTypeMatcher struct {
	domains  []string // 快速判定域名
	patterns []*regexp.Regexp
	handler  processor.Processor
}

/* ---------------------- 变量区 ---------------------- */

// 快速域名 -> matcher 索引映射表（加速匹配）
var matcherMap = make(map[string]*linkTypeMatcher)

// 通用 URL 提取
var genericURLRegex = regexp.MustCompile(`https?://[^\s<>"'()]*[\w/#?=&-]`)

// 配置
var cfg *config.Config

/* ---------------------- 解析器初始化 ---------------------- */

// 初始化
func InitLinkParser(c *config.Config) {
	cfg = c
	for i := range linkTypeMatchers {
		l := &linkTypeMatchers[i]
		for _, d := range l.domains {
			matcherMap[d] = l
		}
	}
}

/* ---------------------- 核心方法 ---------------------- */

// ⚡ ParseLink 解析链接
func ParseLink(text string) (string, processor.Processor) {
	raw := genericURLRegex.FindString(text)
	if raw == "" {
		return "", nil
	}
	// 链接清洗
	raw = cleanURLTrailingChars(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return "", nil
	}

	host := strings.ToLower(u.Host)
	handler, ok := quickMatch(host, u)
	if ok {
		return raw, handler
	}

	// fallback: 正则穷举匹配
	for i := range linkTypeMatchers {
		for _, r := range linkTypeMatchers[i].patterns {
			if r.MatchString(raw) {
				return raw, linkTypeMatchers[i].handler
			}
		}
	}
	return "", nil
}

/* ---------------------- 辅助方法 ---------------------- */

// ⚡Trim
func cleanURLTrailingChars(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	end := len(runes)
	for end > 0 {
		r := runes[end-1]
		if unicode.IsSpace(r) || strings.ContainsRune(".,!:;\"'()`[]{}", r) {
			end--
			continue
		}
		if r > 127 && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			end--
			continue
		}
		break
	}
	return string(runes[:end])
}

func quickMatch(host string, u *url.URL) (processor.Processor, bool) {
	if p, ok := matcherMap[host]; ok {
		for _, re := range p.patterns {
			if re.MatchString(u.String()) {
				// 如果开启了 YoutubeMusicMode，并且当前匹配的是普通 YouTube 视频处理器
				if cfg.YTDLPConfig.YoutubeMusicMode {
					if _, isYoutubeVideo := p.handler.(*video.YoutubeProcessor); isYoutubeVideo {
						return &music.YoutubeMusicProcessor{}, true
					}
				}
				return p.handler, true
			}
		}
	}
	return nil, false
}
