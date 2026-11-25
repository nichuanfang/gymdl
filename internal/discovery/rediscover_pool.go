package discovery

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/nichuanfang/gymdl/utils"
)

const (
	rediscoverPoolTarget     = 10
	rediscoverPoolLow        = 3
	rediscoverPoolTTL        = 30 * time.Minute
	rediscoverRefillTimeout  = 35 * time.Second
	rediscoverRefillCooldown = time.Minute
	rediscoverPoolMaxKeys    = 64
)

type rediscoverCandidate struct {
	source Track
	track  Track
}

type rediscoverPoolKey struct {
	user     int64
	platform string
}

type bufferedRediscoverCandidate struct {
	rediscoverCandidate
	expires time.Time
}

type rediscoverPoolEntry struct {
	candidates []bufferedRediscoverCandidate
	filling    bool
	retryAfter time.Time
}

type rediscoverRefill struct {
	key   rediscoverPoolKey
	shown Track
}

type rediscoverBuffer struct {
	mu       sync.Mutex
	entries  map[rediscoverPoolKey]*rediscoverPoolEntry
	requests chan rediscoverRefill
}

func (p *rediscoverBuffer) queueLocked() chan rediscoverRefill {
	if p.requests == nil {
		p.requests = make(chan rediscoverRefill, 8)
		p.entries = make(map[rediscoverPoolKey]*rediscoverPoolEntry)
	}
	return p.requests
}

func (p *rediscoverBuffer) pruneLocked(now time.Time) {
	for key, entry := range p.entries {
		entry.candidates = slices.DeleteFunc(entry.candidates, func(candidate bufferedRediscoverCandidate) bool {
			return !now.Before(candidate.expires)
		})
		if len(entry.candidates) == 0 && !entry.filling && !now.Before(entry.retryAfter) {
			delete(p.entries, key)
		}
	}
}

func (p *rediscoverBuffer) snapshot(key rediscoverPoolKey) []bufferedRediscoverCandidate {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(time.Now())
	if entry := p.entries[key]; entry != nil {
		return slices.Clone(entry.candidates)
	}
	return nil
}

func (p *rediscoverBuffer) remove(key rediscoverPoolKey, candidate bufferedRediscoverCandidate) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(time.Now())
	entry := p.entries[key]
	if entry == nil {
		return false
	}
	for i, current := range entry.candidates {
		if candidate == current {
			entry.candidates = slices.Delete(entry.candidates, i, i+1)
			return true
		}
	}
	return false
}

// RediscoverBuffered takes one candidate for a Telegram card. Read-only previews
// use Rediscover so a Web or AI preview does not drain this buffer.
func (s *Service) RediscoverBuffered(ctx context.Context, user int64, platform string, limit int) (RediscoverBatch, error) {
	if err := validateRediscoverRequest(platform, limit); err != nil {
		return RediscoverBatch{}, err
	}
	key := rediscoverPoolKey{user: user, platform: platform}
	exclusions, err := s.Store.exclusions(ctx, user, true)
	if err != nil {
		return RediscoverBatch{}, err
	}
	state := newRecommendPick(exclusions)
	for _, candidate := range s.rediscoverPool.snapshot(key) {
		eligible, err := s.checkBufferedRediscover(ctx, state, user, candidate.rediscoverCandidate)
		if err != nil {
			return RediscoverBatch{}, err
		}
		if err := ctx.Err(); err != nil {
			return RediscoverBatch{}, err
		}
		if s.rediscoverPool.remove(key, candidate) && eligible {
			state.picks = append(state.picks, candidate.rediscoverCandidate)
			return s.rediscoverBatch(ctx, state), nil
		}
	}
	return s.searchRediscover(ctx, user, platform, limit, state)
}

func (s *Service) checkBufferedRediscover(ctx context.Context, state *recommendPick, user int64, candidate rediscoverCandidate) (bool, error) {
	sourceID, trackID := Identity(candidate.source), Identity(candidate.track)
	if state.exclude(sourceID, candidate.source) || state.exclude(trackID, candidate.track) {
		return false, nil
	}
	if candidate.source.Favorite {
		// A user may un-like the track or rebind the source account while it waits.
		active, err := s.Store.isActiveFavorite(ctx, user, candidate.source)
		if err != nil || !active {
			return false, err
		}
	}
	if sourceID != trackID {
		eligible, err := s.rediscoverEligible(ctx, state, candidate.source, true)
		if err != nil || !eligible {
			return false, err
		}
	}
	return s.rediscoverEligible(ctx, state, candidate.track, true)
}

// WarmRediscover schedules one bounded refill for a platform after a card was
// shown. The service worker handles it independently of the Telegram request.
func (s *Service) WarmRediscover(user int64, platform string, shown Track) {
	if user <= 0 || validateRediscoverRequest(platform, 1) != nil || s.Store == nil || s.Provider == nil || s.Library == nil {
		return
	}
	key := rediscoverPoolKey{user: user, platform: platform}
	pool := &s.rediscoverPool
	pool.mu.Lock()
	defer pool.mu.Unlock()
	queue := pool.queueLocked()
	now := time.Now()
	pool.pruneLocked(now)
	entry := pool.entries[key]
	if entry == nil {
		if len(pool.entries) >= rediscoverPoolMaxKeys {
			return
		}
		entry = &rediscoverPoolEntry{}
		pool.entries[key] = entry
	}
	if entry.filling || len(entry.candidates) > rediscoverPoolLow || now.Before(entry.retryAfter) {
		return
	}
	select {
	case queue <- rediscoverRefill{key: key, shown: shown}:
		entry.filling = true
	default:
	}
}

func (s *Service) runRediscoverRefills(ctx context.Context) {
	pool := &s.rediscoverPool
	pool.mu.Lock()
	queue := pool.queueLocked()
	pool.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-queue:
			if ctx.Err() != nil {
				return
			}
			s.refillRediscover(ctx, request)
		}
	}
}

func (s *Service) refillRediscover(ctx context.Context, request rediscoverRefill) {
	pool, key := &s.rediscoverPool, request.key
	var added []rediscoverCandidate
	defer func() {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		now := time.Now()
		pool.pruneLocked(now)
		entry := pool.entries[key]
		entry.filling = false
		for _, candidate := range added {
			if len(entry.candidates) >= rediscoverPoolTarget {
				break
			}
			entry.candidates = append(entry.candidates, bufferedRediscoverCandidate{
				rediscoverCandidate: candidate,
				expires:             now.Add(rediscoverPoolTTL),
			})
		}
		if len(entry.candidates) < rediscoverPoolTarget {
			entry.retryAfter = now.Add(rediscoverRefillCooldown)
		}
	}()

	if s.Authorized != nil && !s.Authorized(key.user, key.user) {
		return
	}
	query, cancel := context.WithTimeout(ctx, rediscoverRefillTimeout)
	defer cancel()
	exclusions, err := s.Store.exclusions(query, key.user, true)
	if err != nil {
		return
	}
	state := newRecommendPick(exclusions)
	// Avoid preparing the current card or duplicating the unconsumed stock,
	// even when the current platform credits differ from historical metadata.
	state.picks = append(state.picks, rediscoverCandidate{track: request.shown})
	stock := pool.snapshot(key)
	for _, candidate := range stock {
		state.picks = append(state.picks, candidate.rediscoverCandidate)
		state.exclusions[Identity(candidate.source)] = ExcludedOther
	}
	kept := len(state.picks)
	ranked, err := s.rankRediscover(query, key.user, key.platform, state, false)
	if err == nil {
		// One small search budget per round keeps background traffic bounded.
		err = s.pickTracks(query, state, ranked, rediscoverPoolTarget-len(stock), key.platform, rediscoverPoolTarget)
	}
	if err != nil && query.Err() == nil {
		utils.WarnWithFormat("[Discovery] 遗珠候选补充失败 platform=%s", key.platform)
	}
	if ctx.Err() != nil {
		return
	}
	for _, candidate := range state.picks[kept:] {
		if candidate.track.Verified && candidate.track.URL != "" && candidate.track.Platform == key.platform {
			added = append(added, candidate)
		}
	}
}
