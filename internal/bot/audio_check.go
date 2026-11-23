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
	full    bool
	running bool
	busy    bool
	notice  string
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
	return app.startAudioCheck(c, deep, false, nil)
}

func missingAudioCheckTool() string {
	for _, tool := range []string{"ffprobe", "ffmpeg"} {
		if _, err := exec.LookPath(tool); err != nil {
			return tool
		}
	}
	return ""
}

func (app *BotApp) startAudioCheck(c tb.Context, deep, full bool, previous *audioCheckSession) error {
	if tool := missingAudioCheckTool(); tool != "" {
		return c.Send("曲库体检需要 " + tool + "，当前运行环境未安装。")
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
			for version := range previous.skipped {
				skipped[version] = struct{}{}
			}
		}
	}
	if full {
		deep = true
	}
	session := audioCheckSession{
		token:   strings.ReplaceAll(uuid.NewString(), "-", ""),
		expires: time.Now().Add(audioCheckTTL), owner: c.Sender().ID, message: message, library: library, deep: deep,
		full: full, running: true, skipped: skipped,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	app.audioCheckMu.Lock()
	if app.audioCheckDone != nil || app.audioCheckStopping {
		app.audioCheckMu.Unlock()
		cancel()
		return c.Send("已有一次曲库体检正在进行，请查看原卡片。")
	}
	current := app.audioCheckSessions[c.Chat().ID]
	if current.busy || current.running || (previous != nil && current.token != previous.token) {
		app.audioCheckMu.Unlock()
		cancel()
		return c.Send("原卡片的操作还在处理，请稍后再试。")
	}
	app.audioCheckDone, app.audioCheckCancel = done, cancel
	app.storeAudioCheckSessionLocked(sessionWithMessage(session, c.Chat()))
	app.audioCheckMu.Unlock()

	text, markup := formatAudioCheckProgressCard(session)
	api := c.Bot()
	if message == nil {
		message, err = api.Send(c.Chat(), text, tb.ModeHTML, markup)
	} else {
		_, err = api.Edit(message, text, tb.ModeHTML, markup)
	}
	if err != nil {
		app.resetAudioCheckStart(c.Chat().ID, current)
		app.finishAudioCheck(done, cancel)
		return err
	}
	session.message = message
	if err := persistAudioCheckSession(session); err != nil {
		app.resetAudioCheckStart(c.Chat().ID, current)
		app.finishAudioCheck(done, cancel)
		utils.WarnWithFormat("无法保存体检进度: %v", err)
		if previous != nil {
			text, markup := formatAudioCheckCard(*previous)
			_, _ = api.Edit(message, text, tb.ModeHTML, markup)
		}
		return c.Send("暂时无法保存体检进度，请稍后再试。")
	}
	app.saveRunningAudioCheckSession(c.Chat().ID, session)
	go func() {
		defer app.finishAudioCheck(done, cancel)
		app.runAudioCheck(ctx, api, cfg, session)
	}()
	return nil
}

func sessionWithMessage(session audioCheckSession, chat *tb.Chat) audioCheckSession {
	if session.message == nil {
		session.message = &tb.Message{Chat: chat}
	}
	return session
}

func (app *BotApp) resetAudioCheckStart(chat int64, previous audioCheckSession) {
	app.audioCheckMu.Lock()
	defer app.audioCheckMu.Unlock()
	if previous.message == nil {
		delete(app.audioCheckSessions, chat)
		return
	}
	app.storeAudioCheckSessionLocked(previous)
}

func (app *BotApp) finishAudioCheck(done chan struct{}, cancel context.CancelFunc) {
	cancel()
	app.audioCheckMu.Lock()
	defer app.audioCheckMu.Unlock()
	if app.audioCheckDone == done {
		app.audioCheckDone, app.audioCheckCancel = nil, nil
	}
	close(done)
}

func (app *BotApp) runAudioCheck(ctx context.Context, api tb.API, cfg *config.Config, session audioCheckSession) {
	store, err := audioquality.Open(storage.DefaultDatabasePath)
	if err != nil {
		app.failAudioCheck(api, session, "❌ 曲库体检未完成，已保存的检查记录会保留；稍后可重新运行。")
		return
	}
	defer store.Close()

	loadState := func(stateCtx context.Context) (*audioCheckScanState, error) {
		return newAudioCheckScanState(stateCtx, cfg, store, session.library, session.owner, session.deep)
	}
	if session.full {
		loadState = func(stateCtx context.Context) (*audioCheckScanState, error) {
			return newFullAudioCheckScanState(stateCtx, cfg, store, session.library, session.owner)
		}
	}
	state, err := loadState(ctx)
	if err != nil {
		if ctx.Err() != nil {
			if app.audioCheckIsStopping() {
				return
			}
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			report, cacheErr := cachedAudioCheckReport(cacheCtx, store, session.library, session.owner)
			cancel()
			session.report = filterAudioCheckSkipped(report, session.skipped)
			session.running = false
			session.notice = "目录清单读取已停止；卡片显示此前完成的缓存记录。再次扫描会校验文件版本并继续。"
			if cacheErr != nil {
				session.report = audioCheckReport{resumeAvailable: true}
				session.notice = "目录清单读取已停止，暂时无法读取已完成记录；再次扫描会校验文件版本并继续。"
			}
			if presentErr := app.presentAudioCheck(api, session); presentErr != nil {
				utils.WarnWithFormat("更新曲库体检卡片失败: %v", presentErr)
			}
			return
		}
		if err != nil {
			session.running = false
			session.notice = "❌ 未能读取完整曲库清单；已完成记录保留，可稍后重试。"
			session.report = audioCheckReport{}
			if presentErr := app.presentAudioCheck(api, session); presentErr != nil {
				utils.WarnWithFormat("更新曲库体检卡片失败: %v", presentErr)
			}
			return
		}
	}

	report := state.cachedReport()
	if session.notice != "" && ctx.Err() == nil {
		session.report = filterAudioCheckSkipped(report, session.skipped)
		if err := app.editAudioCheckProgress(api, session); err != nil && !strings.Contains(err.Error(), "message is not modified") {
			utils.WarnWithFormat("更新已恢复的体检进度失败: %v", err)
		}
	}
	var notice string
	if session.full {
		previous := report
		for {
			if ctx.Err() != nil {
				break
			}
			batchCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
			_, scanErr := state.scanBatch(batchCtx, cfg, store, session.library, session.deep)
			timedOut := batchCtx.Err() != nil
			cancel()
			if ctx.Err() != nil {
				break
			}
			if scanErr != nil && !timedOut {
				notice = "❌ 本批体检遇到错误；已完成记录保留，可继续全库体检。"
				report = state.cachedReport()
				break
			}
			report = state.cachedReport()
			session.report = filterAudioCheckSkipped(report, session.skipped)
			app.saveRunningAudioCheckSession(session.message.Chat.ID, session)
			if err := app.editAudioCheckProgress(api, session); err != nil {
				utils.WarnWithFormat("更新全库体检进度失败: %v", err)
			}
			if report.remaining == 0 && report.deferred == 0 && report.referenceQueue == 0 {
				break
			}
			if !audioCheckProgressed(previous, report) {
				notice = "本轮未能继续推进；已完成记录保留，可再次继续全库体检。"
				break
			}
			previous = report
		}
	} else {
		batchCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		_, scanErr := state.scanBatch(batchCtx, cfg, store, session.library, session.deep)
		timedOut := batchCtx.Err() != nil
		cancel()
		report = state.cachedReport()
		if scanErr != nil && !timedOut && ctx.Err() == nil {
			notice = "❌ 本批体检遇到错误；已完成记录保留，可继续扫描。"
		} else if timedOut && ctx.Err() == nil {
			notice = "本批已达到时间上限；已完成记录保留，可继续扫描。"
		}
	}

	if app.audioCheckIsStopping() {
		return
	}
	if ctx.Err() != nil {
		notice = "已停止并显示已完成结果；再次运行会跳过已检查文件并继续。"
		report = state.cachedReport()
	}
	session.report = filterAudioCheckSkipped(report, session.skipped)
	session.running = false
	session.notice = notice
	if err := app.presentAudioCheck(api, session); err != nil {
		utils.WarnWithFormat("更新曲库体检卡片失败: %v", err)
	}
}

func audioCheckProgressed(before, after audioCheckReport) bool {
	return before.checked != after.checked || before.remaining != after.remaining ||
		before.deferred != after.deferred || before.referenceQueue != after.referenceQueue ||
		before.unreadable != after.unreadable || len(before.findings) != len(after.findings)
}

func filterAudioCheckSkipped(report audioCheckReport, skipped map[audioquality.FileVersion]struct{}) audioCheckReport {
	findings := make([]audioCheckFinding, 0, len(report.findings))
	for _, finding := range report.findings {
		if _, skip := skipped[audioFileVersion(finding.file)]; !skip {
			findings = append(findings, finding)
		}
	}
	report.findings = findings
	return report
}

func (app *BotApp) saveRunningAudioCheckSession(chat int64, session audioCheckSession) {
	app.audioCheckMu.Lock()
	if current, ok := app.audioCheckSessions[chat]; ok && current.token == session.token {
		app.audioCheckSessions[chat] = session
	}
	app.audioCheckMu.Unlock()
}

func (app *BotApp) removeAudioCheckSession(chat int64, token string) {
	app.audioCheckMu.Lock()
	if current, ok := app.audioCheckSessions[chat]; ok && current.token == token {
		delete(app.audioCheckSessions, chat)
	}
	app.audioCheckMu.Unlock()
}

func (app *BotApp) audioCheckIsStopping() bool {
	app.audioCheckMu.Lock()
	defer app.audioCheckMu.Unlock()
	return app.audioCheckStopping
}

func (app *BotApp) failAudioCheck(api tb.API, session audioCheckSession, text string) {
	app.removeAudioCheckSession(session.message.Chat.ID, session.token)
	if _, err := api.Edit(session.message, text, &tb.ReplyMarkup{}); err != nil {
		utils.WarnWithFormat("更新曲库体检失败卡片失败: %v", err)
	}
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
	session.running = false
	session.busy = false
	if err := persistAudioCheckSession(session); err != nil {
		utils.WarnWithFormat("保存体检结果卡片失败: %v", err)
		if session.notice != "" {
			session.notice += "\n"
		}
		session.notice += "暂时无法保存卡片进度，已检查的文件记录仍保留。"
	}
	app.setAudioCheckSession(session)
	text, markup := formatAudioCheckCard(session)
	_, err := api.Edit(session.message, text, tb.ModeHTML, markup)
	return err
}

func (app *BotApp) setAudioCheckSession(session audioCheckSession) {
	app.audioCheckMu.Lock()
	defer app.audioCheckMu.Unlock()
	app.storeAudioCheckSessionLocked(session)
}

func (app *BotApp) storeAudioCheckSessionLocked(session audioCheckSession) {
	if app.audioCheckSessions == nil {
		app.audioCheckSessions = make(map[int64]audioCheckSession)
	}
	for chatID, old := range app.audioCheckSessions {
		if !old.running && time.Now().After(old.expires) {
			delete(app.audioCheckSessions, chatID)
		}
	}
	app.audioCheckSessions[session.message.Chat.ID] = session
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
		(!session.running && time.Now().After(session.expires)) || !sameMusicActionMessage(session.message, c.Message()) {
		app.audioCheckMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "这张卡片已过期，请重新发送 /audio_check。", ShowAlert: true})
	}
	if action == "stop" {
		if !session.running || app.audioCheckCancel == nil {
			app.audioCheckMu.Unlock()
			return c.Respond(&tb.CallbackResponse{Text: "当前没有正在运行的体检。"})
		}
		cancel := app.audioCheckCancel
		if err := persistAudioCheckPause(chat, token); err != nil {
			app.audioCheckMu.Unlock()
			utils.WarnWithFormat("保存体检停止状态失败: %v", err)
			return c.Respond(&tb.CallbackResponse{Text: "暂时无法保存停止状态，请稍后再试。", ShowAlert: true})
		}
		cancel()
		app.audioCheckMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "正在停止并整理已完成结果…"})
	}
	if session.running || session.busy || app.audioCheckDone != nil {
		app.audioCheckMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "上一项操作还在处理。"})
	}
	if action == "scan" || action == "deep" || action == "full" {
		app.audioCheckMu.Unlock()
		_ = c.Respond()
		return app.startAudioCheck(c, action == "deep" || session.deep || action == "full", action == "full", &session)
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
	if report.resumeAvailable {
		text.WriteString("\n曲库清单未读完，可再次扫描继续。")
	}
	if session.notice != "" {
		fmt.Fprintf(&text, "\n\n%s", utils.EscapeHTML(session.notice))
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
	if report.remaining > 0 || report.referenceQueue > 0 || (report.resumeAvailable && !session.full) {
		label := "继续扫描"
		if session.deep {
			label = "继续深度扫描"
		}
		rows = append(rows, markup.Row(button(label, "scan")))
	}
	if report.deferred > 0 {
		fmt.Fprintf(&text, "\n%d 首已初筛，可再做完整解码。", report.deferred)
		rows = append(rows, markup.Row(button("深度扫描", "deep")))
	}
	fullLabel := "全库体检"
	if session.full {
		fullLabel = "继续全库体检"
	}
	rows = append(rows, markup.Row(button(fullLabel, "full")))
	markup.Inline(rows...)
	return text.String(), markup
}

func formatAudioCheckProgressCard(session audioCheckSession) (string, *tb.ReplyMarkup) {
	text := "🎧 <b>曲库体检正在运行</b>\n正在读取曲库并检查文件…"
	if session.full {
		text = "🎧 <b>全库体检正在运行</b>\n正在检查曲库全部曲目，包含快速初筛暂缓的文件…"
	} else if session.deep {
		text = "🎧 <b>深度体检正在运行</b>\n正在解码检查曲库文件…"
	}
	if session.report.total > 0 {
		text += fmt.Sprintf("\n已检查 %d/%d 首", session.report.checked, session.report.total)
		if session.report.remaining > 0 {
			text += fmt.Sprintf(" · 剩余 %d 首", session.report.remaining)
		}
	}
	if session.notice != "" {
		text += "\n" + utils.EscapeHTML(session.notice)
	}
	markup := &tb.ReplyMarkup{}
	markup.Inline(markup.Row(markup.Data("终止并查看结果", audioCheckCallback, "stop", session.token)))
	return text, markup
}

func (app *BotApp) editAudioCheckProgress(api tb.API, session audioCheckSession) error {
	text, markup := formatAudioCheckProgressCard(session)
	_, err := api.Edit(session.message, text, tb.ModeHTML, markup)
	return err
}

func audioCheckDuration(seconds float64) string {
	whole := int(seconds + 0.5)
	return fmt.Sprintf("%d:%02d", whole/60, whole%60)
}
