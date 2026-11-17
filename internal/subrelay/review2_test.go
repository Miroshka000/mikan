package subrelay

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type waitingBody struct {
	started chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *waitingBody) Read([]byte) (int, error) {
	b.once.Do(func() { b.started <- struct{}{} })
	<-b.release
	return 0, io.EOF
}
func (*waitingBody) Close() error { return nil }
func TestSlowUploadsDoNotExhaustProfileFetchSlots(t *testing.T) {
	p, _ := New("https://panel.example/secret/api/subscription", "key", "sub")
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return profileResponse(200), nil })}
	started, release := make(chan struct{}, 16), make(chan struct{})
	var wg sync.WaitGroup
	defer func() { close(release); wg.Wait() }()
	for range 16 {
		wg.Go(func() {
			r := httptest.NewRequest("POST", "https://sub.example/sub/tg/session", nil)
			r.Body = &waitingBody{started: started, release: release}
			r.ContentLength = 1
			p.ServeHTTP(httptest.NewRecorder(), r)
		})
	}
	for range 16 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("upload did not start")
		}
	}
	if len(p.inflight) != 0 {
		t.Fatal("slow uploads occupied upstream slots")
	}
	if w := edgeFetch(p, "GET", tokenPath); w.Code != 200 {
		t.Fatalf("slow uploads blocked profile: %d", w.Code)
	}
	// Empty HTTP/2 bodies need not be represented by http.NoBody.
	emptyStream := httptest.NewRequest("GET", "https://sub.example/sub"+tokenPath, nil)
	emptyStream.Body = io.NopCloser(strings.NewReader(""))
	emptyStream.ContentLength = 0
	emptyResponse := httptest.NewRecorder()
	p.ServeHTTP(emptyResponse, emptyStream)
	if emptyResponse.Code != 200 {
		t.Fatal("slow uploads blocked empty HTTP/2 profile stream")
	}

	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", "https://sub.example/sub/tg/session", strings.NewReader("x")))
	if w.Code != 503 || len(p.uploads) != 16 {
		t.Fatal("upload memory budget is unbounded")
	}
}
func TestAllConnectionLinesAreRemovedInBothDirections(t *testing.T) {
	p, _ := New("https://panel.example/secret/api/subscription", "key", "sub")
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Hop-One") != "" || r.Header.Get("X-Hop-Two") != "" {
			t.Errorf("request leaked nominated fields: %v", r.Header)
		}
		resp := profileResponse(200)
		resp.Header["Connection"] = []string{"keep-alive, X-Hop-One", "X-Hop-Two, "}
		resp.Header.Set("X-Hop-One", "secret one")
		resp.Header.Set("X-Hop-Two", "secret two")
		return resp, nil
	})}
	r := httptest.NewRequest("GET", "https://sub.example/sub"+tokenPath, nil)
	r.Header["Connection"] = []string{"keep-alive, X-Hop-One", "X-Hop-Two, "}
	r.Header.Set("X-Hop-One", "one")
	r.Header.Set("X-Hop-Two", "two")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("X-Hop-One") != "" || w.Header().Get("X-Hop-Two") != "" {
		t.Fatalf("response leaked nominated fields: %v", w.Header())
	}
	if w.Header().Get(RelayAuthHeader) != "" {
		t.Fatal("internal auth metadata leaked to client")
	}
}
func TestRepeatedClientIPHeaderLines(t *testing.T) {
	for _, tc := range []struct {
		header, peer string
		lines        []string
		want         string
	}{
		{"X-Forwarded-For", "10.0.0.2:1", []string{"1.1.1.1", "203.0.113.7,10.0.0.3"}, "203.0.113.7"},
		{"CF-Connecting-IP", "10.0.0.2:1", []string{"1.1.1.1", "203.0.113.7"}, "10.0.0.2"},
		{"X-Forwarded-For", "192.0.2.3:1", []string{"1.1.1.1", "203.0.113.7"}, "192.0.2.3"},
	} {
		p, _ := New("https://panel.example/relay", "key", "sub")
		p.SetTrustedProxies("10.0.0.0/8", tc.header)
		r := httptest.NewRequest("GET", "https://sub.example/", nil)
		r.RemoteAddr = tc.peer
		for _, line := range tc.lines {
			r.Header.Add(tc.header, line)
		}
		if got := p.clientIP(r); got != tc.want {
			t.Errorf("%s lines=%v: got %s want %s", tc.header, tc.lines, got, tc.want)
		}
	}
}
func TestClientRefusalCannotClearOtherProfilesThroughAuthProbe(t *testing.T) {
	for _, probeStatus := range []int{204, 503, 429, 403, 200} {
		t.Run(http.StatusText(probeStatus), func(t *testing.T) {
			p, _ := New("https://panel.example/secret/api/subscription/", "key", "sub")
			p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return profileResponse(200), nil })}
			edgeFetch(p, "GET", tokenPath)
			edgeFetch(p, "GET", otherToken)
			probes := 0
			p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, AuthPath) {
					probes++
					if r.Method != "HEAD" || r.URL.Path != "/secret/api/subscription"+AuthPath || r.URL.RawQuery != "" || r.Header.Get("X-Attack") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("User-Agent") != "mikan-sub" {
						t.Errorf("client input reached auth probe: %s %v", r.URL, r.Header)
					}
					if r.Header.Get("Authorization") != "Bearer key" || r.Header.Get(RelayNonceHeader) == "" || r.Header.Get(RelayNonceHeader) == "forged nonce" || r.Header.Get(RelayProofHeader) != "test-issued-proof" {
						t.Error("missing private authentication challenge")
					}
					resp := profileResponse(probeStatus)
					resp.Header.Del(RelayAuthHeader)
					if probeStatus == 204 {
						resp.Header.Set(RelayAuthHeader, r.Header.Get(RelayNonceHeader))
					}
					return resp, nil
				}
				resp := profileResponse(403)
				resp.Header.Del(RelayAuthHeader)
				resp.Header.Del(RelayProofHeader)
				resp.Header.Set(RelayActionHeader, "drop")
				resp.Header.Set(RelayInvalidateHeader, tokenPath)
				return resp, nil
			})}
			r := httptest.NewRequest("POST", "https://sub.example/sub/tg/session?attacker=1", strings.NewReader("invalid init_data"))
			r.Header.Set("X-Attack", "spoof")
			r.Header.Set("Cookie", "secret")
			r.Header.Set(RelayProofHeader, "forged proof")
			r.Header.Set(RelayNonceHeader, "forged nonce")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if w.Code != 403 || probes != 1 || len(p.profiles) != 2 {
				t.Fatalf("client refusal affected others: status=%d probes=%d profiles=%d", w.Code, probes, len(p.profiles))
			}
		})
	}
}
func TestUnmarkedOwnSiteCannotReplaceProfilesOrClearOtherSubscriptions(t *testing.T) {
	p, _ := New("https://panel.example/secret/api/subscription", "key", "sub")
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return profileResponse(200), nil })}
	edgeFetch(p, "GET", tokenPath)
	edgeFetch(p, "GET", otherToken)
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := profileResponse(200)
		resp.Header.Del(RelayAuthHeader)
		resp.Body = io.NopCloser(strings.NewReader("ordinary own site"))
		return resp, nil
	})}
	if w := edgeFetch(p, "GET", otherToken); w.Code != 200 || len(p.profiles) != 1 {
		t.Fatal("unconfirmed refusal cleared others or cached fallback page")
	}
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("outage") })}
	if w := edgeFetch(p, "GET", tokenPath); w.Header().Get("X-Mikan-Offline") != "true" {
		t.Fatal("unconfirmed refusal erased another saved profile")
	}
}

func TestStaleRevocationNonceCannotEraseProfiles(t *testing.T) {
	p, _ := New("https://panel.example/relay", "key", "sub")
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return profileResponse(200), nil })}
	edgeFetch(p, "GET", tokenPath)
	edgeFetch(p, "GET", otherToken)
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := profileResponse(403)
		resp.Header.Del(RelayAuthHeader)
		resp.Header.Del(RelayProofHeader)
		if strings.HasSuffix(r.URL.Path, AuthPath) {
			resp.StatusCode = 204
			resp.Header.Set(RelayAuthHeader, "revoked:old-nonce")
		}
		return resp, nil
	})}
	edgeFetch(p, "POST", "/tg/session")
	if len(p.profiles) != 2 {
		t.Fatal("replayed diagnostic erased saved profiles")
	}
}

func TestProfileWithoutDiagnosticTicketIsNotSaved(t *testing.T) {
	p, _ := New("https://panel.example/relay", "key", "sub")
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		resp := profileResponse(200)
		resp.Header.Del(RelayProofHeader)
		return resp, nil
	})}
	if w := edgeFetch(p, "GET", tokenPath); w.Code != 200 || len(p.profiles) != 0 {
		t.Fatal("saved profile without a revocation-check ticket")
	}
}
