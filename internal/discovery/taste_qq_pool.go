package discovery

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	qqTastePoolSize       = 16
	qqTastePoolLow        = 3
	qqTastePoolTTL        = 30 * time.Minute
	qqTasteRefillCooldown = time.Minute
	qqTasteRefillTimeout  = 45 * time.Second
	qqTastePoolMaxKeys    = 64
)

type qqTasteKey struct {
	user   int64
	filter TasteFilter
}

type qqTasteStock struct {
	choices    []tasteChoice
	expires    time.Time
	refilling  bool
	retryAfter time.Time
}

type qqTasteRefill struct {
	key     qqTasteKey
	request TasteRequest
	shown   Track
}

type qqTastePool struct {
	mu      sync.Mutex
	stock   map[qqTasteKey]*qqTasteStock
	refills chan qqTasteRefill
}

// stockLocked keeps the cache bounded even when users try many different filters.
func (p *qqTastePool) stockLocked(key qqTasteKey, now time.Time, create bool) *qqTasteStock {
	for k, stock := range p.stock {
		if !now.Before(stock.expires) {
			stock.choices = nil
		}
		if len(stock.choices) == 0 && !stock.refilling && !now.Before(stock.retryAfter) {
			delete(p.stock, k)
		}
	}
	stock := p.stock[key]
	if stock == nil && create && len(p.stock) < qqTastePoolMaxKeys {
		if p.stock == nil {
			p.stock = make(map[qqTasteKey]*qqTasteStock)
		}
		stock = &qqTasteStock{}
		p.stock[key] = stock
	}
	return stock
}

func (p *qqTastePool) list(key qqTasteKey) []tasteChoice {
	p.mu.Lock()
	defer p.mu.Unlock()
	if stock := p.stockLocked(key, time.Now(), false); stock != nil {
		return slices.Clone(stock.choices)
	}
	return nil
}

func (p *qqTastePool) remove(key qqTasteKey, choice tasteChoice) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if stock := p.stockLocked(key, time.Now(), false); stock != nil {
		if i := slices.Index(stock.choices, choice); i >= 0 {
			stock.choices = slices.Delete(stock.choices, i, i+1)
			return true
		}
	}
	return false
}

func (p *qqTastePool) add(key qqTasteKey, choices []tasteChoice) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.addLocked(key, time.Now(), choices)
}

func (p *qqTastePool) addLocked(key qqTasteKey, now time.Time, choices []tasteChoice) {
	if len(choices) == 0 {
		return
	}
	stock := p.stockLocked(key, now, true)
	if stock == nil {
		return
	}
	if len(stock.choices) == 0 {
		stock.expires = now.Add(qqTastePoolTTL)
	}
	for _, choice := range choices {
		if len(stock.choices) >= qqTastePoolSize {
			break
		}
		if !slices.ContainsFunc(stock.choices, func(existing tasteChoice) bool {
			return tasteSameSong(existing.track, choice.track)
		}) {
			stock.choices = append(stock.choices, choice)
		}
	}
}

func (s *Service) recommendQQTaste(ctx context.Context, user int64, req TasteRequest, profile tasteProfile) (TasteResult, error) {
	key := qqTasteKey{user: user, filter: req.Filter}
	choices := s.qqTastePool.list(key)
	// Prefer the requested lane but use the other lane if it is all that remains.
	for _, explore := range [2]bool{req.ExploreFirst, !req.ExploreFirst} {
		for _, choice := range choices {
			if choice.explore != explore {
				continue
			}
			track := choice.track
			if profile.exclusions[Identity(track)] != "" || tasteHasSong(track, profile.known[ownershipTitle(track.Title)]) {
				s.qqTastePool.remove(key, choice)
				continue
			}
			owned, pending, err := s.checkTasteAvailability(ctx, track)
			if err != nil {
				return TasteResult{}, err
			}
			if err := ctx.Err(); err != nil {
				return TasteResult{}, err
			}
			if s.qqTastePool.remove(key, choice) && !owned && !pending {
				return choice.result([]string{profile.notice}), nil
			}
		}
	}
	if req.Page > 5 {
		return TasteResult{Notice: "再往下也没有更多单曲了。可以换个场景或平台。"}, nil
	}
	choices, notices, err := s.searchTasteChoices(ctx, req, profile, []string{"qq"})
	if err != nil {
		return TasteResult{}, err
	}
	if len(choices) == 0 {
		return TasteResult{Notice: strings.Join(notices, " · ")}, nil
	}
	s.qqTastePool.add(key, choices[1:])
	return choices[0].result(notices), nil
}

// WarmQQTaste schedules a refill after Telegram successfully shows a song.
func (s *Service) WarmQQTaste(user int64, req TasteRequest, shown Track) {
	if req.Platform != "qq" || req.Page > 5 || user <= 0 {
		return
	}
	pool := &s.qqTastePool
	pool.mu.Lock()
	defer pool.mu.Unlock()
	key := qqTasteKey{user: user, filter: req.Filter}
	now := time.Now()
	stock := pool.stockLocked(key, now, true)
	if stock == nil || stock.refilling || len(stock.choices) > qqTastePoolLow || now.Before(stock.retryAfter) {
		return
	}
	select {
	case pool.queueLocked() <- qqTasteRefill{key: key, request: req, shown: shown}:
		stock.refilling = true
	default:
	}
}

func (p *qqTastePool) queueLocked() chan qqTasteRefill {
	if p.refills == nil {
		p.refills = make(chan qqTasteRefill, 8)
	}
	return p.refills
}

func (s *Service) runQQTasteRefills(ctx context.Context) {
	pool := &s.qqTastePool
	pool.mu.Lock()
	queue := pool.queueLocked()
	pool.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case refill := <-queue:
			if ctx.Err() != nil {
				return
			}
			s.refillQQTaste(ctx, refill)
		}
	}
}

func (s *Service) refillQQTaste(ctx context.Context, refill qqTasteRefill) {
	pool, key := &s.qqTastePool, refill.key
	var choices []tasteChoice
	defer func() {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		now := time.Now()
		if ctx.Err() == nil {
			pool.addLocked(key, now, choices)
		}
		stock := pool.stock[key]
		stock.refilling = false
		stock.retryAfter = now.Add(qqTasteRefillCooldown)
	}()

	if s.Authorized != nil && !s.Authorized(key.user, key.user) {
		return
	}
	query, cancel := context.WithTimeout(ctx, qqTasteRefillTimeout)
	defer cancel()
	profile, err := s.loadTasteProfile(query, key.user)
	if err != nil {
		return
	}
	// Treat the visible song and remaining stock as known while filling so
	// they don't occupy shortlist slots or return with a different ID.
	for _, choice := range pool.list(key) {
		track := choice.track
		title := ownershipTitle(track.Title)
		profile.known[title] = append(profile.known[title], track)
	}
	title := ownershipTitle(refill.shown.Title)
	profile.known[title] = append(profile.known[title], refill.shown)
	choices, _, _ = s.searchTasteChoices(query, refill.request, profile, []string{"qq"})
}
