package auth

import (
	"net/netip"
	"sync"
	"time"
)

// ClientKey is what the limiters count an address as: one client of IPv6 owns a whole
// /64, so rotating through its addresses does not hand out fresh allowances.
func ClientKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	if a = a.Unmap(); a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}

// Limiter blocks a key (client IP or username) after too many failed logins.
// Each repeated block doubles the block time, up to maxBlock.
type Limiter struct {
	mu       sync.Mutex
	entries  map[string]*limitEntry
	maxFails int
	window   time.Duration
	block    time.Duration
	maxBlock time.Duration
}

type limitEntry struct {
	fails        []time.Time
	blockedUntil time.Time
	strikes      int
	lastSeen     time.Time
}

func NewLimiter(maxFails int, window, block, maxBlock time.Duration) *Limiter {
	return &Limiter{entries: map[string]*limitEntry{}, maxFails: maxFails, window: window, block: block, maxBlock: maxBlock}
}

// Allowed reports whether an attempt may proceed and, if not, how long to wait.
func (l *Limiter) Allowed(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil || !now.Before(e.blockedUntil) {
		return true, 0
	}
	return false, e.blockedUntil.Sub(now)
}

// Reserve is Allowed and Fail in one step, under one lock: a blocked key is refused, any
// other attempt is counted at once. Checking first and counting after the password was
// hashed let any number of parallel attempts through the same open window; here only
// maxFails of them get past. A success hands its attempt back with Reset. blocked tells
// that this attempt used up the key's allowance.
func (l *Limiter) Reserve(key string, now time.Time) (ok bool, wait time.Duration, blocked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[key]; e != nil && now.Before(e.blockedUntil) {
		return false, e.blockedUntil.Sub(now), true
	}
	return true, 0, l.failLocked(key, now)
}

// Fail records a failed attempt and returns true if the key just became blocked.
func (l *Limiter) Fail(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failLocked(key, now)
}

func (l *Limiter) failLocked(key string, now time.Time) bool {
	e := l.entries[key]
	if e == nil {
		e = &limitEntry{}
		l.entries[key] = e
	}
	e.lastSeen = now
	cutoff := now.Add(-l.window)
	kept := e.fails[:0]
	for _, t := range e.fails {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	e.fails = append(kept, now)
	if len(e.fails) < l.maxFails {
		return false
	}
	d := l.block << e.strikes
	if d > l.maxBlock || d <= 0 {
		d = l.maxBlock
	}
	e.strikes++
	e.blockedUntil = now.Add(d)
	e.fails = e.fails[:0]
	return true
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// Sweep drops entries idle for longer than maxBlock so the map does not grow without bound.
func (l *Limiter) Sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, e := range l.entries {
		if now.After(e.blockedUntil) && now.Sub(e.lastSeen) > l.maxBlock {
			delete(l.entries, k)
		}
	}
}
