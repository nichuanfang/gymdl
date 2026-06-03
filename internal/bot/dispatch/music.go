package dispatch

import (
    "fmt"
    "strings"

    "github.com/nichuanfang/gymdl/processor/music"
    "github.com/nichuanfang/gymdl/utils"
    tb "gopkg.in/telebot.v4"
)

// HandleMusic
// ---------------------------
// 🎵 音乐处理逻辑
// ---------------------------
func (s *Session) HandleMusic(p music.Processor) error {
    bot := s.Bot
    msg := s.Msg

    _, _ = bot.Edit(msg, fmt.Sprintf("✅ 已识别【<b>%s</b>】链接\n\n🎵 下载中,请稍候...", p.Name()), tb.ModeHTML)

    // 下载阶段
    utils.InfoWithFormat("[Telegram] 下载中...")
    err := p.DownloadMusic(s.Link, func(progress string) {
        _, _ = bot.Edit(msg, fmt.Sprintf("✅ 已识别【<b>%s</b>】链接\n\n🎵 %s", p.Name(), utils.EscapeHTML(progress)), tb.ModeHTML)
    })
    if err != nil {
        utils.ErrorWithFormat("[Telegram] 下载失败: %v", err)
        _, _ = bot.Edit(msg, fmt.Sprintf("❌ 下载失败：\n<pre>%s</pre>", utils.EscapeHTML(utils.TruncateString(err.Error(), 400))), tb.ModeHTML)
        return nil
    }

    // 文件整理 & 处理
    utils.InfoWithFormat("[Telegram] 下载成功，整理中...")
    _, _ = bot.Edit(msg, fmt.Sprintf("✅ 已识别【<b>%s</b>】链接\n\n🎵 %s", p.Name(), "整理中..."), tb.ModeHTML)
    if err = p.BeforeTidy(); err != nil {
        utils.ErrorWithFormat("[Telegram] 文件处理失败: %v", err)
        _, _ = bot.Edit(msg, fmt.Sprintf("⚠️ 文件处理阶段出错：\n<pre>%s</pre>", utils.EscapeHTML(utils.TruncateString(err.Error(), 400))), tb.ModeHTML)
        return nil
    }

    // 入库
    utils.InfoWithFormat("[Telegram] 整理成功，开始入库...")
    _, _ = bot.Edit(msg, fmt.Sprintf("✅ 已识别【<b>%s</b>】链接\n\n🎵 开始入库...", p.Name()), tb.ModeHTML)
    if err = p.TidyMusic(); err != nil {
        utils.ErrorWithFormat("[Telegram] 文件入库失败: %v", err)
        _, _ = bot.Edit(msg, fmt.Sprintf("⚠️ 文件入库失败：\n<pre>%s</pre>", utils.EscapeHTML(utils.TruncateString(err.Error(), 400))), tb.ModeHTML)
        return nil
    }

    // 成功反馈
    s.sendMusicFeedback(p)
    utils.InfoWithFormat("[Telegram] 入库成功!")
    return nil
}

func (s *Session) sendMusicFeedback(p music.Processor) {
    bot := s.Bot
    msg := s.Msg

    songs := p.Songs()
    count := len(songs)

    if count == 0 {
        _, _ = bot.Edit(msg, "⚠️ 没有检测到有效歌曲", tb.ModeHTML)
        return
    }

    // 🎵 单曲反馈
    if count == 1 {
        song := songs[0]
        fileSizeMB := float64(song.MusicSize) / 1024.0 / 1024.0

        successMsg := fmt.Sprintf(
            "🎉 <b>入库成功！</b>\n\n"+
                "🎵 <b>歌曲:</b> %s\n"+
                "🎤 <b>艺术家:</b> %s\n"+
                "💿 <b>专辑:</b> %s\n"+
                "🎧 <b>格式:</b> %s\n"+
                "📊 <b>码率:</b> %s kbps\n"+
                "📦 <b>大小:</b> %.2f MB\n"+
                "☁️ <b>入库方式:</b> %s",
            utils.EscapeHTML(utils.TruncateString(song.SongName, 80)),
            utils.EscapeHTML(utils.TruncateString(song.SongArtists, 80)),
            utils.EscapeHTML(utils.TruncateString(song.SongAlbum, 80)),
            strings.ToUpper(song.FileExt),
            song.Bitrate,
            fileSizeMB,
            strings.ToUpper(song.Tidy),
        )

        _, _ = bot.Edit(msg, successMsg, tb.ModeHTML)
        return
    }

    // 🎶 多曲反馈
    var listBuilder strings.Builder
    for i, s := range songs {
        fileSizeMB := float64(s.MusicSize) / 1024.0 / 1024.0
        listBuilder.WriteString(fmt.Sprintf(
            "🎵 《%s》\n🎤 艺术家：%s\n💿 专辑：%s\n📊 码率：%s kbps | 大小：%.2f MB",
            utils.EscapeHTML(utils.TruncateString(s.SongName, 60)),
            utils.EscapeHTML(utils.TruncateString(s.SongArtists, 40)),
            utils.EscapeHTML(utils.TruncateString(s.SongAlbum, 40)),
            s.Bitrate,
            fileSizeMB,
        ))

        // 如果不是最后一首，添加长横线分隔
        if i < count-1 {
            listBuilder.WriteString("\n──────────────────\n")
        } else {
            listBuilder.WriteString("\n")
        }
    }

    successMsg := fmt.Sprintf(
        "🎉 <b>入库成功！</b>\n\n"+
            "已成功添加 <b>%d</b> 首歌曲至曲库：\n"+
            "──────────────────\n"+
            "%s──────────────────\n"+
            "🎧 <b>格式:</b> %s\n"+
            "☁️ <b>入库方式:</b> %s\n",
        count,
        listBuilder.String(),
        strings.ToUpper(songs[0].FileExt),
        strings.ToUpper(songs[0].Tidy),
    )

    _, _ = bot.Edit(msg, successMsg, tb.ModeHTML)
}