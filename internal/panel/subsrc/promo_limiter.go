package subsrc

import (
	"strings"
	"sync"
	"time"
)

const (
	promoAttemptLimit  = 5
	promoAttemptWindow = 10 * time.Minute
	promoBlockDuration = 15 * time.Minute
)

type promoAttempt struct {
	attempts int
	window   time.Time
	blocked  time.Time
	codes    map[string]struct{}
}

// promoLimiter caps distinct promo codes per Telegram account while allowing repeated
// previews and checkouts of the same normalized code. Old entries are pruned as requests arrive.
type promoLimiter struct {
	mu       sync.Mutex
	accounts map[int64]promoAttempt
}

func (l *promoLimiter) allow(tgID int64, now time.Time, code string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.accounts == nil {
		l.accounts = make(map[int64]promoAttempt)
	}
	l.prune(now)
	a := l.accounts[tgID]
	if now.Before(a.blocked) {
		return false
	}
	if a.window.IsZero() || !now.Before(a.window.Add(promoAttemptWindow)) {
		a = promoAttempt{window: now, codes: make(map[string]struct{})}
	}
	code = strings.ToUpper(strings.Join(strings.Fields(code), ""))
	if _, seen := a.codes[code]; seen {
		l.accounts[tgID] = a
		return true
	}
	if a.codes == nil {
		a.codes = make(map[string]struct{})
	}
	a.codes[code] = struct{}{}
	a.attempts++
	if a.attempts >= promoAttemptLimit {
		a.blocked = now.Add(promoBlockDuration)
	}
	l.accounts[tgID] = a
	return true
}

func (l *promoLimiter) prune(now time.Time) {
	for id, a := range l.accounts {
		if !now.Before(a.blocked) && (a.window.IsZero() || !now.Before(a.window.Add(promoAttemptWindow))) {
			delete(l.accounts, id)
		}
	}
}
