package audiodupe

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/internal/storage"
)

const (
	// skipAbove is strictly above 95%. confirmAt includes both 75% and 95%.
	skipAbove = 0.95
	confirmAt = 0.75
	// fingerprintTimeout caps fpcalc because ingest holds the single workflow gate.
	fingerprintTimeout = 20 * time.Second
)

// Decision is what an ingest should do with one incoming recording.
type Decision string

const (
	DecisionProceed Decision = "proceed"
	DecisionConfirm Decision = "confirm"
	DecisionSkip    Decision = "skip"
)

// Similarity is 1−BER after the pair actually aligned.
// An early exit (BER ≥ 1) is not a score.
func Similarity(c Comparison) float64 {
	if c.BER >= 1 {
		return 0
	}
	sim := 1 - c.BER
	if sim < 0 {
		return 0
	}
	if sim > 1 {
		return 1
	}
	return sim
}

// Decide maps an aligned score onto the ingest bands.
// The cut is the float, not the rounded percent.
func Decide(c Comparison) (Decision, float64) {
	sim := Similarity(c)
	switch {
	case sim > skipAbove:
		return DecisionSkip, sim
	case sim >= confirmAt:
		return DecisionConfirm, sim
	default:
		return DecisionProceed, sim
	}
}

// LibraryMatch is one library file close enough to block or ask.
type LibraryMatch struct {
	Path       string
	Similarity float64
	Decision   Decision
}

func (m *LibraryMatch) Error() string {
	if m == nil {
		return ""
	}
	name := filepath.Base(strings.TrimSpace(m.Path))
	if name == "" || name == "." {
		name = "已入库的录音"
	}
	percent := int(math.Round(m.Similarity * 100))
	switch m.Decision {
	case DecisionSkip:
		return fmt.Sprintf("已经有这段录音\n\n%s\n相似度 %d%%\n\n没有再入库", name, percent)
	case DecisionConfirm:
		return fmt.Sprintf("听感很接近\n\n%s\n相似度 %d%%", name, percent)
	default:
		return fmt.Sprintf("%s\n相似度 %d%%", name, percent)
	}
}

// Closest returns the library track with the highest aligned similarity.
func Closest(candidate Fingerprint, library []Track) (Track, Comparison, bool) {
	return matchLibrary(candidate, library, false)
}

func matchLibrary(candidate Fingerprint, library []Track, stopOnSkip bool) (Track, Comparison, bool) {
	var best Track
	var bestCmp Comparison
	bestSim := -1.0
	found := false
	for _, track := range library {
		if len(track.Fingerprint.Items) == 0 {
			continue
		}
		cmp := Compare(candidate, track.Fingerprint)
		sim := Similarity(cmp)
		if !found || sim > bestSim {
			found = true
			best = track
			bestCmp = cmp
			bestSim = sim
		}
		if stopOnSkip && sim > skipAbove {
			return best, bestCmp, true
		}
	}
	if !found {
		return Track{}, Comparison{}, false
	}
	return best, bestCmp, true
}

var fingerprintFile = func(ctx context.Context, path string) (Fingerprint, error) {
	return FingerprintFile(ctx, path)
}

var openStore = func(path string) (*Store, error) {
	return Open(path)
}

// ReviewFiles checks one new file against the stored fingerprint index.
// Albums, an empty index, a missing fpcalc, and a failed fingerprint continue the ingest.
func ReviewFiles(ctx context.Context, paths []string, bypass bool) error {
	if bypass || len(paths) != 1 || strings.TrimSpace(paths[0]) == "" {
		return nil
	}
	return ReviewFile(ctx, paths[0], 0, false)
}

// ReviewFile compares one temp file. durationSec is the tagged length, used only
// to skip fpcalc when nothing already indexed is even close in length.
func ReviewFile(ctx context.Context, path string, durationSec float64, bypass bool) error {
	if bypass || strings.TrimSpace(path) == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store, err := openStore(storage.DefaultDatabasePath)
	if err != nil {
		return nil
	}
	defer store.Close()

	any, err := store.HasAny(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	if !any {
		return nil
	}
	if durationSec > 0 {
		lo, hi := hintWindow(durationSec)
		near, nearErr := store.HasDuration(ctx, lo, hi)
		if nearErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else if !near {
			return nil
		}
	}

	fp, err := fingerprintFile(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	if fp.Duration <= 0 || len(fp.Items) == 0 {
		return nil
	}
	lo, hi := preciseWindow(fp.Duration)
	library, err := store.ListDuration(ctx, lo, hi, path)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	track, cmp, ok := matchLibrary(fp, library, true)
	if !ok {
		return nil
	}
	decision, sim := Decide(cmp)
	if decision == DecisionProceed {
		return nil
	}
	return &LibraryMatch{Path: track.Path, Similarity: sim, Decision: decision}
}

// FingerprintFile runs fpcalc on one local file. The timeout is inside the call.
func FingerprintFile(ctx context.Context, path string) (Fingerprint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	binary, err := exec.LookPath("fpcalc")
	if err != nil {
		return Fingerprint{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, fingerprintTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "-json", "-raw", path).Output()
	if err != nil {
		return Fingerprint{}, err
	}
	return ParseFpcalc(out)
}

// preciseWindow is the DurationClose neighborhood, widened just enough for float edges.
func preciseWindow(duration float64) (float64, float64) {
	slack := math.Max(1.5, 0.01*duration)
	lo := duration - slack - 0.05
	if lo < 0 {
		lo = 0
	}
	hi := duration / 0.99
	if ceiling := duration + 1.5; ceiling > hi {
		hi = ceiling
	}
	return lo, hi + 0.05
}

// hintWindow is wider than DurationClose so a tag duration off by a few seconds
// still fingerprints. It only skips fpcalc when nothing indexed is close.
func hintWindow(duration float64) (float64, float64) {
	slack := math.Max(8, 0.04*duration)
	lo := duration - slack
	if lo < 0 {
		lo = 0
	}
	return lo, duration + slack
}
