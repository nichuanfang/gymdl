package bot

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/internal/discovery"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
)

// webStudio exposes the Telegram rediscover and AI playlist pipelines to the console.
// It keeps its own preview tokens and does not touch Telegram session maps.
// Chat id -1 is only a lock sentinel. Real Telegram chats are positive.

const webStudioChatID int64 = -1

type webStudio struct {
	app *BotApp
}

type webAIPreview struct {
	token        string
	expires      time.Time
	phase        string
	creating     bool
	playlistID   string
	targetDesc   string
	name         string
	newName      string
	description  string
	intro        string
	usedFallback bool
	songIDs      []string
	existingIDs  []string
	operationID  string
}

func (app *BotApp) registerWebStudio() {
	studio := webStudio{app: app}
	web.SetDiscoveryAPI(studio)
	web.SetAIPlaylistAPI(studio)
}

func (s webStudio) owner() (int64, error) {
	if s.app == nil || s.app.cfg == nil || s.app.cfg.Telegram == nil {
		return 0, errors.New("请先配置 Telegram 授权用户")
	}
	for _, allowed := range s.app.cfg.Telegram.AllowedUsers {
		id, err := strconv.ParseInt(strings.TrimSpace(allowed), 10, 64)
		if err == nil && id > 0 {
			return id, nil
		}
	}
	return 0, errors.New("请先配置 Telegram 授权用户")
}

func (s webStudio) Rediscover(ctx context.Context, platform string, limit int) (web.DiscoveryBatch, error) {
	user, err := s.owner()
	if err != nil {
		return web.DiscoveryBatch{}, err
	}
	if s.app.discovery == nil {
		return web.DiscoveryBatch{}, errors.New("遗珠服务尚未就绪")
	}
	batch, err := s.app.discovery.Rediscover(ctx, user, platform, limit)
	if err != nil {
		return web.DiscoveryBatch{}, err
	}
	tracks := make([]web.DiscoveryTrack, 0, len(batch.Tracks))
	for _, track := range batch.Tracks {
		tracks = append(tracks, webDiscoveryTrack(track))
	}
	return web.DiscoveryBatch{Tracks: tracks, SkippedOwned: batch.SkippedOwned, Unconfirmed: batch.Unconfirmed}, nil
}

func webDiscoveryTrack(track discovery.Track) web.DiscoveryTrack {
	return web.DiscoveryTrack{
		Platform: track.Platform, ID: track.ID, URL: track.URL, Title: track.Title,
		Artist: track.Artist, Album: track.Album, Duration: track.Duration, Verified: track.Verified,
	}
}

func studioDiscoveryTrack(track web.DiscoveryTrack) discovery.Track {
	return discovery.Track{
		Platform: track.Platform, ID: track.ID, URL: track.URL, Title: track.Title,
		Artist: track.Artist, Album: track.Album, Duration: track.Duration, Verified: track.Verified,
	}
}

func (s webStudio) Enqueue(ctx context.Context, tracks []web.DiscoveryTrack) (int, int, error) {
	user, err := s.owner()
	if err != nil {
		return 0, 0, err
	}
	if s.app.discovery == nil || s.app.discovery.Store == nil {
		return 0, 0, errors.New("遗珠服务尚未就绪")
	}
	excluded, err := s.app.discovery.Store.Excluded(ctx, user)
	if err != nil {
		return 0, 0, err
	}
	queued, skipped := 0, 0
	for _, item := range tracks {
		track := studioDiscoveryTrack(item)
		identity := discovery.Identity(track)
		if excluded[identity] {
			skipped++
			continue
		}
		if err = s.app.discovery.Store.Enqueue(ctx, user, user, track); err != nil {
			return queued, skipped, err
		}
		excluded[identity] = true
		queued++
	}
	return queued, skipped, nil
}

func (s webStudio) Dismiss(ctx context.Context, tracks []web.DiscoveryTrack) (int, error) {
	user, err := s.owner()
	if err != nil {
		return 0, err
	}
	if s.app.discovery == nil || s.app.discovery.Store == nil {
		return 0, errors.New("遗珠服务尚未就绪")
	}
	for _, track := range tracks {
		if err = s.app.discovery.Store.Preference(ctx, user, studioDiscoveryTrack(track), true); err != nil {
			return 0, err
		}
	}
	return len(tracks), nil
}

func (s webStudio) List(ctx context.Context) (web.AIPlaylistPage, error) {
	page := web.AIPlaylistPage{Presets: webAIPresets(), Playlists: []web.AIPlaylistItem{}}
	service, err := newAIPlaylistService(s.app.cfg)
	if err != nil {
		return page, err
	}
	defer service.close()
	playlists, err := service.organizer.aiPlaylists(ctx)
	if err != nil {
		return page, err
	}
	for _, playlist := range playlists {
		page.Playlists = append(page.Playlists, web.AIPlaylistItem{
			ID: playlist.ID, Name: playlist.Name, Description: playlist.Description, SongCount: playlist.SongCount,
		})
	}
	return page, nil
}

func webAIPresets() []web.AIPreset {
	presets := make([]web.AIPreset, 0, len(aiPlaylistPresets))
	for _, preset := range aiPlaylistPresets {
		presets = append(presets, web.AIPreset{ID: preset.ID, Label: preset.Label, Text: preset.Text})
	}
	return presets
}

func (s webStudio) Preview(ctx context.Context, req web.AIPreviewRequest) (web.AIPreviewPage, error) {
	playlistID := strings.TrimSpace(req.PlaylistID)
	rawName := strings.TrimSpace(req.Name)
	if playlistID != "" && rawName != "" {
		return web.AIPreviewPage{}, errors.New("请只选择已有歌单，或只填写新歌单名称")
	}
	if playlistID == "" && rawName == "" {
		return web.AIPreviewPage{}, errors.New("请选择已有歌单，或填写新歌单名称")
	}
	if playlistID != "" && !safeNavidromeID(playlistID) {
		return web.AIPreviewPage{}, errors.New("歌单编号无效")
	}
	presetID := strings.TrimSpace(req.Preset)
	presetText := ""
	if presetID != "" {
		preset, ok := findAIPlaylistPreset(presetID)
		if !ok {
			return web.AIPreviewPage{}, errors.New("未知的预设")
		}
		presetText = preset.Text
	}
	request := mergeAIPlaylistRequest(presetText, req.Request)
	if request == "" {
		return web.AIPreviewPage{}, errors.New("请写明希望生成的音乐主题")
	}
	if len([]rune(request)) > aiPlaylistRequestMaxRunes {
		return web.AIPreviewPage{}, errors.New("生成要求最多 500 个字符")
	}
	var name string
	creating := playlistID == ""
	if creating {
		normalized, err := normalizeAIPlaylistName(rawName)
		if err != nil {
			return web.AIPreviewPage{}, err
		}
		name = normalized
	}
	if !s.app.beginAIPlaylistRun(webStudioChatID) {
		return web.AIPreviewPage{}, errors.New("另一项歌单维护正在运行，请稍后重试")
	}
	defer s.app.finishAIPlaylistRun(webStudioChatID)
	service, err := newAIPlaylistService(s.app.cfg)
	if err != nil {
		return web.AIPreviewPage{}, err
	}
	defer service.close()
	target := aiPlaylist{Name: name}
	if !creating {
		playlists, err := service.organizer.aiPlaylists(ctx)
		if err != nil {
			return web.AIPreviewPage{}, err
		}
		current, ok := findAIPlaylist(playlists, playlistID)
		if !ok {
			return web.AIPreviewPage{}, errors.New("目标歌单已不存在，请刷新后重试")
		}
		target = current
		name = current.Name
	}
	draft, err := service.previewAIPlaylist(ctx, target, request, nil)
	if err != nil {
		return web.AIPreviewPage{}, err
	}
	ids := aiPlaylistDraftIDs(draft)
	preview := webPreviewSongs(draft)
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	s.saveWebPreview(webAIPreview{
		token: token, expires: time.Now().Add(aiPlaylistSessionTTL), phase: "preview", creating: creating,
		playlistID: target.ID, targetDesc: target.Description, name: name, newName: name,
		description: draft.Description, intro: draft.Plan.Intro, usedFallback: draft.UsedFallback,
		songIDs: ids, existingIDs: append([]string(nil), draft.ExistingSongIDs...), operationID: token,
	})
	return web.AIPreviewPage{
		Token: token, Name: name, Description: draft.Description, Intro: draft.Plan.Intro,
		Total: len(ids), Preview: preview, UsedFallback: draft.UsedFallback, Creating: creating,
	}, nil
}

func webPreviewSongs(draft aiPlaylistDraft) []web.AIPreviewSong {
	byID := make(map[string]aiPlaylistCandidate, len(draft.Candidates))
	for _, candidate := range draft.Candidates {
		byID[candidate.ID] = candidate
	}
	songs := make([]web.AIPreviewSong, 0, aiPlaylistPreviewLimit)
	for _, item := range draft.Plan.Playlist {
		if len(songs) >= aiPlaylistPreviewLimit {
			break
		}
		candidate := byID[item.ID]
		songs = append(songs, web.AIPreviewSong{Title: candidate.Title, Artist: candidate.Artist, Reason: item.Reason})
	}
	return songs
}

func (s webStudio) Confirm(ctx context.Context, token string) (web.AIConfirmPage, error) {
	token = strings.TrimSpace(token)
	preview, ok := s.loadWebPreview(token)
	if !ok {
		return web.AIConfirmPage{}, errors.New("预览已过期，请重新生成")
	}
	if !s.app.beginAIPlaylistRun(webStudioChatID) {
		return web.AIConfirmPage{}, errors.New("另一项歌单维护正在运行，请稍后重试")
	}
	defer s.app.finishAIPlaylistRun(webStudioChatID)
	service, err := newAIPlaylistService(s.app.cfg)
	if err != nil {
		s.touchWebPreview(token)
		return web.AIConfirmPage{}, err
	}
	defer service.close()
	var result aiPlaylistResult
	if preview.creating {
		result, err = s.confirmNewPlaylist(ctx, service, preview)
	} else {
		result, err = s.confirmExistingPlaylist(ctx, service, preview)
	}
	if err != nil {
		s.touchWebPreview(token)
		return web.AIConfirmPage{}, err
	}
	s.deleteWebPreview(token)
	return web.AIConfirmPage{
		Name: result.Name, Count: result.Count, Intro: result.Intro,
		Created: result.Created, Replaced: result.Replaced, UsedFallback: result.UsedFallback,
	}, nil
}

func (s webStudio) confirmNewPlaylist(ctx context.Context, service *aiPlaylistService, preview webAIPreview) (aiPlaylistResult, error) {
	if preview.phase == "created" && preview.playlistID != "" {
		current, err := service.organizer.getAIPlaylistByID(ctx, preview.playlistID)
		if err != nil {
			return aiPlaylistResult{}, err
		}
		playlist := aiPlaylistFromAPI(current)
		plan, err := service.resumeStore.Load(ctx, preview.playlistID)
		if err != nil {
			return aiPlaylistResult{}, err
		}
		if plan == nil {
			return aiPlaylistResult{}, errors.New("没有找到可续写的计划，请重新生成预览")
		}
		result, err := service.processAIPlaylist(ctx, playlist, plan, false)
		if err != nil {
			return aiPlaylistResult{}, err
		}
		result.Created = true
		return result, nil
	}
	intent := aiPlaylistCreateIntent{
		OperationID: preview.operationID, Name: preview.newName, Description: preview.description,
		Marker: "gymdl-ai-playlist-create:" + preview.operationID, SongIDs: append([]string(nil), preview.songIDs...),
		Intro: preview.intro, UsedFallback: preview.usedFallback,
	}
	playlist, plan, err := service.finishAIPlaylistCreation(ctx, intent)
	if err != nil {
		return aiPlaylistResult{}, err
	}
	preview.phase = "created"
	preview.playlistID = playlist.ID
	preview.creating = true
	s.saveWebPreview(preview)
	result, err := service.processAIPlaylist(ctx, playlist, &plan, false)
	if err != nil {
		return aiPlaylistResult{}, err
	}
	result.Created = true
	return result, nil
}

func (s webStudio) confirmExistingPlaylist(ctx context.Context, service *aiPlaylistService, preview webAIPreview) (aiPlaylistResult, error) {
	if preview.phase == "replace" && preview.playlistID != "" {
		current, err := service.organizer.getAIPlaylistByID(ctx, preview.playlistID)
		if err != nil {
			return aiPlaylistResult{}, err
		}
		playlist := aiPlaylistFromAPI(current)
		plan, err := service.resumeStore.Load(ctx, preview.playlistID)
		if err != nil {
			return aiPlaylistResult{}, err
		}
		if plan == nil || !plan.Replace {
			return aiPlaylistResult{}, errors.New("没有找到可续写的计划，请重新生成预览")
		}
		return service.replaceAIPlaylist(ctx, playlist, *plan)
	}
	live, err := service.organizer.aiPlaylists(ctx)
	if err != nil {
		return aiPlaylistResult{}, err
	}
	current, ok := findAIPlaylist(live, preview.playlistID)
	if !ok {
		return aiPlaylistResult{}, errors.New("目标歌单已不存在，请重新生成预览")
	}
	if current.Description != preview.targetDesc {
		return aiPlaylistResult{}, errors.New("目标歌单备注在预览后发生变化，请重新生成预览")
	}
	if len(preview.songIDs) == 0 {
		return aiPlaylistResult{}, errors.New("预览里没有可写入的歌曲，请重新生成")
	}
	snapshot, err := service.readAIPlaylistTracks(ctx, current)
	if err != nil {
		return aiPlaylistResult{}, err
	}
	if !samePlaylistTrackOrder(snapshot.IDs, preview.existingIDs) {
		return aiPlaylistResult{}, errors.New("歌单曲目在预览后发生变化，本次未替换；请重新生成预览")
	}
	missing, err := service.organizer.missingSongIDs(ctx, preview.songIDs)
	if err != nil {
		return aiPlaylistResult{}, err
	}
	if len(missing) > 0 {
		return aiPlaylistResult{}, errors.New("部分候选歌曲已不在曲库中，请重新生成预览")
	}
	plan := aiPlaylistResumePlan{
		PlaylistID: current.ID, Description: current.Description, SongIDs: append([]string(nil), preview.songIDs...),
		Intro: preview.intro, UsedFallback: preview.usedFallback, Replace: true,
		OriginalSongIDs: append([]string(nil), preview.existingIDs...),
	}
	if err = service.resumeStore.Create(ctx, plan); err != nil {
		return aiPlaylistResult{}, err
	}
	preview.phase = "replace"
	s.saveWebPreview(preview)
	return service.replaceAIPlaylist(ctx, current, plan)
}

func (s webStudio) saveWebPreview(preview webAIPreview) {
	if preview.expires.IsZero() {
		preview.expires = time.Now().Add(aiPlaylistSessionTTL)
	}
	s.app.webAIPreviewMu.Lock()
	defer s.app.webAIPreviewMu.Unlock()
	if s.app.webAIPreviews == nil {
		s.app.webAIPreviews = map[string]webAIPreview{}
	}
	now := time.Now()
	for token, item := range s.app.webAIPreviews {
		if now.After(item.expires) {
			delete(s.app.webAIPreviews, token)
		}
	}
	s.app.webAIPreviews[preview.token] = preview
	for len(s.app.webAIPreviews) > 8 {
		var oldest string
		var oldestAt time.Time
		for token, item := range s.app.webAIPreviews {
			if oldest == "" || item.expires.Before(oldestAt) {
				oldest = token
				oldestAt = item.expires
			}
		}
		if oldest == "" || oldest == preview.token {
			break
		}
		delete(s.app.webAIPreviews, oldest)
	}
}

func (s webStudio) loadWebPreview(token string) (webAIPreview, bool) {
	s.app.webAIPreviewMu.Lock()
	defer s.app.webAIPreviewMu.Unlock()
	preview, ok := s.app.webAIPreviews[token]
	if !ok || time.Now().After(preview.expires) {
		delete(s.app.webAIPreviews, token)
		return webAIPreview{}, false
	}
	return preview, true
}

func (s webStudio) touchWebPreview(token string) {
	s.app.webAIPreviewMu.Lock()
	defer s.app.webAIPreviewMu.Unlock()
	preview, ok := s.app.webAIPreviews[token]
	if !ok {
		return
	}
	preview.expires = time.Now().Add(aiPlaylistSessionTTL)
	s.app.webAIPreviews[token] = preview
}

func (s webStudio) deleteWebPreview(token string) {
	s.app.webAIPreviewMu.Lock()
	defer s.app.webAIPreviewMu.Unlock()
	delete(s.app.webAIPreviews, token)
}
