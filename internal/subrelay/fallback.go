package subrelay

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	lru     *list.Element
}

// SetOfflineTTL bounds how long a previously validated profile may survive a panel
// outage. Configure before serving requests; zero disables offline delivery.
func (p *Proxy) SetOfflineTTL(ttl time.Duration) { p.offlineTTL = ttl }

// SetCacheLimits bounds saved device variants and their combined body/header bytes.
// Configure before serving requests. Both limits must be positive.
func (p *Proxy) SetCacheLimits(entries, bytes int) error {
	if entries <= 0 || bytes <= 0 {
		return fmt.Errorf("cache limits must be positive")
	}
	p.profileLimit, p.byteLimit = entries, bytes
	return nil
}

// SetOfflineRoaming is opt-in: use only without IP-dependent access/response rules.
// Nonempty HWIDs keep device variants separate while their network address changes.
func (p *Proxy) SetOfflineRoaming(on bool) { p.offlineRoaming = on }
func (p *Proxy) profileCacheKey(req *http.Request, host string) string {
	if !p.offlineRoaming || req.Header.Get("X-Hwid") == "" {
		return profileKey(req, host)
	}
	clone := req.Clone(req.Context())
	clone.Header.Del("X-Mikan-Client-IP")
	return profileKey(clone, host)
}

func profilePath(path string) string {
	if len(path) >= 25 && subscriber.MatchString(path) {
		return path[:25]
	}
	return ""
}

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

// Only active fetches need a per-subscription invalidation version. The request
// concurrency limit bounds both maps, and unknown tokens cannot flush other fetches.
func (p *Proxy) beginFetch(path string) (uint64, func()) {
	p.mu.Lock()
	p.activeProfiles[path]++
	generation := p.epoch
	p.mu.Unlock()
	return generation, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.activeProfiles[path]--
		if p.activeProfiles[path] == 0 {
			delete(p.activeProfiles, path)
			delete(p.invalidated, path)
		}
	}
}

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
	if p.invalidationFloor > generation || p.invalidated[path] > generation {
		return
	}
	p.removeProfile(key)
	if !p.now().Before(e.until) || profileSize(e) > p.byteLimit {
		return
	}
	for len(p.profiles) >= p.profileLimit || p.profileBytes+profileSize(e) > p.byteLimit {
		p.removeProfile(p.profileLRU.Back().Value.(string))
	}
	node := p.profileLRU.PushFront(key)
	p.profiles[key] = savedProfile{entry: e, path: path, savedAt: p.now(), lru: node}
	if p.profilePaths[path] == nil {
		p.profilePaths[path] = map[string]struct{}{}
	}
	p.profilePaths[path][key] = struct{}{}
	p.profileBytes += profileSize(e)
}

func (p *Proxy) forget(path string, all bool) {
	path = profilePath(path)
	if !all && path == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.epoch++
	if all {
		p.invalidationFloor = p.epoch
		clear(p.invalidated)
	} else if p.activeProfiles[path] > 0 {
		p.invalidated[path] = p.epoch
	}

	if all {
		clear(p.profiles)
		clear(p.profilePaths)
		p.profileLRU.Init()
		p.profileBytes = 0
	} else {
		for key := range p.profilePaths[path] {
			p.removeProfile(key)
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
		p.removeProfile(key)
		ok = false
	}
	if ok {
		p.profileLRU.MoveToFront(e.lru)
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

// removeProfile is called with mu held. Entry, path index and LRU stay in sync.
func (p *Proxy) removeProfile(key string) {
	e, ok := p.profiles[key]
	if !ok {
		return
	}
	delete(p.profiles, key)
	p.profileBytes -= profileSize(e.entry)
	p.profileLRU.Remove(e.lru)
	delete(p.profilePaths[e.path], key)
	if len(p.profilePaths[e.path]) == 0 {
		delete(p.profilePaths, e.path)
	}
}

// SweepExpired runs periodically, not for every successful fetch.
func (p *Proxy) SweepExpired() {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for key, e := range p.profiles {
		if !now.Before(e.until) {
			p.removeProfile(key)
		}
	}
}
func (p *Proxy) RunMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.SweepExpired()
		}
	}
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
