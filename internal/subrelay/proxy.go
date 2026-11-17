package subrelay

import (
	"bytes"
	"container/list"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

type entry struct {
	body   []byte
	header http.Header
	until  time.Time
}

// Proxy normally validates every profile with the panel. Bounded saved profiles
// are used only when the panel cannot answer, never in place of an explicit refusal.
type Proxy struct {
	upstream                *url.URL
	token, prefix           string
	client                  *http.Client
	mu                      sync.Mutex
	cache                   map[string]entry
	profiles                map[string]savedProfile
	profileBytes            int
	inflight                chan struct{}
	uploads                 chan struct{}
	breaker                 breaker
	stats                   counters
	epoch                   uint64
	invalidated             map[string]uint64
	invalidationFloor       uint64
	activeProfiles          map[string]int
	profileLimit, byteLimit int
	profileLRU              list.List
	profilePaths            map[string]map[string]struct{}
	offlineRoaming          bool
	lastUpstreamWarning     time.Time
	trustedProxies          []netip.Prefix
	clientIPHeader          string
	relayProof              string
	authWarning             sync.Once
	revokedWarning          sync.Once
	offlineTTL              time.Duration
	now                     func() time.Time
}

func New(upstream, token, prefix string) (*Proxy, error) {
	u, err := url.Parse(upstream)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("upstream must be an HTTPS subscription-relay URL")
	}
	if token == "" || strings.ContainsAny(token, "\r\n") || prefix == "" || strings.ContainsAny(prefix, "/\\%?#") {
		return nil, fmt.Errorf("token and a single public path segment are required")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/" + prefix + "/relay"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxResponseHeaderBytes = 64 << 10
	return &Proxy{upstream: u, token: token, prefix: "/" + prefix, client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, cache: map[string]entry{}, profiles: map[string]savedProfile{}, profilePaths: map[string]map[string]struct{}{}, invalidated: map[string]uint64{}, activeProfiles: map[string]int{}, profileLimit: maxProfiles, byteLimit: maxProfileBytes, inflight: make(chan struct{}, 16), uploads: make(chan struct{}, 16), offlineTTL: time.Hour, breaker: breaker{threshold: 3, cooldown: 10 * time.Second}, now: time.Now}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.stats.requests.Add(1)
	if !strings.HasPrefix(r.URL.Path, p.prefix+"/") {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, p.prefix)
	if !Allowed(r.Method, rel) {
		http.NotFound(w, r)
		return
	}
	// Query parameters can alter a response; only query-free assets enter the cache.
	cacheable := r.Method == http.MethodGet && Static(rel) && r.URL.RawQuery == ""
	if cacheable {
		p.mu.Lock()
		e, ok := p.cache[rel]
		p.mu.Unlock()
		if ok && p.now().Before(e.until) {
			write(w, e)
			return
		}
	}
	u := *p.upstream
	u.Path = strings.TrimRight(u.Path, "/") + rel
	u.RawPath, u.RawQuery = "", r.URL.RawQuery
	bodyReader := r.Body
	if bodyReader == nil {
		bodyReader = http.NoBody
	}
	// HTTP/2 may supply a non-NoBody reader for an already-ended, zero-length stream.
	// Only requests which can contain bytes consume the separate bounded upload budget.
	var requestBody []byte
	if bodyReader != http.NoBody && r.ContentLength != 0 {
		select {
		case p.uploads <- struct{}{}:
			defer func() { <-p.uploads }()
		default:
			p.busy(w)
			return
		}
		var err error
		requestBody, err = io.ReadAll(http.MaxBytesReader(w, bodyReader, 1<<20))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "request too large", 413)
			} else {
				http.Error(w, "bad request", 400)
			}
			return
		}
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), bytes.NewReader(requestBody))
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	// Preserve custom response-rule inputs while removing credentials and proxy metadata.
	req.Header = forwardedHeaders(r.Header)
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("X-Mikan-Client-IP", p.clientIP(r))
	select {
	case p.inflight <- struct{}{}:
		defer func() { <-p.inflight }()
	default:
		p.busy(w)
		return
	}
	key := ""
	if profileRequest(r, rel) {
		key = p.profileCacheKey(req, r.Host)
	}
	tracked := key != ""
	permit := probePermit{}
	if tracked {
		permit = p.beginProbe()
	}
	if tracked && !permit.allowed {
		p.stats.skipped.Add(1)
		if p.offline(w, r, key) {
			return
		}
		w.Header().Set("Retry-After", "1")
		http.Error(w, "subscription service unavailable", 503)
		return
	}
	failed := false
	if tracked {
		defer func() { p.finishProbe(permit, failed, r.Context().Err() != nil) }()
	}
	p.stats.upstream.Add(1)
	var generation uint64
	if tracked {
		var release func()
		generation, release = p.beginFetch(rel)
		defer release()
	}
	resp, err := p.client.Do(req)
	if err != nil {
		failed = true
		p.stats.failures.Add(1)
		p.warnUpstreamUnavailable()
		if p.offline(w, r, key) {
			return
		}
		http.Error(w, "subscription service unavailable", 502)
		return
	}
	defer resp.Body.Close()
	stripHopHeaders(resp.Header)
	authenticated := resp.Header.Get(RelayAuthHeader) == "1"
	proof := resp.Header.Get(RelayProofHeader)
	cacheAuthorized := authenticated && proof != "" && len(proof) <= 128
	if cacheAuthorized {
		p.mu.Lock()
		p.relayProof = proof
		p.mu.Unlock()
	}
	if authenticated {
		if resp.Header.Get(RelayActionHeader) == "drop" {
			p.forget(rel, false)
			panic(http.ErrAbortHandler)
		}
		for _, path := range strings.Split(resp.Header.Get(RelayInvalidateHeader), ",") {
			if profilePath(path) != "" {
				p.forget(path, false)
			}
		}

	}

	// 502, 503 and 504: the panel is down or out of reach, and the breaker counts them. A 500
	// is the panel failing this one request (one subscription's profile, say): the saved copy
	// answers it all the same, but the breaker leaves it out, or one broken profile would
	// shut the panel off for everybody.
	outage := resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504
	counted := false
	if outage || resp.StatusCode == http.StatusInternalServerError {
		failed, counted = outage, true
		p.stats.failures.Add(1)
		if p.offline(w, r, key) {
			return
		}
	}
	if (!authenticated || !cacheAuthorized) && (resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode >= 400 && resp.StatusCode < 500) {
		p.authWarning.Do(func() {
			slog.Warn("subscription upstream omitted relay authentication metadata; check the key and proxy routing")
		})
	}
	if !authenticated && (resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode >= 400 && resp.StatusCode < 500) && p.credentialsRejected(r.Context()) {
		p.revokedWarning.Do(func() { slog.Warn("subscription relay credential rejected; check the dedicated key") })
		p.forget(rel, true)
	} else if resp.StatusCode >= 400 && resp.StatusCode < 500 || !authenticated && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		p.forget(rel, false)
	}
	// Buffer before sending status so truncated/oversized upstream bodies become a 502.
	body, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(body) > 8<<20 {
		if !counted {
			p.stats.failures.Add(1)
		}
		failed = true
		if p.offline(w, r, key) {
			return
		}
		http.Error(w, "subscription service unavailable", 502)
		return
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		http.Error(w, "unexpected upstream redirect", 502)
		return
	}
	if r.Method == http.MethodPost && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Read-only Mini App POSTs must not flush everybody's offline cache.
		if subscriber.MatchString(rel) {
			p.forget(rel, false)
		}
	}
	h := resp.Header.Clone()
	for _, name := range []string{"Set-Cookie", "Server", RelayAuthHeader, RelayProofHeader, RelayNonceHeader, RelayActionHeader, RelayInvalidateHeader, "X-Mikan-Relay-Error"} {
		h.Del(name)
	}
	e := entry{body: body, header: h, until: p.now().Add(5 * time.Minute)}
	if key != "" && resp.StatusCode == 200 && cacheAuthorized {
		p.remember(key, rel, e, generation)
	}
	if cacheable && authenticated && resp.StatusCode == 200 && len(body) <= 1<<20 {
		p.mu.Lock()
		if len(p.cache) >= 128 {
			clear(p.cache)
		}
		p.cache[rel] = e
		p.mu.Unlock()
	}
	for name, values := range h {
		w.Header()[name] = values
	}
	// CDN caches must not store a profile, even when the upstream forgot the header.
	if !Static(rel) {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, bytes.NewReader(body))
}

func write(w http.ResponseWriter, e entry) {
	for name, values := range e.header {
		w.Header()[name] = values
	}
	_, _ = w.Write(e.body)
}

func (p *Proxy) busy(w http.ResponseWriter) {
	p.stats.rejected.Add(1)
	w.Header().Set("Retry-After", "1")
	http.Error(w, "subscription service busy", 503)
}

// Outage traffic must not turn stderr into an unbounded per-request work queue.
func (p *Proxy) warnUpstreamUnavailable() {
	now := p.now()
	p.mu.Lock()
	emit := p.lastUpstreamWarning.IsZero() || now.Sub(p.lastUpstreamWarning) >= time.Minute || now.Before(p.lastUpstreamWarning)
	if emit {
		p.lastUpstreamWarning = now
	}
	p.mu.Unlock()
	if emit {
		slog.Warn("subscription upstream unavailable", "status", 502)
	}
}
