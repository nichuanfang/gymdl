package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core/linkparser"
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
	link           string
	cfg            *config.Config
	track          musicdedupe.Track
	reservationKey string
	expiresAt      time.Time
}

func (app *BotApp) handleMusicWithDuplicateCheck(session *dispatch.Session, p music.Processor) error {
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
		_, _ = session.Bot.Edit(session.Msg, "⏳ 这首歌已经有一个下载或确认任务正在处理，请稍后再试。", tb.ModeHTML)
		return nil
	}
	reserved := reservationKey != ""
	defer func() {
		if reserved {
			app.releaseMusicKey(reservationKey)
		}
	}()

	match, err := app.musicDedupe.FindDuplicate(context.Background(), track)
	if err != nil {
		_, _ = session.Bot.Edit(session.Msg, "❌ 本地查重数据库暂时不可用，本次下载未启动，请稍后重试。", tb.ModeHTML)
		utils.ErrorWithFormat("[TelegramDuplicate] SQLite 查询失败: %v", err)
		return nil
	}
	if match != nil {
		if err := app.askDuplicateConfirmation(session, track, *match, reservationKey); err != nil {
			app.releaseMusicKey(reservationKey)
			reserved = false
			return err
		}
		reserved = false
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
	session.OnMusicDownloaded = func(songs []*music.SongInfo) {
		for _, song := range songs {
			if song == nil {
				continue
			}
			record := successfulDownloadRecord(kind, session.Link, identity, song, len(songs))
			if err := app.musicDedupe.RecordSuccessfulDownload(context.Background(), record); err != nil {
				utils.ErrorWithFormat("[TelegramDuplicate] 下载成功但保存去重记录失败: %v", err)
			}
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
		link:           session.Link,
		cfg:            session.Cfg,
		track:          incoming,
		reservationKey: reservationKey,
		expiresAt:      time.Now().Add(duplicateConfirmationTTL),
	}
	app.pendingMu.Lock()
	app.purgeExpiredPendingLocked(time.Now())
	app.pendingMusic[token] = item
	app.pendingMu.Unlock()

	markup := &tb.ReplyMarkup{}
	continueButton := markup.Data("继续下载", duplicateCallbackUnique, "continue", token)
	cancelButton := markup.Data("取消", duplicateCallbackUnique, "cancel", token)
	markup.Inline(markup.Row(continueButton, cancelButton))
	message := fmt.Sprintf(
		"⚠️ 检测到可能重复的歌曲\n\n歌名：《%s》\n歌手：%s\n专辑：%s\n\n匹配依据：%s\n仍要继续下载吗？",
		firstNonEmpty(match.Title, incoming.Title),
		firstNonEmpty(match.Artist, incoming.Artist),
		firstNonEmpty(match.Album, incoming.Album),
		matchReason(match.MatchedBy),
	)
	if _, err := session.Bot.Edit(session.Msg, message, markup); err != nil {
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
	delete(app.pendingMusic, token)
	app.pendingMu.Unlock()

	if action == "cancel" {
		app.releaseMusicKey(pending.reservationKey)
		_ = c.Respond()
		return c.Edit("已取消下载。", &tb.ReplyMarkup{})
	}

	_ = c.Respond()
	if err := c.Edit("✅ 已确认，开始下载…", &tb.ReplyMarkup{}); err != nil {
		app.releaseMusicKey(pending.reservationKey)
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
		Text:    pending.link,
		Context: c,
		User:    c.Sender(),
		Bot:     c.Bot(),
		Msg:     c.Message(),
		Link:    link,
		Start:   time.Now(),
		Cfg:     pending.cfg,
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

func matchReason(reason string) string {
	switch reason {
	case "platform_id":
		return "平台曲目 ID 相同"
	case "url":
		return "来源 URL 相同"
	default:
		return "歌手、歌曲名和专辑相同"
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
