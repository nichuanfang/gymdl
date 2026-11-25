package bot

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/audioquality"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/storage"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

type savedAudioCheck struct {
	Owner     int64
	MessageID int
	Library   string
	Deep      bool
	Full      bool
	Skipped   []audioquality.FileVersion
	Index     int
	Notice    string
	StartedAt int64
	Report    *savedAudioCheckReport
}

type savedAudioCheckReport struct {
	Total, Checked, Remaining, Deferred, Unreadable, ReferenceQueue, WithoutReference int
	Limited, ResumeAvailable                                                          bool
	Findings                                                                          []savedAudioCheckFinding
}

type savedAudioCheckFinding struct {
	File   web.FileEntry
	Result audioquality.Result
}

func checkpointAudioCheck(store *audioquality.Store, ctx context.Context, session audioCheckSession) error {
	saved := savedAudioCheck{
		Owner: session.owner, MessageID: session.message.ID, Library: session.library,
		Deep: session.deep, Full: session.full, Index: session.index, Notice: session.notice,
		StartedAt: session.startedAt.UnixNano(),
	}
	for version := range session.skipped {
		saved.Skipped = append(saved.Skipped, version)
	}
	phase := audioquality.SessionRunning
	if !session.running {
		phase = audioquality.SessionResults
		r := session.report
		saved.Report = &savedAudioCheckReport{
			Total: r.total, Checked: r.checked, Remaining: r.remaining, Deferred: r.deferred,
			Unreadable: r.unreadable, ReferenceQueue: r.referenceQueue, WithoutReference: r.withoutReference,
			Limited: r.limited, ResumeAvailable: r.resumeAvailable,
		}
		for _, finding := range r.findings {
			saved.Report.Findings = append(saved.Report.Findings, savedAudioCheckFinding{File: finding.file, Result: finding.Result})
		}
	}
	payload, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	return store.SaveSession(ctx, audioquality.Session{
		ChatID: session.message.Chat.ID, Token: session.token, Phase: phase, ExpiresAt: session.expires, Payload: payload,
	})
}

func persistAudioCheckSession(session audioCheckSession) error {
	store, err := audioquality.Open(storage.DefaultDatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return checkpointAudioCheck(store, ctx, session)
}

func persistAudioCheckPause(chat int64, token string) error {
	store, err := audioquality.Open(storage.DefaultDatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return store.PauseSession(ctx, chat, token)
}

func decodeAudioCheckSession(record audioquality.Session) (audioCheckSession, error) {
	var saved savedAudioCheck
	if err := json.Unmarshal(record.Payload, &saved); err != nil {
		return audioCheckSession{}, err
	}
	session := audioCheckSession{
		token: record.Token, expires: record.ExpiresAt, owner: saved.Owner, library: saved.Library,
		message: &tb.Message{ID: saved.MessageID, Chat: &tb.Chat{ID: record.ChatID, Type: tb.ChatPrivate}},
		index:   saved.Index, deep: saved.Deep, full: saved.Full, running: record.Phase == audioquality.SessionRunning,
		notice: saved.Notice, skipped: make(map[audioquality.FileVersion]struct{}, len(saved.Skipped)),
	}
	if saved.StartedAt > 0 {
		session.startedAt = time.Unix(0, saved.StartedAt)
	}
	for _, version := range saved.Skipped {
		session.skipped[version] = struct{}{}
	}
	if r := saved.Report; r != nil {
		session.report = audioCheckReport{
			total: r.Total, checked: r.Checked, remaining: r.Remaining, deferred: r.Deferred,
			unreadable: r.Unreadable, referenceQueue: r.ReferenceQueue, withoutReference: r.WithoutReference,
			limited: r.Limited, resumeAvailable: r.ResumeAvailable,
		}
		for _, finding := range r.Findings {
			session.report.findings = append(session.report.findings, audioCheckFinding{file: finding.File, Result: finding.Result})
		}
	}
	session.index = max(0, min(session.index, len(session.report.findings)))
	return session, nil
}

func (app *BotApp) restoreAudioChecks(api tb.API) {
	store, err := audioquality.Open(storage.DefaultDatabasePath)
	if err != nil {
		utils.WarnWithFormat("无法恢复曲库体检: %v", err)
		return
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	records, err := store.Sessions(ctx)
	if err != nil {
		utils.WarnWithFormat("无法载入曲库体检进度: %v", err)
		return
	}
	cfg := config.WithLiveTaskOverrides(app.cfg)
	library, libraryErr := audioCheckLibrary(cfg)
	for _, record := range records {
		session, err := decodeAudioCheckSession(record)
		if err != nil || session.owner != record.ChatID || session.owner <= 0 ||
			!app.audioCheckOwnerAllowed(session.owner) || session.message.ID <= 0 {
			continue
		}
		if record.Phase == audioquality.SessionResults {
			if libraryErr == nil && library == session.library {
				app.setAudioCheckSession(session)
				// The last process may have saved results just before it
				// could replace the Telegram progress card.
				text, markup := formatAudioCheckCard(session)
				if _, err := api.Edit(session.message, text, tb.ModeHTML, markup); err != nil &&
					!strings.Contains(err.Error(), "message is not modified") {
					utils.WarnWithFormat("无法恢复曲库体检结果卡片: %v", err)
				}
			}
			continue
		}
		notice := "已恢复停止前的体检记录。"
		if record.Phase == audioquality.SessionRunning {
			switch {
			case libraryErr != nil || library != session.library:
				notice = "曲库配置暂无法核对，已保留此前记录。"
			case missingAudioCheckTool() != "":
				notice = "当前缺少音频检查工具，已保留此前记录。"
			case app.launchRestoredAudioCheck(api, cfg, session):
				continue
			default:
				if app.audioCheckIsStopping() {
					return
				}
				notice = "已有一次体检正在恢复，已保留本任务的结果。"
			}
		}
		session.running = false
		report, loadErr := cachedAudioCheckReport(ctx, store, session.library, session.owner)
		if loadErr != nil {
			report = audioCheckReport{resumeAvailable: true}
		}
		session.report = filterAudioCheckSkipped(report, session.skipped)
		session.notice = notice
		if err := app.presentAudioCheck(api, session); err != nil {
			utils.WarnWithFormat("无法恢复曲库体检卡片: %v", err)
		}
	}
}

func (app *BotApp) audioCheckOwnerAllowed(owner int64) bool {
	if app.cfg == nil || app.cfg.Telegram == nil {
		return false
	}
	for _, allowed := range app.cfg.Telegram.AllowedUsers {
		if allowed == strconv.FormatInt(owner, 10) {
			return true
		}
	}
	return false
}

func (app *BotApp) launchRestoredAudioCheck(api tb.API, cfg *config.Config, session audioCheckSession) bool {
	app.audioCheckMu.Lock()
	if app.audioCheckDone != nil || app.audioCheckStopping {
		app.audioCheckMu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	app.audioCheckDone, app.audioCheckCancel = done, cancel
	app.storeAudioCheckSessionLocked(session)
	app.audioCheckMu.Unlock()
	go func() {
		defer app.finishAudioCheck(done, cancel)
		session.notice = "容器重启后正在恢复扫描进度…"
		if err := app.editAudioCheckProgress(api, session); err != nil && !strings.Contains(err.Error(), "message is not modified") {
			utils.WarnWithFormat("无法更新恢复中的体检卡片: %v", err)
		}
		app.runAudioCheck(ctx, api, cfg, session)
	}()
	return true
}
