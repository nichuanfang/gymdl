// Package discovery finds missing favourites and maintains a durable import queue.
package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/nichuanfang/gymdl/utils"
	"golang.org/x/text/unicode/norm"
)

type Track struct {
	Platform   string    `json:"platform"`
	ID         string    `json:"id"`
	URL        string    `json:"url"`
	CoverURL   string    `json:"cover_url,omitempty"`
	Title      string    `json:"title"`
	Artist     string    `json:"artist"`
	Album      string    `json:"album,omitempty"`
	Duration   int       `json:"duration,omitempty"`
	Plays      int       `json:"plays,omitempty"`
	LastPlayed time.Time `json:"last_played,omitempty"`
	Favorite   bool      `json:"favorite,omitempty"`
	NonMusic   bool      `json:"non_music,omitempty"`
	// Verified means metadata identifies one music recording, not a video title/uploader.
	Verified         bool `json:"verified"`
	Approved         bool `json:"approved,omitempty"`
	TasteRecommended bool `json:"taste_recommended,omitempty"`
	// SourceCard is the recommendation card that queued this download.
	// Empty for favorites sync and manual downloads. It is not shown to the user.
	SourceCard string `json:"source_card,omitempty"`
	// CardAction is only for a saved recommendation card.
	// Empty means undecided. "dismissed" is permanent. Other values mirror job
	// states such as queued, running, and completed. Clear it before enqueueing.
	CardAction string `json:"card_action,omitempty"`
	// IngestID ties a recommendation row to its download job. Clear it before enqueueing.
	IngestID string `json:"ingest_id,omitempty"`
	// IngestNote is a short failure reason shown on the recommendation card.
	IngestNote string `json:"ingest_note,omitempty"`
}

func Normalize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, norm.NFKC.String(utils.ToSimpleChinese(strings.TrimSpace(s))))
}

// Identity deliberately retains edition qualifiers such as live, remix and cover.
func Identity(t Track) string {
	value := Normalize(t.Title) + "\x00" + Normalize(t.Artist) + "\x00" + recordingVersion(t)
	if t.Title == "" || t.Artist == "" {
		value = t.Platform + "\x00" + t.ID
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func SameRecording(a, b Track) bool {
	if !b.Verified || Normalize(a.Title) == "" || Normalize(a.Artist) == "" || Normalize(a.Title) != Normalize(b.Title) || Normalize(a.Artist) != Normalize(b.Artist) {
		return false
	}
	if recordingVersion(a) != recordingVersion(b) {
		return false
	}
	if a.Duration > 0 && b.Duration > 0 {
		d := a.Duration - b.Duration
		if d < 0 {
			d = -d
		}
		if d > 5 || float64(d) > float64(a.Duration)*0.03 {
			return false
		}
	}
	return true
}

type Snapshot struct {
	Platform string  `json:"platform"`
	Account  string  `json:"account"`
	Tracks   []Track `json:"tracks"`
	Complete bool    `json:"complete"`
}
type Binding struct {
	User         int64
	Chat         int64
	Platform     string
	Account      string
	Paused       bool
	PausedByUser bool
	LastSync     int64
	Error        string
}
type IngestedFile struct {
	Path      string
	Storage   string
	Extension string
	Size      int64
	Bitrate   string
}

type Job struct {
	ID            string
	User          int64
	Chat          int64
	Track         Track
	State         string
	Attempts      int
	Next          int64
	Error         string
	Updated       int64
	Result        string
	File          *IngestedFile `json:"-"`
	SourceAccount string        `json:"-"`
}
type Card struct {
	Token     string
	User      int64
	Chat      int64
	Kind      string
	MessageID int `json:"message_id,omitempty"`
	Tracks    []Track
	Snapshot  *Snapshot
	Expires   int64
	// SkippedOwned is how many distinct library works were left off this card.
	SkippedOwned int `json:"skipped_owned,omitempty"`
	// Unconfirmed is how many distinct works had no single version on the chosen platform.
	Unconfirmed int `json:"unconfirmed,omitempty"`
	// The remaining counters explain why a short batch was not filled.
	SkippedShown     int `json:"skipped_shown,omitempty"`
	SkippedPending   int `json:"skipped_pending,omitempty"`
	SkippedDismissed int `json:"skipped_dismissed,omitempty"`
	SkippedQueued    int `json:"skipped_queued,omitempty"`
	SkippedArtistCap int `json:"skipped_artist_cap,omitempty"`
	SkippedDuplicate int `json:"skipped_duplicate,omitempty"`
	SkippedOther     int `json:"skipped_other,omitempty"`
	Unavailable      int `json:"unavailable,omitempty"`
	// LibraryStale records that ownership was checked against a recent
	// snapshot because Navidrome could not be refreshed during this request.
	LibraryStale      bool  `json:"library_stale,omitempty"`
	LibrarySnapshotAt int64 `json:"library_snapshot_at,omitempty"`
	// Requested is the 3, 6, or 9 the user asked for.
	Requested int `json:"requested,omitempty"`
	// AutoPlatform keeps randomized rediscover cards randomized when drawing again.
	AutoPlatform bool `json:"auto_platform,omitempty"`
	// Page is the group currently shown on the Telegram message.
	Page  int        `json:"page,omitempty"`
	Taste *TasteCard `json:"taste,omitempty"`
}

const TasteCardKind = "taste_recommend"

type TasteCard struct {
	Request TasteRequest `json:"request"`
	Reason  string       `json:"reason,omitempty"`
	Notice  string       `json:"notice,omitempty"`
}

func (c Card) IsSongCard() bool {
	return c.Kind == "recommend" || c.Kind == TasteCardKind
}

const (
	Queued            = "queued"
	Running           = "running"
	PendingUpload     = "pending_upload"
	Completed         = "completed"
	Owned             = "owned"
	NeedsConfirmation = "needs_confirmation"
	Failed            = "failed"
	Ignored           = "ignored"
)

var albumEdition = regexp.MustCompile(`(?i)(?:^|[\s(\[._-])(live|remix|acoustic|instrumental|cover|remaster(?:ed)?)(?:$|[\s)\]._:-])|现场|演唱会|不插电|混音|翻唱|伴奏|重制`)

// Albums can carry edition information missing from a song title. Keep those
// editions separate, while allowing ordinary compilation/album re-releases.
func recordingVersion(t Track) string {
	album := utils.ToSimpleChinese(t.Album)
	if albumEdition.MatchString(album) {
		return Normalize(album)
	}
	return ""
}
