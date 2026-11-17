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
	p, _ := New("https://panel.example/sub/relay", "secret-token", "sub")
	now := time.Now()
	p.now = func() time.Time { return now }
	calls, status := 0, 200
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/plain"}, RelayAuthHeader: []string{"1"}, RelayProofHeader: []string{"test-issued-proof"}}, Body: io.NopCloser(strings.NewReader("profile"))}, nil
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
	p, _ := New("https://panel.example/sub/relay", "key", "sub")
	now := time.Now()
	p.now = func() time.Time { return now }
	status := 200
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/plain"}, RelayAuthHeader: []string{"1"}, RelayProofHeader: []string{"test-issued-proof"}}, Body: io.NopCloser(strings.NewReader("profile"))}, nil
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
	p, _ := New("https://panel.example/sub/relay", "key", "sub")
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

// A 500 is the panel failing one request, not the panel down: the saved copy answers it,
// and the breaker, opened here by a single counted failure, stays closed.
func TestInternalErrorUsesSavedCopyWithoutOpeningCircuit(t *testing.T) {
	p, _ := New("https://panel.example/sub/relay", "key", "sub")
	p.SetCircuitBreaker(1, time.Minute)
	status := 200
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return profileResponse(status), nil })}
	if w := edgeFetch(p, "GET", tokenPath); w.Code != 200 {
		t.Fatal(w.Code)
	}
	status = 500
	if w := edgeFetch(p, "GET", tokenPath); w.Code != 200 || w.Header().Get("X-Mikan-Offline") != "true" {
		t.Fatalf("a 500 did not fall back to the saved copy: %d", w.Code)
	}
	if w := edgeFetch(p, "GET", otherToken); w.Code != 500 {
		t.Fatalf("without a saved copy the panel's 500 is not passed on: %d", w.Code)
	}
	if !p.breaker.until.IsZero() {
		t.Fatal("a 500 opened the circuit")
	}
	metrics := httptest.NewRecorder()
	p.Metrics().ServeHTTP(metrics, httptest.NewRequest("GET", "http://localhost/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "mikan_sub_upstream_failures_total 2") {
		t.Fatalf("500s not counted as upstream failures: %s", metrics.Body)
	}
}
