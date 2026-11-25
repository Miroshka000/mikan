package subrelay

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

type counters struct {
	requests, upstream, failures, offline, skipped, rejected atomic.Uint64
}

// Metrics returns Prometheus text without subscriber, device, host or URL labels.
// Expose on a private listener, never on the public subscription route.
func (p *Proxy) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" || r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		for _, m := range []struct {
			name  string
			value uint64
		}{
			{"requests_total", p.stats.requests.Load()}, {"upstream_requests_total", p.stats.upstream.Load()},
			{"upstream_failures_total", p.stats.failures.Load()}, {"offline_responses_total", p.stats.offline.Load()},
			{"circuit_skipped_total", p.stats.skipped.Load()}, {"overload_rejections_total", p.stats.rejected.Load()},
		} {
			fmt.Fprintf(w, "# TYPE mikan_sub_%s counter\nmikan_sub_%s %d\n", m.name, m.name, m.value)
		}
		p.mu.Lock()
		count, bytes := 0, 0
		oldestAge := 0.0
		now := p.now()
		for _, e := range p.profiles {
			if now.Before(e.until) {
				oldestAge = max(oldestAge, now.Sub(e.savedAt).Seconds())
				count++
				bytes += profileSize(e.entry)
			}
		}
		open := 0
		if !p.breaker.until.IsZero() {
			open = 1
		}
		p.mu.Unlock()
		fmt.Fprintf(w, "# TYPE mikan_sub_oldest_profile_age_seconds gauge\nmikan_sub_oldest_profile_age_seconds %.3f\n", oldestAge)
		fmt.Fprintf(w, "# TYPE mikan_sub_cached_profiles gauge\nmikan_sub_cached_profiles %d\n# TYPE mikan_sub_cache_bytes gauge\nmikan_sub_cache_bytes %d\n# TYPE mikan_sub_circuit_open gauge\nmikan_sub_circuit_open %d\n", count, bytes, open)
	})
}
