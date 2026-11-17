package subrelay

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLRUSweepAndPathIndexRemainConsistent(t *testing.T) {
	p, _ := New("https://panel.example", "key", "sub")
	p.SetCacheLimits(3, 4096)
	now := time.Unix(1800000000, 0)
	p.now = func() time.Time { return now }
	e := entry{body: []byte("profile"), header: http.Header{"Content-Type": []string{"text/plain"}}}
	p.remember("first", tokenPath, e, p.generation())
	expiring := entry{body: []byte("profile"), header: e.header.Clone()}
	expiring.header.Set("Subscription-Userinfo", fmt.Sprint("expire=", now.Add(time.Second).Unix()))
	p.remember("expires", otherToken, expiring, p.generation())
	p.remember("recent", tokenPath, e, p.generation())
	p.offline(httptest.NewRecorder(), httptest.NewRequest("GET", "https://sub.example/", nil), "first")
	p.remember("new", otherToken, e, p.generation())
	if _, ok := p.profiles["expires"]; ok {
		t.Fatal("did not evict least recently used entry")
	}
	p.remember("expires", otherToken, expiring, p.generation())
	now = now.Add(2 * time.Second)
	p.SweepExpired()
	if _, ok := p.profiles["expires"]; ok {
		t.Fatal("sweep retained expired entry")
	}
	if p.profileLRU.Len() != len(p.profiles) {
		t.Fatal("LRU and cache differ")
	}
	p.forget(otherToken, false)
	for _, v := range p.profiles {
		if v.path == otherToken {
			t.Fatal("path invalidation retained variant")
		}
	}
	if len(p.profilePaths[otherToken]) != 0 {
		t.Fatal("path index retained removed entries")
	}
	p.forget("", true)
	if p.profileLRU.Len() != 0 || len(p.profilePaths) != 0 || p.profileBytes != 0 {
		t.Fatal("global invalidation retained state")
	}
}
func TestOutageLogsAreRateLimitedWithoutDroppingCounters(t *testing.T) {
	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	defer slog.SetDefault(previous)
	p, _ := New("https://panel.example", "key", "sub")
	p.SetCircuitBreaker(0, 0)
	now := time.Unix(1800000000, 0)
	p.now = func() time.Time { return now }
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("outage") })}
	for range 100 {
		edgeFetch(p, "GET", tokenPath)
	}
	if got := strings.Count(log.String(), "subscription upstream unavailable"); got != 1 {
		t.Fatalf("logged %d times during outage", got)
	}
	now = now.Add(time.Minute)
	edgeFetch(p, "GET", tokenPath)
	if strings.Count(log.String(), "subscription upstream unavailable") != 2 || p.stats.failures.Load() != 101 {
		t.Fatal("log rate limit hid failures or did not recover")
	}
}
func TestOfflineRoamingIsOptInAndRequiresHWID(t *testing.T) {
	for _, on := range []bool{false, true} {
		for _, hwid := range []string{"", "known-device"} {
			t.Run(fmt.Sprint(on, hwid), func(t *testing.T) {
				p, _ := New("https://panel.example", "key", "sub")
				p.SetOfflineRoaming(on)
				p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return profileResponse(200), nil })}
				fetch := func(ip, device string) *httptest.ResponseRecorder {
					r := httptest.NewRequest("GET", "https://sub.example/sub"+tokenPath, nil)
					r.RemoteAddr = ip + ":1"
					r.Header.Set("X-Hwid", device)
					w := httptest.NewRecorder()
					p.ServeHTTP(w, r)
					return w
				}
				fetch("192.0.2.1", hwid)
				p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("X-Mikan-Client-IP") != "192.0.2.2" {
						t.Error("roaming changed the real IP sent to the panel")
					}
					return nil, errors.New("outage")
				})}
				w := fetch("192.0.2.2", hwid)
				if (w.Header().Get("X-Mikan-Offline") == "true") != (on && hwid != "") {
					t.Fatalf("unexpected roaming response %d %v", w.Code, w.Header())
				}
				if got := fetch("192.0.2.2", "another-device"); got.Header().Get("X-Mikan-Offline") != "" {
					t.Fatal("roaming shared a profile between different HWIDs")
				}
			})
		}
	}
}
func TestUpstreamRootDerivesSubscriptionRelay(t *testing.T) {
	p, err := New("https://panel.example:2053/", "key", "public-sub")
	if err != nil || p.upstream.Path != "/public-sub/relay" {
		t.Fatalf("unexpected relay URL: %v %v", p, err)
	}
}
func BenchmarkProfileCacheRemember(b *testing.B) {
	for _, size := range []int{256, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			p, _ := New("https://panel.example", "key", "sub")
			p.SetCacheLimits(size, 64<<20)
			e := entry{body: []byte("profile"), header: http.Header{"Content-Type": []string{"text/plain"}}}
			for i := range size {
				p.remember(fmt.Sprint(i), tokenPath, e, p.generation())
			}
			i := size
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				p.remember(fmt.Sprint(i), tokenPath, e, p.generation())
				i++
			}
		})
	}
}
