package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/diag"
	"mikan/internal/nodeapi"
)

// The panel asks the node to look at its server. Whatever the request says, the node goes
// only to its fixed targets, and a second request soon after gets the same answer.
func TestDiagnoseEndpoint(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	prev := diagOptions
	diagOptions = func(e *Engine) diag.Options {
		return diag.Options{DataDir: e.dataDir, HTTP: srv.Client(),
			Targets: diag.Targets{Name: "localhost", Internet: []string{srv.Listener.Addr().String()}, GitHub: srv.URL + "/", GHCR: srv.URL + "/v2/"}}
	}
	defer func() { diagOptions = prev }()
	diagnosing.last = nodeapi.Diagnosis{}

	_, h, _ := updateEngine(t)
	ask := func() nodeapi.Diagnosis {
		rec := post(h, "/v1/diagnose", `{"targets":{"github":"https://attacker.example/"},"url":"http://169.254.169.254/"}`)
		var d nodeapi.Diagnosis
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &d) != nil {
			t.Fatalf("diagnose: %d %s", rec.Code, rec.Body)
		}
		return d
	}
	first := ask()
	if len(first.Items) != 7 {
		t.Fatalf("items: %+v", first.Items)
	}
	for _, it := range first.Items {
		if it.ID == "github" && it.Status != nodeapi.CheckOK {
			t.Fatalf("github stand-in: %+v", it)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("the stand-ins got %d requests, want GitHub and GHCR once each", hits.Load())
	}
	if second := ask(); !second.At.Equal(first.At) || hits.Load() != 2 {
		t.Fatalf("a request right after: a new look (%v, %d hits)", second.At, hits.Load())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/diagnose", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}
}

// The node's clock goes into its health: the panel tells a skew from it.
func TestHealthCarriesTheClock(t *testing.T) {
	_, h, _ := updateEngine(t)
	before := time.Now()
	got := health(t, h).Time
	if got.Before(before.Add(-time.Second)) || got.After(time.Now().Add(time.Second)) {
		t.Fatalf("time %v", got)
	}
}
