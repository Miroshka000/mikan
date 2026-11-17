package subrelay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxProfiles = 256
const maxProfileBytes = 64 << 20

type savedProfile struct {
	entry
	path    string
	savedAt time.Time
}

// SetOfflineTTL bounds how long a previously validated profile may survive a panel
// outage. Configure before serving requests; zero disables offline delivery.
func (p *Proxy) SetOfflineTTL(ttl time.Duration) { p.offlineTTL = ttl }

func profileRequest(r *http.Request, rel string) bool {
	return r.Method == http.MethodGet && len(rel) == 25 && subscriber.MatchString(rel)
}

// Include every forwarded input that can affect rendering or device authorization.
// Hashing keeps subscriber tokens, device IDs and client addresses out of map keys.
func profileKey(req *http.Request, host string) string {
	data, _ := json.Marshal(struct {
		URL, Host string
		Headers   http.Header
	}{req.URL.String(), host, req.Header})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (p *Proxy) generation() uint64 { p.mu.Lock(); defer p.mu.Unlock(); return p.epoch }

func (p *Proxy) remember(key, path string, e entry, generation uint64) {
	contentType, _, _ := mime.ParseMediaType(e.header.Get("Content-Type"))
	if p.offlineTTL <= 0 || len(e.body) == 0 || len(e.body) > 1<<20 || (contentType != "text/plain" && contentType != "text/yaml" && contentType != "application/json") {
		p.forget(path, false)
		return
	}
	e.until = p.now().Add(p.offlineTTL)
	// A subscription must never outlive its declared account expiry, even offline.
	for _, field := range strings.Split(e.header.Get("Subscription-Userinfo"), ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if ok && name == "expire" {
			seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil || seconds < 0 {
				p.forget(path, false)
				return
			}
			if seconds > 0 && time.Unix(seconds, 0).Before(e.until) {
				e.until = time.Unix(seconds, 0)
			}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.epoch != generation {
		return
	}
	if old, ok := p.profiles[key]; ok {
		p.profileBytes -= profileSize(old.entry)
		delete(p.profiles, key)
	}
	for k, v := range p.profiles {
		if !p.now().Before(v.until) {
			p.profileBytes -= profileSize(v.entry)
			delete(p.profiles, k)
		}
	}
	if !p.now().Before(e.until) {
		return
	}
	for len(p.profiles) >= maxProfiles || p.profileBytes+profileSize(e) > maxProfileBytes {
		for k, v := range p.profiles {
			p.profileBytes -= profileSize(v.entry)
			delete(p.profiles, k)
			break
		}
	}
	p.profiles[key] = savedProfile{entry: e, path: path, savedAt: p.now()}
	p.profileBytes += profileSize(e)
}

func (p *Proxy) forget(path string, all bool) {
	if len(path) >= 25 && subscriber.MatchString(path) {
		path = path[:25]
	}
	p.mu.Lock()
	p.epoch++
	defer p.mu.Unlock()
	for k, v := range p.profiles {
		if all || v.path == path {
			p.profileBytes -= profileSize(v.entry)
			delete(p.profiles, k)
		}
	}
}

func (p *Proxy) offline(w http.ResponseWriter, r *http.Request, key string) bool {
	if key == "" || p.offlineTTL <= 0 || r.Context().Err() != nil {
		return false
	}
	p.mu.Lock()
	e, ok := p.profiles[key]
	if ok && !p.now().Before(e.until) {
		p.profileBytes -= profileSize(e.entry)
		delete(p.profiles, key)
		ok = false
	}
	p.mu.Unlock()
	if !ok {
		return false
	}
	for name, values := range e.header {
		w.Header()[name] = values
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Mikan-Offline", "true")
	p.stats.offline.Add(1)
	_, _ = w.Write(e.body)
	return true
}

// Account for header payload as well as profile bodies in the memory budget.
func profileSize(e entry) int {
	size := len(e.body)
	for name, values := range e.header {
		size += len(name)
		for _, value := range values {
			size += len(value)
		}
	}
	return size
}
