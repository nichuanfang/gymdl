package bot

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/audiodupe"
	"github.com/nichuanfang/gymdl/internal/audioquality"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/storage"
	"github.com/nichuanfang/gymdl/utils"
	tb "gopkg.in/telebot.v4"
)

type audioCheckDuplicate struct {
	File       web.FileEntry
	Result     audioquality.Result
	Kind       audiodupe.Kind
	Similarity float64
}

type audioCheckPair struct {
	First, Second audioquality.FileVersion
}

func audioCheckFilePair(a, b web.FileEntry) audioCheckPair {
	first, second := audioFileVersion(a), audioFileVersion(b)
	if second.Path < first.Path {
		first, second = second, first
	}
	return audioCheckPair{First: first, Second: second}
}

type audioCheckFingerprint struct {
	file   web.FileEntry
	result audioquality.Result
	fp     audiodupe.Fingerprint
}

type audioCheckDuplicateIndex struct {
	byDuration map[int][]audioCheckFingerprint
	reviewed   map[audioCheckPair]bool
	trashed    map[audioquality.FileVersion]bool
}

func newAudioCheckDuplicateIndex(reviews []audioquality.DuplicateReview) *audioCheckDuplicateIndex {
	index := &audioCheckDuplicateIndex{
		byDuration: make(map[int][]audioCheckFingerprint),
		reviewed:   make(map[audioCheckPair]bool, len(reviews)),
		trashed:    make(map[audioquality.FileVersion]bool),
	}
	for _, review := range reviews {
		index.review(review)
	}
	return index
}

func (index *audioCheckDuplicateIndex) review(review audioquality.DuplicateReview) {
	index.reviewed[audioCheckPair{First: review.First, Second: review.Second}] = true
	switch review.TrashedPath {
	case review.First.Path:
		index.trashed[review.First] = true
	case review.Second.Path:
		index.trashed[review.Second] = true
	}
}

func (index *audioCheckDuplicateIndex) match(file web.FileEntry, fp audiodupe.Fingerprint, skipped map[audioCheckPair]struct{}) *audioCheckDuplicate {
	var best *audioCheckDuplicate
	// Same bounds as DurationClose, capped on the left by the current file
	// and on the right by the candidate when its duration is longer.
	lo := int(fp.Duration - math.Max(1.5, 0.01*fp.Duration))
	hi := int(math.Max(fp.Duration+1.5, fp.Duration/0.99))
	for seconds := lo; seconds <= hi; seconds++ {
		for _, candidate := range index.byDuration[seconds] {
			if candidate.file.Path == file.Path || index.trashed[audioFileVersion(candidate.file)] || !audiodupe.DurationClose(fp.Duration, candidate.fp.Duration) {
				continue
			}
			pair := audioCheckFilePair(file, candidate.file)
			if _, skip := skipped[pair]; skip || index.reviewed[pair] {
				continue
			}
			comparison := audiodupe.Compare(fp, candidate.fp)
			score := audiodupe.Similarity(comparison)
			if comparison.Kind == audiodupe.KindDifferent || (best != nil &&
				(best.Kind == audiodupe.KindSame && comparison.Kind != audiodupe.KindSame ||
					best.Kind == comparison.Kind && best.Similarity >= score)) {
				continue
			}
			best = &audioCheckDuplicate{File: candidate.file, Result: candidate.result, Kind: comparison.Kind, Similarity: score}
		}
	}
	return best
}

func (index *audioCheckDuplicateIndex) add(file web.FileEntry, result audioquality.Result, fp audiodupe.Fingerprint) {
	seconds := int(fp.Duration)
	index.byDuration[seconds] = append(index.byDuration[seconds], audioCheckFingerprint{file: file, result: result, fp: fp})
}

func formatAudioCheckDuplicate(session audioCheckSession, finding audioCheckFinding, markup *tb.ReplyMarkup, button func(string, string) tb.Btn) (string, *tb.ReplyMarkup) {
	other := audioCheckFinding{file: finding.duplicate.File, Result: finding.duplicate.Result}
	title := finding.musicAction("", 1).title
	description := "相似录音"
	if finding.duplicate.Kind == audiodupe.KindSame {
		description = "疑似重复录音"
	}
	var text strings.Builder
	fmt.Fprintf(&text, "🎧 <b>曲库体检 · %s</b>\n──────────────\n<b>%s</b>\n音频指纹相似度 %.0f%%", description,
		musicSourceLabel(title), 100*finding.duplicate.Similarity)
	for i, recording := range []audioCheckFinding{finding, other} {
		fmt.Fprintf(&text, "\n\n<b>%c</b> · %s", 'A'+i, musicSourceLabel(recording.file.Path))
		if recording.Codec != "" {
			fmt.Fprintf(&text, "\n%s", utils.EscapeHTML(strings.ToUpper(recording.Codec)))
		}
		if recording.Bitrate > 0 {
			fmt.Fprintf(&text, " · %d kbps", recording.Bitrate)
		}
		if recording.Duration > 0 {
			fmt.Fprintf(&text, " · %s", audioCheckDuration(recording.Duration))
		}
	}
	if session.confirmTrash != "" {
		fmt.Fprintf(&text, "\n\n将 <b>%s</b> 加入 Navidrome 回收站歌单？", utils.EscapeHTML(session.confirmTrash))
		markup.Inline(markup.Row(button("确认加入回收站", "confirm"), button("返回", "back")))
	} else {
		markup.Inline(
			markup.Row(button("试听 A", "play"), button("试听 B", "play_other")),
			markup.Row(button("A 加入回收站", "trash_a"), button("B 加入回收站", "trash_b")),
			markup.Row(button("两版都保留", "keep"), button("跳过并继续", "skip")),
		)
	}
	if session.notice != "" {
		fmt.Fprintf(&text, "\n\n%s", utils.EscapeHTML(session.notice))
	}
	return text.String(), markup
}

func (app *BotApp) handleAudioCheckDuplicate(c tb.Context, session audioCheckSession, finding audioCheckFinding, action string) error {
	other := audioCheckFinding{file: finding.duplicate.File, Result: finding.duplicate.Result}
	pair := audioCheckFilePair(finding.file, other.file)
	switch action {
	case "play":
		return app.openAudioCheckFile(c, session, finding, false)
	case "play_other":
		return app.openAudioCheckFile(c, session, other, false)
	case "trash_a", "trash_b":
		session.confirmTrash = "A"
		if action == "trash_b" {
			session.confirmTrash = "B"
		}
		_ = c.Respond()
		return app.presentAudioCheck(c.Bot(), session)
	case "back":
		session.confirmTrash = ""
		session.notice = ""
		_ = c.Respond()
		return app.presentAudioCheck(c.Bot(), session)
	case "skip":
		_ = c.Respond(&tb.CallbackResponse{Text: "已跳过，继续检查"})
		session.notice = ""
		session.skippedDuplicates = maps.Clone(session.skippedDuplicates)
		session.skippedDuplicates[pair] = struct{}{}
		return app.advanceAudioCheck(c, session)
	case "keep", "confirm":
		if action == "confirm" && session.confirmTrash != "A" && session.confirmTrash != "B" {
			return c.Respond(&tb.CallbackResponse{Text: "请先选择要加入回收站的版本。"})
		}
	default:
		return c.Respond(&tb.CallbackResponse{Text: "这张卡片不支持该操作。"})
	}

	cfg := config.WithLiveTaskOverrides(app.cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	_ = c.Respond(&tb.CallbackResponse{Text: "正在核对两个文件…"})
	review := audioquality.DuplicateReview{First: pair.First, Second: pair.Second}
	store, err := audioquality.Open(storage.DefaultDatabasePath)
	if err != nil {
		return c.Send("暂时无法保存查重处理结果，请重试。")
	}
	defer store.Close()
	for _, file := range []web.FileEntry{finding.file, other.file} {
		if _, err := audioCheckCurrentFile(ctx, cfg, session, file); err != nil {
			return c.Send("其中一个文件已变化或暂不可读，请跳过后继续。")
		}
	}
	if action == "confirm" {
		app.audioCheckTrashMu.Lock()
		defer app.audioCheckTrashMu.Unlock()
		file, keep := finding.file, other.file
		if session.confirmTrash == "B" {
			file, keep = keep, file
		}
		if err := trashAudioCheckCopy(ctx, cfg, file, &keep); err != nil {
			utils.WarnWithFormat("[AudioCheck] 加入回收站失败: %v", err)
			return c.Send("暂时无法确认已加入回收站，请重试。")
		}
		review.TrashedPath = file.Path
	}
	err = store.SaveDuplicateReview(ctx, session.library, session.owner, review)
	_ = store.Close()
	if err != nil {
		return c.Send("暂时无法保存查重处理结果，请重试。")
	}
	if session.state != nil && session.state.duplicates != nil {
		session.state.duplicates.review(review)
	}
	session.notice = "已保存保留两版的决定。"
	if action == "confirm" {
		session.notice = session.confirmTrash + " 已加入回收站。"
	}
	return app.advanceAudioCheck(c, session)
}

func trashAudioCheckCopy(ctx context.Context, cfg *config.Config, file web.FileEntry, keep *web.FileEntry) error {
	service, err := newPlaylistOrganizerService(cfg.PlaylistOrganizer)
	if err != nil {
		return err
	}
	songID, err := web.LookupNavidromeAudioID(ctx, cfg.PlaylistOrganizer, file.Path)
	if err != nil {
		return err
	}
	var keepID string
	if keep != nil {
		keepID, err = web.LookupNavidromeAudioID(ctx, cfg.PlaylistOrganizer, keep.Path)
		if err != nil {
			return err
		}
		if keepID == songID {
			return fmt.Errorf("两个路径指向同一首 Navidrome 歌曲")
		}
	}
	trash := emptyTrashService{organizer: service}
	_, ids, _, err := trash.readTrashPlaylist(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(ids, keepID) {
		return fmt.Errorf("要保留的版本已在回收站")
	}
	if slices.Contains(ids, songID) {
		return nil
	}
	// The playlist update can be retried after an uncertain response.
	_, err = service.addSongToPlaylists(ctx, songID, []string{"回收站"})
	if err != nil {
		return err
	}
	_, ids, _, err = trash.readTrashPlaylist(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(ids, songID) || slices.Contains(ids, keepID) {
		return fmt.Errorf("回收站未返回预期状态")
	}
	return nil
}
