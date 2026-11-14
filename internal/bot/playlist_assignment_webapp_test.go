package bot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nichuanfang/gymdl/config"
)

func TestPlaylistAssignmentWebAppURLUsesLegacyPageAndPerSongDefaults(t *testing.T) {
	assignment := pendingPlaylistAssignment{
		playlists:            []config.PlaylistInfo{{Name: "华语", Desc: "中文歌曲"}, {Name: "摇滚", Desc: "摇滚"}},
		aiSuggestedPlaylists: []string{"摇滚"},
	}
	raw, err := playlistAssignmentWebAppURL("https://WEBAPP.example.com:443", assignment, "01234567890123456789012345678901")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "webapp.example.com" {
		t.Fatalf("WebApp URL origin was not canonicalized: %s", raw)
	}
	var page struct {
		Title   string   `json:"title"`
		Options []string `json:"options"`
		Default []string `json:"default"`
	}
	if err := json.Unmarshal([]byte(parsed.Query().Get("data")), &page); err != nil {
		t.Fatalf("decode existing WebApp page data: %v", err)
	}
	if page.Title != "请选择歌单" || strings.Join(page.Options, ",") != "华语,摇滚" || strings.Join(page.Default, ",") != "摇滚" {
		t.Fatalf("unexpected existing WebApp page payload: %#v", page)
	}
	callback, err := url.Parse(parsed.Query().Get("callbackUrl"))
	if err != nil || callback.Host != parsed.Host || callback.Path != playlistWebAppCallbackPath+"01234567890123456789012345678901" {
		t.Fatalf("unexpected same-origin callback URL: %q, err=%v", parsed.Query().Get("callbackUrl"), err)
	}
}

func TestPlaylistAssignmentWebAppURLRejectsUnsafeOriginsAndOversizeData(t *testing.T) {
	for _, raw := range []string{"http://webapp.example.com", "https://user@webapp.example.com", "https://webapp.example.com/path", "https://webapp.example.com/?x=1"} {
		if _, err := playlistWebAppBaseURL(raw); err == nil {
			t.Errorf("playlist WebApp URL %q should be rejected", raw)
		}
	}
	assignment := pendingPlaylistAssignment{playlists: []config.PlaylistInfo{{Name: strings.Repeat("歌", 5000)}}}
	if _, err := playlistAssignmentWebAppURL("https://webapp.example.com", assignment, "01234567890123456789012345678901"); err == nil {
		t.Fatal("oversized chooser URL should be rejected")
	}
}

func TestValidateTelegramWebAppInitData(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	valid := signedTelegramWebAppInitData("bot-token", 123, now)
	if err := validateTelegramWebAppInitData(valid, "bot-token", 123, now); err != nil {
		t.Fatalf("valid initData rejected: %v", err)
	}
	for name, test := range map[string]struct {
		data     string
		botToken string
		userID   int64
		now      time.Time
	}{
		"wrong bot token": {data: valid, botToken: "wrong-token", userID: 123, now: now},
		"wrong user":      {data: valid, botToken: "bot-token", userID: 124, now: now},
		"expired":         {data: signedTelegramWebAppInitData("bot-token", 123, now.Add(-playlistAssignmentTTL-time.Second)), botToken: "bot-token", userID: 123, now: now},
		"future":          {data: signedTelegramWebAppInitData("bot-token", 123, now.Add(time.Minute)), botToken: "bot-token", userID: 123, now: now},
		"tampered":        {data: strings.Replace(valid, "auth_date=1800000000", "auth_date=1800000001", 1), botToken: "bot-token", userID: 123, now: now},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateTelegramWebAppInitData(test.data, test.botToken, test.userID, test.now); err == nil {
				t.Fatal("invalid initData accepted")
			}
		})
	}
	duplicate := valid + "&user=%7B%22id%22%3A123%7D"
	if err := validateTelegramWebAppInitData(duplicate, "bot-token", 123, now); err == nil {
		t.Fatal("duplicate initData fields should be rejected")
	}
}

func TestHandlePlaylistAssignmentWebAppSelectionVerifiesUserAndSelection(t *testing.T) {
	now := time.Now()
	const botToken = "test-bot-token"
	const selectionToken = "0123456789abcdef0123456789abcdef"
	app := &BotApp{cfg: &config.Config{Telegram: &config.TelegramConfig{BotToken: botToken}}}
	if !app.registerPlaylistAssignment(pendingPlaylistAssignment{
		token:          "assignment",
		userID:         123,
		chatID:         456,
		song:           unplaylistSong{ID: "song-1"},
		playlists:      []config.PlaylistInfo{{Name: "华语"}, {Name: "摇滚"}},
		selectionToken: selectionToken,
		handledSongIDs: map[string]struct{}{},
		expiresAt:      now.Add(time.Minute),
	}) {
		t.Fatal("failed to register playlist assignment")
	}
	valid := signedTelegramWebAppInitData(botToken, 123, now)
	wrongUser := signedTelegramWebAppInitData(botToken, 124, now)
	if err := app.HandlePlaylistAssignmentWebAppSelection(selectionToken, []string{"摇滚"}, wrongUser); err == nil {
		t.Fatal("another Telegram user should not select playlists for this session")
	}
	if err := app.HandlePlaylistAssignmentWebAppSelection(selectionToken, []string{"不存在"}, valid); err == nil {
		t.Fatal("selection outside the configured playlist list should be rejected")
	}
	assignment, ok := app.getPlaylistAssignment("assignment")
	if !ok || assignment.selectionComplete {
		t.Fatal("rejected selection changed the pending assignment")
	}
	if err := app.HandlePlaylistAssignmentWebAppSelection(selectionToken, []string{"摇滚"}, valid); err != nil {
		t.Fatalf("valid selection callback failed: %v", err)
	}
	if err := app.HandlePlaylistAssignmentWebAppSelection(selectionToken, []string{"摇滚"}, valid); err != nil {
		t.Fatalf("idempotent network retry failed: %v", err)
	}
	if err := app.HandlePlaylistAssignmentWebAppSelection(selectionToken, []string{"华语"}, valid); err == nil {
		t.Fatal("a consumed selection token should not accept altered data")
	}
	assignment, ok = app.getPlaylistAssignment("assignment")
	if !ok || !assignment.selectionComplete || len(assignment.selectedPlaylists) != 1 || assignment.selectedPlaylists[0] != "摇滚" {
		t.Fatalf("confirmed playlist selection was not preserved: %#v", assignment)
	}
}

func signedTelegramWebAppInitData(botToken string, userID int64, authTime time.Time) string {
	values := url.Values{
		"auth_date": {strconv.FormatInt(authTime.Unix(), 10)},
		"query_id":  {"AAE-test"},
		"user":      {`{"id":` + strconv.FormatInt(userID, 10) + `,"first_name":"Test"}`},
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+values.Get(key))
	}
	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretMAC.Write([]byte(botToken))
	checkMAC := hmac.New(sha256.New, secretMAC.Sum(nil))
	checkMAC.Write([]byte(strings.Join(lines, "\n")))
	values.Set("hash", hex.EncodeToString(checkMAC.Sum(nil)))
	return values.Encode()
}
