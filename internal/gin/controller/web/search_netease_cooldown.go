package web

import (
	"sync"
	"time"
)

const neteaseSearchCooldownDuration = 2 * time.Minute

type neteaseSearchCooldown struct {
	mu    sync.Mutex
	until time.Time
}

func (c *neteaseSearchCooldown) block(now time.Time, duration time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	until := now.Add(duration)
	if until.After(c.until) {
		c.until = until
	}
	return c.until
}

func (c *neteaseSearchCooldown) remaining(now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !now.Before(c.until) {
		return 0
	}
	return c.until.Sub(now)
}

var neteaseSearchCircuit = &neteaseSearchCooldown{}
