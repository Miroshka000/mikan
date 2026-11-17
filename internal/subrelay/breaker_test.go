package subrelay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCircuitFastFallbackAndRecovery(t *testing.T) {
	p, _ := New("https://panel.example/api/subscription", "secret-token", "sub")
	now := time.Now()
	p.now = func() time.Time { return now }
	calls, status := 0, 200
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("profile"))}, nil
	})}
	fetch := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("GET", "https://sub.example/sub"+tokenPath, nil))
		return w
	}
	if fetch().Code != 200 {
		t.Fatal("prime")
	}
	status = 503
	for range 3 {
		if w := fetch(); w.Header().Get("X-Mikan-Offline") != "true" {
			t.Fatal("failed fallback")
		}
	}
	count := calls
	for range 10 {
		if w := fetch(); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if calls != count {
		t.Fatal("open circuit called panel")
	}
	now = now.Add(11 * time.Second)
	probe := p.beginProbe()
	if !probe.allowed || p.beginProbe().allowed {
		t.Fatal("recovery must allow exactly one probe")
	}
	p.finishProbe(probe, true, false)
	if p.beginProbe().allowed {
		t.Fatal("failed recovery did not reopen circuit")
	}
	now = now.Add(11 * time.Second)
	status = 200
	if w := fetch(); w.Code != 200 || w.Header().Get("X-Mikan-Offline") != "" {
		t.Fatal("did not recover")
	}
	if !p.breaker.until.IsZero() {
		t.Fatal("circuit stayed open")
	}
	w := httptest.NewRecorder()
	p.Metrics().ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/metrics", nil))
	text := w.Body.String()
	if !strings.Contains(text, "mikan_sub_circuit_skipped_total 10") || strings.Contains(text, "secret-token") || strings.Contains(text, tokenPath) {
		t.Fatalf("bad metrics: %s", text)
	}
	if fetch().Code != 200 {
		t.Fatal("normal forwarding did not resume")
	}
}

func TestCircuitDoesNotMaskRefusal(t *testing.T) {
	p, _ := New("https://panel.example/api/subscription", "key", "sub")
	now := time.Now()
	p.now = func() time.Time { return now }
	status := 200
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("profile"))}, nil
	})}
	fetch := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("GET", "https://sub.example/sub"+tokenPath, nil))
		return w
	}
	if fetch().Code != 200 {
		t.Fatal("prime")
	}
	status = 503
	for range 3 {
		if fetch().Code != 200 {
			t.Fatal("missing offline response")
		}
	}
	now = now.Add(11 * time.Second)
	status = 401
	if w := fetch(); w.Code != 401 || w.Header().Get("X-Mikan-Offline") != "" {
		t.Fatal("recovery masked revoked credentials")
	}
	status = 503
	if w := fetch(); w.Code != 503 || w.Header().Get("X-Mikan-Offline") != "" {
		t.Fatal("revoked profile survived recovery")
	}
}

func TestOldFailureCannotReleaseRecoveryProbe(t *testing.T) {
	p, _ := New("https://panel.example/api/subscription", "key", "sub")
	p.SetCircuitBreaker(1, time.Second)
	now := time.Now()
	p.now = func() time.Time { return now }
	first, late := p.beginProbe(), p.beginProbe()
	p.finishProbe(first, true, false)
	now = now.Add(2 * time.Second)
	probe := p.beginProbe()
	if !probe.allowed {
		t.Fatal("recovery probe not admitted")
	}
	p.finishProbe(late, true, false)
	now = now.Add(2 * time.Second)
	if p.beginProbe().allowed {
		t.Fatal("late failure released an active recovery probe")
	}
	p.finishProbe(probe, false, false)
	if !p.beginProbe().allowed {
		t.Fatal("successful recovery did not reopen normal traffic")
	}
}
