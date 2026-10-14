package addons

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func signed(t *testing.T, c any) ([]byte, string, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(c)
	return data, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)), pub
}

// The catalog is trusted only with a good signature, and only what this panel can run
// is offered.
func TestParseCatalog(t *testing.T) {
	c := Catalog{Version: 1, Adapters: []Entry{
		{ID: "yookassa", Protocol: 1, Image: "ghcr.io/getmikan/adapter-yookassa", Digest: digest, MinPanel: "0.4.3"},
		{ID: "future", Protocol: 2, Image: "x", Digest: digest},
		{ID: "newer", Protocol: 1, Image: "x", Digest: digest, MinPanel: "0.9.0"},
		{ID: "Bad Id", Protocol: 1, Image: "x", Digest: digest},
		{ID: "tagged", Protocol: 1, Image: "x", Digest: "latest"},
	}}
	data, sig, pub := signed(t, c)
	got, err := ParseCatalog(data, sig+"\n", pub, "0.4.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Adapters) != 1 || got.Adapters[0].ID != "yookassa" {
		t.Fatalf("adapters: %+v", got.Adapters)
	}
	tampered := []byte(strings.Replace(string(data), "yookassa", "yookassb", 1))
	if _, err := ParseCatalog(tampered, sig, pub, "0.4.3"); err == nil {
		t.Fatal("a changed catalog passed")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := ParseCatalog(data, sig, other, "0.4.3"); err == nil {
		t.Fatal("another key passed")
	}
	if got, _ := ParseCatalog(data, sig, pub, "0.4.2"); len(got.Adapters) != 0 {
		t.Fatal("an adapter for a newer panel offered to an older one")
	}
}

// Protocol 2 brings the other kinds: each with its mode, known permissions and sane
// resources. A payment adapter stays protocol 1, and protocol 1 carries none of it.
func TestParseCatalogProtocol2(t *testing.T) {
	img := "ghcr.io/getmikan/telegram"
	c := Catalog{Version: 1, Adapters: []Entry{
		{ID: "telegram", Protocol: 2, Kind: KindBot, Mode: ModeHTTP, Permissions: []string{"bot", "kv"}, Resources: Resources{MemMB: 128}, Image: img, Digest: digest},
		{ID: "script", Protocol: 2, Kind: KindBot, Mode: ModeClient, Image: img, Digest: digest},
		{ID: "page", Protocol: 2, Kind: KindUI, Image: img, Digest: digest},
		{ID: "yookassa", Protocol: 1, Image: img, Digest: digest},
		// The permissions that reach every subscriber are ones an entry may ask for.
		{ID: "reach", Protocol: 2, Kind: KindUI, Permissions: []string{"users:all", "users:grant"}, Image: img, Digest: digest},
		{ID: "no-kind", Protocol: 2, Image: img, Digest: digest},
		{ID: "pay-v2", Protocol: 2, Kind: KindPayment, Image: img, Digest: digest},
		{ID: "odd-mode", Protocol: 2, Kind: KindBot, Mode: "socket", Image: img, Digest: digest},
		{ID: "root", Protocol: 2, Kind: KindBot, Permissions: []string{"admin"}, Image: img, Digest: digest},
		{ID: "twice", Protocol: 2, Kind: KindBot, Permissions: []string{"kv", "kv"}, Image: img, Digest: digest},
		{ID: "greedy", Protocol: 2, Kind: KindBot, Resources: Resources{MemMB: 4096}, Image: img, Digest: digest},
		{ID: "tiny", Protocol: 2, Kind: KindBot, Resources: Resources{MemMB: 8}, Image: img, Digest: digest},
		{ID: "v1-bot", Protocol: 1, Kind: KindBot, Image: img, Digest: digest},
		{ID: "v1-perms", Protocol: 1, Permissions: []string{"kv"}, Image: img, Digest: digest},
		{ID: "v3", Protocol: 3, Kind: KindBot, Image: img, Digest: digest},
	}}
	data, sig, pub := signed(t, c)
	got, err := ParseCatalog(data, sig, pub, "dev")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range got.Adapters {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "telegram,script,page,yookassa,reach" {
		t.Fatalf("kept %v", ids)
	}
	if e := got.Adapters[2]; e.Mode != ModeHTTP {
		t.Fatalf("a protocol 2 entry without a mode serves HTTP: %+v", e)
	}
	if e := got.Adapters[3]; e.Kind != KindPayment || e.Mode != ModeHTTP {
		t.Fatalf("a protocol 1 entry is a payment adapter: %+v", e)
	}
	if pay := got.Of(KindPayment); len(pay) != 1 || pay[0].ID != "yookassa" {
		t.Fatalf("payments: %+v", pay)
	}
}

// Before protocol 2 the host wrote payment adapters only, with no kind: they stay payment
// adapters, and the payment side sees nothing else.
func TestStateKinds(t *testing.T) {
	s := State{Adapters: map[string]Installed{
		"yookassa": {Status: "running"},
		"telegram": {Protocol: Protocol2, Kind: KindBot, Status: "running", Listen: "127.0.0.1:1", Token: "tok"},
	}}
	if pay := s.Of(KindPayment); len(pay) != 1 || pay["yookassa"].Status != "running" {
		t.Fatalf("payments: %+v", pay)
	}
	if bots := s.Of(KindBot); len(bots) != 1 {
		t.Fatalf("bots: %+v", bots)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "addons"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, "addons", "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(dir, "", "dev", slog.New(slog.DiscardHandler), time.Now)
	if _, err := m.Client("telegram"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("a bot is no payment adapter to call: %v", err)
	}
	if _, err := m.Info(context.Background(), "telegram"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("a bot has no payment description: %v", err)
	}
}

// fakeAdapter answers protocol v1 behind a token.
func fakeAdapter(t *testing.T, infos *atomic.Int64) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":"unauthorized","message":"no"}`)
			return
		}
		switch r.URL.Path {
		case "/v1/info":
			infos.Add(1)
			_, _ = io.WriteString(w, `{"id":"yookassa","protocol":1,"version":"1.0.0","name":{"ru":"ЮKassa"},"currencies":["RUB"],"capabilities":["webhook"],"settings":[{"key":"shop_id","type":"string","required":true}]}`)
		case "/v1/invoices":
			_, _ = io.WriteString(w, `{"external_id":"2f1c","pay_url":"https://pay.example/2f1c"}`)
		case "/v1/check":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"code":"bad_credentials","message":"shop refused"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestManagerAndClient(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, "", "0.4.3", slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	if s, err := m.State(); err != nil || len(s.Adapters) != 0 {
		t.Fatalf("empty state: %+v %v", s, err)
	}
	if _, err := m.Client("yookassa"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("client before install: %v", err)
	}
	if err := m.Ask(Request{Action: "install", ID: "../etc"}); !errors.Is(err, ErrUnknown) {
		t.Fatalf("bad id: %v", err)
	}
	if err := m.Ask(Request{Action: "install", ID: "yookassa"}); err != nil {
		t.Fatal(err)
	}
	if q := m.Pending(); len(q) != 1 || q[0].ID != "yookassa" || q[0].Action != "install" {
		t.Fatalf("pending: %+v", q)
	}
	wake, err := os.ReadFile(filepath.Join(dir, "update", "request"))
	if err != nil || !strings.Contains(string(wake), `"do":"addons"`) {
		t.Fatalf("host not woken: %s %v", wake, err)
	}
	if err := m.Ask(Request{Action: "remove", ID: "yookassa"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second request while one waits: %v", err)
	}
	// An update the admin asked for is not turned into an adapter request.
	other := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(other, "update"), 0o755))
	must(os.WriteFile(filepath.Join(other, "update", "request"), []byte(`{"at":"2026-10-01T00:00:00Z"}`), 0o644))
	must(New(other, "", "0.4.3", m.log, time.Now).Ask(Request{Action: "install", ID: "yookassa"}))
	if wake, _ := os.ReadFile(filepath.Join(other, "update", "request")); strings.Contains(string(wake), "addons") {
		t.Fatalf("update request replaced: %s", wake)
	}

	// The host ran it.
	var infos atomic.Int64
	srv := fakeAdapter(t, &infos)
	listen := strings.TrimPrefix(srv.URL, "http://")
	state := State{Adapters: map[string]Installed{"yookassa": {Version: "1.0.0", Digest: digest, Listen: listen, Token: "tok", Status: "running"}}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(dir, "addons", "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 3 {
		info, err := m.Info(ctx, "yookassa")
		if err != nil || info.Name.In("en") != "ЮKassa" || !info.Takes("RUB") || !info.Can("webhook") || info.Can("refund") {
			t.Fatalf("info: %+v %v", info, err)
		}
	}
	if infos.Load() != 1 {
		t.Fatalf("info asked %d times, want once per image", infos.Load())
	}
	c, err := m.Client("yookassa")
	if err != nil {
		t.Fatal(err)
	}
	if inv, err := c.CreateInvoice(ctx, InvoiceRequest{Amount: 19900, Currency: "RUB"}); err != nil || inv.ExternalID != "2f1c" {
		t.Fatalf("invoice: %+v %v", inv, err)
	}
	var ae *Error
	if err := c.Check(ctx, Settings{"shop_id": "1"}); !errors.As(err, &ae) || ae.Code != "bad_credentials" || ae.Status != 422 {
		t.Fatalf("check: %v", err)
	}
	if err := NewClient(srv.URL, "wrong", http.DefaultClient).Check(ctx, nil); !errors.As(err, &ae) || ae.Status != 401 {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := NewClient("http://127.0.0.1:1", "tok", http.DefaultClient).Info(ctx); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("down: %v", err)
	}
}

// Requests for different addons wait in line, numbered one past the highest queued, and the
// host takes them by number; a second one for an addon still in line is refused.
func TestQueue(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, "", "dev", slog.New(slog.DiscardHandler), time.Now)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(m.Ask(Request{Action: "install", ID: "telegram"}))
	must(m.Ask(Request{Action: "remove", ID: "yookassa"}))
	own := Request{Action: "install", ID: "my-bot", Source: SourceCustom, Image: "ghcr.io/someone/my-bot", Digest: digest, Kind: KindBot, Permissions: []string{"bot", "kv"}}
	must(m.Ask(own))
	if err := m.Ask(Request{Action: "remove", ID: "telegram"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second request for an addon in line: %v", err)
	}
	q := filepath.Join(dir, "addons", "requests")
	names := func() []string {
		entries, _ := os.ReadDir(q)
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}
	if got := strings.Join(names(), " "); got != "0000000001-telegram.json 0000000002-yookassa.json 0000000003-my-bot.json" {
		t.Fatalf("queue: %s", got)
	}
	data, _ := os.ReadFile(filepath.Join(q, "0000000003-my-bot.json"))
	var back Request
	if json.Unmarshal(data, &back) != nil || back.Source != SourceCustom || back.Digest != digest || back.At == "" || len(back.Permissions) != 2 {
		t.Fatalf("the custom request as written: %s", data)
	}
	// The host took the first two; the number keeps going up past what is left.
	must(os.Remove(filepath.Join(q, "0000000001-telegram.json")))
	must(os.Remove(filepath.Join(q, "0000000002-yookassa.json")))
	must(os.WriteFile(filepath.Join(q, ".0000000009-x.json.1.tmp"), []byte("{}"), 0o644))
	must(os.WriteFile(filepath.Join(q, "0000000007-planted.json"), []byte(`{"action":"install","id":"other"}`), 0o644))
	must(m.Ask(Request{Action: "install", ID: "telegram"}))
	if _, err := os.Stat(filepath.Join(q, "0000000008-telegram.json")); err != nil {
		t.Fatalf("not one past the highest: %v %v", err, names())
	}
	var ids []string
	for _, r := range m.Pending() {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "my-bot,telegram" {
		t.Fatalf("pending: %v (a file named for another addon is not its request)", ids)
	}
}

// What the panel queues for an addon of the admin's own is checked as the host checks it.
func TestRequestValid(t *testing.T) {
	custom := Request{Action: "install", ID: "my-bot", Source: SourceCustom, Image: "registry.example.com:5000/a/b_c", Digest: digest, Kind: KindBot}
	external := Request{Action: "install", ID: "mine", Source: SourceExternal, URL: "http://127.0.0.1:45618", Kind: KindGeneric, Mode: ModeHTTP}
	good := []Request{
		{Action: "install", ID: "telegram"},
		{Action: "remove", ID: "telegram"},
		custom,
		external,
		{Action: "install", ID: "mine", Source: SourceExternal, Kind: KindBot, Mode: ModeClient},
	}
	for _, r := range good {
		if !r.Valid() {
			t.Errorf("refused %+v", r)
		}
	}
	with := func(r Request, f func(*Request)) Request {
		f(&r)
		return r
	}
	bad := map[string]Request{
		"an official one with an image":  {Action: "install", ID: "telegram", Image: "x"},
		"an official source named":       {Action: "install", ID: "telegram", Source: SourceOfficial},
		"a remove of a custom one":       with(custom, func(r *Request) { r.Action = "remove" }),
		"a tag":                          with(custom, func(r *Request) { r.Image = "someone/bot:latest" }),
		"YAML in the image":              with(custom, func(r *Request) { r.Image = "x\"\n    privileged: true" }),
		"uppercase":                      with(custom, func(r *Request) { r.Image = "Someone/bot" }),
		"a bad host":                     with(custom, func(r *Request) { r.Image = "my_reg.io/x" }),
		"no digest":                      with(custom, func(r *Request) { r.Digest = "" }),
		"a payment adapter of its own":   with(custom, func(r *Request) { r.Kind = KindPayment }),
		"an unknown permission":          with(custom, func(r *Request) { r.Permissions = []string{"root"} }),
		"too much memory":                with(custom, func(r *Request) { r.MemMB = 4096 }),
		"an external one off loopback":   with(external, func(r *Request) { r.URL = "http://0.0.0.0:45618" }),
		"an external one on a low port":  with(external, func(r *Request) { r.URL = "http://127.0.0.1:80" }),
		"an external one with a path":    with(external, func(r *Request) { r.URL = "http://127.0.0.1:45618/x" }),
		"an external one with a sign":    with(external, func(r *Request) { r.URL = "http://127.0.0.1:+45618" }),
		"an external server without URL": with(external, func(r *Request) { r.URL = "" }),
		"an external one with an image":  with(external, func(r *Request) { r.Image = "alpine" }),
		"an unknown source":              with(custom, func(r *Request) { r.Source = "elsewhere" }),
		"a bad id":                       with(custom, func(r *Request) { r.ID = "../x" }),
	}
	for what, r := range bad {
		if r.Valid() {
			t.Errorf("%s passed: %+v", what, r)
		}
	}
}

// A host before the open permissions would drop an addon that asks for them.
func TestHostTakes(t *testing.T) {
	for _, c := range []struct {
		host  string
		perms []string
		want  bool
	}{
		{"0.5.1.0-rc.10", []string{"bot", "kv"}, true},
		{"0.5.1.0-rc.10", []string{"users:all"}, false},
		{"0.5.1.0-rc.10", []string{"events", "users:grant"}, false},
		{"0.5.1.0-rc.11", []string{"users:all", "users:grant"}, true},
		{"0.5.1.0", []string{"users:grant"}, true},
		{"dev", []string{"users:grant"}, true},
	} {
		if got := HostTakes(c.host, c.perms); got != c.want {
			t.Errorf("HostTakes(%q, %v) = %v, want %v", c.host, c.perms, got, c.want)
		}
	}
}
