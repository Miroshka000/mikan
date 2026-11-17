package subrelay

import "time"

// Breaker state shares Proxy.mu with cache state. Epochs keep requests which
// started before opening from releasing or overwriting a later recovery probe.
type breaker struct {
	failures, threshold int
	cooldown            time.Duration
	until               time.Time
	probing             bool
	epoch               uint64
}

type probePermit struct {
	allowed bool
	epoch   uint64
}

// SetCircuitBreaker configures before serving. Zero disables the breaker.
func (p *Proxy) SetCircuitBreaker(threshold int, cooldown time.Duration) {
	p.breaker = breaker{threshold: threshold, cooldown: cooldown}
}

func (p *Proxy) beginProbe() probePermit {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := &p.breaker
	if b.threshold <= 0 || b.until.IsZero() {
		return probePermit{true, b.epoch}
	}
	if p.now().Before(b.until) || b.probing {
		return probePermit{}
	}
	b.probing = true
	return probePermit{true, b.epoch}
}

func (p *Proxy) finishProbe(permit probePermit, failed, cancelled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := &p.breaker
	if b.threshold <= 0 || !permit.allowed || permit.epoch != b.epoch {
		return
	}
	if cancelled {
		b.probing = false
		return
	}
	if failed {
		b.failures++
		if b.failures >= b.threshold {
			b.until = p.now().Add(b.cooldown)
			b.epoch++
		}
	} else {
		if !b.until.IsZero() {
			b.epoch++
		}
		b.failures = 0
		b.until = time.Time{}
	}
	b.probing = false
}
