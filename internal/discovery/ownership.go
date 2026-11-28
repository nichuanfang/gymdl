package discovery

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nichuanfang/gymdl/utils"
)

// Ownership matching answers "is this already in the library?" It is looser
// than SameRecording on purpose. Last.fm credits, platform durations and
// remaster album tags often disagree with the file, and those disagreements
// were recommending songs the library already had.
//
// SameRecording stays strict for cross-platform resolve, so a remix is not
// treated as the same download.

var (
	featWord        = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:feat\.?|ft\.?|featuring)(?:[^a-z0-9]|$)`)
	trailingFeat    = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|featuring)\s+.+$`)
	trailingVersion = regexp.MustCompile(`(?i)\s*[(\[（【]\s*(?:album|original|digital(?:ly)?\s+remaster(?:ed)?|remaster(?:ed)?|explicit|clean)(?:\s+(?:version|edition))?(?:\s+\d{4})?\s*[)\]）】]\s*$`)
	artistSplit     = regexp.MustCompile(`(?i)\s*(?:,|，|、|;|；|/|／|&|＆|•|·|・|\||｜|\bx\b|\bfeat\.?\b|\bft\.?\b|\bfeaturing\b|\bwith\b)\s*`)
	albumEditionIn  = regexp.MustCompile(`(?i)\s*[(\[（【][^)\]）】]{0,80}(?:remaster(?:ed)?|deluxe|anniversary|edition|version|single|live)[^)\]）】]{0,24}[)\]）】]\s*`)
	albumEditionEnd = regexp.MustCompile(`(?i)(?:\s*-\s*|\s+)(?:single|ep|deluxe(?:\s+edition)?|remaster(?:ed)?(?:\s+edition)?|live|anniversary(?:\s+edition)?)(?:\s+\d{4})?\s*$`)
	albumYear       = regexp.MustCompile(`^(?:19|20)\d{2}$`)
)

var genericAlbumToken = map[string]bool{
	"original": true, "soundtrack": true, "remaster": true, "remastered": true,
	"deluxe": true, "edition": true, "version": true, "single": true,
	"music": true, "sound": true, "selection": true, "audio": true,
	"ost": true, "the": true, "and": true, "vol": true, "volume": true,
	"hd": true, "cd": true, "ep": true, "lp": true, "remix": true,
	"live": true, "explicit": true, "clean": true, "anniversary": true,
	"bonus": true, "track": true, "tracks": true,
}

func ownershipTitle(title string) string {
	cleaned := trailingFeat.ReplaceAllString(stripFeatGroups(title), " ")
	return Normalize(trailingVersion.ReplaceAllString(cleaned, " "))
}

func stripFeatGroups(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); {
		r, size := utf8.DecodeRuneInString(value[i:])
		closer, ok := featOpeners[r]
		if !ok {
			b.WriteRune(r)
			i += size
			continue
		}
		rest := value[i+size:]
		end := strings.IndexRune(rest, closer)
		if end < 0 {
			b.WriteString(value[i:])
			break
		}
		inner := rest[:end]
		closerLen := utf8.RuneLen(closer)
		if featMention(inner) {
			b.WriteByte(' ')
		} else {
			b.WriteString(value[i : i+size+end+closerLen])
		}
		i += size + end + closerLen
	}
	return b.String()
}

var featOpeners = map[rune]rune{'(': ')', '（': '）', '[': ']', '【': '】'}

func featMention(value string) bool {
	if strings.Contains(value, "客串") {
		return true
	}
	return featWord.MatchString(value)
}

func artistCredits(artist string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range artistSplit.Split(artist, -1) {
		key := Normalize(part)
		if key == "" || seen[key] || len([]rune(key)) < 2 {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

func artistsOverlap(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	rightSet := map[string]struct{}{}
	for _, credit := range artistCredits(right) {
		rightSet[credit] = struct{}{}
	}
	for _, credit := range artistCredits(left) {
		if _, ok := rightSet[credit]; ok {
			return true
		}
	}
	return false
}

func albumWorkKey(album string) string {
	cleaned := strings.TrimSpace(album)
	prev := ""
	for cleaned != "" && cleaned != prev {
		prev = cleaned
		cleaned = strings.TrimSpace(albumEditionIn.ReplaceAllString(cleaned, " "))
		cleaned = strings.TrimSpace(albumEditionEnd.ReplaceAllString(cleaned, ""))
	}
	return Normalize(cleaned)
}

func albumsSameWork(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	a, b := albumWorkKey(left), albumWorkKey(right)
	if a != "" && a == b {
		return true
	}
	return albumsShareToken(left, right)
}

func albumsShareToken(left, right string) bool {
	rightSet := albumTokens(right)
	for token := range albumTokens(left) {
		if _, ok := rightSet[token]; ok {
			return true
		}
	}
	return false
}

func albumTokens(album string) map[string]struct{} {
	lowered := strings.ToLower(utils.ToSimpleChinese(strings.TrimSpace(album)))
	out := map[string]struct{}{}
	var b strings.Builder
	flush := func() {
		token := b.String()
		b.Reset()
		if distinctiveAlbumToken(token) {
			out[token] = struct{}{}
		}
	}
	for _, r := range lowered {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

func distinctiveAlbumToken(token string) bool {
	if token == "" || genericAlbumToken[token] || albumYear.MatchString(token) {
		return false
	}
	return len([]rune(token)) >= 6
}

// sameOwnedWork reports that two tracks are the same library work even when
// duration, remaster tags or extra featured credits differ.
func sameOwnedWork(left, right Track) bool {
	title := ownershipTitle(left.Title)
	if title == "" || title != ownershipTitle(right.Title) {
		return false
	}
	if artistsOverlap(left.Artist, right.Artist) {
		return true
	}
	return albumsSameWork(left.Album, right.Album)
}
