package subrelay

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOfflineProfiles(t *testing.T) {
	now := time.Unix(1800000000, 0)
	p, _ := New("https://panel.example/sub/relay", "key", "sub")
	p.SetCircuitBreaker(0, 0)
	p.now = func() time.Time { return now }
	status := 200
	fail := false
	expiry := ""
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if fail {
			return nil, errors.New("panel unreachable")
		}
		h := http.Header{"Content-Type": []string{"text/plain"}, RelayAuthHeader: []string{"1"}, RelayProofHeader: []string{"test-issued-proof"}}
		if expiry != "" {
			h.Set("Subscription-Userinfo", "expire="+expiry)
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader("profile:" + r.Header.Get("X-Hwid")))}, nil
	})}
	get := func(path, hwid, ua, ip string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://sub.example/sub"+path, nil)
		r.RemoteAddr = ip + ":1234"
		r.Header.Set("X-Hwid", hwid)
		r.Header.Set("User-Agent", ua)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, code int, offline bool) {
		t.Helper()
		if w.Code != code || (w.Header().Get("X-Mikan-Offline") == "true") != offline {
			t.Fatalf("code=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
		}
	}
	check(get(tokenPath, "a", "mihomo", "192.0.2.1"), 200, false)
	fail = true
	check(get(tokenPath, "a", "mihomo", "192.0.2.1"), 200, true)
	for _, variant := range []struct{ path, hwid, ua, ip string }{
		{tokenPath, "b", "mihomo", "192.0.2.1"},
		{tokenPath, "a", "sing-box", "192.0.2.1"},
		{tokenPath + "?format=singbox", "a", "mihomo", "192.0.2.1"},
		{tokenPath, "a", "mihomo", "192.0.2.2"},
		{"/zyxwvutsrqponmlkjihgfedc", "a", "mihomo", "192.0.2.1"},
		{tokenPath + "/info", "a", "mihomo", "192.0.2.1"},
	} {
		check(get(variant.path, variant.hwid, variant.ua, variant.ip), 502, false)
	}
	fail = false
	status = 503
	check(get(tokenPath, "a", "mihomo", "192.0.2.1"), 200, true)
	status = 404
	check(get(tokenPath, "a", "mihomo", "192.0.2.1"), 404, false)
	fail = true
	check(get(tokenPath, "a", "mihomo", "192.0.2.1"), 502, false)
	fail = false
	status = 200
	check(get(tokenPath, "a", "mihomo", "192.0.2.1"), 200, false)
	now = now.Add(time.Hour + time.Second)
	fail = true
	check(get(tokenPath, "a", "mihomo", "192.0.2.1"), 502, false)
	fail = false
	expiry = fmt.Sprint(now.Add(time.Minute).Unix())
	check(get(tokenPath, "a", "mihomo", "192.0.2.1"), 200, false)
	now = now.Add(2 * time.Minute)
	fail = true
	check(get(tokenPath, "a", "mihomo", "192.0.2.1"), 502, false)
	fail = false
	expiry = ""
	p.SetOfflineTTL(0)
	check(get(tokenPath, "a", "mihomo", "192.0.2.1"), 200, false)
	fail = true
	check(get(tokenPath, "a", "mihomo", "192.0.2.1"), 502, false)
}

func TestProfileCacheLimitsAndInvalidation(t *testing.T) {
	p, _ := New("https://panel.example/sub/relay", "key", "sub")
	p.SetCircuitBreaker(0, 0)
	e := entry{body: make([]byte, 1<<20), header: http.Header{"Content-Type": []string{"text/plain"}}}
	for i := range 300 {
		p.remember(fmt.Sprint(i), tokenPath, e, p.generation())
	}
	if len(p.profiles) > maxProfiles || p.profileBytes > maxProfileBytes {
		t.Fatal("unbounded cache")
	}
	p.forget(tokenPath, true)
	if len(p.profiles) != 0 || p.profileBytes != 0 {
		t.Fatal("invalidation failed")
	}
	e.header.Set("Content-Type", "text/html")
	p.remember("html", tokenPath, e, p.generation())
	if len(p.profiles) != 0 {
		t.Fatal("cached browser page")
	}
}

func TestProfileCacheConcurrent(t *testing.T) {
	p, _ := New("https://panel.example/sub/relay", "key", "sub")
	p.SetCircuitBreaker(0, 0)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			key := fmt.Sprint(i)
			for range 100 {
				p.remember(key, tokenPath, entry{body: []byte("profile"), header: http.Header{"Content-Type": []string{"text/plain"}}}, p.generation())
				p.offline(httptest.NewRecorder(), httptest.NewRequest("GET", "https://sub.example/", nil), key)
				p.forget(tokenPath, true)
			}
		})
	}
	wg.Wait()
}

func TestRefusalPreventsInflightCacheResurrection(t *testing.T) {
	p, _ := New("https://panel.example/sub/relay", "key", "sub")
	p.SetCircuitBreaker(0, 0)
	generation := p.generation()
	p.forget(tokenPath, true)
	p.remember("late", tokenPath, entry{body: []byte("old profile"), header: http.Header{"Content-Type": []string{"text/plain"}}}, generation)
	if len(p.profiles) != 0 {
		t.Fatal("inflight response restored a refused profile")
	}
}

func TestOfflineRefusesExplicitAuthorizationErrors(t *testing.T) {
	for _, status := range []int{401, 403, 404, 410, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			p, _ := New("https://panel.example/sub/relay", "key", "sub")
			p.SetCircuitBreaker(0, 0)
			code := 200
			p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{"text/plain"}, RelayAuthHeader: []string{"1"}, RelayProofHeader: []string{"test-issued-proof"}}, Body: io.NopCloser(strings.NewReader("profile"))}, nil
			})}
			fetch := func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				p.ServeHTTP(w, httptest.NewRequest("GET", "https://sub.example/sub"+tokenPath, nil))
				return w
			}
			if fetch().Code != 200 || len(p.profiles) != 1 {
				t.Fatal("profile was not saved")
			}
			code = status
			if w := fetch(); w.Code != status || w.Header().Get("X-Mikan-Offline") != "" {
				t.Fatal("masked explicit refusal")
			}
			code = 503
			if w := fetch(); w.Code != 503 || w.Header().Get("X-Mikan-Offline") != "" {
				t.Fatal("reused refused profile")
			}
		})
	}
}
