package bot

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core/linkparser"
	"github.com/nichuanfang/gymdl/internal/audiodupe"
	"github.com/nichuanfang/gymdl/internal/bot/dispatch"
	"github.com/nichuanfang/gymdl/internal/musicdedupe"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

const duplicateCallbackUnique = "music_dedupe"
const duplicateConfirmationTTL = 10 * time.Minute

var duplicateCallbackEndpoint = tb.InlineButton{Unique: duplicateCallbackUnique}

type pendingMusicDownload struct {
	userID         int64
	chatID         int64
	origin         *tb.Message
	link           string
	cfg            *config.Config
	track          musicdedupe.Track
	reservationKey string
	candidateToken string
	expiresAt      time.Time
}

func (app *BotApp) handleMusicWithDuplicateCheck(session *dispatch.Session, p music.Processor) error {
	if app.musicDedupe == nil {
		if _, err := session.Bot.Edit(session.Msg, "❌ 本地查重暂时不可用，本次下载未启动，请稍后重试。", tb.ModeHTML); err != nil {
			return err
		}
		return errMusicDedupeUnavailable
	}
	track := musicdedupe.IdentityFromLink(p.Name(), session.Link)
	if musicdedupe.IsSingleTrackLink(p.Name(), session.Link) {
		metadata, err := resolveMusicMetadata(session.Cfg, p.Name(), session.Link)
		if err == nil && metadata != nil {
			track.MetadataResolved = true
			track.Title = metadata.SongName
			track.Artist = metadata.SongArtists
			track.Album = metadata.SongAlbum
		} else if err != nil {
			utils.WarnWithFormat("[TelegramDuplicate] 元数据预检失败，继续用平台 ID/链接查重: %v", err)
		}
	}

	reservationKey := musicdedupe.ReservationKey(track)
	if reservationKey != "" && !app.reserveMusicKey(reservationKey) {
		if _, err := session.Bot.Edit(session.Msg, "⏳ 这首歌已经有一个下载或确认任务正在处理，请稍后再试。", tb.ModeHTML); err != nil {
			return err
		}
		return errMusicDownloadAlreadyProcessing
	}
	reserved := reservationKey != ""
	defer func() {
		if reserved {
			app.releaseMusicKey(reservationKey)
		}
	}()

	match, err := app.musicDedupe.FindDuplicate(context.Background(), track)
	if err != nil {
		if _, editErr := session.Bot.Edit(session.Msg, "❌ 本地查重数据库暂时不可用，本次下载未启动，请稍后重试。", tb.ModeHTML); editErr != nil {
			return editErr
		}
		utils.ErrorWithFormat("[TelegramDuplicate] SQLite 查询失败: %v", err)
		return errMusicDedupeUnavailable
	}
	if match != nil {
		if err := app.askDuplicateConfirmation(session, track, *match, reservationKey); err != nil {
			app.releaseMusicKey(reservationKey)
			reserved = false
			return err
		}
		reserved = false
		return errDuplicateConfirmationPending
	}

	session.AskSimilar = app.askSimilarAudio(session, track, reservationKey, &reserved)
	session.BeforeDownload = func() error {
		match, err := app.musicDedupe.FindDuplicate(context.Background(), track)
		if err != nil {
			return errMusicDedupeUnavailable
		}
		if match != nil {
			if err = app.askDuplicateConfirmation(session, track, *match, reservationKey); err != nil {
				return err
			}
			reserved = false
			return errDuplicateConfirmationPending
		}
		return nil
	}
	app.attachDownloadRecorder(session, p.Name(), track)
	return session.HandleMusic(p)
}

func resolveMusicMetadata(cfg *config.Config, kind processor.LinkType, rawURL string) (*music.SongInfo, error) {
	switch kind {
	case processor.LinkNetEase:
		return music.ResolveNeteaseSingleMetadata(cfg, rawURL)
	case processor.LinkQQMusic:
		return music.ResolveQQSingleMetadata(cfg, rawURL)
	case processor.LinkYoutubeMusic, processor.LinkBiliBiliMusic, processor.LinkSoundcloud:
		return music.ResolveYTDLPMusicMetadata(cfg, rawURL)
	default:
		return nil, nil
	}
}

func (app *BotApp) attachDownloadRecorder(session *dispatch.Session, kind processor.LinkType, identity musicdedupe.Track) {
	previousCallback := session.OnMusicDownloaded
	session.OnMusicDownloaded = func(songs []*music.SongInfo) {
		for _, song := range songs {
			if song == nil {
				continue
			}
			record := successfulDownloadRecord(kind, session.Link, identity, song, len(songs))
			if app.musicDedupe != nil {
				if err := app.musicDedupe.RecordSuccessfulDownload(context.Background(), record); err != nil {
					utils.ErrorWithFormat("[TelegramDuplicate] 下载成功但保存去重记录失败: %v", err)
				}
			} else {
				utils.ErrorWithFormat("[TelegramDuplicate] 下载成功但查重数据库不可用")
			}
		}
		if len(songs) > 0 && session.CandidateToken != "" && app.aiStore != nil && session.User != nil {
			userID, chatID := session.User.ID, int64(0)
			if session.Context != nil && session.Context.Chat() != nil {
				chatID = session.Context.Chat().ID
			}
			if err := app.aiStore.MarkCandidateOwned(userID, chatID, session.CandidateToken); err != nil {
				utils.WarnWithFormat("[AI] 下载已入库，但更新候选状态失败: %v", err)
			}
		}
		if previousCallback != nil {
			previousCallback(songs)
		}
	}
}

func successfulDownloadRecord(kind processor.LinkType, link string, identity musicdedupe.Track, song *music.SongInfo, songCount int) musicdedupe.Track {
	record := musicdedupe.Track{
		Platform: identity.Platform,
		Title:    song.SongName,
		Artist:   song.SongArtists,
		Album:    song.SongAlbum,
	}
	if songCount == 1 && musicdedupe.IsSingleTrackLink(kind, link) {
		record.PlatformTrackID = identity.PlatformTrackID
		record.SourceURL = identity.SourceURL
		if identity.MetadataResolved {
			record.Title = identity.Title
			record.Artist = identity.Artist
			record.Album = identity.Album
		}
	}
	return record
}

func (app *BotApp) askDuplicateConfirmation(session *dispatch.Session, incoming musicdedupe.Track, match musicdedupe.Match, reservationKey string) error {
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	item := pendingMusicDownload{
		userID:         session.User.ID,
		origin:         session.Msg,
		link:           session.Link,
		cfg:            session.Cfg,
		track:          incoming,
		reservationKey: reservationKey,
		candidateToken: session.CandidateToken,
		expiresAt:      time.Now().Add(duplicateConfirmationTTL),
	}
	if session.Context != nil && session.Context.Chat() != nil {
		item.chatID = session.Context.Chat().ID
	}
	app.pendingMu.Lock()
	app.purgeExpiredPendingLocked(time.Now())
	app.pendingMusic[token] = item
	app.pendingMu.Unlock()

	message := duplicateConfirmText(
		firstNonEmpty(match.Title, incoming.Title),
		firstNonEmpty(match.Artist, incoming.Artist),
		firstNonEmpty(match.Album, incoming.Album),
		matchReason(match.MatchedBy),
	)
	if _, err := session.Bot.Edit(session.Msg, utils.EscapeHTML(message), duplicateChoiceMarkup(token), tb.ModeHTML); err != nil {
		app.pendingMu.Lock()
		delete(app.pendingMusic, token)
		app.pendingMu.Unlock()
		return err
	}
	return nil
}

func (app *BotApp) HandleDuplicateMusicCallback(c tb.Context) error {
	args := c.Args()
	if len(args) < 2 {
		return c.Respond(&tb.CallbackResponse{Text: "确认信息无效或已过期", ShowAlert: true})
	}
	action, token := args[0], args[1]
	if action != "continue" && action != "cancel" {
		return c.Respond(&tb.CallbackResponse{Text: "未知操作", ShowAlert: true})
	}

	app.pendingMu.Lock()
	app.purgeExpiredPendingLocked(time.Now())
	pending, ok := app.pendingMusic[token]
	if !ok {
		app.pendingMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "确认已过期，请重新发送歌曲链接", ShowAlert: true})
	}
	if c.Sender() == nil || c.Sender().ID != pending.userID {
		app.pendingMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "只有发起下载的用户可以确认", ShowAlert: true})
	}
	var callbackMessage *tb.Message
	if callback := c.Callback(); callback != nil {
		callbackMessage = callback.Message
	}
	if !sameMusicActionMessage(pending.origin, callbackMessage) || (pending.chatID != 0 && (c.Chat() == nil || c.Chat().ID != pending.chatID)) {
		app.pendingMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "请使用原下载提示中的按钮", ShowAlert: true})
	}
	delete(app.pendingMusic, token)
	app.pendingMu.Unlock()

	if action == "cancel" {
		app.releaseMusicKey(pending.reservationKey)
		_ = c.Respond()
		return c.Edit("已取消下载。", &tb.ReplyMarkup{})
	}

	_ = c.Respond()
	if err := c.Edit("✅ 已确认，开始下载…", &tb.ReplyMarkup{}); err != nil {
		app.pendingMu.Lock()
		if !time.Now().After(pending.expiresAt) {
			app.pendingMusic[token] = pending
		}
		app.pendingMu.Unlock()
		return err
	}
	defer app.releaseMusicKey(pending.reservationKey)

	link, executor := linkparser.ParseLinkWithConfig(pending.cfg, pending.link)
	p, ok := executor.(music.Processor)
	if link == "" || !ok {
		return c.Edit("❌ 无法恢复下载任务，请重新发送歌曲链接。", &tb.ReplyMarkup{})
	}
	p.Init(pending.cfg)
	session := &dispatch.Session{
		Text:                    pending.link,
		Context:                 c,
		User:                    c.Sender(),
		Bot:                     c.Bot(),
		Msg:                     c.Message(),
		Link:                    link,
		Start:                   time.Now(),
		Cfg:                     pending.cfg,
		CandidateToken:          pending.candidateToken,
		AcceptSimilarAudio:      true,
		CreateMusicActionMarkup: app.musicActionMarkup,
		SetMusicActionMessages:  app.setMusicActionMessages,
	}
	app.attachDownloadRecorder(session, p.Name(), pending.track)
	return session.HandleMusic(p)
}

func (app *BotApp) reserveMusicKey(key string) bool {
	if key == "" {
		return true
	}
	app.pendingMu.Lock()
	defer app.pendingMu.Unlock()
	app.purgeExpiredPendingLocked(time.Now())
	if _, exists := app.activeMusicKeys[key]; exists {
		return false
	}
	app.activeMusicKeys[key] = struct{}{}
	return true
}

func (app *BotApp) releaseMusicKey(key string) {
	if key == "" {
		return
	}
	app.pendingMu.Lock()
	delete(app.activeMusicKeys, key)
	app.pendingMu.Unlock()
}

func (app *BotApp) purgeExpiredPendingLocked(now time.Time) {
	for token, pending := range app.pendingMusic {
		if now.After(pending.expiresAt) {
			delete(app.pendingMusic, token)
			delete(app.activeMusicKeys, pending.reservationKey)
		}
	}
}

func (app *BotApp) askSimilarAudio(session *dispatch.Session, track musicdedupe.Track, reservationKey string, reserved *bool) func(error) error {
	return func(err error) error {
		var match *audiodupe.LibraryMatch
		if !errors.As(err, &match) {
			return err
		}
		if askErr := app.askAudioConfirmation(session, track, match, reservationKey); askErr != nil {
			return askErr
		}
		if reserved != nil {
			*reserved = false
		}
		return errDuplicateConfirmationPending
	}
}

func (app *BotApp) askAudioConfirmation(session *dispatch.Session, incoming musicdedupe.Track, match *audiodupe.LibraryMatch, reservationKey string) error {
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	item := pendingMusicDownload{
		userID:         session.User.ID,
		origin:         session.Msg,
		link:           session.Link,
		cfg:            session.Cfg,
		track:          incoming,
		reservationKey: reservationKey,
		candidateToken: session.CandidateToken,
		expiresAt:      time.Now().Add(duplicateConfirmationTTL),
	}
	if session.Context != nil && session.Context.Chat() != nil {
		item.chatID = session.Context.Chat().ID
	}
	app.pendingMu.Lock()
	app.purgeExpiredPendingLocked(time.Now())
	app.pendingMusic[token] = item
	app.pendingMu.Unlock()

	message := match.Error() + "\n\n要再存一份吗？"
	if _, err := session.Bot.Edit(session.Msg, utils.EscapeHTML(message), duplicateChoiceMarkup(token), tb.ModeHTML); err != nil {
		app.pendingMu.Lock()
		delete(app.pendingMusic, token)
		app.pendingMu.Unlock()
		return err
	}
	return nil
}

func duplicateChoiceMarkup(token string) *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	markup.Inline(markup.Row(
		markup.Data("再存一份", duplicateCallbackUnique, "continue", token),
		markup.Data("取消", duplicateCallbackUnique, "cancel", token),
	))
	return markup
}

func duplicateConfirmText(title, artist, album, reason string) string {
	var b strings.Builder
	b.WriteString("可能是同一首歌\n\n")
	wrote := false
	for _, line := range []string{cleanCardLine(title), cleanCardLine(artist), cleanCardLine(album)} {
		if line == "" {
			continue
		}
		if wrote {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		wrote = true
	}
	if !wrote {
		b.WriteString("一首已入库的歌")
	}
	if reason = cleanCardLine(reason); reason != "" {
		b.WriteString("\n\n")
		b.WriteString(reason)
	}
	b.WriteString("\n\n要再存一份吗？")
	return b.String()
}

func cleanCardLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "未知" {
		return ""
	}
	return value
}

func matchReason(reason string) string {
	switch reason {
	case "platform_id":
		return "同一条平台曲目"
	case "url":
		return "同一个链接"
	default:
		return "歌名、歌手和专辑一致"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "未知"
}
