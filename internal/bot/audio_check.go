package bot

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/audioquality"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/storage"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

const audioCheckCallback = "audio_check"
const audioCheckTTL = 24 * time.Hour

var audioCheckEndpoint = tb.InlineButton{Unique: audioCheckCallback}

type audioCheckSession struct {
	token   string
	expires time.Time
	owner   int64
	message *tb.Message
	library string
	report  audioCheckReport
	index   int
	deep    bool
	busy    bool
	skipped map[audioquality.FileVersion]struct{}
}

func (app *BotApp) AudioCheckCommand(c tb.Context) error {
	if !app.discoveryAuthorized(c) {
		return c.Send("请由授权用户在私聊中使用 /audio_check。")
	}
	payload := strings.TrimSpace(c.Message().Payload)
	deep := payload == "deep"
	if payload != "" && !deep {
		return c.Send("发送 /audio_check 开始体检，或发送 /audio_check deep 逐批深度扫描。")
	}
	return app.startAudioCheck(c, deep, nil)
}

func (app *BotApp) startAudioCheck(c tb.Context, deep bool, previous *audioCheckSession) error {
	for _, tool := range []string{"ffprobe", "ffmpeg"} {
		if _, err := exec.LookPath(tool); err != nil {
			return c.Send("曲库体检需要 " + tool + "，当前运行环境未安装。")
		}
	}
	cfg := config.WithLiveTaskOverrides(app.cfg)
	if cfg.Tidy == nil {
		return c.Send("整理目录尚未配置。")
	}
	library, err := audioCheckLibrary(cfg)
	if err != nil {
		return c.Send("无法开始曲库体检：" + err.Error())
	}
	var message *tb.Message
	skipped := make(map[audioquality.FileVersion]struct{})
	if previous != nil {
		message = previous.message
		if previous.library == library {
			skipped = previous.skipped
		}
	}
	app.audioCheckMu.Lock()
	if app.audioCheckDone != nil || app.audioCheckStopping {
		app.audioCheckMu.Unlock()
		return c.Send("已有一次曲库体检正在进行，请查看原卡片。")
	}
	if message == nil && app.audioCheckSessions[c.Chat().ID].busy {
		app.audioCheckMu.Unlock()
		return c.Send("原卡片的操作还在处理，请稍后再试。")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	done := make(chan struct{})
	app.audioCheckDone, app.audioCheckCancel = done, cancel
	app.audioCheckMu.Unlock()

	finish := func() {
		cancel()
		app.audioCheckMu.Lock()
		app.audioCheckDone, app.audioCheckCancel = nil, nil
		close(done)
		app.audioCheckMu.Unlock()
	}
	api, owner := c.Bot(), c.Sender().ID
	loading := "🔍 正在分批体检曲库，完成后会在这里显示结果…"
	if deep {
		loading = "🔍 正在逐个解码这批歌曲，完成后会在这里显示结果…"
	}
	if message == nil {
		message, err = api.Send(c.Chat(), loading)
	} else {
		_, err = api.Edit(message, loading, &tb.ReplyMarkup{})
	}
	if err != nil {
		finish()
		return err
	}
	app.audioCheckMu.Lock()
	delete(app.audioCheckSessions, c.Chat().ID)
	app.audioCheckMu.Unlock()
	go func() {
		defer finish()
		store, err := audioquality.Open(storage.DefaultDatabasePath)
		var report audioCheckReport
		if err == nil {
			defer store.Close()
			report, err = scanAudioCheckBatch(ctx, cfg, store, library, owner, deep)
		}
		if err != nil {
			utils.WarnWithFormat("曲库体检失败: %v", err)
			_, _ = api.Edit(message, "❌ 曲库体检未完成，请稍后重新发送 /audio_check。", &tb.ReplyMarkup{})
			return
		}
		findings := report.findings[:0]
		for _, finding := range report.findings {
			if _, skip := skipped[audioFileVersion(finding.file)]; !skip {
				findings = append(findings, finding)
			}
		}
		report.findings = findings
		session := audioCheckSession{owner: owner, message: message, library: library, report: report, deep: deep, skipped: skipped}
		if err := app.presentAudioCheck(api, session); err != nil {
			utils.WarnWithFormat("更新曲库体检卡片失败: %v", err)
		}
	}()
	return nil
}

func (app *BotApp) stopAudioCheck() {
	app.audioCheckMu.Lock()
	app.audioCheckStopping = true
	done, cancel := app.audioCheckDone, app.audioCheckCancel
	app.audioCheckMu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (app *BotApp) presentAudioCheck(api tb.API, session audioCheckSession) error {
	session.token = strings.ReplaceAll(uuid.NewString(), "-", "")
	session.expires = time.Now().Add(audioCheckTTL)
	session.busy = false
	chat := session.message.Chat.ID
	app.audioCheckMu.Lock()
	if app.audioCheckSessions == nil {
		app.audioCheckSessions = make(map[int64]audioCheckSession)
	}
	for chatID, old := range app.audioCheckSessions {
		if time.Now().After(old.expires) {
			delete(app.audioCheckSessions, chatID)
		}
	}
	app.audioCheckSessions[chat] = session
	app.audioCheckMu.Unlock()
	text, markup := formatAudioCheckCard(session)
	_, err := api.Edit(session.message, text, tb.ModeHTML, markup)
	return err
}

func (app *BotApp) HandleAudioCheckCallback(c tb.Context) error {
	args := c.Args()
	if len(args) != 2 || !app.discoveryAuthorized(c) {
		return c.Respond(&tb.CallbackResponse{Text: "操作无效，请在私聊中使用 /audio_check。", ShowAlert: true})
	}
	action, token := args[0], args[1]
	chat := c.Chat().ID
	app.audioCheckMu.Lock()
	session, ok := app.audioCheckSessions[chat]
	if !ok || session.owner != c.Sender().ID || session.token != token ||
		time.Now().After(session.expires) || !sameMusicActionMessage(session.message, c.Message()) {
		app.audioCheckMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "这张卡片已过期，请重新发送 /audio_check。", ShowAlert: true})
	}
	if session.busy || app.audioCheckDone != nil {
		app.audioCheckMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "上一项操作还在处理。"})
	}
	session.busy = true
	app.audioCheckSessions[chat] = session
	app.audioCheckMu.Unlock()
	defer func() {
		app.audioCheckMu.Lock()
		if current := app.audioCheckSessions[chat]; current.token == token {
			current.busy = false
			app.audioCheckSessions[chat] = current
		}
		app.audioCheckMu.Unlock()
	}()
	switch action {
	case "scan", "deep":
		_ = c.Respond()
		return app.startAudioCheck(c, action == "deep" || session.deep, &session)
	case "play", "sources", "normal", "skip":
		if session.index >= len(session.report.findings) {
			return c.Respond(&tb.CallbackResponse{Text: "当前没有待处理歌曲。"})
		}
	default:
		return c.Respond(&tb.CallbackResponse{Text: "未知体检操作。", ShowAlert: true})
	}

	finding := session.report.findings[session.index]
	switch action {
	case "skip":
		_ = c.Respond()
		session.skipped[audioFileVersion(finding.file)] = struct{}{}
		session.index++
		return app.presentAudioCheck(c.Bot(), session)
	case "normal":
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		store, err := audioquality.Open(storage.DefaultDatabasePath)
		var marked bool
		if err == nil {
			defer store.Close()
			marked, err = store.MarkNormal(ctx, session.library, session.owner, audioFileVersion(finding.file))
		}
		if err != nil {
			return c.Respond(&tb.CallbackResponse{Text: "标记未保存，请稍后再试。", ShowAlert: true})
		}
		if !marked {
			return c.Respond(&tb.CallbackResponse{Text: "这份文件的体检记录已更新，请重新检查。", ShowAlert: true})
		}
		_ = c.Respond(&tb.CallbackResponse{Text: "已处理这份文件"})
		session.index++
		return app.presentAudioCheck(c.Bot(), session)
	}
	_ = c.Respond(&tb.CallbackResponse{Text: "正在核对文件…"})
	cfg := config.WithLiveTaskOverrides(app.cfg)
	library, err := audioCheckLibrary(cfg)
	if err != nil || library != session.library {
		return c.Send("曲库位置已变化，请重新发送 /audio_check。")
	}
	currentPath, err := web.CurrentLibraryAudioPath(cfg, finding.file)
	if err != nil {
		return c.Send("文件已变化或暂不可读，请重新体检后再操作。")
	}
	actionSong := finding.musicAction(currentPath, cfg.Tidy.Mode)
	if action == "play" {
		if err := app.sendMusicActionAudio(c, actionSong); err != nil {
			return c.Send("播放失败，文件可能已损坏，或 Telegram 无法发送这个格式。")
		}
		return nil
	}
	message, err := c.Bot().Send(c.Chat(), "🔎 正在查找其他音源…")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return app.presentMusicSourceChoices(ctx, c, message, actionSong, true)
}

func (f audioCheckFinding) musicAction(filePath string, tidyMode int) pendingMusicAction {
	storage := "LOCAL"
	if tidyMode == 2 {
		storage = "WEBDAV"
	}
	name := strings.TrimSuffix(f.file.Name, filepath.Ext(f.file.Name))
	title, artist, album := f.Title, strings.TrimSpace(f.Artist), strings.TrimSpace(f.Album)
	if artist == "" {
		artist = strings.TrimSpace(f.file.Artist)
	}
	if album == "" {
		album = strings.TrimSpace(f.file.Album)
	}
	if title == "" {
		title = name
		if prefix, suffix, found := strings.Cut(name, " - "); found && strings.TrimSpace(prefix) != "" &&
			strings.TrimSpace(suffix) != "" && (artist == "" || sameStoredSongTag(artist, prefix)) {
			title = strings.TrimSpace(suffix)
			if artist == "" {
				artist = strings.TrimSpace(prefix)
			}
		}
	}
	return pendingMusicAction{
		path: filePath, storage: storage, title: title, artist: artist,
		album: album, sourceURL: f.Source,
	}
}

func formatAudioCheckCard(session audioCheckSession) (string, *tb.ReplyMarkup) {
	report := session.report
	markup := &tb.ReplyMarkup{}
	button := func(text, action string) tb.Btn { return markup.Data(text, audioCheckCallback, action, session.token) }
	rows := []tb.Row{}
	var text strings.Builder
	text.WriteString("🎧 <b>曲库体检</b>\n")
	fmt.Fprintf(&text, "已检查 %d/%d 首", report.checked, report.total)
	if report.unreadable > 0 {
		fmt.Fprintf(&text, " · %d 首未能检查", report.unreadable)
	}
	if report.remaining > 0 {
		fmt.Fprintf(&text, "\n还有 %d 首可分批检查。", report.remaining)
	}
	if report.referenceQueue > 0 {
		fmt.Fprintf(&text, "\n%d 首可继续核对同版本时长。", report.referenceQueue)
	}
	if report.limited {
		text.WriteString("\n曲库目录超过扫描上限，本次只覆盖已列出的文件。")
	}
	if session.index < len(report.findings) {
		file := report.findings[session.index]
		song := file.musicAction("", 1)
		fmt.Fprintf(&text, "\n\n%d/%d  <b>%s</b>", session.index+1, len(report.findings), musicSourceLabel(song.title))
		if song.artist != "" {
			fmt.Fprintf(&text, "\n%s", musicSourceLabel(song.artist))
		}
		if file.Damaged {
			text.WriteString("\n⚠️ 文件损坏或音轨不完整")
			if file.Duration > 0 && file.Decoded > 0 && file.Decoded < file.Duration {
				fmt.Fprintf(&text, "（解码 %s／标注 %s）", audioCheckDuration(file.Decoded), audioCheckDuration(file.Duration))
			}
		}
		if file.ShorterThanSource() {
			actual := file.Decoded
			if actual == 0 {
				actual = file.Duration
			}
			fmt.Fprintf(&text, "\n⚠️ 疑似不完整 · 文件 %s／同版本 %s", audioCheckDuration(actual), audioCheckDuration(file.Reference))
		}
		if audioquality.LowBitrate(file.Codec, file.Bitrate) {
			fmt.Fprintf(&text, "\n⚠️ 低码率 · %s %d kbps", utils.EscapeHTML(strings.ToUpper(file.Codec)), file.Bitrate)
		}
		rows = append(rows, markup.Row(button("播放", "play"), button("其他音源", "sources")),
			markup.Row(button("跳过", "skip"), button("标记正常", "normal")))
	} else if len(report.findings) > 0 {
		text.WriteString("\n\n已浏览本批发现的问题。")
	} else {
		text.WriteString("\n\n已检查的文件中暂无待处理问题。")
	}
	if session.index >= len(report.findings) && report.withoutReference > 0 {
		fmt.Fprintf(&text, "\n%d 首缺少可核实的同版本时长。", report.withoutReference)
	}
	if report.remaining > 0 || report.referenceQueue > 0 {
		rows = append(rows, markup.Row(button("继续扫描", "scan")))
	}
	if report.deferred > 0 {
		fmt.Fprintf(&text, "\n%d 首已初筛，可再做完整解码。", report.deferred)
		rows = append(rows, markup.Row(button("深度扫描", "deep")))
	}
	markup.Inline(rows...)
	return text.String(), markup
}

func audioCheckDuration(seconds float64) string {
	whole := int(seconds + 0.5)
	return fmt.Sprintf("%d:%02d", whole/60, whole%60)
}
