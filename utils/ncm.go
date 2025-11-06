package utils

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"regexp"

	"github.com/XiaoMengXinX/Music163Api-Go/api"
	"github.com/XiaoMengXinX/Music163Api-Go/types"
	"github.com/sirupsen/logrus"

	"github.com/XiaoMengXinX/Music163Api-Go/utils"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// data 网易云 cookie
var data utils.RequestData

var bot *tgbotapi.BotAPI
var botAdmin []int
var botAdminStr []string
var botName string
var cacheDir = "./cache"
var botAPI = "https://api.telegram.org"

// maxRetryTimes 最大重试次数, downloaderTimeout 下载超时时间
var maxRetryTimes, downloaderTimeout int

var (
	reg1   = regexp.MustCompile(`(.*)song\?id=`)
	reg2   = regexp.MustCompile("(.*)song/")
	regP1  = regexp.MustCompile(`(.*)program\?id=`)
	regP2  = regexp.MustCompile("(.*)program/")
	regP3  = regexp.MustCompile(`(.*)dj\?id=`)
	regP4  = regexp.MustCompile("(.*)dj/")
	reg5   = regexp.MustCompile("/(.*)")
	reg4   = regexp.MustCompile("&(.*)")
	reg3   = regexp.MustCompile(`\?(.*)`)
	regInt = regexp.MustCompile(`\d+`)
	regUrl = regexp.MustCompile("(http|https)://[\\w\\-_]+(\\.[\\w\\-_]+)+([\\w\\-.,@?^=%&:/~+#]*[\\w\\-@?^=%&/~+#])?")
)

var mdV2Replacer = strings.NewReplacer(
	"_", "\\_", "*", "\\*", "[", "\\[", "]", "\\]", "(",
	"\\(", ")", "\\)", "~", "\\~", "`", "\\`", ">", "\\>",
	"#", "\\#", "+", "\\+", "-", "\\-", "=", "\\=", "|",
	"\\|", "{", "\\{", "}", "\\}", ".", "\\.", "!", "\\!",
)

var (
	aboutText = `*Music163bot-Go v2*
Github: https://github.com/XiaoMengXinX/Music163bot-Go

\[编译环境] %s
\[编译版本] %s
\[编译哈希] %s
\[编译日期] %s
\[运行环境] %s`
	musicInfo = `「%s」- %s
专辑: %s
#网易云音乐 #%s %.2fMB %.2fkbps
via @%s`
	musicInfoMsg = `%s
专辑: %s
%s %.2fMB
`
	uploadFailed = `下载/发送失败
%v`
	statusInfo = `*\[统计信息\]*
数据库中总缓存歌曲数量: %d
当前对话 \[%s\] 缓存歌曲数量: %d
当前用户 \[[%d](tg://user?id=%d)\] 缓存歌曲数量: %d
`
	rmcacheReport    = `清除 [%s] 缓存成功`
	inputKeyword     = "请输入搜索关键词"
	inputIDorKeyword = "请输入歌曲ID或歌曲关键词"
	inputContent     = "请输入歌曲关键词/歌曲分享链接/歌曲ID"
	searching        = `搜索中...`
	noResults        = `未找到结果`
	noCache          = `歌曲未缓存`
	tapToDownload    = `点击上方按钮缓存歌曲`
	tapMeToDown      = `点我缓存歌曲`
	hitCache         = `命中缓存, 正在发送中...`
	sendMeTo         = `Send me to...`
	getLrcFailed     = `获取歌词失败, 歌曲可能不存在或为纯音乐`
	getUrlFailed     = `获取歌曲下载链接失败`
	fetchInfo        = `正在获取歌曲信息...`
	fetchInfoFailed  = `获取歌曲信息失败`
	waitForDown      = `等待下载中...`
	downloading      = `下载中...`
	downloadStatus   = " %s\n%.2fMB/%.2fMB %d%%"
	redownloading    = `下载失败，尝试重新下载中...`
	uploading        = `下载完成, 发送中...`
	md5VerFailed     = "MD5校验失败"
	reTrying         = "尝试重新下载中 (%d/%d)"
	retryLater       = "请稍后重试"

	reloading    = "重新加载中"
	callbackText = "Success"

	fetchingLyric   = "正在获取歌词中"
	downloadTimeout = `下载超时`
)

// 判断数组包含关系
func In(target string, strArray []string) bool {
	sort.Strings(strArray)
	index := sort.SearchStrings(strArray, target)
	if index < len(strArray) && strArray[index] == target {
		return true
	}
	return false
}

// 解析作曲家信息
func ParseArtist(songDetail types.SongDetailData) string {
	var artists string
	for i, ar := range songDetail.Ar {
		if i == 0 {
			artists = ar.Name
		} else {
			artists = fmt.Sprintf("%s/%s", artists, ar.Name)
		}
	}
	return artists
}

// 判断文件夹是否存在/新建文件夹
func DirExists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		err := os.Mkdir(path, os.ModePerm)
		if err != nil {
			logrus.Errorf("mkdir %v failed: %v\n", path, err)
		}
		return false
	}
	logrus.Errorf("Error: %v\n", err)
	return false
}

// 校验 md5
func VerifyMD5(filePath string, md5str string) (bool, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer f.Close()
	md5hash := md5.New()
	if _, err := io.Copy(md5hash, f); err != nil {
		return false, err
	}
	if hex.EncodeToString(md5hash.Sum(nil)) != md5str {
		return false, fmt.Errorf("failed:%v", md5VerFailed)
	}
	return true, nil
}

// 解析 MusicID 返回值1:单曲(1)or列表(2) 返回值2:MusicID
func ParseMusicID(text string) (int, int) {
	if strings.Contains(text, "163cn.tv") || strings.Contains(text, "163cn.link") {
		text = GetRedirectUrl(text)
	}
	var replacer = strings.NewReplacer("\n", "", " ", "")
	messageText := replacer.Replace(text)
	musicUrl := regUrl.FindStringSubmatch(messageText)
	if len(musicUrl) != 0 {
		var linkType int
		if strings.Contains(musicUrl[0], "song") {
			// 单曲
			linkType = 1
		} else if strings.Contains(musicUrl[0], "playlist") {
			// 列表
			linkType = 2
		}
		ur, _ := url.Parse(musicUrl[0])
		id := ur.Query().Get("id")
		if musicid, _ := strconv.Atoi(id); musicid != 0 {
			return linkType, musicid
		}
	}
	// 降级
	musicid, _ := strconv.Atoi(linkTestMusic(messageText))
	return 1, musicid
}

// 解析 ProgramID
func ParseProgramID(text string) int {
	var replacer = strings.NewReplacer("\n", "", " ", "")
	messageText := replacer.Replace(text)
	programid, _ := strconv.Atoi(linkTestProgram(messageText))
	return programid
}

// 提取数字
func extractInt(text string) string {
	matchArr := regInt.FindStringSubmatch(text)
	if len(matchArr) == 0 {
		return ""
	}
	return matchArr[0]
}

// 解析分享链接
func linkTestMusic(text string) string {
	return extractInt(reg5.ReplaceAllString(reg4.ReplaceAllString(reg3.ReplaceAllString(reg2.ReplaceAllString(reg1.ReplaceAllString(text, ""), ""), ""), ""), ""))
}

func linkTestProgram(text string) string {
	return extractInt(reg5.ReplaceAllString(reg4.ReplaceAllString(reg3.ReplaceAllString(regP4.ReplaceAllString(regP3.ReplaceAllString(regP2.ReplaceAllString(regP1.ReplaceAllString(text, ""), ""), ""), ""), ""), ""), ""))
}

// 判断 error 是否为超时错误
func isTimeout(err error) bool {
	if strings.Contains(fmt.Sprintf("%v", err), "context deadline exceeded") {
		return true
	}
	return false
}

// 获取电台节目的 MusicID
func GetProgramRealID(programID int) int {
	programDetail, err := api.GetProgramDetail(data, programID)
	if err != nil {
		return 0
	}
	if programDetail.Program.MainSong.ID != 0 {
		return programDetail.Program.MainSong.ID
	}
	return 0
}

// 获取重定向后的地址
func GetRedirectUrl(text string) string {
	var replacer = strings.NewReplacer("\n", "", " ", "")
	messageText := replacer.Replace(text)
	musicUrl := regUrl.FindStringSubmatch(messageText)
	if len(musicUrl) != 0 {
		if strings.Contains(musicUrl[0], "163cn.tv") {
			var url = musicUrl[0]
			// 创建新的请求
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				return text
			}

			// 设置 CheckRedirect 函数来处理重定向
			client := &http.Client{
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}

			// 执行请求
			resp, err := client.Do(req)
			if err != nil {
				return text
			}
			defer resp.Body.Close()

			// 返回最终重定向的网址
			location := resp.Header.Get("location")
			return location
		}
	}
	return text
}

// 判断歌词是否真正有内容
func hasMeaningfulContent(lyric string) bool {
	// 去掉时间戳 [00:00.000]
	re := regexp.MustCompile(`\[\d{2}:\d{2}\.\d{3}\]`)
	clean := re.ReplaceAllString(lyric, "")
	// 去掉空白字符后看看是否还有内容
	return strings.TrimSpace(clean) != ""
}

func ParseNCMLyric(lyricsData *types.SongLyricData) string {
	if hasMeaningfulContent(lyricsData.Tlyric.Lyric) {
		return lyricsData.Tlyric.Lyric
	}
	return lyricsData.Lrc.Lyric
}

// ParseNCMYear 解析年代
func ParseNCMYear(detailData *types.SongsDetailData) int {
	publishTime := int64(detailData.Songs[0].PublishTime) // 毫秒级时间戳
	t := time.Unix(publishTime/1000, 0)
	return t.Year()
}

// ParseNCMYear 解析年代 2020-10-16
func ParseQQYear(timePublic string) int {
	if len(timePublic) < 4 {
		return 0
	}
	yearStr := timePublic[:4]
	year, err := strconv.Atoi(yearStr)
	if err != nil {
		return 0
	}
	return year
}
