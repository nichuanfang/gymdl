package linkparser

import (
	"regexp"

	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/processor/video"
)

// 处理器规则集
var linkTypeMatchers = []linkTypeMatcher{
	/* ---------------------- 网易云音乐 ---------------------- */
	{
		domains: []string{
			"music.163.com",
			"y.music.163.com",
			"163cn.tv",
			"163cn.link",
		},
		patterns: []*regexp.Regexp{
			// 网页端 / 移动端链接
			regexp.MustCompile(`^https?://(?:y\.)?music\.163\.com/(?:#/)?(?:m/)?(?:song|playlist|album|artist|djradio|program)\?id=\d+(?:&\S*)?$`),

			// 网易云 App 短链
			regexp.MustCompile(`^https?://163cn\.tv/[A-Za-z0-9]+(?:\?.*)?$`),

			// 新版短链
			regexp.MustCompile(`^https?://163cn\.link/[A-Za-z0-9]+(?:\?.*)?$`),
		},
		handler: &music.NetEaseProcessor{},
	},

	/* ---------------------- YouTube Music ---------------------- */
	{
		domains: []string{"music.youtube.com"},
		patterns: []*regexp.Regexp{
			// YouTube Music 视频
			regexp.MustCompile(`^https?://music\.youtube\.com/watch\?v=[\w-]+(?:&.*)?$`),
		},
		handler: &music.YoutubeMusicProcessor{},
	},

	/* ---------------------- Apple Music ---------------------- */
	{
		domains: []string{"music.apple.com"},
		patterns: []*regexp.Regexp{
			// 公共播放列表（pl.u- 开头）
			regexp.MustCompile(`^https?://music\.apple\.com/[a-z]{2}/playlist/[A-Za-z0-9._%\-]+/pl\.u-[A-Za-z0-9]+(?:\?.*)?$`),

			// 资料库播放列表（p. 开头）
			regexp.MustCompile(`^https?://music\.apple\.com/library/playlist/p\.[A-Za-z0-9]+(?:\?.*)?$`),

			// 单曲（song）
			regexp.MustCompile(`^https?://music\.apple\.com/[a-z]{2}/song/[A-Za-z0-9%._\-]+/\d+(?:\?.*)?$`),

			// 专辑（album）
			regexp.MustCompile(`^https?://music\.apple\.com/[a-z]{2}/album/[A-Za-z0-9%._\-]+/\d+(?:\?.*)?$`),
		},
		handler: &music.AppleMusicProcessor{},
	},

	/* ---------------------- SoundCloud ---------------------- */
	{
		domains: []string{"soundcloud.com", "snd.sc"},
		patterns: []*regexp.Regexp{
			// 用户主页 / 曲目 / 播放列表
			regexp.MustCompile(`^https?://(?:soundcloud\.com|snd\.sc)/[A-Za-z0-9_\-]+/(?:sets/[A-Za-z0-9_\-]+|[A-Za-z0-9_\-]+)(?:\?.*)?$`),
		},
		handler: &music.SoundCloudProcessor{},
	},

	/* ---------------------- QQ 音乐 ---------------------- */
	{
		domains: []string{"y.qq.com", "c.y.qq.com", "m.y.qq.com", "i.y.qq.com"},
		patterns: []*regexp.Regexp{
			// 原来的匹配
			regexp.MustCompile(`^https?://(?:y\.qq\.com|c\d*\.y\.qq\.com|m\.y\.qq\.com|i\.y\.qq\.com)/(?:n/ryqq/(?:songDetail|albumDetail|playlist)|base/fcgi-bin/u|v8/playsong\.html)(?:[/?#].*)?$`),

			// 新增：匹配新版分享页（歌单、专辑、歌曲等）
			regexp.MustCompile(`^https?://(?:i\.y\.qq\.com|m\.y\.qq\.com)/n2/m/share/details/(?:taoge|album|song)\.html(?:[/?#].*)?$`),
		},
		handler: &music.QQMusicProcessor{},
	},

	/* ---------------------- Spotify ---------------------- */
	{
		domains: []string{"open.spotify.com", "play.spotify.com"},
		patterns: []*regexp.Regexp{
			// track / album / playlist + ID (通常 22 字符)
			regexp.MustCompile(`^https?://(?:open\.spotify\.com|play\.spotify\.com)/(?:track|album|playlist)/[A-Za-z0-9]+(?:\?.*)?$`),
		},
		handler: &music.SpotifyProcessor{},
	},
	/* ---------------------- YouTube ---------------------- */
	{
		domains: []string{"www.youtube.com", "youtube.com", "youtu.be"},
		patterns: []*regexp.Regexp{
			// 普通 YouTube 视频
			regexp.MustCompile(`^https?://(?:www\.)?youtube\.com/watch\?v=[\w-]+(?:&.*)?$`),

			// 短链格式
			regexp.MustCompile(`^https?://youtu\.be/[\w-]+(?:\?.*)?$`),
		},
		handler: &video.YoutubeProcessor{},
	},
	/* ---------------------- 抖音 ---------------------- */
	{
		domains: []string{"www.douyin.com", "v.douyin.com"},
		patterns: []*regexp.Regexp{
			// 正常视频链接
			regexp.MustCompile(`https?://www\.douyin\.com/video/[\w-]+`),
			// 短链接形式
			regexp.MustCompile(`https?://v\.douyin\.com/[\w-]+/?`),
		},
		handler: &video.DouyinProcessor{},
	},
    /* ---------------------- Bilibili ---------------------- */
    {
        domains: []string{"www.bilibili.com", "bilibili.com", "b23.tv", "m.bilibili.com"},
        patterns: []*regexp.Regexp{
            // 网页端标准视频链接 (BV/av)
            regexp.MustCompile(`^https?://(?:www\.)?bilibili\.com/video/(?:BV|av)[A-Za-z0-9]+(?:[/?#].*)?$`),

            // B站短链接 (b23.tv)
            regexp.MustCompile(`^https?://b23\.tv/[A-Za-z0-9]+(?:\?.*)?$`),

            // 手机端分享链接
            regexp.MustCompile(`^https?://m\.bilibili\.com/video/(?:BV|av)[A-Za-z0-9]+(?:[/?#].*)?$`),

            // 稍后再看或特定播放列表中的视频
            regexp.MustCompile(`^https?://(?:www\.)?bilibili\.com/list/(?:watchlater|ml\d+)(?:.*)?\b(?:BV|av)[A-Za-z0-9]+(?:[/?#].*)?$`),
        },
        // 假设你在 video package 下定义了 BilibiliProcessor
        handler: &video.BiliBiliProcessor{},
    },
	/* ---------------------- 待补充 ---------------------- */
}
