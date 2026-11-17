// Package audiodupe compares Chromaprint fingerprints locally.
// It groups re-encodes of the same audio and keeps edited versions apart.
// Nothing in this package deletes or moves files.
package audiodupe

import (
	"math"
	"math/bits"
)

const (
	// One Chromaprint value covers 4096/3 samples of 11025 Hz audio.
	itemSeconds  = 4096.0 / 3.0 / 11025.0
	blockItems   = 20 // about 2.5 seconds
	prefilterN   = 160
	prefilterBER = 0.30
	minItems     = 40
	minOverlap   = 0.9
	maxOffset    = 10
	sameMaxBER   = 0.10
	sameMaxWin   = 0.13
	similarMax   = 0.20
)

// Kind is the relationship between two fingerprints.
type Kind string

const (
	KindSame      Kind = "same"
	KindSimilar   Kind = "similar"
	KindDifferent Kind = "different"
)

// Fingerprint is a raw Chromaprint sequence plus the file duration in seconds.
type Fingerprint struct {
	Duration float64
	Items    []uint32
}

// Comparison is one pairwise result. Different means the pair is not a duplicate.
type Comparison struct {
	Kind         Kind
	BER          float64
	MaxWindowBER float64
}

// DurationClose reports whether two files are close enough in length to be copies.
// Encoder padding stays inside this window. A remix or a shortened edit usually does not.
func DurationClose(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	diff := math.Abs(a - b)
	limit := math.Max(1.5, 0.01*math.Max(a, b))
	return diff <= limit
}

// LowInformation is true for clips that are too short, silent, or nearly constant.
// Those fingerprints resemble each other without identifying a recording.
func LowInformation(fp Fingerprint) bool {
	if len(fp.Items) < minItems {
		return true
	}
	counts := map[uint32]int{}
	maxCount := 0
	for _, item := range fp.Items {
		counts[item]++
		if counts[item] > maxCount {
			maxCount = counts[item]
		}
	}
	return float64(len(counts)) < 0.15*float64(len(fp.Items)) || maxCount > len(fp.Items)/2
}

// Compare aligns two fingerprints and scores the fraction of differing bits.
func Compare(a, b Fingerprint) Comparison {
	different := Comparison{Kind: KindDifferent, BER: 1, MaxWindowBER: 1}
	if len(a.Items) < minItems || len(b.Items) < minItems {
		return different
	}
	if !DurationClose(a.Duration, b.Duration) && a.Duration > 0 && b.Duration > 0 {
		return different
	}
	offset, windowBER, ok := bestOffset(a.Items, b.Items)
	if !ok || windowBER > prefilterBER {
		return different
	}
	left, right := a.Items, b.Items
	if offset >= 0 {
		left = a.Items[offset:]
	} else {
		right = b.Items[-offset:]
	}
	n := len(left)
	if len(right) < n {
		n = len(right)
	}
	if float64(n) < minOverlap*float64(max(len(a.Items), len(b.Items))) {
		return different
	}
	bitErrors := 0
	blockErrors := make([]int, 0, n/blockItems)
	blockBits := 0
	blockCount := 0
	for i := 0; i < n; i++ {
		diff := bits.OnesCount32(left[i] ^ right[i])
		bitErrors += diff
		blockBits += diff
		blockCount++
		if blockCount == blockItems {
			blockErrors = append(blockErrors, blockBits)
			blockBits = 0
			blockCount = 0
		}
	}
	ber := float64(bitErrors) / float64(32*n)
	maxWindow := ber
	if len(blockErrors) > 0 {
		worst := 0
		for _, value := range blockErrors {
			if value > worst {
				worst = value
			}
		}
		maxWindow = float64(worst) / float64(32*blockItems)
	}
	kind := KindDifferent
	switch {
	case ber <= sameMaxBER && maxWindow <= sameMaxWin:
		kind = KindSame
	case ber <= similarMax:
		kind = KindSimilar
	}
	return Comparison{Kind: kind, BER: ber, MaxWindowBER: maxWindow}
}

func bestOffset(a, b []uint32) (int, float64, bool) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	width := prefilterN
	if candidate := n - 2*maxOffset; candidate < width {
		width = candidate
	}
	if width < minItems/2 {
		width = n - 2*maxOffset
		if width <= 0 {
			return 0, 0, false
		}
	}
	start := maxOffset
	if quarter := n / 4; quarter > start {
		start = quarter
	}
	if limit := n - width - maxOffset; start > limit {
		start = limit
	}
	if start < maxOffset {
		start = maxOffset
	}
	if start+width > len(a) {
		return 0, 0, false
	}
	lo := start - maxOffset
	hi := start + maxOffset + width
	if lo < 0 || hi > len(b) {
		return 0, 0, false
	}
	window := a[start : start+width]
	best := math.MaxFloat64
	bestI := 0
	for i := 0; i <= maxOffset*2; i++ {
		seg := b[lo+i : lo+i+width]
		diff := 0
		for k := range window {
			diff += bits.OnesCount32(window[k] ^ seg[k])
		}
		score := float64(diff) / (32 * float64(width))
		if score < best {
			best = score
			bestI = i
		}
	}
	return start - (lo + bestI), best, true
}
