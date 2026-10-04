package web

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/core/linkparser"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/utils"
	"github.com/studio-b12/gowebdav"
	"golang.org/x/sync/singleflight"
)

const maxLocalLibraryEntries = 20000
const duplicateMetadataCacheTTL = 20 * time.Second
const maxDuplicateMetadataCacheEntries = 512

type duplicateMatch struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Ext  string `json:"ext"`
}

type duplicateTrackInfo struct {
	Name   string `json:"name"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
	Ext    string `json:"ext"`
}

type duplicateCheckResult struct {
	Status    string              `json:"status"` // duplicate, clear, unknown, not_applicable
	Reason    string              `json:"reason,omitempty"`
	Target    *duplicateTrackInfo `json:"target,omitempty"`
	Match     *duplicateMatch     `json:"match,omitempty"`
	CheckedAt time.Time           `json:"checked_at"`
}

var duplicatePreflight = checkDuplicateCandidate

// Cache only source metadata, never the duplicate decision. A recently added
// library file must still be detected on the next download attempt.
var duplicateMetadataCache = struct {
	sync.Mutex
	items map[string]duplicateMetadataCacheItem
}{items: make(map[string]duplicateMetadataCacheItem)}
var duplicateMetadataFlights singleflight.Group

type duplicateMetadataCacheItem struct {
	info      music.SongInfo
	expiresAt time.Time
}

func checkDuplicateCandidate(cfg *config.Config, rawURL string) duplicateCheckResult {
	result := duplicateCheckResult{Status: "unknown", CheckedAt: time.Now()}
	if cfg == nil {
		result.Reason = "配置不可用，无法确认是否重复"
		return result
	}
	link, executor := linkparserParse(cfg, rawURL)
	if link == "" || executor == nil {
		result.Status = "not_applicable"
		result.Reason = "无法识别为单曲链接"
		return result
	}
	if !isSingleDownloadLink(executor.Name(), link) {
		result.Status = "not_applicable"
		result.Reason = "歌单、专辑或多曲链接不逐曲预检"
		return result
	}

	if executor.Name() != processor.LinkNetEase && executor.Name() != processor.LinkQQMusic {
		result.Reason = "该平台暂不支持可靠的目标音质元数据查重，可确认后继续下载"
		return result
	}
	target, err := resolveDuplicateSourceMetadata(cfg, executor.Name(), link)
	if err != nil {
		result.Reason = "读取歌曲下载元数据失败：" + err.Error()
		return result
	}
	if !hasDuplicateFields(target) {
		result.Reason = "歌曲名、歌手、专辑或格式不完整，无法确认是否重复"
		return result
	}
	result.Target = &duplicateTrackInfo{
		Name: target.SongName, Artist: target.SongArtists, Album: target.SongAlbum,
		Ext: normalizeAudioExt(target.FileExt),
	}

	match, status, reason := findDuplicate(cfg, target)
	result.Status, result.Match, result.Reason = status, match, reason
	return result
}

func resolveDuplicateSourceMetadata(cfg *config.Config, kind processor.LinkType, rawURL string) (*music.SongInfo, error) {
	key := string(kind) + "\x00" + rawURL
	if kind == processor.LinkQQMusic && cfg != nil && cfg.QQMusicApiConfig != nil {
		key += "\x00" + strings.TrimSpace(cfg.QQMusicApiConfig.Endpoint) + "\x00" + strings.ToLower(strings.TrimSpace(cfg.QQMusicApiConfig.VipLevel))
	}
	return loadDuplicateSourceMetadata(key, func() (*music.SongInfo, error) {
		switch kind {
		case processor.LinkNetEase:
			return music.ResolveNeteaseSingleMetadata(cfg, rawURL)
		case processor.LinkQQMusic:
			return music.ResolveQQSingleMetadata(cfg, rawURL)
		default:
			return nil, fmt.Errorf("unsupported duplicate metadata platform: %s", kind)
		}
	})
}

func loadDuplicateSourceMetadata(key string, resolver func() (*music.SongInfo, error)) (*music.SongInfo, error) {
	if info, ok := getDuplicateMetadata(key); ok {
		return info, nil
	}
	value, err, _ := duplicateMetadataFlights.Do(key, func() (any, error) {
		if info, ok := getDuplicateMetadata(key); ok {
			return info, nil
		}
		info, err := resolver()
		if err != nil {
			return nil, err
		}
		if !hasDuplicateFields(info) {
			return nil, errors.New("歌曲下载元数据不完整")
		}
		setDuplicateMetadata(key, info)
		return cloneDuplicateMetadata(info), nil
	})
	if err != nil {
		return nil, err
	}
	info, ok := value.(*music.SongInfo)
	if !ok || info == nil {
		return nil, errors.New("歌曲下载元数据为空")
	}
	return cloneDuplicateMetadata(info), nil
}

func getDuplicateMetadata(key string) (*music.SongInfo, bool) {
	duplicateMetadataCache.Lock()
	defer duplicateMetadataCache.Unlock()
	item, ok := duplicateMetadataCache.items[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(item.expiresAt) {
		delete(duplicateMetadataCache.items, key)
		return nil, false
	}
	info := item.info
	return &info, true
}

func setDuplicateMetadata(key string, info *music.SongInfo) {
	if info == nil {
		return
	}
	duplicateMetadataCache.Lock()
	defer duplicateMetadataCache.Unlock()
	if len(duplicateMetadataCache.items) >= maxDuplicateMetadataCacheEntries {
		now := time.Now()
		var earliestKey string
		var earliest time.Time
		for candidateKey, item := range duplicateMetadataCache.items {
			if !now.Before(item.expiresAt) {
				delete(duplicateMetadataCache.items, candidateKey)
				continue
			}
			if earliest.IsZero() || item.expiresAt.Before(earliest) {
				earliestKey, earliest = candidateKey, item.expiresAt
			}
		}
		if len(duplicateMetadataCache.items) >= maxDuplicateMetadataCacheEntries && earliestKey != "" {
			delete(duplicateMetadataCache.items, earliestKey)
		}
	}
	duplicateMetadataCache.items[key] = duplicateMetadataCacheItem{info: *info, expiresAt: time.Now().Add(duplicateMetadataCacheTTL)}
}

func cloneDuplicateMetadata(info *music.SongInfo) *music.SongInfo {
	if info == nil {
		return nil
	}
	copy := *info
	return &copy
}

// linkparserParse is kept small to make the preflight explicitly independent of
// task submission. It shares only the existing URL-to-processor classification.
func linkparserParse(cfg *config.Config, rawURL string) (string, processor.Processor) {
	snapshot := *cfg
	if snapshot.AdditionalConfig == nil {
		snapshot.AdditionalConfig = &config.AdditionalConfig{}
	}
	return linkparser.ParseLinkWithConfig(&snapshot, rawURL)
}

func isSingleDownloadLink(kind processor.LinkType, rawURL string) bool {
	switch kind {
	case processor.LinkNetEase:
		linkType, id := utils.ParseMusicID(rawURL)
		return linkType == 1 && id > 0
	case processor.LinkQQMusic:
		return music.IsQQMusicSingleLink(rawURL)
	case processor.LinkYoutube, processor.LinkYoutubeMusic:
		u, err := url.Parse(rawURL)
		if err != nil {
			return false
		}
		if u.Query().Get("v") != "" {
			return true
		}
		if strings.Contains(u.Hostname(), "youtu.be") {
			return strings.Trim(u.Path, "/") != ""
		}
		return strings.Contains(u.Path, "/shorts/") || strings.Contains(u.Path, "/watch/")
	case processor.LinkBilibili, processor.LinkBiliBiliMusic:
		u, err := url.Parse(rawURL)
		return err == nil && strings.Contains(u.Path, "/video/")
	case processor.LinkSpotify:
		return strings.Contains(rawURL, "/track/")
	case processor.LinkAppleMusic:
		return strings.Contains(rawURL, "/song/") || urlQueryHas(rawURL, "i")
	case processor.LinkSoundcloud:
		u, err := url.Parse(rawURL)
		if err != nil || strings.Contains(u.Path, "/sets/") {
			return false
		}
		segments := strings.Split(strings.Trim(u.Path, "/"), "/")
		return len(segments) >= 2 && len(segments) <= 3
	default:
		return false
	}
}

func urlQueryHas(rawURL, key string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Query().Get(key) != ""
}

func hasDuplicateFields(info *music.SongInfo) bool {
	return info != nil && strings.TrimSpace(info.SongName) != "" && strings.TrimSpace(info.SongArtists) != "" &&
		strings.TrimSpace(info.SongAlbum) != "" && normalizeAudioExt(info.FileExt) != ""
}

func normalizeAudioExt(ext string) string {
	normalized := strings.ToLower(strings.TrimSpace(ext))
	return strings.TrimPrefix(normalized, ".")
}

func normalizedSongInfo(info *music.SongInfo) songIdentity {
	return songIdentity{
		name:   strings.TrimSpace(info.SongName),
		artist: strings.TrimSpace(info.SongArtists),
		album:  strings.TrimSpace(info.SongAlbum),
		ext:    normalizeAudioExt(info.FileExt),
	}
}

type songIdentity struct {
	name, artist, album string
	ext                 string
}

func (expected songIdentity) matches(actual *music.SongInfo) bool {
	if actual == nil {
		return false
	}
	got := normalizedSongInfo(actual)
	return expected.name == got.name && expected.artist == got.artist && expected.album == got.album && expected.ext == got.ext
}

func findDuplicate(cfg *config.Config, target *music.SongInfo) (*duplicateMatch, string, string) {
	if cfg.Tidy == nil {
		return nil, "unknown", "整理配置不可用"
	}
	if !hasDuplicateFields(target) {
		return nil, "unknown", "歌曲元数据不完整，无法确认是否重复"
	}
	if cfg.Tidy.Mode == 2 {
		if core.GlobalWebDAV == nil || core.GlobalWebDAV.Client == nil {
			return nil, "unknown", "WebDAV 未初始化，无法确认是否重复"
		}
		return findWebDAVDuplicate(webDAVDirectory(cfg), target, core.GlobalWebDAV.Client, music.ReadDuplicateTags)
	}
	return findLocalDuplicate(cfg.Tidy.DistDir, target, music.ReadDuplicateTags)
}

func findLocalDuplicate(root string, target *music.SongInfo, readTags func(string) (*music.SongInfo, error)) (*duplicateMatch, string, string) {
	if strings.TrimSpace(root) == "" {
		return nil, "unknown", "本地整理目录未配置"
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, "unknown", "本地整理目录路径无效"
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if os.IsNotExist(err) {
		return nil, "clear", "本地整理目录尚不存在"
	}
	if err != nil {
		return nil, "unknown", "无法访问本地整理目录：" + err.Error()
	}
	return scanLocalDuplicate(realRoot, target, readTags)
}

func scanLocalDuplicate(root string, target *music.SongInfo, readTags func(string) (*music.SongInfo, error)) (*duplicateMatch, string, string) {
	expected := normalizedSongInfo(target)
	unknownReason := ""
	var matched *duplicateMatch
	visited := 0
	err := filepath.WalkDir(root, func(fullPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			unknownReason = "读取本地整理目录时遇到错误：" + walkErr.Error()
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		visited++
		if visited > maxLocalLibraryEntries {
			unknownReason = "本地整理目录文件过多，已停止查重"
			return filepath.SkipAll
		}
		if !audioExtension(entry.Name()) {
			return nil
		}
		if normalizeAudioExt(filepath.Ext(entry.Name())) != expected.ext {
			return nil
		}
		rel, err := filepath.Rel(root, fullPath)
		if err != nil {
			unknownReason = "无法验证本地文件路径：" + err.Error()
			return nil
		}
		safePath, err := localSafePath(root, filepath.ToSlash(rel))
		if err != nil {
			unknownReason = "本地文件路径校验失败：" + err.Error()
			return nil
		}
		existing, err := readTags(safePath)
		if err != nil {
			unknownReason = "读取候选音频标签失败：" + err.Error()
			return nil
		}
		if !hasDuplicateFields(existing) {
			unknownReason = "候选音频缺少完整标签，无法确认是否重复"
			return nil
		}
		if expected.matches(existing) {
			matched = &duplicateMatch{Path: filepath.ToSlash(rel), Name: entry.Name(), Ext: expected.ext}
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		unknownReason = "扫描本地整理目录失败：" + err.Error()
	}
	if matched != nil {
		return matched, "duplicate", "整理目录中存在歌手、歌曲名、专辑名和格式均一致的歌曲"
	}
	if unknownReason != "" {
		return nil, "unknown", unknownReason
	}
	return nil, "clear", "整理目录中没有四项元数据完全一致的歌曲"
}

// duplicateRemote is the narrow surface needed for WebDAV lookups, allowing
// path, missing-directory, stream, and tag-reading behavior to be tested.
type duplicateRemote interface {
	ReadDir(string) ([]os.FileInfo, error)
	ReadStream(string) (io.ReadCloser, error)
}

func findWebDAVDuplicate(baseDir string, target *music.SongInfo, client duplicateRemote, readTags func(string) (*music.SongInfo, error)) (*duplicateMatch, string, string) {
	if !hasDuplicateFields(target) {
		return nil, "unknown", "歌曲元数据不完整，无法确认是否重复"
	}
	artist := utils.SanitizeFileName(strings.TrimSpace(target.SongArtists))
	album := utils.SanitizeFileName(strings.TrimSpace(target.SongAlbum))
	if artist == "" || album == "" {
		return nil, "unknown", "歌手或专辑目录名无效，无法确认是否重复"
	}
	dir, err := remoteSafePath(baseDir, filepath.ToSlash(filepath.Join(artist, album)))
	if err != nil {
		return nil, "unknown", "WebDAV 候选目录路径校验失败：" + err.Error()
	}
	entries, err := client.ReadDir(dir)
	if err != nil {
		if gowebdav.IsErrNotFound(err) {
			return nil, "clear", "WebDAV 中尚无对应歌手/专辑目录"
		}
		return nil, "unknown", "读取 WebDAV 候选目录失败：" + err.Error()
	}
	expected := normalizedSongInfo(target)
	unknownReason := ""
	for _, entry := range entries {
		if entry.IsDir() || !audioExtension(entry.Name()) || normalizeAudioExt(filepath.Ext(entry.Name())) != expected.ext {
			continue
		}
		remotePath, err := remoteSafePath(baseDir, path.Join(artist, album, entry.Name()))
		if err != nil {
			unknownReason = "WebDAV 文件路径校验失败：" + err.Error()
			continue
		}
		reader, err := client.ReadStream(remotePath)
		if err != nil {
			unknownReason = "读取 WebDAV 候选音频失败：" + err.Error()
			continue
		}
		temp, err := os.CreateTemp("", "gymdl-duplicate-*"+filepath.Ext(entry.Name()))
		if err != nil {
			_ = reader.Close()
			unknownReason = "无法创建临时文件读取 WebDAV 标签：" + err.Error()
			continue
		}
		_, copyErr := io.Copy(temp, reader)
		closeErr := temp.Close()
		readerErr := reader.Close()
		if copyErr != nil || closeErr != nil || readerErr != nil {
			_ = os.Remove(temp.Name())
			unknownReason = "复制 WebDAV 音频以读取标签失败"
			continue
		}
		existing, tagErr := readTags(temp.Name())
		_ = os.Remove(temp.Name())
		if tagErr != nil {
			unknownReason = "读取 WebDAV 候选音频标签失败：" + tagErr.Error()
			continue
		}
		if !hasDuplicateFields(existing) {
			unknownReason = "WebDAV 候选音频缺少完整标签，无法确认是否重复"
			continue
		}
		match := &duplicateMatch{Path: path.Join(strings.Trim(baseDir, "/"), artist, album, entry.Name()), Name: entry.Name(), Ext: normalizeAudioExt(filepath.Ext(entry.Name()))}
		if expected.matches(existing) {
			return match, "duplicate", "整理目录中存在歌手、歌曲名、专辑名和格式均一致的歌曲"
		}
	}
	if unknownReason != "" {
		return nil, "unknown", unknownReason
	}
	return nil, "clear", "WebDAV 候选目录中没有四项元数据完全一致的歌曲"
}

// Ensure the concrete WebDAV client continues to satisfy the narrow adapter.
var _ duplicateRemote = (*gowebdav.Client)(nil)

func duplicateCheckConfirmation(result duplicateCheckResult) string {
	if result.Status == "duplicate" {
		if result.Match != nil {
			return fmt.Sprintf("已发现歌曲名、歌手、专辑和格式均一致的文件：%s", result.Match.Path)
		}
		return "整理目录中已存在相同歌曲"
	}
	if result.Reason != "" {
		return result.Reason
	}
	return "无法确认是否重复"
}
