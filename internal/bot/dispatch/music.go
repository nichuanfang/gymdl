package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/nichuanfang/gymdl/internal/audiodupe"
	"github.com/nichuanfang/gymdl/internal/musicdedupe"
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

	_, _ = bot.Edit(msg, fmt.Sprintf("✅ 已识别【<b>%s</b>】链接\n\n🎵 下载中，请稍候…", p.Name()), tb.ModeHTML)

	preflightStopped := false
	_, err := music.RunWorkflowWithHooks(context.Background(), p, s.Link, func(progress string) {
		_, _ = bot.Edit(msg, fmt.Sprintf("🎵 %s", utils.EscapeHTML(progress)), tb.ModeHTML)
	}, music.WorkflowHooks{Before: func() error {
		if s.BeforeDownload != nil {
			err := s.BeforeDownload()
			preflightStopped = err != nil
			return err
		}
		return nil
	}, BeforeStore: func() error {
		for _, song := range nonNilMusicSongs(p.Songs()) {
			// Provider-reported sizes can predate tag and cover writes.
			if file, err := os.Stat(song.MusicPath); err == nil && file.Mode().IsRegular() {
				song.MusicSize = file.Size()
			}
		}
		err := reviewSessionAudio(s, p)
		if err == nil {
			return nil
		}
		var match *audiodupe.LibraryMatch
		if errors.As(err, &match) && match.Decision == audiodupe.DecisionConfirm && s.AskSimilar != nil {
			preflightStopped = true
			return s.AskSimilar(err)
		}
		return err
	}, After: func(songs []*music.SongInfo) error {
		s.setStoredMusicPaths(songs)
		if s.OnMusicDownloaded != nil {
			s.OnMusicDownloaded(songs)
		}
		return nil
	}})
	if err != nil {
		if preflightStopped {
			return err
		}
		_, _ = bot.Edit(msg, fmt.Sprintf("❌ %s", utils.EscapeHTML(utils.TruncateString(err.Error(), 400))), tb.ModeHTML)
		return err
	}

	// 成功反馈
	s.sendMusicFeedback(p)
	utils.InfoWithFormat("[Telegram] 入库成功!")
	return nil
}

func (s *Session) sendMusicFeedback(p music.Processor) {
	bot := s.Bot
	msg := s.Msg

	songs := nonNilMusicSongs(p.Songs())
	count := len(songs)

	if count == 0 {
		_, _ = bot.Edit(msg, "⚠️ 没有检测到有效歌曲", tb.ModeHTML)
		return
	}

	// 🎵 单曲反馈
	if count == 1 {
		song := songs[0]
		fileSizeMB := float64(song.MusicSize) / 1024.0 / 1024.0
		storageLabel := tidyStorageLabel(song.Tidy)

		successMsg := fmt.Sprintf(
			"🎉 <b>入库成功！</b>\n\n"+
				"🎵 <b>歌曲:</b> %s\n"+
				"🎤 <b>艺术家:</b> %s\n"+
				"💿 <b>专辑:</b> %s\n"+
				"🎧 <b>格式:</b> %s\n"+
				"📊 <b>码率:</b> %s kbps\n"+
				"📦 <b>大小:</b> %.2f MB\n"+
				"☁️ <b>入库状态:</b> %s",
			utils.EscapeHTML(utils.TruncateString(song.SongName, 80)),
			utils.EscapeHTML(utils.TruncateString(song.SongArtists, 80)),
			utils.EscapeHTML(utils.TruncateString(song.SongAlbum, 80)),
			utils.EscapeHTML(strings.ToUpper(song.FileExt)),
			utils.EscapeHTML(song.Bitrate),
			fileSizeMB,
			utils.EscapeHTML(storageLabel),
		)

		var sourceURL string
		if musicdedupe.IsSingleTrackLink(p.Name(), s.Link) {
			sourceURL = s.Link
		}
		s.editMusicSuccess(successMsg, songs, sourceURL)
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
			utils.EscapeHTML(s.Bitrate),
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
			"☁️ <b>入库状态:</b> %s\n",
		count,
		listBuilder.String(),
		utils.EscapeHTML(strings.ToUpper(songs[0].FileExt)),
		utils.EscapeHTML(tidyStorageLabel(songs[0].Tidy)),
	)

	s.editMusicSuccess(successMsg, songs, "")
}

func reviewSessionAudio(s *Session, p music.Processor) error {
	songs := nonNilMusicSongs(p.Songs())
	if len(songs) != 1 || strings.TrimSpace(songs[0].MusicPath) == "" {
		return nil
	}
	return audiodupe.ReviewFile(context.Background(), songs[0].MusicPath, float64(songs[0].Duration), s.AcceptSimilarAudio)
}

func nonNilMusicSongs(songs []*music.SongInfo) []*music.SongInfo {
	valid := make([]*music.SongInfo, 0, len(songs))
	for _, song := range songs {
		if song != nil {
			valid = append(valid, song)
		}
	}
	return valid
}

func tidyStorageLabel(tidy string) string {
	if strings.EqualFold(strings.TrimSpace(tidy), "WEBDAV_PENDING") {
		return "待上传（NAS 恢复后自动补传）"
	}
	return strings.ToUpper(tidy)
}

func (s *Session) editMusicSuccess(text string, songs []*music.SongInfo, sourceURL string) {
	options := []interface{}{tb.ModeHTML}
	var actionTokens []string
	var actionMarkup *tb.ReplyMarkup
	if s.CreateMusicActionMarkup != nil {
		for _, song := range songs {
			token, markup := s.CreateMusicActionMarkup(song, sourceURL)
			if token == "" || markup == nil {
				continue
			}
			actionTokens = append(actionTokens, token)
			if actionMarkup == nil {
				actionMarkup = &tb.ReplyMarkup{}
			}
			actionMarkup.InlineKeyboard = append(actionMarkup.InlineKeyboard, markup.InlineKeyboard...)
		}
	}
	if actionMarkup != nil {
		options = append(options, actionMarkup)
	}

	updated, err := s.Bot.Edit(s.Msg, text, options...)
	if err != nil {
		utils.WarnWithFormat("[Telegram] 更新入库成功消息失败: %v", err)
		return
	}
	if len(actionTokens) > 0 && s.SetMusicActionMessages != nil {
		if updated == nil {
			updated = s.Msg
		}
		var userID, chatID int64
		if s.User != nil {
			userID = s.User.ID
		}
		if s.Context != nil && s.Context.Chat() != nil {
			chatID = s.Context.Chat().ID
		}
		s.SetMusicActionMessages(actionTokens, updated, actionMarkup, userID, chatID)
	}
}

func (s *Session) setStoredMusicPaths(songs []*music.SongInfo) {
	if s.Cfg == nil || s.Cfg.Tidy == nil {
		return
	}
	for _, song := range songs {
		if song == nil || song.MusicPath == "" {
			continue
		}
		if strings.EqualFold(song.Tidy, "WEBDAV_PENDING") {
			song.MusicPath = ""
			continue
		}
		if stored := music.StoredFilePath(song, s.Cfg.Tidy); stored != "" {
			song.MusicPath = stored
		}
	}
}
