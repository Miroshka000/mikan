package addons

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// What the panel fetches on a source's word is narrow: where it connects, what it follows, how
// much it reads, and whose key it holds the file to.

func srcManager(t *testing.T, n *sourceNet) *Manager {
	t.Helper()
	m := New("", "", "dev", slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	m.src = n
	return m
}

func devNet() *sourceNet { return newSourceNet(true) }

func entryDoc(id, digestChar string) map[string]any {
	return map[string]any{"id": id, "name": map[string]string{"en": id}, "version": "1.0.0", "protocol": 2, "kind": "generic",
		"permissions": []string{"kv"}, "image": "registry.example.com/me/" + id, "digest": "sha256:" + strings.Repeat(digestChar, 64)}
}

// signedDoc returns the file and its signature, the file naming the key as a signed source does.
func signedDoc(t *testing.T, doc map[string]any, priv ed25519.PrivateKey) (data []byte, sig string) {
	t.Helper()
	doc["key"] = base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return data, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
}

func newPriv(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// fileServer serves what is in files and counts nothing else: a path it does not have is a 404.
type fileServer struct {
	mu    sync.Mutex
	files map[string][]byte
	srv   *httptest.Server
}

func newFileServer(t *testing.T) *fileServer {
	t.Helper()
	f := &fileServer{files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		data, ok := f.files[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fileServer) put(path string, data []byte) {
	f.mu.Lock()
	f.files[path] = data
	f.mu.Unlock()
}

func (f *fileServer) del(path string) {
	f.mu.Lock()
	delete(f.files, path)
	f.mu.Unlock()
}

func TestSourceAddressesAllowed(t *testing.T) {
	prod, dev := newSourceNet(false), newSourceNet(true)
	for addr, want := range map[string]bool{
		"8.8.8.8":              true,
		"2606:4700:4700::1111": true,
		"127.0.0.1":            false,
		"::1":                  false,
		"10.0.0.1":             false,
		"172.16.5.4":           false,
		"192.168.1.1":          false,
		"169.254.169.254":      false,
		"100.64.0.1":           false,
		"0.0.0.0":              false,
		"fc00::1":              false,
		"fe80::1":              false,
		"::ffff:10.0.0.1":      false, // IPv4 in an IPv6 coat
		"::ffff:127.0.0.1":     false,
		"64:ff9b::a00:1":       false, // 10.0.0.1 behind NAT64
		"64:ff9b::7f00:1":      false, // 127.0.0.1 behind NAT64
		"64:ff9b::808:808":     true,  // 8.8.8.8 behind NAT64
	} {
		if got := prod.allowed(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: allowed = %v, want %v", addr, got, want)
		}
	}
	// Development opens the one address an author's own machine has, and nothing else.
	for addr, want := range map[string]bool{"127.0.0.1": true, "::ffff:127.0.0.1": true, "127.0.0.2": false, "::1": false, "10.0.0.1": false} {
		if got := dev.allowed(netip.MustParseAddr(addr)); got != want {
			t.Errorf("dev %s: allowed = %v, want %v", addr, got, want)
		}
	}
}

// The check is on the connection, so it holds for whatever name led there.
func TestSourceNeverDialsAPrivateAddress(t *testing.T) {
	f := newFileServer(t)
	f.put("/mikan-addon.json", []byte(`{}`))
	_, _, err := newSourceNet(false).get(t.Context(), f.srv.URL+"/mikan-addon.json", SourceLimit, "")
	if !errors.Is(err, ErrSourceAddress) {
		t.Fatalf("a loopback server in production: %v", err)
	}
	if got := SourceCode(err); got != "source_address_refused" {
		t.Fatalf("code: %s", got)
	}
	body, status, err := devNet().get(t.Context(), f.srv.URL+"/mikan-addon.json", SourceLimit, "")
	if err != nil || status != http.StatusOK || string(body) != "{}" {
		t.Fatalf("a loopback server in development: %q %d %v", body, status, err)
	}
}

func TestSourceParseURL(t *testing.T) {
	n := devNet()
	good := map[string]string{
		"https://example.com/mikan-addon.json":                            "https://example.com/mikan-addon.json",
		"  https://Example.com/addons/index.json ":                        "https://example.com/addons/index.json",
		"github.com/owner/repo":                                           "github.com/owner/repo",
		"GitHub.com/owner/repo":                                           "github.com/owner/repo",
		"https://github.com/owner/repo":                                   "github.com/owner/repo",
		"https://github.com/owner/repo.git":                               "github.com/owner/repo",
		"https://github.com/owner/repo/releases/download/v1/index.json":   "https://github.com/owner/repo/releases/download/v1/index.json",
		"http://127.0.0.1:8080/mikan-addon.json":                          "http://127.0.0.1:8080/mikan-addon.json",
		"https://raw.githubusercontent.com/owner/repo/main/index.json":    "https://raw.githubusercontent.com/owner/repo/main/index.json",
		"https://example.com:8443/a/b/mikan-addon.json":                   "https://example.com:8443/a/b/mikan-addon.json",
		"https://github.com/owner/repo/releases/download/v1/x/index.json": "https://github.com/owner/repo/releases/download/v1/x/index.json",
	}
	for in, want := range good {
		got, _, err := n.parseURL(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", " ", "words", "owner/repo", "ftp://example.com/index.json", "http://example.com/index.json", "file:///etc/passwd",
		"https://localhost/index.json", "https://127.0.0.1/index.json", "https://10.0.0.5/index.json", "https://[::1]/index.json",
		"https://169.254.169.254/latest/meta-data", "https://user:pass@example.com/index.json", "https://example.com/index.json?x=1",
		"https://example.com/index.json#frag", "https://example.com/", "https://example.com", "https://example.com/dir/",
		"github.com/owner", "github.com/owner/", "github.com//repo", "github.com/ow ner/repo", "github.com/owner/repo/issues",
		"https://github.com/owner/repo/tree/main", "https://github.com/owner/repo/releases", "github.com/-bad/repo", "github.com/owner/..",
		"https://example.com/a\nb.json", "http://127.0.0.2:8080/index.json", "http://localhost:8080/index.json", "https://" + strings.Repeat("a", 600) + ".com/x.json",
	} {
		if got, _, err := n.parseURL(in); !errors.Is(err, ErrSourceURL) {
			t.Errorf("%q: %q %v, want source_url_invalid", in, got, err)
		}
	}
	// Plain http to the loopback is for development only.
	if _, _, err := newSourceNet(false).parseURL("http://127.0.0.1:8080/mikan-addon.json"); !errors.Is(err, ErrSourceURL) {
		t.Fatalf("http in production: %v", err)
	}
}

func TestSourceRedirects(t *testing.T) {
	n := newSourceNet(false)
	req := func(rawURL string) *http.Request {
		u, _ := url.Parse(rawURL)
		return &http.Request{URL: u}
	}
	for _, c := range []struct {
		name     string
		from, to string
		ok       bool
	}{
		{"same host", "https://example.com/a", "https://example.com/b", true},
		{"another host", "https://example.com/a", "https://evil.example/b", false},
		{"a subdomain is another host", "https://example.com/a", "https://cdn.example.com/b", false},
		{"downgrade to http", "https://example.com/a", "http://example.com/b", false},
		{"another port", "https://example.com/a", "https://example.com:8443/b", false},
		{"github api to an asset", "https://api.github.com/repos/o/r/releases/latest", "https://objects.githubusercontent.com/x", true},
		{"github release to its asset host", "https://github.com/o/r/releases/download/v1/index.json", "https://release-assets.githubusercontent.com/x", true},
		{"github to any other host", "https://github.com/o/r/releases/download/v1/index.json", "https://evil.example/x", false},
		{"another host to github's assets", "https://example.com/a", "https://objects.githubusercontent.com/x", false},
	} {
		err := n.checkRedirect(req(c.to), []*http.Request{req(c.from)})
		if (err == nil) != c.ok || err != nil && !errors.Is(err, ErrSourceRedirect) {
			t.Errorf("%s: %v, want ok=%v", c.name, err, c.ok)
		}
	}
	// A chain is cut after three hops, whoever it goes through.
	via := []*http.Request{req("https://example.com/0"), req("https://example.com/1"), req("https://example.com/2"), req("https://example.com/3")}
	if err := n.checkRedirect(req("https://example.com/4"), via); !errors.Is(err, ErrSourceRedirect) {
		t.Fatalf("a long chain: %v", err)
	}
}

// A redirect through a real server: to the same host it is followed, to another it is not,
// and the refusal is the code the admin sees.
func TestSourceFollowsRedirectsOnlyWhereExpected(t *testing.T) {
	f := newFileServer(t)
	f.put("/mikan-addon.json", []byte(`{"ok":true}`))
	other := newFileServer(t)
	other.put("/mikan-addon.json", []byte(`{"ok":false}`))
	mux := http.NewServeMux()
	mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/mikan-addon.json", http.StatusFound)
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.srv.URL+"/mikan-addon.json", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { f.srv.Config.Handler.ServeHTTP(w, r) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	n := devNet()
	if body, _, err := n.get(t.Context(), srv.URL+"/same", SourceLimit, ""); err != nil || string(body) != `{"ok":true}` {
		t.Fatalf("same host: %q %v", body, err)
	}
	if _, _, err := n.get(t.Context(), srv.URL+"/away", SourceLimit, ""); !errors.Is(err, ErrSourceRedirect) {
		t.Fatalf("another host: %v", err)
	}
	if _, _, err := n.get(t.Context(), srv.URL+"/loop", SourceLimit, ""); !errors.Is(err, ErrSourceRedirect) {
		t.Fatalf("a loop: %v", err)
	}
}

func TestSourceSizeLimit(t *testing.T) {
	mux := http.NewServeMux()
	body := func(n int) []byte { return []byte(strings.Repeat("a", n)) }
	mux.HandleFunc("/exact", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body(SourceLimit)) })
	mux.HandleFunc("/over", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body(SourceLimit + 1)) })
	// No Content-Length: the limit is held by reading, not by what the server says.
	mux.HandleFunc("/chunked", func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		_, _ = w.Write(body(SourceLimit + 10))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	n := devNet()
	if b, _, err := n.get(t.Context(), srv.URL+"/exact", SourceLimit, ""); err != nil || len(b) != SourceLimit {
		t.Fatalf("exactly the limit: %d %v", len(b), err)
	}
	for _, path := range []string{"/over", "/chunked"} {
		if _, _, err := n.get(t.Context(), srv.URL+path, SourceLimit, ""); !errors.Is(err, ErrSourceTooLarge) {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestSourceFileKinds(t *testing.T) {
	f := newFileServer(t)
	priv := newPriv(t)
	one := entryDoc("hello", "b")
	one["title"] = "Hello author"
	data, sig := signedDoc(t, one, priv)
	f.put("/mikan-addon.json", data)
	f.put("/mikan-addon.json.sig", []byte(sig))

	catalog := map[string]any{"version": 1, "title": "A catalog", "adapters": []any{entryDoc("one", "b"), entryDoc("two", "c")}}
	f.put("/index.json", mustJSON(t, catalog))

	m := srcManager(t, devNet())
	ctx := t.Context()

	// One entry, signed: the key is the file's.
	doc, err := m.FetchSource(ctx, f.srv.URL+"/mikan-addon.json", SourcePin{})
	if err != nil || len(doc.Entries) != 1 || doc.Entries[0].ID != "hello" || doc.Title != "Hello author" || doc.Key != base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)) {
		t.Fatalf("one entry: %+v %v", doc, err)
	}
	if fp := Fingerprint(doc.Key); len(fp) != 39 || strings.Count(fp, ":") != 7 {
		t.Fatalf("fingerprint: %q", fp)
	}
	// A catalog, unsigned: no key, the host's name as its title.
	doc, err = m.FetchSource(ctx, f.srv.URL+"/index.json", SourcePin{})
	if err != nil || len(doc.Entries) != 2 || doc.Key != "" || doc.Title != "A catalog" {
		t.Fatalf("catalog: %+v %v", doc, err)
	}
	// Nothing at the address.
	if _, err := m.FetchSource(ctx, f.srv.URL+"/nope.json", SourcePin{}); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// Not json, and json that lists nothing this panel installs.
	f.put("/html.json", []byte("<html></html>"))
	if _, err := m.FetchSource(ctx, f.srv.URL+"/html.json", SourcePin{}); !errors.Is(err, ErrSourceBadAnswer) {
		t.Fatalf("html: %v", err)
	}
	f.put("/empty.json", []byte(`{"adapters": []}`))
	if _, err := m.FetchSource(ctx, f.srv.URL+"/empty.json", SourcePin{}); !errors.Is(err, ErrSourceEmpty) {
		t.Fatalf("empty: %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The first signature pins its key. The same file under another key, a broken signature, or
// none at all is refused; only the admin's word on the new fingerprint takes another key.
func TestSourceKeyPinning(t *testing.T) {
	f := newFileServer(t)
	m := srcManager(t, devNet())
	ctx := t.Context()
	author, thief := newPriv(t), newPriv(t)
	authorKey := base64.StdEncoding.EncodeToString(author.Public().(ed25519.PublicKey))
	thiefKey := base64.StdEncoding.EncodeToString(thief.Public().(ed25519.PublicKey))
	publish := func(priv ed25519.PrivateKey, digestChar string) {
		data, sig := signedDoc(t, entryDoc("hello", digestChar), priv)
		f.put("/mikan-addon.json", data)
		f.put("/mikan-addon.json.sig", []byte(sig))
	}
	url := f.srv.URL + "/mikan-addon.json"

	publish(author, "b")
	doc, err := m.FetchSource(ctx, url, SourcePin{})
	if err != nil || doc.Key != authorKey {
		t.Fatalf("first fetch: %+v %v", doc, err)
	}
	pin := SourcePin{Key: authorKey}
	if _, err := m.FetchSource(ctx, url, pin); err != nil {
		t.Fatalf("same key: %v", err)
	}

	// Another key signs the file: refused, and the error carries the key it would take.
	publish(thief, "c")
	_, err = m.FetchSource(ctx, url, pin)
	var kc *KeyChangedError
	if !errors.Is(err, ErrSourceKeyChanged) || !errors.As(err, &kc) || kc.Key != thiefKey || SourceCode(err) != "source_key_changed" {
		t.Fatalf("another key: %v", err)
	}
	// A fingerprint that is not the new key's does not take it; its own does.
	if _, err := m.FetchSource(ctx, url, SourcePin{Key: authorKey, Accept: Fingerprint(authorKey)}); !errors.Is(err, ErrSourceKeyChanged) {
		t.Fatalf("the old fingerprint: %v", err)
	}
	if _, err := m.FetchSource(ctx, url, SourcePin{Key: authorKey, Accept: "0000:0000"}); !errors.Is(err, ErrSourceKeyChanged) {
		t.Fatalf("a made-up fingerprint: %v", err)
	}
	if doc, err := m.FetchSource(ctx, url, SourcePin{Key: authorKey, Accept: Fingerprint(thiefKey)}); err != nil || doc.Key != thiefKey {
		t.Fatalf("accepted: %+v %v", doc, err)
	}

	// No signature at all where one was pinned: refused, and nothing can accept that.
	data, _ := json.Marshal(entryDoc("hello", "d"))
	f.put("/mikan-addon.json", data)
	f.del("/mikan-addon.json.sig")
	for _, accept := range []string{"", Fingerprint(authorKey), Fingerprint("")} {
		_, err = m.FetchSource(ctx, url, SourcePin{Key: authorKey, Accept: accept})
		if !errors.As(err, &kc) || kc.Key != "" {
			t.Fatalf("unsigned after signed (accept %q): %v", accept, err)
		}
	}
	// A source being added with no key pins the one it is signed with.
	publish(author, "e")
	if doc, err := m.FetchSource(ctx, url, SourcePin{}); err != nil || doc.Key != authorKey {
		t.Fatalf("first fetch of a signed source: %+v %v", doc, err)
	}
	// A source held without a key that begins to sign is not pinned unasked: the new key waits
	// for the admin's fingerprint like any other.
	unsigned := SourcePin{Unsigned: true}
	if _, err := m.FetchSource(ctx, url, unsigned); !errors.As(err, &kc) || kc.Key != authorKey {
		t.Fatalf("unsigned then signed: %v", err)
	}
	if _, err := m.FetchSource(ctx, url, SourcePin{Unsigned: true, Accept: Fingerprint(thiefKey)}); !errors.Is(err, ErrSourceKeyChanged) {
		t.Fatalf("another key's fingerprint: %v", err)
	}
	if doc, err := m.FetchSource(ctx, url, SourcePin{Unsigned: true, Accept: Fingerprint(authorKey)}); err != nil || doc.Key != authorKey {
		t.Fatalf("accepted: %+v %v", doc, err)
	}
	// Still unsigned is no change.
	f.del("/mikan-addon.json.sig")
	if doc, err := m.FetchSource(ctx, url, unsigned); err != nil || doc.Key != "" {
		t.Fatalf("still unsigned: %+v %v", doc, err)
	}
	publish(author, "e")

	// A signature that does not verify, or a file naming a key it was not signed with.
	f.put("/mikan-addon.json.sig", []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))))
	if _, err := m.FetchSource(ctx, url, SourcePin{}); !errors.Is(err, ErrSourceSignature) {
		t.Fatalf("zero signature: %v", err)
	}
	f.put("/mikan-addon.json.sig", []byte("not base64 at all"))
	if _, err := m.FetchSource(ctx, url, SourcePin{}); !errors.Is(err, ErrSourceSignature) {
		t.Fatalf("garbage signature: %v", err)
	}
	f.put("/mikan-addon.json.sig", nil)
	if _, err := m.FetchSource(ctx, url, SourcePin{}); !errors.Is(err, ErrSourceSignature) {
		t.Fatalf("empty signature: %v", err)
	}
	// Signed by one key while naming another.
	doc2 := entryDoc("hello", "f")
	doc2["key"] = thiefKey
	forged, _ := json.Marshal(doc2)
	f.put("/mikan-addon.json", forged)
	f.put("/mikan-addon.json.sig", []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(author, forged))))
	if _, err := m.FetchSource(ctx, url, SourcePin{}); !errors.Is(err, ErrSourceSignature) {
		t.Fatalf("signed by another key than named: %v", err)
	}
	// A signature over other bytes (the file changed after it was signed).
	data, sig := signedDoc(t, entryDoc("hello", "b"), author)
	f.put("/mikan-addon.json", append(data[:len(data)-1:len(data)-1], []byte(` }`)...))
	f.put("/mikan-addon.json.sig", []byte(sig))
	if _, err := m.FetchSource(ctx, url, SourcePin{}); !errors.Is(err, ErrSourceSignature) {
		t.Fatalf("a changed file: %v", err)
	}
}

// What a source lists is checked entry by entry: the good ones stay, the rest is named.
func TestSourceEntriesAreChecked(t *testing.T) {
	f := newFileServer(t)
	m := srcManager(t, devNet())
	m.version = "0.5.1.0"
	mod := func(id string, change func(map[string]any)) map[string]any {
		e := entryDoc(id, "b")
		change(e)
		return e
	}
	entries := []any{
		entryDoc("good", "b"),
		mod("tagged", func(e map[string]any) { e["image"] = "registry.example.com/me/tagged:latest" }),
		mod("digestless", func(e map[string]any) { delete(e, "digest") }),
		mod("digestinimage", func(e map[string]any) { e["image"] = "registry.example.com/me/x@sha256:" + strings.Repeat("b", 64) }),
		mod("shortdigest", func(e map[string]any) { e["digest"] = "sha256:abc" }),
		mod("pay", func(e map[string]any) { e["kind"] = "payment" }),
		mod("pay1", func(e map[string]any) { e["protocol"] = 1; delete(e, "kind"); delete(e, "permissions") }),
		mod("proto3", func(e map[string]any) { e["protocol"] = 3 }),
		mod("badperm", func(e map[string]any) { e["permissions"] = []string{"root"} }),
		mod("dupperm", func(e map[string]any) { e["permissions"] = []string{"kv", "kv"} }),
		mod("badkind", func(e map[string]any) { e["kind"] = "evil" }),
		mod("bigmem", func(e map[string]any) { e["resources"] = map[string]int{"mem_mb": 4096} }),
		mod("Bad_ID", func(e map[string]any) {}),
		mod("future", func(e map[string]any) { e["min_panel"] = "9.0.0" }),
		mod("badversion", func(e map[string]any) { e["version"] = "1 0 0\n" }),
		mod("goodtwice", func(e map[string]any) {}),
		mod("goodtwice", func(e map[string]any) {}),
		mod("badimage", func(e map[string]any) { e["image"] = "Registry.Example.com/../x" }),
		mod("texts", func(e map[string]any) {
			e["name"] = map[string]string{"en": "  Name\x00 with\x1b controls  ", "waytoolonglanguagetag": "x"}
			e["description"] = map[string]string{"en": strings.Repeat("я", 900)}
			e["homepage"] = "javascript:alert(1)"
		}),
		mod("homepage", func(e map[string]any) { e["homepage"] = "https://example.com/ok" }),
		mod("hubimg", func(e map[string]any) { e["image"] = "me/hub" }),
		mod("portimg", func(e map[string]any) { e["image"] = "registry.example.com:5000/me/x" }),
		// The official bot's slot is not for a source's addon; nor is a registry on this machine
		// or its network.
		mod("telegram", func(e map[string]any) {}),
		mod("loopimg", func(e map[string]any) { e["image"] = "127.0.0.1:2375/x" }),
		mod("localimg", func(e map[string]any) { e["image"] = "localhost/x" }),
		mod("lanimg", func(e map[string]any) { e["image"] = "10.0.0.5:5000/x" }),
		mod("bareimg", func(e map[string]any) { e["image"] = "registry:5000/x" }),
	}
	f.put("/index.json", mustJSON(t, map[string]any{"adapters": entries}))
	doc, err := m.FetchSource(t.Context(), f.srv.URL+"/index.json", SourcePin{})
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, e := range doc.Entries {
		kept = append(kept, e.ID)
	}
	if got := strings.Join(kept, ","); got != "good,goodtwice,texts,homepage,hubimg,portimg" {
		t.Fatalf("kept: %s", got)
	}
	reasons := map[string]string{}
	for _, s := range doc.Skipped {
		reasons[s.ID] = s.Reason
	}
	for id, want := range map[string]string{"tagged": "image_invalid", "digestinimage": "image_invalid", "digestless": "digest_invalid", "shortdigest": "digest_invalid", "pay": "payment", "pay1": "payment",
		"proto3": "invalid", "badperm": "invalid", "dupperm": "invalid", "badkind": "invalid", "bigmem": "invalid", "Bad_ID": "id_invalid", "future": "min_panel",
		"badversion": "version_invalid", "goodtwice": "duplicate", "badimage": "image_invalid",
		"telegram": "id_taken", "loopimg": "image_invalid", "localimg": "image_invalid", "lanimg": "image_invalid", "bareimg": "image_invalid"} {
		if reasons[id] != want {
			t.Errorf("%s: %q, want %q (all: %v)", id, reasons[id], want, reasons)
		}
	}
	texts := doc.Entries[2]
	// Control characters are dropped, not turned into spaces; the ends are trimmed.
	if texts.Name["en"] != "Name with controls" {
		t.Errorf("name: %q", texts.Name["en"])
	}
	if _, long := texts.Name["waytoolonglanguagetag"]; long || len([]rune(texts.Description["en"])) != 500 || texts.Homepage != "" {
		t.Errorf("texts: %+v", texts)
	}
	if doc.Entries[3].Homepage != "https://example.com/ok" {
		t.Errorf("homepage: %q", doc.Entries[3].Homepage)
	}
	// More than the limit: the rest is named, the first ones stay.
	var many []any
	for i := range MaxSourceEntries + 5 {
		many = append(many, entryDoc(fmt.Sprintf("a%02d", i), "b"))
	}
	f.put("/many.json", mustJSON(t, map[string]any{"adapters": many}))
	doc, err = m.FetchSource(t.Context(), f.srv.URL+"/many.json", SourcePin{})
	if err != nil || len(doc.Entries) != MaxSourceEntries || len(doc.Skipped) != 5 || doc.Skipped[0].Reason != "too_many" {
		t.Fatalf("many: %d kept, %d skipped, %v", len(doc.Entries), len(doc.Skipped), err)
	}
}

// ghFake is GitHub for the shorthand: the API names the release's assets, a download hands
// over to another host as GitHub's does.
type ghFake struct {
	api, assets *httptest.Server
	mu          sync.Mutex
	release     map[string]any // nil: no release
	files       map[string][]byte
}

func newGHFake(t *testing.T) *ghFake {
	t.Helper()
	g := &ghFake{files: map[string][]byte{}}
	g.assets = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		data, ok := g.files[r.URL.Path]
		g.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	g.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.EqualFold(r.URL.Path, "/repos/owner/repo/releases/latest"):
			g.mu.Lock()
			rel := g.release
			g.mu.Unlock()
			if rel == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(rel)
		case strings.HasPrefix(r.URL.Path, "/owner/repo/releases/download/"):
			// github.com answers a download with a redirect to where the file is.
			http.Redirect(w, r, g.assets.URL+"/blob"+strings.TrimPrefix(r.URL.Path, "/owner/repo/releases/download"), http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.api.Close)
	t.Cleanup(g.assets.Close)
	return g
}

func (g *ghFake) net() *sourceNet {
	n := newSourceNet(true)
	n.ghAPI, n.ghWeb = g.api.URL, g.api.URL
	n.ghHosts = map[string]bool{strings.TrimPrefix(g.api.URL, "http://"): true}
	n.assetHosts = map[string]bool{strings.TrimPrefix(g.assets.URL, "http://"): true}
	return n
}

func (g *ghFake) publish(names ...string) {
	var assets []map[string]string
	for _, name := range names {
		assets = append(assets, map[string]string{"name": name, "browser_download_url": g.api.URL + "/owner/repo/releases/download/v1/" + name})
	}
	g.mu.Lock()
	g.release = map[string]any{"tag_name": "v1", "assets": assets}
	g.mu.Unlock()
}

func (g *ghFake) file(name string, data []byte) {
	g.mu.Lock()
	g.files["/blob/v1/"+name] = data
	g.mu.Unlock()
}

func TestSourceGitHubShorthand(t *testing.T) {
	g := newGHFake(t)
	m := srcManager(t, g.net())
	priv := newPriv(t)
	ctx := t.Context()
	const src = "github.com/owner/repo"

	// No release yet.
	if _, err := m.FetchSource(ctx, src, SourcePin{}); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("no release: %v", err)
	}
	// A release without the files.
	g.publish("something-else.zip")
	if _, err := m.FetchSource(ctx, src, SourcePin{}); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("no asset: %v", err)
	}
	// index.json when there is no mikan-addon.json, unsigned.
	g.publish("index.json")
	g.file("index.json", mustJSON(t, map[string]any{"adapters": []any{entryDoc("one", "b"), entryDoc("two", "c")}}))
	doc, err := m.FetchSource(ctx, src, SourcePin{})
	if err != nil || len(doc.Entries) != 2 || doc.Title != "owner/repo" || doc.Key != "" {
		t.Fatalf("index.json: %+v %v", doc, err)
	}
	// mikan-addon.json wins over index.json, and its signature is the asset beside it.
	data, sig := signedDoc(t, entryDoc("solo", "d"), priv)
	g.publish("index.json", "mikan-addon.json", "mikan-addon.json.sig")
	g.file("mikan-addon.json", data)
	g.file("mikan-addon.json.sig", []byte(sig))
	doc, err = m.FetchSource(ctx, src, SourcePin{})
	if err != nil || len(doc.Entries) != 1 || doc.Entries[0].ID != "solo" || doc.Key == "" {
		t.Fatalf("mikan-addon.json: %+v %v", doc, err)
	}
	// The same source later signed with another key.
	data2, sig2 := signedDoc(t, entryDoc("solo", "e"), newPriv(t))
	g.file("mikan-addon.json", data2)
	g.file("mikan-addon.json.sig", []byte(sig2))
	if _, err := m.FetchSource(ctx, src, SourcePin{Key: doc.Key}); !errors.Is(err, ErrSourceKeyChanged) {
		t.Fatalf("another key: %v", err)
	}
	// A listed file that is not on the repository's own release page is not fetched.
	g.mu.Lock()
	g.release = map[string]any{"assets": []map[string]string{{"name": "mikan-addon.json", "browser_download_url": "https://evil.example/mikan-addon.json"},
		{"name": "index.json", "browser_download_url": g.api.URL + "/other/repo/releases/download/v1/index.json"},
		{"name": "mikan-addon.json.sig", "browser_download_url": g.api.URL + "/owner/repo/releases/download/v1/x?token=1"}}}
	g.mu.Unlock()
	if _, err := m.FetchSource(ctx, src, SourcePin{}); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("foreign asset urls: %v", err)
	}
	// A broken answer from the API.
	g.mu.Lock()
	g.release = map[string]any{"assets": "none"}
	g.mu.Unlock()
	if _, err := m.FetchSource(ctx, src, SourcePin{}); !errors.Is(err, ErrSourceBadAnswer) {
		t.Fatalf("broken release: %v", err)
	}
	// GitHub names the repository as it was created; the admin may have typed it otherwise.
	g.publish("mikan-addon.json")
	if doc, err := m.FetchSource(ctx, "github.com/Owner/REPO", SourcePin{}); err != nil || len(doc.Entries) != 1 {
		t.Fatalf("another spelling of the repository: %+v %v", doc, err)
	}
	// The same shorthand written as a URL is the same source.
	if c, err := m.CanonicalSourceURL("https://github.com/owner/repo"); err != nil || c != src {
		t.Fatalf("canonical: %q %v", c, err)
	}
}

func TestSourceGitHubHandOverIsLimitedToItsAssetHosts(t *testing.T) {
	g := newGHFake(t)
	evil := newFileServer(t)
	evil.put("/mikan-addon.json", mustJSON(t, entryDoc("evil", "b")))
	g.publish("mikan-addon.json")
	g.file("mikan-addon.json", mustJSON(t, entryDoc("hello", "b")))
	n := g.net()
	m := srcManager(t, n)
	if doc, err := m.FetchSource(t.Context(), "github.com/owner/repo", SourcePin{}); err != nil || doc.Entries[0].ID != "hello" {
		t.Fatalf("through the asset host: %+v %v", doc, err)
	}
	// The asset host is not one of GitHub's: the download that hands over to it is refused.
	n.assetHosts = map[string]bool{strings.TrimPrefix(evil.srv.URL, "http://"): true}
	if _, err := m.FetchSource(t.Context(), "github.com/owner/repo", SourcePin{}); !errors.Is(err, ErrSourceRedirect) {
		t.Fatalf("a hand-over to another host: %v", err)
	}
}

func TestOwners(t *testing.T) {
	got := Owners(map[string]bool{"telegram": true, "yookassa": true},
		map[string]int64{"installed": 7, "telegram": 9},
		// A claim outlives the lower number of a source that lists the id later; the panel's own
		// and an installed addon's ids stay what they are.
		map[string]int64{"shared": 3, "claimed": 7, "yookassa": 4, "installed": 2})
	want := map[string]int64{"telegram": 0, "yookassa": 0, "installed": 7, "shared": 3, "claimed": 7}
	if len(got) != len(want) {
		t.Fatalf("owners: %v", got)
	}
	for id, src := range want {
		if got[id] != src {
			t.Errorf("%s: %d, want %d", id, got[id], src)
		}
	}
}

// A file signed as the official catalog is (no key inside it) is read with the key the admin
// gives; with none, the panel asks for it rather than calling the signature wrong.
func TestSourceSignedWithoutAKeyInside(t *testing.T) {
	f := newFileServer(t)
	m := srcManager(t, devNet())
	ctx := t.Context()
	author := newPriv(t)
	authorKey := base64.StdEncoding.EncodeToString(author.Public().(ed25519.PublicKey))
	data, err := json.Marshal(map[string]any{"version": 1, "adapters": []any{entryDoc("hello", "b")}})
	if err != nil {
		t.Fatal(err)
	}
	f.put("/index.json", data)
	f.put("/index.json.sig", []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(author, data))))
	url := f.srv.URL + "/index.json"

	if _, err := m.FetchSource(ctx, url, SourcePin{}); !errors.Is(err, ErrSourceKeyNeeded) || SourceCode(err) != "source_key_needed" {
		t.Fatalf("no key anywhere: %v", err)
	}
	if doc, err := m.FetchSource(ctx, url, SourcePin{Key: authorKey}); err != nil || doc.Key != authorKey || len(doc.Entries) != 1 {
		t.Fatalf("the admin's key: %+v %v", doc, err)
	}
	other := base64.StdEncoding.EncodeToString(newPriv(t).Public().(ed25519.PublicKey))
	if _, err := m.FetchSource(ctx, url, SourcePin{Key: other}); !errors.Is(err, ErrSourceSignature) {
		t.Fatalf("a key that did not sign it: %v", err)
	}
}
