package bot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/audiodupe"
	"github.com/nichuanfang/gymdl/internal/audioquality"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/musicdedupe"
	"github.com/nichuanfang/gymdl/processor"
)

// audioCheckFileTimeout bounds one file's download + analysis + fingerprint.
// A 36MB FLAC took 13.6s to fetch over the frp tunnel on a healthy day, and a
// library file may be 512MB, so a 50s budget marked every large file unreadable
// during an outage. The download dominates, hence the generous value.
const audioCheckFileTimeout = 3 * time.Minute

type audioCheckFinding struct {
	file web.FileEntry
	audioquality.Result
	duplicate *audioCheckDuplicate
}

func (f audioCheckFinding) needsReview() bool {
	return f.duplicate != nil || f.NeedsReview()
}

type audioCheckReport struct {
	findings         []audioCheckFinding
	total            int
	checked          int
	remaining        int
	deferred         int
	unreadable       int
	referenceQueue   int
	withoutReference int
	unfingerprinted  int
	limited          bool
	resumeAvailable  bool
}

type audioCheckScanState struct {
	files      []web.FileEntry
	cached     map[string]audioquality.CachedResult
	cursor     int
	report     audioCheckReport
	duplicates *audioCheckDuplicateIndex
	pending    *audioCheckFingerprint
}

func audioCheckLibrary(cfg *config.Config) (string, error) {
	if cfg == nil || cfg.Tidy == nil {
		return "", fmt.Errorf("整理目录尚未配置")
	}
	var root string
	switch cfg.Tidy.Mode {
	case 1:
		if strings.TrimSpace(cfg.Tidy.DistDir) == "" {
			return "", fmt.Errorf("本地整理目录尚未配置")
		}
		var err error
		root, err = filepath.Abs(cfg.Tidy.DistDir)
		if err != nil {
			return "", err
		}
	case 2:
		if core.GlobalWebDAV == nil || core.GlobalWebDAV.Config == nil || core.GlobalWebDAV.Client == nil {
			return "", fmt.Errorf("WebDAV 尚未初始化")
		}
		dav := core.GlobalWebDAV.Config
		root = dav.WebDAVUrl + "\n" + dav.WebDAVDir + "\n" + dav.WebDAVUser
	default:
		return "", fmt.Errorf("整理模式不可用")
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s", cfg.Tidy.Mode, root)))), nil
}

func audioFileVersion(file web.FileEntry) audioquality.FileVersion {
	return audioquality.FileVersion{Path: file.Path, Size: file.Size, Mod: file.ModTime.UnixNano()}
}

func newAudioCheckScanState(ctx context.Context, cfg *config.Config, store *audioquality.Store, library string, user int64, fingerprint bool) (*audioCheckScanState, error) {
	files, limited, err := web.ListAllLibraryAudio(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cached, err := store.Results(ctx, library, user)
	if err != nil {
		return nil, fmt.Errorf("无法读取体检记录: %w", err)
	}
	// Surface known issues and retry due files before walking healthy cache hits.
	now := time.Now()
	priority := func(file web.FileEntry) int {
		item, exists := cached[file.Path]
		switch {
		// A previous transfer failure is retried first: it was never judged.
		case exists && item.ReadFailed:
			return 0
		case !exists || item.FileVersion != audioFileVersion(file) || (item.Unreadable && !now.Before(item.RetryAt)):
			return 1
		case !item.Unreadable && !item.Reviewed && item.NeedsReview():
			return 0
		default:
			return 2
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if a, b := priority(files[i]), priority(files[j]); a != b {
			return a < b
		}
		return files[i].Path < files[j].Path
	})
	state := &audioCheckScanState{
		files: files, cached: cached,
		report: audioCheckReport{total: len(files), remaining: len(files), limited: limited},
	}
	if fingerprint {
		reviews, err := store.DuplicateReviews(ctx, library, user)
		if err != nil {
			return nil, err
		}
		state.duplicates = newAudioCheckDuplicateIndex(reviews)
	}
	return state, nil
}

// scanNext stops at the first actionable finding. The cursor and lookup index
// live with the card, while file results survive a process restart.
func (s *audioCheckScanState) scanNext(ctx context.Context, cfg *config.Config, store *audioquality.Store, fingerprints *audiodupe.Store, session audioCheckSession, progress func(audioCheckReport)) (*audioCheckFinding, error) {
	for s.pending != nil || s.cursor < len(s.files) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if pending := s.pending; pending != nil {
			if !s.duplicates.trashed[audioFileVersion(pending.file)] {
				if match := s.duplicates.match(pending.file, pending.fp, session.skippedDuplicates); match != nil {
					return &audioCheckFinding{file: pending.file, Result: pending.result, duplicate: match}, nil
				}
				s.duplicates.add(pending.file, pending.result, pending.fp)
			}
			s.pending = nil
		}
		if s.cursor == len(s.files) {
			break
		}
		file := s.files[s.cursor]
		item, fp, err := s.checkFile(ctx, cfg, store, fingerprints, session.library, file)
		if err != nil {
			return nil, err
		}
		_, skipped := session.skipped[item.FileVersion]
		trashed := s.duplicates != nil && s.duplicates.trashed[item.FileVersion]
		if !skipped && !trashed && !item.Unreadable && !item.Reviewed && !item.NeedsReview() &&
			!item.RetryAt.IsZero() && !time.Now().Before(item.RetryAt) {
			web.EnsureAgentRuntime(cfg)
			item.Reference = audioCheckSourceDuration(ctx, item.Result)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			item.RetryAt = time.Time{}
			if item.Reference == 0 {
				item.RetryAt = time.Now().Add(6 * time.Hour)
			}
			if err := store.Save(ctx, session.library, item); err != nil {
				return nil, err
			}
			s.cached[file.Path] = item
		}
		s.cursor++
		s.report.remaining--
		if item.ReadFailed {
			// A fetch failure is not a damaged song. Counting it as deferred
			// keeps it out of the "暂时没读到" total, which is reserved for
			// files that were fetched but could not be decoded.
			s.report.deferred++
			progress(s.report)
			continue
		}
		if item.Unreadable {
			s.report.unreadable++
			progress(s.report)
			continue
		}
		s.report.checked++
		if item.Reference == 0 && !item.Damaged {
			s.report.withoutReference++
		}
		if s.duplicates != nil && !trashed {
			if audiodupe.LowInformation(fp) {
				s.report.unfingerprinted++
			} else {
				s.pending = &audioCheckFingerprint{file: file, result: item.Result, fp: fp}
			}
		}
		if !skipped && !trashed && !item.Reviewed && item.NeedsReview() {
			return &audioCheckFinding{file: file, Result: item.Result}, nil
		}
		progress(s.report)
	}

	return nil, nil
}

func (s *audioCheckScanState) checkFile(ctx context.Context, cfg *config.Config, store *audioquality.Store, fingerprints *audiodupe.Store, library string, file web.FileEntry) (audioquality.CachedResult, audiodupe.Fingerprint, error) {
	version := audioFileVersion(file)
	item, known := s.cached[file.Path]
	known = known && item.FileVersion == version
	// A transfer failure is not a verdict on the audio: always retry it.
	if known && item.ReadFailed {
		known = false
	}
	if known && item.Unreadable && time.Now().Before(item.RetryAt) {
		return item, audiodupe.Fingerprint{}, nil
	}
	item.FileVersion = version
	inspect := !known || item.Unreadable
	var fp audiodupe.Fingerprint
	fingerprint := s.duplicates != nil && !s.duplicates.trashed[version] && (inspect || !item.Damaged || item.TailGlitch())
	if fingerprint {
		var hit bool
		var err error
		fp, hit, err = fingerprints.LoadFingerprint(ctx, file.Path, version.Size, version.Mod)
		if err != nil {
			return item, fp, err
		}
		fingerprint = !hit
	}
	if !inspect && !fingerprint {
		return item, fp, nil
	}
	if file.Size > web.MaxLibraryAudioBytes && cfg.Tidy.Mode == 2 {
		if inspect {
			item = audioquality.CachedResult{FileVersion: version, Result: audioquality.Result{Unreadable: true}}
		}
		return item, fp, nil
	}

	fileCtx, cancel := context.WithTimeout(ctx, audioCheckFileTimeout)
	defer cancel()
	local, cleanup, readErr := web.MaterializeLibraryAudio(fileCtx, cfg, file)
	if cleanup != nil {
		defer cleanup()
	}
	// A fetch failure says nothing about the audio. Report it as a transfer
	// problem so the file is retried on the next scan instead of being
	// remembered as damaged for an hour.
	transferFailed := readErr != nil
	if readErr == nil && inspect {
		item, readErr = inspectAudioCheckFile(fileCtx, local, version)
	}
	if readErr == nil && fingerprint && (!item.Damaged || item.TailGlitch()) {
		fp, _ = audiodupe.FingerprintFile(fileCtx, local)
	}
	if readErr == nil {
		// A slow fingerprint should not invalidate a successful quality check.
		versionCtx, cancelVersion := context.WithTimeout(ctx, 8*time.Second)
		_, readErr = web.CurrentLibraryAudioPathContext(versionCtx, cfg, file)
		cancelVersion()
	}
	if err := ctx.Err(); err != nil {
		return item, fp, err
	}
	if transferFailed {
		fp = audiodupe.Fingerprint{}
		if inspect {
			// Keep the row so a later scan can reuse it, but mark it as a
			// transfer failure and drop the retry gate: the very next scan
			// must try again as soon as the tunnel is back.
			item = audioquality.CachedResult{FileVersion: version, ReadFailed: true}
			if err := store.Save(ctx, library, item); err != nil {
				return item, fp, err
			}
			s.cached[file.Path] = item
		}
		return item, fp, nil
	}
	if readErr != nil {
		fp = audiodupe.Fingerprint{}
		if inspect {
			item = audioquality.CachedResult{FileVersion: version, Result: audioquality.Result{Unreadable: true}, RetryAt: time.Now().Add(time.Hour)}
		}
	}
	if inspect {
		if err := store.Save(ctx, library, item); err != nil {
			return item, fp, err
		}
		s.cached[file.Path] = item
	}
	if fingerprint && len(fp.Items) > 0 {
		if err := fingerprints.SaveFingerprint(ctx, file.Path, version.Size, version.Mod, fp); err != nil {
			return item, fp, err
		}
	}
	return item, fp, nil
}

func cachedAudioCheckReport(ctx context.Context, store *audioquality.Store, library string, user int64) (audioCheckReport, error) {
	cached, err := store.Results(ctx, library, user)
	if err != nil {
		return audioCheckReport{}, err
	}
	report := audioCheckReport{total: len(cached), resumeAvailable: true}
	for name, item := range cached {
		if item.ReadFailed {
			// Not counted as checked; the next scan retries the transfer.
			report.deferred++
			continue
		}
		if item.Unreadable {
			if !item.RetryAt.IsZero() && !time.Now().Before(item.RetryAt) {
				report.remaining++
			} else {
				report.unreadable++
			}
			continue
		}
		report.checked++
		if !item.Damaged && item.Reference == 0 {
			report.withoutReference++
		}
		if !item.Reviewed && item.NeedsReview() {
			filePath := filepath.ToSlash(name)
			file := web.FileEntry{
				Name: path.Base(filePath), Path: filePath, Size: item.Size,
				ModTime: time.Unix(0, item.Mod), Ext: strings.ToLower(filepath.Ext(filePath)),
				Artist: item.Artist, Album: item.Album,
			}
			report.findings = append(report.findings, audioCheckFinding{file: file, Result: item.Result})
		}
	}
	sort.SliceStable(report.findings, func(i, j int) bool {
		left, right := report.findings[i], report.findings[j]
		if left.Damaged != right.Damaged {
			return left.Damaged
		}
		return left.ShorterThanSource() && !right.ShorterThanSource()
	})
	return report, nil
}

func inspectAudioCheckFile(ctx context.Context, local string, version audioquality.FileVersion) (audioquality.CachedResult, error) {
	result, err := audioquality.Inspect(ctx, local)
	if err != nil {
		return audioquality.CachedResult{}, err
	}
	result.Source, _ = audioCheckSource(result.Source)
	item := audioquality.CachedResult{FileVersion: version, Result: result}
	if !result.Damaged && result.Duration > 0 && result.Source != "" && result.Title != "" && result.Artist != "" {
		item.RetryAt = time.Now()
	}
	return item, nil
}

// Keep only a public provider identity from the file's comment. The source is
// never fetched directly. Search calls go through the existing provider adapters.
func audioCheckSource(comment string) (string, string) {
	raw := playlistAssignmentSourceURL(comment)
	link, err := url.Parse(raw)
	if err != nil || link.User != nil {
		return "", ""
	}
	switch strings.ToLower(link.Hostname()) {
	case "music.163.com", "www.music.163.com":
		if id := musicdedupe.IdentityFromLink(processor.LinkNetEase, raw).PlatformTrackID; id != "" {
			return "https://music.163.com/song?id=" + url.QueryEscape(id), "netease"
		}
	case "y.qq.com":
		if id := musicdedupe.IdentityFromLink(processor.LinkQQMusic, raw).PlatformTrackID; id != "" {
			return "https://y.qq.com/n/ryqq/songDetail/" + url.PathEscape(id), "qq"
		}
	case "www.youtube.com", "youtube.com", "youtu.be", "music.youtube.com":
		if id, ok := strings.CutPrefix(musicdedupe.CanonicalURL(raw), "youtube:"); ok && id != "" {
			return "https://music.youtube.com/watch?v=" + url.QueryEscape(id), "youtube"
		}
	}
	return "", ""
}

func audioCheckSourceDuration(ctx context.Context, local audioquality.Result) float64 {
	source, platform := audioCheckSource(local.Source)
	if source == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	items, err := web.SearchAudioReference(ctx, local.Artist+" "+local.Title, platform)
	if err != nil {
		return 0
	}
	return matchingAudioSourceDuration(source, local, items)
}

func matchingAudioSourceDuration(source string, local audioquality.Result, items []web.SearchResultItem) float64 {
	for _, item := range items {
		itemSource, _ := audioCheckSource(item.URL)
		if itemSource != source || item.DurationSec <= 0 ||
			discovery.Normalize(item.Name) != discovery.Normalize(local.Title) ||
			discovery.Normalize(item.Artists) != discovery.Normalize(local.Artist) {
			continue
		}
		if local.Album != "" && item.Album != "" && discovery.Normalize(local.Album) != discovery.Normalize(item.Album) {
			continue
		}
		return float64(item.DurationSec)
	}
	return 0
}
