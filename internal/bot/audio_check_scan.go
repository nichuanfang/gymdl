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
	"github.com/nichuanfang/gymdl/internal/audioquality"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/musicdedupe"
	"github.com/nichuanfang/gymdl/processor"
)

const (
	audioCheckBatchFiles = 24
	audioCheckBatchBytes = 192 << 20
	audioCheckReferences = 4
)

type audioCheckFinding struct {
	file web.FileEntry
	audioquality.Result
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
	limited          bool
	resumeAvailable  bool
}

type audioCheckScanState struct {
	files   []web.FileEntry
	limited bool
	remote  bool
	deep    bool
	indexed map[string][]discovery.NavidromeAudio
	cached  map[string]audioquality.CachedResult
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

func scanAudioCheckBatch(ctx context.Context, cfg *config.Config, store *audioquality.Store, library string, user int64, deep bool) (audioCheckReport, error) {
	state, err := newAudioCheckScanState(ctx, cfg, store, library, user, deep)
	if err != nil {
		return audioCheckReport{}, err
	}
	return state.scanBatch(ctx, cfg, store, library, deep)
}

func newAudioCheckScanState(ctx context.Context, cfg *config.Config, store *audioquality.Store, library string, user int64, deep bool) (*audioCheckScanState, error) {
	return newAudioCheckScanStateMode(ctx, cfg, store, library, user, deep, false)
}

func newFullAudioCheckScanState(ctx context.Context, cfg *config.Config, store *audioquality.Store, library string, user int64) (*audioCheckScanState, error) {
	return newAudioCheckScanStateMode(ctx, cfg, store, library, user, true, true)
}

func newAudioCheckScanStateMode(ctx context.Context, cfg *config.Config, store *audioquality.Store, library string, user int64, deep, full bool) (*audioCheckScanState, error) {
	var files []web.FileEntry
	var limited bool
	var err error
	if full {
		files, limited, err = web.ListAllLibraryAudio(ctx, cfg)
	} else {
		files, limited, err = web.ListLibraryAudio(ctx, cfg)
	}
	if err != nil {
		return nil, err
	}
	cached, err := store.Results(ctx, library, user)
	if err != nil {
		return nil, fmt.Errorf("无法读取体检记录: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	remote := cfg.Tidy.Mode == 2
	state := &audioCheckScanState{files: files, limited: limited, remote: remote, deep: deep, cached: cached}
	var indexed map[string][]discovery.NavidromeAudio
	if remote && !deep {
		// Share the existing Navidrome inventory wire format. If inventory
		// is unavailable, still inspect files under the ordinary batch budget.
		indexCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		songs, _ := discovery.ReadNavidromeAudio(indexCtx, cfg.PlaylistOrganizer, nil)
		cancel()
		indexed = make(map[string][]discovery.NavidromeAudio, len(songs))
		for _, song := range songs {
			if song.Path != "" {
				name := path.Base(filepath.ToSlash(song.Path))
				indexed[name] = append(indexed[name], song)
			}
		}
	}
	state.indexed = indexed
	return state, nil
}

func (state *audioCheckScanState) scanBatch(ctx context.Context, cfg *config.Config, store *audioquality.Store, library string, deep bool) (audioCheckReport, error) {
	files, cached := state.files, state.cached
	report := audioCheckReport{total: len(files), limited: state.limited}
	now := time.Now()
	attempted := 0
	var bytes int64
	for _, file := range files {
		version := audioFileVersion(file)
		if item, ok := cached[file.Path]; ok && item.FileVersion == version && (!item.Unreadable || now.Before(item.RetryAt)) {
			continue
		}
		if state.remote && !deep && indexedAudioLooksNormal(file, state.indexed[file.Name]) {
			report.deferred++
			continue
		}
		if file.Size > web.MaxLibraryAudioBytes {
			report.unreadable++
			continue
		}
		if ctx.Err() != nil || attempted >= audioCheckBatchFiles || (state.remote && attempted > 0 && bytes+file.Size > audioCheckBatchBytes) {
			report.remaining++
			continue
		}
		attempted++
		bytes += max(file.Size, 0)
		item, err := inspectAudioCheckFile(ctx, cfg, file)
		if err != nil {
			if ctx.Err() != nil {
				report.remaining++
				continue
			}
			item = audioquality.CachedResult{FileVersion: version, Result: audioquality.Result{Unreadable: true}, RetryAt: now.Add(time.Hour)}
		}
		if err := store.Save(ctx, library, item); err != nil {
			if ctx.Err() != nil {
				report.remaining++
				break
			}
			return report, fmt.Errorf("无法保存体检记录: %w", err)
		}
		state.cached[file.Path] = item
	}

	references := 0
	for _, file := range files {
		item, ok := cached[file.Path]
		if !ok || item.FileVersion != audioFileVersion(file) {
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
		if !item.Reviewed && !item.RetryAt.IsZero() && !time.Now().Before(item.RetryAt) {
			if ctx.Err() == nil && references < audioCheckReferences {
				references++
				web.EnsureAgentRuntime(cfg)
				item.Reference = audioCheckSourceDuration(ctx, item.Result)
				item.RetryAt = time.Time{}
				if item.Reference == 0 {
					item.RetryAt = time.Now().Add(6 * time.Hour)
				}
				if ctx.Err() == nil {
					if err := store.Save(ctx, library, item); err != nil {
						return report, fmt.Errorf("无法保存时长对照: %w", err)
					}
					state.cached[file.Path] = item
				}
			} else {
				report.referenceQueue++
			}
		}
		report.checked++
		if !item.Damaged && item.Reference == 0 {
			report.withoutReference++
		}
		if !item.Reviewed && item.NeedsReview() {
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

func (state *audioCheckScanState) cachedReport() audioCheckReport {
	report := audioCheckReport{total: len(state.files), limited: state.limited}
	now := time.Now()
	for _, file := range state.files {
		item, ok := state.cached[file.Path]
		if !ok || item.FileVersion != audioFileVersion(file) {
			if state.remote && !state.deep && indexedAudioLooksNormal(file, state.indexed[file.Name]) {
				report.deferred++
				continue
			}
			if file.Size > web.MaxLibraryAudioBytes {
				report.unreadable++
				continue
			}
			report.remaining++
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
		if !item.Reviewed && !item.RetryAt.IsZero() && !now.Before(item.RetryAt) {
			report.referenceQueue++
		}
		report.checked++
		if !item.Damaged && item.Reference == 0 {
			report.withoutReference++
		}
		if !item.Reviewed && item.NeedsReview() {
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
	return report
}

func cachedAudioCheckReport(ctx context.Context, store *audioquality.Store, library string, user int64) (audioCheckReport, error) {
	cached, err := store.Results(ctx, library, user)
	if err != nil {
		return audioCheckReport{}, err
	}
	report := audioCheckReport{total: len(cached), resumeAvailable: true}
	for name, item := range cached {
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

func indexedAudioLooksNormal(file web.FileEntry, tracks []discovery.NavidromeAudio) bool {
	var hit *discovery.NavidromeAudio
	for i := range tracks {
		track := &tracks[i]
		if track.Size <= 0 || track.Size != file.Size {
			continue
		}
		indexedPath := "/" + strings.TrimLeft(filepath.ToSlash(track.Path), "/")
		if !strings.HasSuffix(indexedPath, "/"+strings.TrimLeft(file.Path, "/")) {
			continue
		}
		if hit != nil {
			return false
		}
		hit = track
	}
	if hit == nil || hit.Duration < 90 {
		return false
	}
	switch strings.ToLower(file.Ext) {
	case ".flac", ".wav":
		return true
	case ".mp3":
		return hit.BitRate >= 192
	case ".m4a", ".aac", ".ogg", ".opus", ".wma":
		return hit.BitRate >= 128
	default:
		return false
	}
}

func inspectAudioCheckFile(ctx context.Context, cfg *config.Config, file web.FileEntry) (audioquality.CachedResult, error) {
	local, cleanup, err := web.MaterializeLibraryAudio(ctx, cfg, file)
	if err != nil {
		return audioquality.CachedResult{}, err
	}
	if cleanup != nil {
		defer cleanup()
	}
	result, err := audioquality.Inspect(ctx, local)
	if err != nil {
		return audioquality.CachedResult{}, err
	}
	if _, err := web.CurrentLibraryAudioPath(cfg, file); err != nil {
		return audioquality.CachedResult{}, err
	}
	result.Source, _ = audioCheckSource(result.Source)
	item := audioquality.CachedResult{FileVersion: audioFileVersion(file), Result: result}
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
