package bot

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

var (
	errIngestedSongNotFound  = errors.New("ingested song not in Navidrome")
	errIngestedSongAmbiguous = errors.New("multiple Navidrome songs match the ingested file")
)

func (s *playlistOrganizerService) findIngestedSong(ctx context.Context, target pendingMusicAction) (unplaylistSong, error) {
	if strings.TrimSpace(target.title) == "" {
		return unplaylistSong{}, errIngestedSongNotFound
	}
	const pageSize = 50
	endpoint, err := url.JoinPath(s.config.NavidromeURL, "rest", "search3.view")
	if err != nil {
		return unplaylistSong{}, err
	}
	salt := uuid.NewString()
	hash := md5.Sum([]byte(s.config.NavidromePassword + salt))
	query := url.Values{
		"u": {s.config.NavidromeUsername}, "t": {fmt.Sprintf("%x", hash)}, "s": {salt},
		"v": {"1.16.1"}, "c": {"gymdl"}, "f": {"json"},
		"query": {strings.TrimSpace(target.title)}, "artistCount": {"0"}, "albumCount": {"0"},
		"songCount": {strconv.Itoa(pageSize)},
	}
	body, status, err := doPlaylistHTTPRequest(ctx, s.client, http.MethodGet, endpoint+"?"+query.Encode(), nil, nil, "", "")
	if err != nil || status != http.StatusOK {
		return unplaylistSong{}, errors.New("Navidrome song search failed")
	}
	var result struct {
		Response struct {
			Status string
			Search struct {
				Songs []struct {
					unplaylistSong
					Suffix  string
					Size    int64
					Created string
				} `json:"song"`
			} `json:"searchResult3"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Response.Status != "ok" {
		return unplaylistSong{}, errors.New("invalid Navidrome song search response")
	}
	var match unplaylistSong
	for _, hit := range result.Response.Search.Songs {
		if !safeNavidromeID(hit.ID) || !sameStoredSongTag(hit.Title, target.title) ||
			!sameStoredSongTag(hit.Artist, target.artist) || !sameStoredSongTag(hit.Album, target.album) {
			continue
		}
		if (target.extension != "" && !strings.EqualFold(hit.Suffix, strings.TrimPrefix(target.extension, "."))) ||
			(target.size > 0 && hit.Size != target.size) {
			continue
		}
		if match.ID != "" && match.ID != hit.ID {
			return unplaylistSong{}, errIngestedSongAmbiguous
		}
		match = hit.unplaylistSong
		if strings.TrimSpace(match.Comment) == "" {
			match.Comment = target.sourceURL
		}
		match.CreatedAt = hit.Created
	}
	if len(result.Response.Search.Songs) >= pageSize {
		return unplaylistSong{}, errIngestedSongAmbiguous
	}
	if match.ID == "" {
		return unplaylistSong{}, errIngestedSongNotFound
	}
	return match, nil
}

func sameStoredSongTag(left, right string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(left), " "), strings.Join(strings.Fields(right), " "))
}
