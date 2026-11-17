package subrelay

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const otherToken = "/zyxwvutsrqponmlkjihgfedc"

func edgeFetch(p *Proxy, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest(method, "https://sub.example/sub"+path, nil))
	return w
}
func profileResponse(code int) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("validated profile"))}
}
func TestMiniAppAndDeviceRefusalsKeepOtherProfiles(t *testing.T) {
	for _, tc := range []struct {
		path   string
		status int
	}{{"/tg/session", 401}, {otherToken + "/devices/1/unbind", 403}} {
		t.Run(tc.path, func(t *testing.T) {
			p, _ := New("https://panel.example/secret/api/subscription", "key", "sub")
			offline := false
			p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if offline {
					return nil, errors.New("outage")
				}
				if r.Method == "POST" {
					return profileResponse(tc.status), nil
				}
				return profileResponse(200), nil
			})}
			edgeFetch(p, "GET", tokenPath)
			if got := edgeFetch(p, "POST", tc.path).Code; got != tc.status {
				t.Fatal(got)
			}
			offline = true
			if w := edgeFetch(p, "GET", tokenPath); w.Header().Get("X-Mikan-Offline") != "true" {
				t.Fatalf("unrelated refusal erased profile: %d", w.Code)
			}
		})
	}
}
func TestUnrelated404DoesNotDropInflightProfile(t *testing.T) {
	p, _ := New("https://panel.example/secret/api/subscription", "key", "sub")
	started, release := make(chan struct{}), make(chan struct{})
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, tokenPath) {
			close(started)
			<-release
			return profileResponse(200), nil
		}
		return profileResponse(404), nil
	})}
	var wg sync.WaitGroup
	wg.Go(func() { edgeFetch(p, "GET", tokenPath) })
	<-started
	for range 1100 {
		edgeFetch(p, "GET", otherToken)
	}
	close(release)
	wg.Wait()
	if len(p.profiles) != 1 {
		t.Fatal("unrelated 404 prevented cache save")
	}
}
func TestOneBrokenProfileDoesNotOpenCircuitForEveryone(t *testing.T) {
	p, _ := New("https://panel.example/secret/api/subscription", "key", "sub")
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, otherToken) {
			return profileResponse(500), nil
		}
		return profileResponse(200), nil
	})}
	for range 3 {
		if w := edgeFetch(p, "GET", otherToken); w.Code != 500 {
			t.Fatal(w.Code)
		}
	}
	if w := edgeFetch(p, "GET", tokenPath); w.Code != 200 {
		t.Fatalf("isolated 500 opened shared breaker: %d", w.Code)
	}
}
func TestRelayCredentialFailureInvalidatesAll(t *testing.T) {
	p, _ := New("https://panel.example/secret/api/subscription", "key", "sub")
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return profileResponse(200), nil })}
	edgeFetch(p, "GET", tokenPath)
	edgeFetch(p, "GET", otherToken)
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := profileResponse(404)
		resp.Header.Set(RelayErrorHeader, "credential")
		return resp, nil
	})}
	w := edgeFetch(p, "GET", otherToken)
	if len(p.profiles) != 0 || w.Header().Get(RelayErrorHeader) != "" {
		t.Fatal("credential revocation was not private and global")
	}
}
func TestIntentionalDropDoesNotUseOfflineProfile(t *testing.T) {
	p, _ := New("https://panel.example/secret/api/subscription", "key", "sub")
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return profileResponse(200), nil })}
	edgeFetch(p, "GET", tokenPath)
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := profileResponse(204)
		resp.Header.Set(RelayActionHeader, "drop")
		return resp, nil
	})}
	func() {
		defer func() {
			if v := recover(); v != http.ErrAbortHandler {
				t.Fatalf("drop was not preserved: %v", v)
			}
		}()
		edgeFetch(p, "GET", tokenPath)
	}()
	if len(p.profiles) != 0 || p.breaker.failures != 0 {
		t.Fatal("intentional drop cached or counted as outage")
	}
}
func TestTrustedProxyIPsAndCustomRuleHeaders(t *testing.T) {
	for _, tc := range []struct{ peer, header, value, want string }{
		{"192.0.2.2:3", "X-Forwarded-For", "203.0.113.7", "192.0.2.2"},
		{"10.0.0.2:3", "X-Forwarded-For", "1.1.1.1, 203.0.113.7, 10.0.0.3", "203.0.113.7"},
		{"10.0.0.2:3", "CF-Connecting-IP", "203.0.113.7", "203.0.113.7"},
		{"10.0.0.2:3", "X-Forwarded-For", "invalid", "10.0.0.2"},
	} {
		p, _ := New("https://panel.example/secret/api/subscription", "key", "sub")
		if err := p.SetTrustedProxies("10.0.0.0/8", tc.header); err != nil {
			t.Fatal(err)
		}
		p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("X-Mikan-Client-IP") != tc.want || r.Header.Get("X-Custom-Rule") != "match" || r.Header.Get("X-Mikan-Features") != "groups" {
				t.Errorf("wrong rule inputs: %v", r.Header)
			}
			for _, n := range []string{"Cookie", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-IP", "CF-Connecting-IP", "X-Hop", "X-Mikan-Relay-Error"} {
				if r.Header.Get(n) != "" {
					t.Errorf("forwarded %s", n)
				}
			}
			return profileResponse(200), nil
		})}
		r := httptest.NewRequest("GET", "https://sub.example/sub"+tokenPath, nil)
		r.RemoteAddr = tc.peer
		r.Header.Set(tc.header, tc.value)
		r.Header.Set("X-Custom-Rule", "match")
		r.Header.Set("X-Mikan-Features", "groups")
		r.Header.Set("Cookie", "secret")
		r.Header.Set("Connection", "X-Hop")
		r.Header.Set("X-Hop", "bad")
		r.Header.Set("X-Mikan-Relay-Error", "credential")
		p.ServeHTTP(httptest.NewRecorder(), r)
	}
}
func TestScopedInvalidationAndLRUEviction(t *testing.T) {
	p, _ := New("https://panel.example/secret/api/subscription", "key", "sub")
	if err := p.SetCacheLimits(2, 1024); err != nil {
		t.Fatal(err)
	}
	e := entry{body: []byte("profile"), header: http.Header{"Content-Type": []string{"text/plain"}}}
	p.remember("old", tokenPath, e, p.generation())
	p.remember("recent", otherToken, e, p.generation())
	p.offline(httptest.NewRecorder(), httptest.NewRequest("GET", "https://sub.example/", nil), "old")
	p.remember("third", tokenPath, e, p.generation())
	if _, ok := p.profiles["recent"]; ok {
		t.Fatal("cache failed to evict least recently used variant")
	}
	generation, release := p.beginFetch(tokenPath)
	defer release()
	p.forget(tokenPath, false)
	p.remember("stale", tokenPath, e, generation)
	if len(p.profiles) != 0 {
		t.Fatal("inflight response resurrected invalidated token")
	}
}
