package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/musicrag"
	"github.com/nichuanfang/gymdl/utils"
)

type TasteSources struct {
	Search       func(ctx context.Context, platform, artist string) ([]Track, error)
	SearchPage   func(ctx context.Context, platform, artist string, offset int) ([]Track, error)
	Similar      func(ctx context.Context, artist string) ([]string, error)
	TrackTags    func(context.Context, Track) ([]string, error)
	LibraryTags  func(context.Context) ([]musicrag.Song, error)
	HistoryOwner int64 // The configured Last.fm history represents one listener.
	Daily        func(ctx context.Context) ([]Track, error)
}

func NewTasteSources(cfg *config.Config, libraryTags func(context.Context) ([]musicrag.Song, error)) *TasteSources {
	sources := &TasteSources{
		LibraryTags: libraryTags,
		SearchPage: func(ctx context.Context, platform, artist string, offset int) ([]Track, error) {
			if platform == "qq" && (cfg.QQMusicApiConfig == nil || !cfg.QQMusicApiConfig.Enable) {
				return nil, errors.New("QQ 音乐未启用")
			}
			hits, _, err := web.SearchSinglesForAgentPage(ctx, artist, []string{platform}, offset, tasteSearchPage)
			if err != nil {
				return nil, err
			}
			tracks := make([]Track, 0, len(hits))
			for _, hit := range hits {
				tracks = append(tracks, fromSearch(hit.SearchResultItem))
			}
			return tracks, nil
		},
	}
	sources.Search = func(ctx context.Context, platform, artist string) ([]Track, error) {
		return sources.SearchPage(ctx, platform, artist, 0)
	}
	sources.Daily = func(ctx context.Context) ([]Track, error) {
		items, err := web.NeteaseDailySongs(ctx)
		if err != nil {
			return nil, err
		}
		tracks := make([]Track, 0, len(items))
		for _, item := range items {
			tracks = append(tracks, fromSearch(item))
		}
		return tracks, nil
	}
	if cfg.Lastfm == nil || strings.TrimSpace(cfg.Lastfm.APIKey) == "" {
		return sources
	}
	client := &tasteLastFM{
		endpoint: "https://ws.audioscrobbler.com/2.0/",
		apiKey:   strings.TrimSpace(cfg.Lastfm.APIKey),
		client:   utils.SharedHTTPClient(),
		cache:    make(map[string]tasteMetadata),
	}
	sources.Similar = func(ctx context.Context, artist string) ([]string, error) {
		return client.names(ctx, "artist.getSimilar", artist, "")
	}
	sources.TrackTags = func(ctx context.Context, track Track) ([]string, error) {
		artist := track.Artist
		if credits := artistSplit.Split(artist, -1); len(credits) > 0 {
			artist = strings.TrimSpace(credits[0])
		}
		return client.names(ctx, "track.getTopTags", artist, track.Title)
	}
	return sources
}

type tasteMetadata struct {
	names   []string
	expires time.Time
}

type tasteLastFM struct {
	endpoint string
	apiKey   string
	client   *http.Client
	mu       sync.Mutex
	cache    map[string]tasteMetadata
}

func (l *tasteLastFM) names(ctx context.Context, method, artist, track string) ([]string, error) {
	key := method + "\n" + Normalize(artist) + "\n" + Normalize(track)
	l.mu.Lock()
	entry, found := l.cache[key]
	l.mu.Unlock()
	if found && time.Now().Before(entry.expires) {
		return entry.names, nil
	}
	endpoint, err := url.Parse(l.endpoint)
	if err != nil {
		return nil, errors.New("Last.fm 地址不可用")
	}
	query := url.Values{"method": {method}, "artist": {artist}, "api_key": {l.apiKey}, "format": {"json"}, "autocorrect": {"1"}}
	if track != "" {
		query.Set("track", track)
	} else {
		query.Set("limit", "8")
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("Last.fm 请求不可用")
	}
	request.Header.Set("User-Agent", "gymdl/1.0 (music-recommendation)")
	response, err := l.client.Do(request)
	if err != nil {
		// A transport error can include the request URL and API key.
		return nil, errors.New("Last.fm 暂时无法连接")
	}
	defer response.Body.Close()
	type item struct {
		Name string `json:"name"`
	}
	var payload struct {
		Error   int `json:"error"`
		Similar *struct {
			Artists []item `json:"artist"`
		} `json:"similarartists"`
		Tags *struct {
			Items []item `json:"tag"`
		} `json:"toptags"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 512<<10)).Decode(&payload) != nil || payload.Error != 0 {
		return nil, errors.New("Last.fm 暂时返回不了音乐标签")
	}
	var items []item
	switch {
	case method == "artist.getSimilar" && payload.Similar != nil:
		items = payload.Similar.Artists
	case method == "track.getTopTags" && payload.Tags != nil:
		items = payload.Tags.Items
	default:
		return nil, errors.New("Last.fm 返回的音乐标签格式无效")
	}
	names := make([]string, 0, min(12, len(items)))
	for _, item := range items[:min(12, len(items))] {
		if name := strings.TrimSpace(item.Name); name != "" {
			names = append(names, name)
		}
	}
	l.mu.Lock()
	if len(l.cache) >= 2048 {
		for candidate := range l.cache {
			delete(l.cache, candidate)
			break
		}
	}
	l.cache[key] = tasteMetadata{names: names, expires: time.Now().Add(6 * time.Hour)}
	l.mu.Unlock()
	return names, nil
}
