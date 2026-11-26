package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/audiodupe"
	"github.com/nichuanfang/gymdl/internal/gin/response"
	"github.com/nichuanfang/gymdl/internal/musicrag"
	"github.com/nichuanfang/gymdl/internal/storage"
)

type duplicateKeepRequest struct {
	Keep   string   `json:"keep"`
	Remove []string `json:"remove"`
}

type libraryPruner struct {
	lookup          func(context.Context, string) (string, error)
	dropVector      func(context.Context, string) error
	dropRows        func(context.Context, []string) error
	dropFile        func(string) error
	dropFingerprint func(context.Context, string) error
}

func HandleKeepLibraryDuplicate(c *gin.Context) {
	var req duplicateKeepRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, "参数错误")
		return
	}
	libraryDuplicates.mu.Lock()
	running := libraryDuplicates.running
	report := libraryDuplicates.report
	libraryDuplicates.mu.Unlock()
	if _, err := planDuplicateRemoval(report, running, req.Keep, req.Remove); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errDuplicateScanRunning) {
			status = http.StatusConflict
		}
		response.Fail(c, status, err.Error())
		return
	}
	cfg := GetWebConfig()
	pruner, closeStore, err := newLibraryPruner(cfg)
	if err != nil {
		response.Fail(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer closeStore()
	removed := cleanDuplicatePaths(req.Remove)
	done, err := pruner.apply(c.Request.Context(), removed)
	if len(done) > 0 {
		rememberRemovedDuplicates(done)
	}
	if err != nil {
		response.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	response.Success(c, gin.H{"kept": cleanDuplicatePath(req.Keep), "removed": done})
}

func newLibraryPruner(cfg *config.Config) (libraryPruner, func(), error) {
	noop := func() {}
	if cfg == nil || cfg.AIPlaylist == nil || strings.TrimSpace(cfg.AIPlaylist.PineconeHost) == "" || strings.TrimSpace(cfg.AIPlaylist.PineconeAPIKey) == "" {
		return libraryPruner{}, noop, errors.New("向量库还没配好，没有删除")
	}
	if cfg.PlaylistOrganizer == nil || strings.TrimSpace(cfg.PlaylistOrganizer.NavidromeURL) == "" || strings.TrimSpace(cfg.PlaylistOrganizer.NavidromeUsername) == "" || cfg.PlaylistOrganizer.NavidromePassword == "" {
		return libraryPruner{}, noop, errors.New("曲库编号还对不上，没有删除")
	}
	rows, err := musicrag.Open(storage.DefaultDatabasePath)
	if err != nil {
		return libraryPruner{}, noop, errors.New("歌曲资料库打不开，没有删除")
	}
	pinecone := musicrag.Pinecone{
		Host:      strings.TrimSpace(cfg.AIPlaylist.PineconeHost),
		APIKey:    strings.TrimSpace(cfg.AIPlaylist.PineconeAPIKey),
		Namespace: strings.TrimSpace(cfg.AIPlaylist.PineconeNamespace),
		Client:    &http.Client{Timeout: 20 * time.Second},
	}
	closeStore := func() { _ = rows.Close() }
	return libraryPruner{
		lookup: func(ctx context.Context, path string) (string, error) {
			return LookupNavidromeAudioID(ctx, cfg.PlaylistOrganizer, path)
		},
		dropVector: pinecone.Delete,
		dropRows:   rows.Delete,
		dropFile: func(path string) error {
			return removeLibraryAudio(cfg, path)
		},
		dropFingerprint: func(ctx context.Context, path string) error {
			store, err := audiodupe.Open(storage.DefaultDatabasePath)
			if err != nil {
				return err
			}
			defer store.Close()
			return store.DeleteFingerprint(ctx, path)
		},
	}, closeStore, nil
}

func (p libraryPruner) apply(ctx context.Context, paths []string) ([]string, error) {
	ids := make([]string, len(paths))
	for i, path := range paths {
		id, err := p.lookup(ctx, path)
		if err != nil {
			return nil, err
		}
		if !musicrag.SafeID(id) {
			return nil, fmt.Errorf("曲库编号无效，没有删除")
		}
		ids[i] = id
	}
	for _, id := range ids {
		if err := p.dropVector(ctx, id); err != nil {
			return nil, fmt.Errorf("向量没有清完，文件还在：%w", err)
		}
	}
	if err := p.dropRows(ctx, ids); err != nil {
		return nil, fmt.Errorf("曲库记录没有清完，文件还在：%w", err)
	}
	done := make([]string, 0, len(paths))
	for _, path := range paths {
		if err := p.dropFile(path); err != nil {
			return done, fmt.Errorf("记录和向量已清理，文件还在：%s", filepathBase(path))
		}
		done = append(done, path)
		if p.dropFingerprint != nil {
			_ = p.dropFingerprint(ctx, path)
		}
	}
	return done, nil
}

var errDuplicateScanRunning = errors.New("查重还在进行，先等它结束")

func planDuplicateRemoval(report audiodupe.Report, running bool, keep string, remove []string) (audiodupe.Group, error) {
	if running {
		return audiodupe.Group{}, errDuplicateScanRunning
	}
	if report.Status != "done" {
		return audiodupe.Group{}, errors.New("先完成一次查重，再决定留下哪一版")
	}
	keep = cleanDuplicatePath(keep)
	remove = cleanDuplicatePaths(remove)
	if keep == "" || len(remove) == 0 {
		return audiodupe.Group{}, errors.New("先选定要留下的版本，并勾选要删除的版本")
	}
	for _, path := range remove {
		if path == keep {
			return audiodupe.Group{}, errors.New("要留下的版本不能同时删除")
		}
	}
	var found *audiodupe.Group
	for i := range report.Groups {
		if !groupHasPath(report.Groups[i], keep) {
			continue
		}
		if found != nil {
			return audiodupe.Group{}, errors.New("这首出现在多组里，请重新查重")
		}
		group := report.Groups[i]
		found = &group
	}
	if found == nil {
		return audiodupe.Group{}, errors.New("要留下的版本不在这次查重结果里")
	}
	// Historical reports could predate lyric evidence. Never delete from a
	// group that mixes a vocal and an instrumental; re-scan instead.
	if found.HasLyricConflict() {
		return audiodupe.Group{}, errors.New("这组里有一首有歌词、一首是纯音乐，需要人工核对，请重新查重")
	}
	for _, group := range report.LyricConflicts {
		if groupHasPath(group, keep) {
			return audiodupe.Group{}, errors.New("这组属于歌词冲突核对项，不能批量删歌")
		}
	}
	for _, path := range remove {
		if !groupHasPath(*found, path) {
			return audiodupe.Group{}, errors.New("要删除的文件不在这一组里")
		}
	}
	if len(found.Tracks)-len(remove) < 1 {
		return audiodupe.Group{}, errors.New("至少要留下一首")
	}
	return *found, nil
}

func groupHasPath(group audiodupe.Group, path string) bool {
	for _, track := range group.Tracks {
		if cleanDuplicatePath(track.Path) == path {
			return true
		}
	}
	return false
}

func cleanDuplicatePath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	path = strings.Trim(path, "/")
	if path == "" || path == "." || strings.Contains(path, "..") {
		return ""
	}
	return path
}

func cleanDuplicatePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		path = cleanDuplicatePath(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

func rememberRemovedDuplicates(paths []string) {
	libraryDuplicates.mu.Lock()
	if libraryDuplicates.running {
		libraryDuplicates.mu.Unlock()
		return
	}
	report := dropRemovedDuplicateTracks(libraryDuplicates.report, paths)
	libraryDuplicates.report = report
	libraryDuplicates.mu.Unlock()
	persistDuplicateReport(report)
}

func dropRemovedDuplicateTracks(report audiodupe.Report, removed []string) audiodupe.Report {
	gone := map[string]struct{}{}
	for _, path := range removed {
		gone[cleanDuplicatePath(path)] = struct{}{}
	}
	groups := make([]audiodupe.Group, 0, len(report.Groups))
	for _, group := range report.Groups {
		kept := make([]audiodupe.Track, 0, len(group.Tracks))
		for _, track := range group.Tracks {
			if _, ok := gone[cleanDuplicatePath(track.Path)]; ok {
				continue
			}
			kept = append(kept, track)
		}
		if len(kept) < 2 {
			continue
		}
		group.Tracks = kept
		groups = append(groups, group)
	}
	if groups == nil {
		groups = []audiodupe.Group{}
	}
	report.Groups = groups
	conflicts := make([]audiodupe.Group, 0, len(report.LyricConflicts))
	for _, group := range report.LyricConflicts {
		kept := make([]audiodupe.Track, 0, len(group.Tracks))
		for _, track := range group.Tracks {
			if _, ok := gone[cleanDuplicatePath(track.Path)]; ok {
				continue
			}
			kept = append(kept, track)
		}
		if len(kept) < 2 {
			continue
		}
		group.Tracks = kept
		conflicts = append(conflicts, group)
	}
	report.LyricConflicts = conflicts
	report.LyricConflictCount = len(conflicts)
	report.UpdatedAt = time.Now().UTC()
	return report
}

// LookupNavidromeAudioID resolves an unambiguous Navidrome song by its library path.
func LookupNavidromeAudioID(ctx context.Context, cfg *config.PlaylistOrganizerConfig, relPath string) (string, error) {
	if cfg == nil {
		return "", errors.New("Navidrome 尚未配置")
	}
	relPath = cleanDuplicatePath(relPath)
	if relPath == "" {
		return "", errors.New("文件路径无效，没有删除")
	}
	token, err := navidromeToken(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("曲库编号还对不上，没有删除")
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.NavidromeURL), "/")
	endpoint, err := url.Parse(base + "/api/song")
	if err != nil {
		return "", errors.New("曲库编号还对不上，没有删除")
	}
	query := endpoint.Query()
	query.Set("_start", "0")
	query.Set("_end", "30")
	query.Set("path", relPath)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", errors.New("曲库编号还对不上，没有删除")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Nd-Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("曲库编号还对不上，没有删除")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.New("曲库编号还对不上，没有删除")
	}
	return chooseNavidromeSongID(parseNavidromeSongs(body), relPath)
}

func chooseNavidromeSongID(songs []navidromeSongRef, relPath string) (string, error) {
	want := cleanDuplicatePath(relPath)
	if want == "" {
		return "", errors.New("文件路径无效，没有删除")
	}
	var exact, suffix []string
	seenExact := map[string]struct{}{}
	seenSuffix := map[string]struct{}{}
	for _, song := range songs {
		if !musicrag.SafeID(song.ID) {
			continue
		}
		stored := cleanDuplicatePath(song.Path)
		if stored == "" {
			continue
		}
		if stored == want {
			if _, ok := seenExact[song.ID]; ok {
				continue
			}
			seenExact[song.ID] = struct{}{}
			exact = append(exact, song.ID)
			continue
		}
		if strings.HasSuffix(stored, "/"+want) {
			if _, ok := seenSuffix[song.ID]; ok {
				continue
			}
			seenSuffix[song.ID] = struct{}{}
			suffix = append(suffix, song.ID)
		}
	}
	matched := exact
	if len(matched) == 0 {
		matched = suffix
	}
	if len(matched) != 1 {
		return "", fmt.Errorf("曲库索引里对不上「%s」，没有删除", filepathBase(want))
	}
	return matched[0], nil
}

type navidromeSongRef struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

func parseNavidromeSongs(body []byte) []navidromeSongRef {
	var songs []navidromeSongRef
	if json.Unmarshal(body, &songs) == nil && songs != nil {
		return songs
	}
	var envelope struct {
		Data []navidromeSongRef `json:"data"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		return envelope.Data
	}
	return nil
}

func navidromePathMatches(stored, want string) bool {
	stored = cleanDuplicatePath(stored)
	want = cleanDuplicatePath(want)
	return stored != "" && (stored == want || strings.HasSuffix(stored, "/"+want))
}

func navidromeToken(ctx context.Context, cfg *config.PlaylistOrganizerConfig) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.NavidromeURL), "/")
	body, err := json.Marshal(map[string]string{"username": cfg.NavidromeUsername, "password": cfg.NavidromePassword})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/auth/login", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(raw, &parsed) != nil || strings.TrimSpace(parsed.Token) == "" {
		return "", errors.New("缺少 token")
	}
	return parsed.Token, nil
}

func filepathBase(path string) string {
	path = cleanDuplicatePath(path)
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	if path == "" {
		return "这首"
	}
	return path
}
