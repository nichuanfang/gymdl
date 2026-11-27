package bot

import (
	"context"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/audiodupe"
	"github.com/nichuanfang/gymdl/internal/audioquality"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/storage"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

const audioCheckCallback = "audio_check"
const audioCheckTTL = 12 * time.Hour

var audioCheckEndpoint = tb.InlineButton{Unique: audioCheckCallback}

type audioCheckSession struct {
	token             string
	expires           time.Time
	owner             int64
	message           *tb.Message
	library           string
	report            audioCheckReport
	index             int
	running           bool
	busy              bool
	notice            string
	skipped           map[audioquality.FileVersion]struct{}
	startedAt         time.Time
	state             *audioCheckScanState
	processed         int
	skippedDuplicates map[audioCheckPair]struct{}
	confirmTrash      string
}

func (app *BotApp) AudioCheckCommand(c tb.Context) error {
	if !app.discoveryAuthorized(c) {
		return c.Send("请由授权用户在私聊中使用 /audio_check。")
	}
	payload := strings.TrimSpace(c.Message().Payload)
	if payload != "" && payload != "deep" {
		return c.Send("发送 /audio_check 开始体检。")
	}
	return app.startAudioCheck(c, nil)
}

func missingAudioCheckTool() string {
	for _, tool := range []string{"ffprobe", "ffmpeg"} {
		if _, err := exec.LookPath(tool); err != nil {
			return tool
		}
	}
	return ""
}

func (app *BotApp) startAudioCheck(c tb.Context, previous *audioCheckSession) error {
	app.audioCheckCardMu.Lock()
	defer app.audioCheckCardMu.Unlock()
	if previous == nil {
		if current, ok := app.currentAudioCheck(c.Chat().ID, c.Sender().ID); ok {
			return app.takeoverAudioCheckCard(c, current)
		}
	}
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
	session := audioCheckSession{
		token: audioCheckToken(), expires: time.Now().Add(audioCheckTTL),
		owner: c.Sender().ID, library: library, running: true,
		skipped:           make(map[audioquality.FileVersion]struct{}),
		skippedDuplicates: make(map[audioCheckPair]struct{}), startedAt: time.Now(),
	}
	if previous != nil {
		if previous.library != library {
			return c.Send("曲库位置已变化，请重新发送 /audio_check。")
		}
		session.message = previous.message
		session.state, session.report = previous.state, previous.report
		session.skipped, session.skippedDuplicates = previous.skipped, previous.skippedDuplicates
		session.processed, session.notice = previous.processed, previous.notice
		if !previous.startedAt.IsZero() {
			session.startedAt = previous.startedAt
		}
	}
	message := session.message
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	app.audioCheckMu.Lock()
	if app.audioCheckDone != nil || app.audioCheckStopping {
		app.audioCheckMu.Unlock()
		cancel()
		return c.Send("已有一次曲库体检正在进行，请查看原卡片。")
	}
	current := app.audioCheckSessions[c.Chat().ID]
	if current.running || (previous == nil && current.busy) || (previous != nil && current.token != previous.token) {
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

func audioCheckToken() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
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

// audioCheckTransportReady reports whether the library is reachable. Local
// tidy mode always is; WebDAV mode pings the endpoint because a dead tunnel
// would otherwise be recorded as a library full of unreadable files.
func audioCheckTransportReady(ctx context.Context, cfg *config.Config) bool {
	if cfg == nil || cfg.Tidy == nil || cfg.Tidy.Mode != 2 {
		return true
	}
	if core.GlobalWebDAV == nil || core.GlobalWebDAV.Client == nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return core.GlobalWebDAV.ClientWithContext(probeCtx).Connect() == nil
}

func (app *BotApp) runAudioCheck(ctx context.Context, api tb.API, cfg *config.Config, session audioCheckSession) {
	store, err := audioquality.Open(storage.DefaultDatabasePath)
	if err != nil {
		utils.WarnWithFormat("[AudioCheck] 打开体检记录失败: %v", err)
		app.completeAudioCheck(api, session, "暂时无法读取体检记录，可继续体检重试。", true)
		return
	}
	defer store.Close()

	fingerprints, err := audiodupe.Open(storage.DefaultDatabasePath)
	if err != nil {
		app.completeAudioCheck(api, session, "暂时无法打开查重记录，请继续体检重试。", true)
		return
	}
	defer fingerprints.Close()

	state := session.state
	fingerprint := true
	if _, err := exec.LookPath("fpcalc"); err != nil {
		fingerprint = false
	}
	if state == nil {
		// Every file is downloaded before it can be inspected, so an
		// unreachable tunnel would mark the whole library unreadable. Check it
		// once up front and leave the cached verdicts alone.
		if !audioCheckTransportReady(ctx, cfg) {
			app.completeAudioCheck(api, session, "WebDAV 当前连不上，先不体检，稍后再试。", true)
			return
		}
		// Clear last outage's transfer failures so this pass retries them.
		if dropped, dropErr := store.ForgetReadFailures(ctx, session.library); dropErr == nil && dropped > 0 {
			utils.InfoWithFormat("[AudioCheck] 清理 %d 条传输失败记录，本次重新尝试", dropped)
		}
		state, err = newAudioCheckScanState(ctx, cfg, store, session.library, session.owner, fingerprint)
		if err != nil {
			if app.audioCheckIsStopping() {
				return
			}
			if ctx.Err() != nil {
				cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				report, cacheErr := cachedAudioCheckReport(cacheCtx, store, session.library, session.owner)
				cancel()
				if cacheErr == nil {
					session.report = filterAudioCheckSkipped(report, session.skipped)
					session.index = 0
				}
				app.completeAudioCheck(api, session, "已停止读取目录，检查记录已保留。", true)
				return
			}
			utils.WarnWithFormat("[AudioCheck] 读取曲库失败: %v", err)
			app.completeAudioCheck(api, session, "暂时无法读取曲库，可继续体检重试。", true)
			return
		}
	}
	session.state = state
	lastUpdate := time.Now()
	progress := func(report audioCheckReport) {
		if time.Since(lastUpdate) < 5*time.Second {
			return
		}
		lastUpdate = time.Now()
		session.report = report
		if err := app.editAudioCheckProgress(api, session); err != nil && !strings.Contains(err.Error(), "message is not modified") {
			utils.WarnWithFormat("[AudioCheck] 更新进度失败: %v", err)
		}
	}

	finding, err := state.scanNext(ctx, cfg, store, fingerprints, session, progress)
	if app.audioCheckIsStopping() {
		return
	}
	session.index = 0
	session.report = state.report
	if finding != nil {
		session.report.findings = []audioCheckFinding{*finding}
	} else if err == nil && session.report.deferred > 0 {
		// The cursor is exhausted. Continuing must list the library again so
		// it can retry the files that could not be fetched in this pass.
		session.state = nil
	}
	notice := session.notice
	if strings.HasPrefix(notice, "容器重启后") || strings.HasPrefix(notice, "已停止") {
		notice = ""
	}
	more := finding != nil
	if ctx.Err() != nil {
		notice, more = "已停止，检查记录已保留。", true
	} else if err != nil {
		utils.WarnWithFormat("[AudioCheck] 检查中断: %v", err)
		notice, more = "本轮暂时无法继续，检查记录已保留。", true
	} else if !fingerprint {
		notice = "当前缺少 fpcalc，已执行音质检查。"
	}
	app.completeAudioCheck(api, session, notice, more)
}

func (app *BotApp) completeAudioCheck(api tb.API, session audioCheckSession, notice string, more bool) {
	app.audioCheckCardMu.Lock()
	defer app.audioCheckCardMu.Unlock()
	if binding, ok := app.liveAudioCheckBinding(session.message.Chat.ID); ok {
		session.message = binding.message
	}
	session.report.resumeAvailable = more
	session.notice = notice
	if err := app.presentAudioCheck(api, session); err != nil {
		utils.WarnWithFormat("[AudioCheck] 更新体检卡片失败: %v", err)
	}
	report := session.report
	utils.InfoWithFormat("[AudioCheck] 已检查 %d/%d，待处理 %d，未读到 %d，未取得指纹 %d",
		report.checked, report.total, len(report.findings), report.unreadable, report.unfingerprinted)
}

func filterAudioCheckSkipped(report audioCheckReport, skipped map[audioquality.FileVersion]struct{}) audioCheckReport {
	findings := make([]audioCheckFinding, 0, len(report.findings))
	for _, finding := range report.findings {
		if _, skip := skipped[audioFileVersion(finding.file)]; finding.duplicate != nil || !skip {
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

func (app *BotApp) audioCheckIsStopping() bool {
	app.audioCheckMu.Lock()
	defer app.audioCheckMu.Unlock()
	return app.audioCheckStopping
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
	session.token = audioCheckToken()
	session.expires = time.Now().Add(audioCheckTTL)
	session.running = false
	session.busy = false
	current, hasCurrent := currentAudioCheckFinding(session)
	if hasCurrent {
		session.report.findings = []audioCheckFinding{current}
	} else {
		session.report.findings = nil
	}
	session.index = 0
	if err := persistAudioCheckSession(session); err != nil {
		app.setAudioCheckSession(session)
		return err
	}
	text, markup := formatAudioCheckCard(session)
	if _, err := api.Edit(session.message, text, tb.ModeHTML, markup); err != nil && !strings.Contains(err.Error(), "message is not modified") {
		message, sendErr := api.Send(session.message.Chat, text, tb.ModeHTML, markup)
		if sendErr != nil {
			app.setAudioCheckSession(session)
			return fmt.Errorf("更新体检卡片失败: %v，补发失败: %w", err, sendErr)
		}
		session.message = message
		if err := persistAudioCheckSession(session); err != nil {
			app.setAudioCheckSession(session)
			return err
		}
	}
	app.setAudioCheckSession(session)
	return nil
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
	// The 返回体检 button lives on a source/download card, so it is handled
	// before the "is this the current card message" check that follows.
	if action == "resume" {
		return app.resumeAudioCheckCard(c, token)
	}
	chat := c.Chat().ID
	app.audioCheckMu.Lock()
	session, ok := app.audioCheckSessions[chat]
	if !ok || session.owner != c.Sender().ID ||
		(!session.running && time.Now().After(session.expires)) || !sameMusicActionMessage(session.message, c.Message()) {
		app.audioCheckMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "这张卡片已过期，请重新发送 /audio_check。", ShowAlert: true})
	}
	if session.token != token {
		app.audioCheckMu.Unlock()
		return c.Respond(&tb.CallbackResponse{Text: "体检卡片已更新，请使用卡片上最新的按钮。", ShowAlert: true})
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
	finding, hasFinding := currentAudioCheckFinding(session)
	switch action {
	case "continue", "scan", "deep", "full":
		if hasFinding {
			return c.Respond(&tb.CallbackResponse{Text: "请先处理当前歌曲。"})
		}
		_ = c.Respond()
		return app.startAudioCheck(c, &session)
	case "play", "sources", "normal", "skip", "play_other", "keep", "trash_a", "trash_b", "confirm", "back":
		if !hasFinding {
			return c.Respond(&tb.CallbackResponse{Text: "当前没有待处理歌曲。"})
		}
	default:
		return c.Respond(&tb.CallbackResponse{Text: "未知体检操作。", ShowAlert: true})
	}

	if finding.duplicate != nil {
		return app.handleAudioCheckDuplicate(c, session, finding, action)
	}
	switch action {
	case "skip":
		_ = c.Respond(&tb.CallbackResponse{Text: "已跳过，继续检查"})
		session.notice = ""
		session.skipped = maps.Clone(session.skipped)
		session.skipped[audioFileVersion(finding.file)] = struct{}{}
		return app.advanceAudioCheck(c, session)
	case "normal":
		_ = c.Respond(&tb.CallbackResponse{Text: "正在保存标记…"})
		cfg := config.WithLiveTaskOverrides(app.cfg)
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if _, err := audioCheckCurrentFile(ctx, cfg, session, finding.file); err != nil {
			return c.Send("文件已变化或暂不可读，请跳过后继续。")
		}
		store, err := audioquality.Open(storage.DefaultDatabasePath)
		var marked bool
		if err == nil {
			marked, err = store.MarkNormal(ctx, session.library, session.owner, audioFileVersion(finding.file))
			_ = store.Close()
		}
		if err != nil || !marked {
			return c.Send("未能保存标记，请稍后重试。")
		}
		if session.state != nil {
			item := session.state.cached[finding.file.Path]
			item.Reviewed = true
			session.state.cached[finding.file.Path] = item
		}
		session.notice = "已标记正常。"
		return app.advanceAudioCheck(c, session)
	case "play", "sources":
		return app.openAudioCheckFile(c, session, finding, action == "sources")
	default:
		return c.Respond(&tb.CallbackResponse{Text: "这张卡片不支持该操作。"})
	}
}

func currentAudioCheckFinding(session audioCheckSession) (audioCheckFinding, bool) {
	for index := max(0, session.index); index < len(session.report.findings); index++ {
		if finding := session.report.findings[index]; finding.needsReview() {
			return finding, true
		}
	}
	return audioCheckFinding{}, false
}

func (app *BotApp) advanceAudioCheck(c tb.Context, session audioCheckSession) error {
	session.index++
	session.processed++
	session.confirmTrash = ""
	if _, hasNext := currentAudioCheckFinding(session); hasNext {
		return app.presentAudioCheck(c.Bot(), session)
	}
	if session.state == nil && len(session.report.findings) > 0 {
		// Cards saved by older versions may have completed the quality pass
		// before fingerprinting was part of the check.
		session.report.resumeAvailable = true
	}
	session.report.findings = nil
	session.index = 0
	if !audioCheckCanContinue(session) {
		return app.presentAudioCheck(c.Bot(), session)
	}
	return app.startAudioCheck(c, &session)
}

func audioCheckCurrentFile(ctx context.Context, cfg *config.Config, session audioCheckSession, file web.FileEntry) (string, error) {
	library, err := audioCheckLibrary(cfg)
	if err != nil || library != session.library {
		return "", fmt.Errorf("曲库位置已变化，请重新发送 /audio_check。")
	}
	return web.CurrentLibraryAudioPathContext(ctx, cfg, file)
}

func (app *BotApp) openAudioCheckFile(c tb.Context, session audioCheckSession, finding audioCheckFinding, sources bool) error {
	_ = c.Respond(&tb.CallbackResponse{Text: "正在核对文件…"})
	cfg := config.WithLiveTaskOverrides(app.cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	currentPath, err := audioCheckCurrentFile(ctx, cfg, session, finding.file)
	if err != nil {
		return c.Send("文件已变化或暂不可读，请跳过后继续。")
	}
	song := finding.musicAction(currentPath, cfg.Tidy.Mode)
	song.checkToken = session.token
	if !sources {
		if err := app.sendMusicActionAudio(c, song); err != nil {
			return c.Send("试听失败，文件可能已损坏，或 Telegram 无法发送这个格式。")
		}
		return nil
	}
	message, err := c.Bot().Send(c.Chat(), "🔎 正在查找其他音源…")
	if err != nil {
		return err
	}
	return app.presentMusicSourceChoices(ctx, c, message, song, true)
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

func audioCheckCanContinue(session audioCheckSession) bool {
	report := session.report
	return report.resumeAvailable || report.remaining > 0 || report.deferred > 0 || report.referenceQueue > 0
}

func formatAudioCheckCard(session audioCheckSession) (string, *tb.ReplyMarkup) {
	report := session.report
	finding, hasFinding := currentAudioCheckFinding(session)
	markup := &tb.ReplyMarkup{}
	button := func(text, action string) tb.Btn { return markup.Data(text, audioCheckCallback, action, session.token) }
	var rows []tb.Row
	var text strings.Builder
	text.WriteString("🎧 <b>曲库体检</b>\n──────────────")
	if hasFinding {
		if finding.duplicate != nil {
			return formatAudioCheckDuplicate(session, finding, markup, button)
		}
		song := finding.musicAction("", 1)
		fmt.Fprintf(&text, "\n<b>%s</b>", musicSourceLabel(song.title))
		if song.artist != "" {
			fmt.Fprintf(&text, "\n%s", musicSourceLabel(song.artist))
		}
		if finding.Damaged && !finding.TailGlitch() {
			text.WriteString("\n⚠️ 文件损坏或音轨不完整")
			if finding.Duration > 0 && finding.Decoded > 0 && finding.Decoded < finding.Duration {
				shownDecoded, shownDeclared := audioCheckDuration(finding.Decoded), audioCheckDuration(finding.Duration)
				if shownDecoded == shownDeclared {
					fmt.Fprintf(&text, "（少 %.1f 秒）", finding.Duration-finding.Decoded)
				} else {
					fmt.Fprintf(&text, "（解码 %s／标注 %s）", shownDecoded, shownDeclared)
				}
			}
		}
		if finding.ShorterThanSource() {
			actual := finding.Decoded
			if actual == 0 {
				actual = finding.Duration
			}
			fmt.Fprintf(&text, "\n⚠️ 疑似不完整 · 文件 %s／同版本 %s", audioCheckDuration(actual), audioCheckDuration(finding.Reference))
		}
		if audioquality.LowBitrate(finding.Codec, finding.Bitrate) {
			fmt.Fprintf(&text, "\n⚠️ 低码率 · %s %d kbps", utils.EscapeHTML(strings.ToUpper(finding.Codec)), finding.Bitrate)
		}
		rows = append(rows, markup.Row(button("▶️ 试听", "play"), button("其他音源", "sources")),
			markup.Row(button("跳过并继续", "skip"), button("标记正常", "normal")))
	}
	if report.total > 0 || session.notice == "" {
		fmt.Fprintf(&text, "\n\n已检查 %d/%d 首", report.checked, report.total)
	}
	if !hasFinding {
		if session.processed > 0 {
			fmt.Fprintf(&text, "\n已处理 %d 个问题", session.processed)
		} else if session.notice == "" && report.checked > 0 {
			text.WriteString("\n已检查的文件暂未发现问题")
		}
		if pending := report.remaining + report.deferred; pending > 0 {
			fmt.Fprintf(&text, "\n%d 首待检查", pending)
		}
		// Files the tunnel could not deliver this pass. They are part of 待检查
		// because the next scan retries them, and must not read as damaged.
		if report.deferred > 0 {
			fmt.Fprintf(&text, "\n其中 %d 首没能取到文件，下次体检会重试", report.deferred)
		}
		if report.referenceQueue > 0 {
			fmt.Fprintf(&text, "\n%d 首待核对音源时长", report.referenceQueue)
		}
		if report.unreadable > 0 {
			fmt.Fprintf(&text, "\n%d 首暂时没读到，可稍后重新体检", report.unreadable)
		}
		if report.unfingerprinted > 0 {
			fmt.Fprintf(&text, "\n%d 首没有可用指纹", report.unfingerprinted)
		}
		if report.withoutReference > 0 {
			fmt.Fprintf(&text, "\n%d 首没有可靠的同版本时长", report.withoutReference)
		}
		if report.limited {
			text.WriteString("\n部分曲库目录未能读取")
		}
		if audioCheckCanContinue(session) {
			rows = append(rows, markup.Row(button("继续体检", "continue")))
		}
	}
	if session.notice != "" {
		fmt.Fprintf(&text, "\n\n%s", utils.EscapeHTML(session.notice))
	}
	markup.Inline(rows...)
	return text.String(), markup
}

func formatAudioCheckProgressCard(session audioCheckSession) (string, *tb.ReplyMarkup) {
	text := "🎧 <b>曲库体检</b>\n──────────────\n正在检查曲库…"
	if session.report.total > 0 {
		text = fmt.Sprintf("🎧 <b>曲库体检</b>\n──────────────\n正在检查 · 已检查 %d/%d 首", session.report.checked, session.report.total)
	}
	if session.notice != "" {
		text += "\n" + utils.EscapeHTML(session.notice)
	}
	markup := &tb.ReplyMarkup{}
	markup.Inline(markup.Row(markup.Data("停止", audioCheckCallback, "stop", session.token)))
	return text, markup
}

func (app *BotApp) editAudioCheckProgress(api tb.API, session audioCheckSession) error {
	app.audioCheckCardMu.Lock()
	defer app.audioCheckCardMu.Unlock()
	chat := session.message.Chat.ID
	app.audioCheckMu.Lock()
	if binding, ok := app.audioCheckSessions[chat]; ok {
		session.message, session.token = binding.message, binding.token
		binding.report = session.report
		app.audioCheckSessions[chat] = binding
	}
	app.audioCheckMu.Unlock()
	text, markup := formatAudioCheckProgressCard(session)
	_, err := api.Edit(session.message, text, tb.ModeHTML, markup)
	return err
}

func audioCheckDuration(seconds float64) string {
	whole := int(seconds + 0.5)
	return fmt.Sprintf("%d:%02d", whole/60, whole%60)
}
