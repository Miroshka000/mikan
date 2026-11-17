package subrelay

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
	upstream      *url.URL
	token, prefix string
	client        *http.Client
	mu            sync.Mutex
	cache         map[string]entry
	profiles      map[string]savedProfile
	profileBytes  int
	inflight      chan struct{}
	breaker       breaker
	stats         counters
	epoch         uint64
	offlineTTL    time.Duration
	now           func() time.Time
}

func New(upstream, token, prefix string) (*Proxy, error) {
	u, err := url.Parse(upstream)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("upstream must be an HTTPS subscription-relay URL")
	}
	if token == "" || strings.ContainsAny(token, "\r\n") || prefix == "" || strings.ContainsAny(prefix, "/\\%?#") {
		return nil, fmt.Errorf("token and a single public path segment are required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxResponseHeaderBytes = 64 << 10
	return &Proxy{upstream: u, token: token, prefix: "/" + prefix, client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, cache: map[string]entry{}, profiles: map[string]savedProfile{}, inflight: make(chan struct{}, 16), offlineTTL: time.Hour, breaker: breaker{threshold: 3, cooldown: 10 * time.Second}, now: time.Now}, nil
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
	// Bound concurrent request/response buffers under load. Cache hits need no slot.
	select {
	case p.inflight <- struct{}{}:
		defer func() { <-p.inflight }()
	default:
		p.stats.rejected.Add(1)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "subscription service busy", 503)
		return
	}
	u := *p.upstream
	u.Path = strings.TrimRight(u.Path, "/") + rel
	u.RawPath, u.RawQuery = "", r.URL.RawQuery
	bodyReader := r.Body
	if bodyReader == nil {
		bodyReader = http.NoBody
	}
	requestBody, err := io.ReadAll(http.MaxBytesReader(w, bodyReader, 1<<20))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request too large", 413)
		} else {
			http.Error(w, "bad request", 400)
		}
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), bytes.NewReader(requestBody))
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	// An allowlist keeps cookies, admin credentials and spoofed forwarding headers out.
	for _, name := range []string{"User-Agent", "Accept", "Accept-Language", "Content-Type", "Origin", "Sec-Fetch-Site", "X-Hwid", "X-Device-Os", "X-Ver-Os", "X-Device-Model"} {
		if v := r.Header.Values(name); len(v) > 0 {
			req.Header[name] = v
		}
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	req.Header.Set("X-Mikan-Client-IP", ip)
	key := ""
	if profileRequest(r, rel) {
		key = profileKey(req, r.Host)
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
	generation := p.generation()
	resp, err := p.client.Do(req)
	if err != nil {
		failed = true
		p.stats.failures.Add(1)
		if p.offline(w, r, key) {
			return
		}
		http.Error(w, "subscription service unavailable", 502)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
		failed = true
		p.stats.failures.Add(1)
		if p.offline(w, r, key) {
			return
		}
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		p.forget(rel, true)
	} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		p.forget(rel, false)
	}
	// Buffer before sending status so truncated/oversized upstream bodies become a 502.
	body, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(body) > 8<<20 {
		if !failed {
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
		} else if rel != "/tg/session" && rel != "/tg/shop" && rel != "/tg/promo-history" {
			p.forget(rel, true)
		}
	}
	h := resp.Header.Clone()
	for _, name := range strings.Split(h.Get("Connection"), ",") {
		h.Del(strings.TrimSpace(name))
	}
	for _, name := range []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Set-Cookie", "Server"} {
		h.Del(name)
	}
	e := entry{body: body, header: h, until: p.now().Add(5 * time.Minute)}
	if key != "" && resp.StatusCode == 200 {
		p.remember(key, rel, e, generation)
	}
	if cacheable && resp.StatusCode == 200 && len(body) <= 1<<20 {
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
