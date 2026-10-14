package updates

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/release"
)

func manifest(version string) []byte {
	m := release.Manifest{Version: version, Published: time.Unix(1_800_000_000, 0).UTC(), Image: "ghcr.io/o0hhs/mikan",
		Digest: "sha256:" + strings.Repeat("a", 64), Installer: map[string]release.Asset{}, Notes: map[string]string{"en": "- x"}}
	data, _ := json.Marshal(m)
	return data
}

func found(version string) func(context.Context, Query) (Found, error) {
	return func(context.Context, Query) (Found, error) {
		var f Found
		return f, json.Unmarshal(manifest(version), &f.Manifest)
	}
}

// releases is a GitHub of signed files: manifests at /v<version>/manifest.json, the index
// at /updates/index.json, and "latest" at /latest/manifest.json.
type releases struct {
	t     *testing.T
	priv  ed25519.PrivateKey
	mu    sync.Mutex
	files map[string][]byte
	srv   *httptest.Server
}

func newReleases(t *testing.T, priv ed25519.PrivateKey) *releases {
	r := &releases{t: t, priv: priv, files: map[string][]byte{}}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		b, ok := r.files[req.URL.Path]
		r.mu.Unlock()
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// put publishes a signed file.
func (r *releases) put(path string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[path] = data
	r.files[path+".sig"] = []byte(release.Sign(data, r.priv))
}

// set replaces one file as it is, its signature or not.
func (r *releases) set(path string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[path] = data
}

func (r *releases) get(path string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.files[path]
}

func (r *releases) drop(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.files, path)
	delete(r.files, path+".sig")
}

func (r *releases) release(version string) {
	r.put("/v"+version+"/manifest.json", manifest(version))
}

// index publishes an index of entries "version channel from".
func (r *releases) index(entries ...string) {
	var list []string
	for _, e := range entries {
		f := strings.Fields(e)
		list = append(list, fmt.Sprintf(`{"version":%q,"channel":%q,"from":%q,"manifest":%q}`, f[0], f[1], f[2], r.srv.URL+"/v"+f[0]+"/manifest.json"))
	}
	r.put("/updates/index.json", []byte(`{"schema":1,"published":"2026-10-04T10:00:00Z","releases":[`+strings.Join(list, ",")+`]}`))
}

func (r *releases) source(pub ed25519.PublicKey) Source {
	return fetch(r.srv.URL+"/updates/index.json", r.srv.URL+"/latest/manifest.json", pub, r.srv.Client())
}

// The index chooses the release, the release's own manifest is what the panel believes,
// and a hop, an unreachable release and the beta channel come out of the same rule as the
// installer's (testdata/index.json).
func TestFetchFromTheIndex(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	r := newReleases(t, priv)
	for _, v := range []string{"0.4.5", "0.5.0.0", "0.5.0.1", "0.5.0.2-rc.1", "0.6.0.0"} {
		r.release(v)
	}
	r.put("/latest/manifest.json", manifest("0.4.5"))
	r.index("0.4.5 stable 0.4.0", "0.5.0.0 stable 0.4.5", "0.5.0.1 stable 0.4.5", "0.5.0.2-rc.1 beta 0.5.0.0", "0.6.0.0 stable 0.5.0.1")
	ctx := context.Background()
	for _, c := range []struct {
		current, channel, version, newest string
		unreachable                       bool
	}{
		{"0.5.0.1", "stable", "0.6.0.0", "", false},
		{"0.4.5", "stable", "0.5.0.1", "0.6.0.0", false},
		{"0.5.0.0", "beta", "0.5.0.2-rc.1", "0.6.0.0", false},
		{"0.5.0.0", "nightly", "0.5.0.1", "0.6.0.0", false},
		{"0.6.0.0", "stable", "0.6.0.0", "", false},
		{"0.3.9", "stable", "0.6.0.0", "0.6.0.0", true},
	} {
		f, err := r.source(pub)(ctx, Query{Current: c.current, Channel: c.channel})
		if err != nil || f.Fallback != "" || f.Manifest.Version != c.version || f.Newest != c.newest || f.Unreachable != c.unreachable {
			t.Errorf("%s on %s: %+v %v", c.current, c.channel, f, err)
		}
	}
}

// A broken index never stops updates: whatever is wrong with it, the check takes GitHub's
// latest release and says why.
func TestFetchFallsBackToTheLatestRelease(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	r := newReleases(t, priv)
	r.release("0.5.0.1")
	r.put("/latest/manifest.json", manifest("0.5.0.0"))
	q := Query{Current: "0.4.5", Channel: release.Stable}
	ctx := context.Background()
	fallback := func(what string) {
		t.Helper()
		f, err := r.source(pub)(ctx, q)
		if err != nil || f.Manifest.Version != "0.5.0.0" || f.Fallback == "" {
			t.Errorf("%s: %+v %v", what, f, err)
		}
	}
	fallback("no index yet")

	r.index("0.5.0.1 stable 0.4.5")
	if f, err := r.source(pub)(ctx, q); err != nil || f.Manifest.Version != "0.5.0.1" || f.Fallback != "" {
		t.Fatalf("the index: %+v %v", f, err)
	}
	r.set("/updates/index.json.sig", []byte(release.Sign(r.get("/updates/index.json"), otherPriv)))
	fallback("another key's index")
	r.put("/updates/index.json", []byte("{not json"))
	fallback("a malformed index")
	r.index("0.5.0.1 nightly 0.4.5")
	fallback("an index with no release of the channel")
	r.index("0.5.0.1 stable 0.4.5")
	r.put("/v0.5.0.1/manifest.json", manifest("0.5.0.2"))
	fallback("a manifest of another version than the index says")
	r.drop("/v0.5.0.1/manifest.json")
	fallback("a manifest that is not there")

	r.drop("/latest/manifest.json")
	if _, err := r.source(pub)(ctx, q); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("no release at all: %v", err)
	}
}

// The panel believes a release only with the release key's signature.
func TestFetchChecksTheSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	r := newReleases(t, priv)
	r.release("0.3.10")
	r.put("/latest/manifest.json", manifest("0.3.10"))
	r.index("0.3.10 stable 0.3.0")
	ctx := context.Background()
	q := Query{Current: "0.3.9", Channel: release.Stable}
	if f, err := r.source(pub)(ctx, q); err != nil || f.Manifest.Version != "0.3.10" {
		t.Fatalf("signed: %+v %v", f, err)
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if _, err := r.source(other)(ctx, q); !errors.Is(err, release.ErrSignature) {
		t.Fatalf("another key: %v", err)
	}
	r.set("/latest/manifest.json", manifest("9.9.9"))
	r.set("/v0.3.10/manifest.json", manifest("9.9.9"))
	if _, err := r.source(pub)(ctx, q); !errors.Is(err, release.ErrSignature) {
		t.Fatalf("a swapped manifest: %v", err)
	}
}

// The panel and the host updater meet in files: the switch and the channel, the request,
// the report.
func TestHostFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	var asked Query
	c := New(dir, "0.3.9", func(ctx context.Context, q Query) (Found, error) {
		asked = q
		return found("0.3.10")(ctx, q)
	}, nil, func() time.Time { return now })
	if c.State().Available() {
		t.Fatal("nothing checked yet")
	}
	c.Check(context.Background())
	if s := c.State(); !s.Available() || s.Latest.Version != "0.3.10" || !s.CheckedAt.Equal(now) || asked != (Query{"0.3.9", "stable"}) {
		t.Fatalf("checked: %+v, asked %+v", s, asked)
	}
	if err := c.SetPolicy(Policy{Auto: true, Channel: release.Beta}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "update", "policy.json")); string(b) != `{"auto":true,"channel":"beta"}` {
		t.Fatalf("policy: %s", b)
	}
	if s := c.State(); s.Latest != nil || s.Channel != "beta" {
		t.Fatalf("what the stable channel found is forgotten: %+v", s)
	}
	c.Check(context.Background())
	if asked.Channel != "beta" {
		t.Fatalf("the check asks for the chosen channel: %+v", asked)
	}
	// Only the two exact words go to the host.
	if err := c.SetPolicy(Policy{Auto: false, Channel: "beta; rm -rf /"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "update", "policy.json")); string(b) != `{"auto":false,"channel":"stable"}` {
		t.Fatalf("policy: %s", b)
	}
	if _, ok := c.Requested(); ok {
		t.Fatal("no request yet")
	}
	if err := c.Request(); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Requested(); !ok {
		t.Fatal("the request is there for the host")
	}
	if _, ok := c.Host(); ok {
		t.Fatal("the host has not reported")
	}
	status := `{"state":"failed","version":"0.3.10","from":"0.3.9","error":"did not start","at":"2026-10-01T03:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "update", "status.json"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	if s, ok := c.Host(); !ok || s.State != "failed" || s.Error != "did not start" {
		t.Fatalf("host: %+v", s)
	}

	off := New("", "0.3.9", nil, nil, time.Now)
	if err := off.Request(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no data dir: %v", err)
	}
}

// A newer release this version cannot reach is shown, but there is nothing to request.
func TestUnreachableIsNotAvailable(t *testing.T) {
	c := New("", "0.3.9", func(ctx context.Context, q Query) (Found, error) {
		f, err := found("0.6.0.0")(ctx, q)
		f.Newest, f.Unreachable = "0.6.0.0", true
		return f, err
	}, nil, time.Now)
	c.Check(context.Background())
	if s := c.State(); s.Available() || s.Latest == nil || !s.Found.Unreachable {
		t.Fatalf("unreachable: %+v", s)
	}
}

// A release published between the two requests (manifest, then signature) leaves a new
// manifest with the old signature: the check asks again instead of failing for a day.
func TestFetchAsksAgainOnAMismatchedPair(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	oldData, newData := manifest("0.3.9"), manifest("0.3.10")
	var manifestGets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			manifestGets++
			_, _ = w.Write(newData)
		case "/manifest.json.sig":
			// The first signature fetched is still the old release's.
			if manifestGets == 1 {
				_, _ = w.Write([]byte(release.Sign(oldData, priv)))
				return
			}
			_, _ = w.Write([]byte(release.Sign(newData, priv)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f, err := fetch(srv.URL+"/index.json", srv.URL+"/manifest.json", pub, srv.Client())(context.Background(), Query{Current: "0.3.8"})
	if err != nil || f.Manifest.Version != "0.3.10" || manifestGets != 2 {
		t.Fatalf("%+v %v after %d manifest requests", f, err, manifestGets)
	}
}

// A check that failed is tried again within the hour, not at the next daily one; a
// repository without a release is not a failure to hurry about.
func TestRunRetriesAFailedCheck(t *testing.T) {
	oldCheck, oldRetry, oldFirst := checkEvery, retryAfter, firstCheck
	checkEvery, retryAfter, firstCheck = time.Hour, 30*time.Millisecond, time.Millisecond
	t.Cleanup(func() { checkEvery, retryAfter, firstCheck = oldCheck, oldRetry, oldFirst })
	if oldRetry > 2*time.Hour {
		t.Fatalf("production retry %s", oldRetry)
	}
	var calls int
	var mu sync.Mutex
	c := New("", "0.3.9", func(ctx context.Context, q Query) (Found, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return Found{}, errors.New("github is down")
		}
		return found("0.3.10")(ctx, q)
	}, nil, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { c.Run(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped }()
	deadline := time.Now().Add(5 * time.Second)
	for !c.State().Available() {
		if time.Now().After(deadline) {
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("after %d checks: %+v", calls, c.State())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
