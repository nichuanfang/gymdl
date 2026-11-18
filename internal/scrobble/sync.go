// Package scrobble copies new Last.fm listens into the existing
// music_scrobble_records table. Listening reports and rediscover keep reading
// that same table.
package scrobble

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const pageSize = 200

// fetchURL is the Last.fm endpoint. Tests point it at a local server.
var fetchURL = "https://ws.audioscrobbler.com/2.0/"

// Track is one finished listen. A track without a date is still playing and is skipped.
type Track struct {
	Name   string
	Artist string
	Album  string
	Image  string
	MBID   string
	URL    string
	Date   string
}

// Store writes a listen only when it is not already present.
type Store interface {
	Latest(ctx context.Context) (time.Time, bool, error)
	InsertIfNew(ctx context.Context, track Track) (bool, error)
}

// ShouldInsert is true when the URL has not been stored or this title, artist
// and time have not been stored. An empty URL is decided by the title tuple.
func ShouldInsert(urlSeen, tupleSeen bool, trackURL string) bool {
	if trackURL == "" {
		return !tupleSeen
	}
	return !urlSeen || !tupleSeen
}

// Sync reads recent tracks since the latest stored listen and inserts the new
// ones. One bad row does not stop the rest of the page. A failed page stops
// the run so a later page is not mistaken for a complete sync.
func Sync(ctx context.Context, client *http.Client, user, apiKey string, since time.Time, store Store) (int, error) {
	if client == nil {
		client = http.DefaultClient
	}
	inserted := 0
	var first error
	page := 1
	pages := 1
	for page <= pages {
		if err := ctx.Err(); err != nil {
			return inserted, err
		}
		body, err := fetchPage(ctx, client, user, apiKey, since, page)
		if err != nil {
			return inserted, err
		}
		tracks, totalPages, err := ParseRecent(body)
		if err != nil {
			return inserted, err
		}
		if totalPages > pages {
			pages = totalPages
		}
		for _, track := range tracks {
			if track.Date == "" || track.Name == "" {
				continue
			}
			ok, err := store.InsertIfNew(ctx, track)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			if ok {
				inserted++
			}
		}
		page++
	}
	return inserted, first
}

func fetchPage(ctx context.Context, client *http.Client, user, apiKey string, since time.Time, page int) ([]byte, error) {
	endpoint, err := url.Parse(fetchURL)
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("method", "user.getrecenttracks")
	query.Set("user", user)
	query.Set("api_key", apiKey)
	query.Set("format", "json")
	query.Set("limit", strconv.Itoa(pageSize))
	query.Set("page", strconv.Itoa(page))
	if !since.IsZero() {
		query.Set("from", strconv.FormatInt(since.Unix()+1, 10))
	}
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gymdl/1.0 (listening-sync)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取 Last.fm 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Last.fm 返回 HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// ParseRecent extracts finished tracks and the page count from a Last.fm payload.
func ParseRecent(body []byte) ([]Track, int, error) {
	var payload struct {
		RecentTracks struct {
			Track []struct {
				Name   string `json:"name"`
				MBID   string `json:"mbid"`
				URL    string `json:"url"`
				Artist struct {
					Text string `json:"#text"`
				} `json:"artist"`
				Album struct {
					Text string `json:"#text"`
				} `json:"album"`
				Image []struct {
					Text string `json:"#text"`
				} `json:"image"`
				Date struct {
					UTS string `json:"uts"`
				} `json:"date"`
			} `json:"track"`
			Attr struct {
				TotalPages string `json:"totalPages"`
			} `json:"@attr"`
		} `json:"recenttracks"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0, fmt.Errorf("解析 Last.fm 响应失败: %w", err)
	}
	pages, _ := strconv.Atoi(payload.RecentTracks.Attr.TotalPages)
	if pages < 1 {
		pages = 1
	}
	tracks := make([]Track, 0, len(payload.RecentTracks.Track))
	for _, item := range payload.RecentTracks.Track {
		image := ""
		if len(item.Image) > 1 {
			image = item.Image[1].Text
		} else if len(item.Image) == 1 {
			image = item.Image[0].Text
		}
		tracks = append(tracks, Track{
			Name: item.Name, Artist: item.Artist.Text, Album: item.Album.Text,
			Image: image, MBID: item.MBID, URL: item.URL, Date: FormatUTS(item.Date.UTS),
		})
	}
	return tracks, pages, nil
}

// FormatUTS renders a Last.fm unix timestamp as the wall time stored in
// music_scrobble_records. The existing rows are Shanghai local time without a zone.
func FormatUTS(uts string) string {
	if uts == "" {
		return ""
	}
	seconds, err := strconv.ParseInt(uts, 10, 64)
	if err != nil {
		return ""
	}
	return time.Unix(seconds, 0).In(shanghai).Format("2006-01-02 15:04:05")
}

// ParseStored reads max(date) values written by the old workflow.
func ParseStored(value string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04:05", value, shanghai)
}

var shanghai = time.FixedZone("CST", 8*3600)
