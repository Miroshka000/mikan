package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/store/db"
)

// A lab for the third-party sources: the panel in development mode (a source on 127.0.0.1 is
// allowed there), a signed official catalog, a source server whose files the test changes, and
// the host's state file.
type srcLab struct {
	t        *testing.T
	h        *harness
	dir      string
	csrf     map[string]string
	api      string
	src      *httptest.Server
	mu       sync.Mutex
	files    map[string][]byte
	failing  map[string]bool // paths that answer 503
	hits     map[string]int
	official ed25519.PublicKey
}

func digestOf(c string) string { return "sha256:" + strings.Repeat(c, 64) }

// srcEntry is an entry of a source: protocol 2, an image by its digest.
func srcEntry(id, version, digestChar, kind string, perms ...string) map[string]any {
	if perms == nil {
		perms = []string{}
	}
	return map[string]any{"id": id, "name": map[string]string{"en": "Addon " + id}, "version": version, "protocol": 2, "kind": kind,
		"permissions": perms, "image": "registry.example.com/me/" + id, "digest": digestOf(digestChar), "homepage": "https://example.com/" + id}
}

func newSrcLab(t *testing.T) *srcLab {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	data, _ := json.Marshal(addons.Catalog{Version: 1, Adapters: []addons.Entry{
		{ID: "telegram", Version: "1.0.0", Protocol: 2, Kind: addons.KindBot, Permissions: []string{"bot"}, Image: "ghcr.io/getmikan/telegram", Digest: digestOf("a")},
		{ID: "yookassa", Version: "1.0.0", Protocol: 1, Image: "ghcr.io/getmikan/adapter-yookassa", Digest: digestOf("a")},
	}})
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			_, _ = w.Write(data)
		case "/index.json.sig":
			_, _ = io.WriteString(w, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(catalog.Close)
	l := &srcLab{t: t, dir: t.TempDir(), files: map[string][]byte{}, failing: map[string]bool{}, hits: map[string]int{}, official: pub}
	l.src = httptest.NewServer(http.HandlerFunc(l.serve))
	t.Cleanup(l.src.Close)
	l.h = newHarness(t, func(o *Options) { o.DataDir, o.AddonsCatalog, o.Dev = l.dir, catalog.URL+"/index.json", true })
	l.h.p.Addons.SetKey(pub)
	if resp, _ := l.h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	l.csrf = map[string]string{"X-CSRF-Token": l.h.csrf}
	l.api = "/" + adminPath + "/api/v1"
	l.host(addons.State{Adapters: map[string]addons.Installed{}, HostVersion: "0.5.1.0"})
	return l
}

func (l *srcLab) serve(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	l.hits[r.URL.Path]++
	data, ok := l.files[r.URL.Path]
	down := l.failing[r.URL.Path]
	l.mu.Unlock()
	switch {
	case down:
		http.Error(w, "down", http.StatusServiceUnavailable)
	case r.URL.Path == "/redirect-other":
		// Another name for the same machine: another host as far as redirects go.
		http.Redirect(w, r, "http://localhost:"+strings.Split(l.src.URL, ":")[2]+"/mikan-addon.json", http.StatusFound)
	case r.URL.Path == "/redirect-same":
		http.Redirect(w, r, "/mikan-addon.json", http.StatusFound)
	case r.URL.Path == "/boom.json":
		http.Error(w, "boom", http.StatusInternalServerError)
	case r.URL.Path == "/huge.json":
		_, _ = w.Write(append([]byte(`{"x":"`), []byte(strings.Repeat("a", addons.SourceLimit))...))
	case !ok:
		http.NotFound(w, r)
	default:
		_, _ = w.Write(data)
	}
}

// publish sets the file at path to doc. A key signs it (the doc names the key, as a signed
// source does); nil leaves it unsigned.
func (l *srcLab) publish(path string, doc map[string]any, priv ed25519.PrivateKey) {
	l.t.Helper()
	if priv != nil {
		doc["key"] = base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	}
	data, err := json.Marshal(doc)
	if err != nil {
		l.t.Fatal(err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files[path] = data
	if priv != nil {
		l.files[path+".sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)))
	} else {
		delete(l.files, path+".sig")
	}
}

func (l *srcLab) catalogDoc(entries ...map[string]any) map[string]any {
	return map[string]any{"version": 1, "title": "Test author", "adapters": entries}
}

func (l *srcLab) url(path string) string { return l.src.URL + path }

func (l *srcLab) host(st addons.State) {
	l.t.Helper()
	data, _ := json.Marshal(st)
	if err := os.MkdirAll(filepath.Join(l.dir, "addons"), 0o755); err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.dir, "addons", "state.json"), data, 0o600); err != nil {
		l.t.Fatal(err)
	}
}

// hostRan: the host took the requests and left these addons running.
func (l *srcLab) hostRan(adapters map[string]addons.Installed) {
	l.t.Helper()
	if err := os.RemoveAll(filepath.Join(l.dir, "addons", "requests")); err != nil {
		l.t.Fatal(err)
	}
	l.host(addons.State{Adapters: adapters, HostVersion: "0.5.1.0"})
}

func (l *srcLab) call(method, path string, body any, wantStatus int, wantIn ...string) []byte {
	l.t.Helper()
	resp, out := l.h.do(method, l.api+path, body, l.csrf)
	if resp.StatusCode != wantStatus {
		l.t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, out, wantStatus)
	}
	for _, w := range wantIn {
		if !strings.Contains(string(out), w) {
			l.t.Fatalf("%s %s: %s, want %q in it", method, path, out, w)
		}
	}
	return out
}

type srcView struct {
	Sources []struct {
		ID             int64  `json:"id"`
		URL            string `json:"url"`
		Title          string `json:"title"`
		Signed         bool   `json:"signed"`
		Fingerprint    string `json:"fingerprint"`
		NewFingerprint string `json:"new_fingerprint"`
		Error          string `json:"error"`
		FetchedAt      string `json:"fetched_at"`
		Installed      []string
		Entries        int `json:"entries"`
		Skipped        []struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	} `json:"sources"`
}

func (l *srcLab) sources() srcView {
	l.t.Helper()
	var v srcView
	if err := json.Unmarshal(l.call(http.MethodGet, "/addon-sources", nil, http.StatusOK), &v); err != nil {
		l.t.Fatal(err)
	}
	return v
}

type marketV struct {
	Catalog []struct {
		ID          string   `json:"id"`
		Version     string   `json:"version"`
		Permissions []string `json:"permissions"`
		Installed   bool     `json:"installed"`
		Update      bool     `json:"update"`
		Wider       bool     `json:"wider"`
		Origin      *struct {
			ID     int64  `json:"id"`
			Title  string `json:"title"`
			Signed bool   `json:"signed"`
		} `json:"origin"`
	} `json:"catalog"`
	Installed []struct {
		ID      string   `json:"id"`
		Version string   `json:"version"`
		Source  string   `json:"source"`
		Granted []string `json:"granted"`
		Origin  *struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"origin"`
	} `json:"installed"`
}

func (l *srcLab) market() marketV {
	l.t.Helper()
	var v marketV
	if err := json.Unmarshal(l.call(http.MethodGet, "/marketplace", nil, http.StatusOK), &v); err != nil {
		l.t.Fatal(err)
	}
	return v
}

func (l *srcLab) grant(id string) (db.AddonGrant, bool) {
	l.t.Helper()
	g, err := l.h.st.Q.GetAddonGrant(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return g, false
	}
	if err != nil {
		l.t.Fatal(err)
	}
	return g, true
}

func (l *srcLab) origin(id string) (int64, bool) {
	l.t.Helper()
	rows, err := l.h.st.Q.ListAddonOrigins(context.Background())
	if err != nil {
		l.t.Fatal(err)
	}
	for _, o := range rows {
		if o.AddonID == id {
			return o.SourceID, true
		}
	}
	return 0, false
}

// A full API key, for the refusals.
func (l *srcLab) key() string {
	l.t.Helper()
	resp, body := l.h.do(http.MethodPost, l.api+"/api-keys", map[string]any{"name": "full", "scope": "full", "password": password}, l.csrf)
	var k struct {
		Key string `json:"key"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &k) != nil {
		l.t.Fatalf("make a key: %d %s", resp.StatusCode, body)
	}
	return k.Key
}

func (l *srcLab) asKey(key, method, path string, body any) (*http.Response, []byte) {
	l.t.Helper()
	jar := l.h.client.Jar
	l.h.client.Jar = nil
	defer func() { l.h.client.Jar = jar }()
	return l.h.do(method, l.api+path, body, map[string]string{"Authorization": "Bearer " + key})
}

func newKey(t *testing.T) ed25519.PrivateKey {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func approve(perms ...string) map[string]any {
	return map[string]any{"permissions": append([]string{}, perms...)}
}

// Adding a source reads it first: what is no source, is not safe to read, lists nothing the
// panel can install, or names an id that is taken, is refused with nothing kept.
func TestAddonSourcesRefusals(t *testing.T) {
	l := newSrcLab(t)
	author := newKey(t)
	l.publish("/ok.json", l.catalogDoc(srcEntry("hello", "1.0.0", "b", "generic", "kv")), author)
	l.publish("/mikan-addon.json", srcEntry("hello", "1.0.0", "b", "generic", "kv"), nil)
	l.publish("/notjson.json", map[string]any{}, nil)
	l.mu.Lock()
	l.files["/notjson.json"] = []byte("<html>not a source</html>")
	l.mu.Unlock()
	l.publish("/payment.json", l.catalogDoc(map[string]any{"id": "pay", "version": "1.0.0", "protocol": 1, "image": "registry.example.com/me/pay", "digest": digestOf("b")}), nil)
	tagged := srcEntry("tagged", "1.0.0", "b", "generic")
	tagged["image"] = "registry.example.com/me/tagged:latest"
	l.publish("/tagged.json", l.catalogDoc(tagged), nil)
	l.publish("/official.json", l.catalogDoc(srcEntry("telegram", "9.9.9", "b", "bot", "bot")), nil)
	l.publish("/payid.json", l.catalogDoc(srcEntry("yookassa", "9.9.9", "b", "generic")), nil)
	l.publish("/badsig.json", l.catalogDoc(srcEntry("hello", "1.0.0", "b", "generic")), author)
	l.mu.Lock()
	l.files["/badsig.json.sig"] = []byte(base64.StdEncoding.EncodeToString(make([]byte, 64)))
	l.mu.Unlock()

	url := func(u string) map[string]any { return map[string]any{"url": u} }
	for what, c := range map[string]struct {
		body map[string]any
		want string
		in   string
	}{
		"ftp":                 {url("ftp://example.com/index.json"), "source_url_invalid", "body.url"},
		"plain http":          {url("http://example.com/index.json"), "source_url_invalid", "body.url"},
		"private address":     {url("http://10.0.0.5/index.json"), "source_url_invalid", "body.url"},
		"localhost":           {url("https://localhost/index.json"), "source_url_invalid", "body.url"},
		"loopback over tls":   {url("https://127.0.0.1/index.json"), "source_url_invalid", "body.url"},
		"link local":          {url("https://169.254.169.254/latest"), "source_url_invalid", "body.url"},
		"credentials":         {url("https://user:pass@example.com/index.json"), "source_url_invalid", "body.url"},
		"query":               {url(l.url("/ok.json") + "?token=1"), "source_url_invalid", "body.url"},
		"no file name":        {url(l.url("/")), "source_url_invalid", "body.url"},
		"github without repo": {url("github.com/owner"), "source_url_invalid", "body.url"},
		"not a url":           {url("just words"), "source_url_invalid", "body.url"},
		"not found":           {url(l.url("/missing.json")), "source_not_found", "body.url"},
		"not json":            {url(l.url("/notjson.json")), "source_bad_answer", "body.url"},
		"server error":        {url(l.url("/boom.json")), "source_unreachable", "body.url"},
		"too large":           {url(l.url("/huge.json")), "source_too_large", "body.url"},
		"redirect elsewhere":  {url(l.url("/redirect-other")), "source_redirect_refused", "body.url"},
		"payment adapter":     {url(l.url("/payment.json")), "source_empty", "payment"},
		"image with a tag":    {url(l.url("/tagged.json")), "source_empty", "image_invalid"},
		"official bot slot":   {url(l.url("/official.json")), "source_empty", "id_taken"},
		"official id":         {url(l.url("/payid.json")), "addon_id_taken", "yookassa"},
		"bad signature":       {url(l.url("/badsig.json")), "source_signature_invalid", "body.url"},
		"bad key":             {map[string]any{"url": l.url("/ok.json"), "key": "not a key"}, "source_key_invalid", "body.key"},
		"other key":           {map[string]any{"url": l.url("/ok.json"), "key": base64.StdEncoding.EncodeToString(newKey(t).Public().(ed25519.PublicKey))}, "source_key_changed", "body.key"},
		"key but unsigned":    {map[string]any{"url": l.url("/mikan-addon.json"), "key": base64.StdEncoding.EncodeToString(author.Public().(ed25519.PublicKey))}, "source_key_changed", "body.key"},
	} {
		resp, out := l.h.do(http.MethodPost, l.api+"/addon-sources", c.body, l.csrf)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(out), c.want) || !strings.Contains(string(out), c.in) {
			t.Errorf("%s: %d %s, want 422 %s in %s", what, resp.StatusCode, out, c.want, c.in)
		}
	}
	// A redirect to the same host is no problem.
	l.call(http.MethodPost, "/addon-sources", url(l.url("/redirect-same")), http.StatusCreated)
	if v := l.sources(); len(v.Sources) != 1 {
		t.Fatalf("only the good one is kept: %+v", v)
	}
	l.call(http.MethodPost, "/addon-sources", url(l.url("/redirect-same")), http.StatusUnprocessableEntity, "source_exists")

	// An API key never adds, refreshes or removes one, however full.
	key := l.key()
	for name, c := range map[string]struct {
		method, path string
		body         any
	}{
		"add":     {http.MethodPost, "/addon-sources", url(l.url("/ok.json"))},
		"refresh": {http.MethodPost, "/addon-sources/1/refresh", nil},
		"remove":  {http.MethodDelete, "/addon-sources/1", nil},
		"list":    {http.MethodGet, "/addon-sources", nil},
	} {
		resp, out := l.asKey(key, c.method, c.path, c.body)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(out), "session_only") {
			t.Errorf("%s with a full key: %d %s", name, resp.StatusCode, out)
		}
	}
	// Nor does it install from one.
	resp, out := l.asKey(key, http.MethodPost, "/marketplace/hello/install", approve("kv"))
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(out), "session_only") {
		t.Errorf("install with a full key: %d %s", resp.StatusCode, out)
	}
	// Nothing without a login at all.
	jar := l.h.client.Jar
	l.h.client.Jar = nil
	if resp, out := l.h.do(http.MethodPost, l.api+"/addon-sources", url(l.url("/ok.json")), nil); resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Errorf("add without a session: %d %s", resp.StatusCode, out)
	}
	l.h.client.Jar = jar
	// Unknown ids.
	l.call(http.MethodDelete, "/addon-sources/999", nil, http.StatusNotFound, "source_unknown")
	l.call(http.MethodPost, "/addon-sources/999/refresh", nil, http.StatusNotFound, "source_unknown")
	l.call(http.MethodPost, "/marketplace/nothere/install", approve(), http.StatusNotFound, "addon_unknown")
	l.call(http.MethodPost, "/marketplace/telegram/install", approve("kv"), http.StatusConflict, "permissions_changed")
}

// A source end to end: added signed, its entries in the marketplace as unofficial, installed
// with exactly the permissions shown, updated with a new approval when they widen, held to its
// key, and removable only once nothing of it is installed.
func TestAddonSourcesInstall(t *testing.T) {
	l := newSrcLab(t)
	author := newKey(t)
	bad := srcEntry("yoo", "1.0.0", "b", "generic")
	bad["kind"] = "payment"
	l.publish("/index.json", l.catalogDoc(srcEntry("hello", "1.0.0", "b", "generic", "kv"), srcEntry("robot", "2.0.0", "c", "bot", "bot", "kv"), bad), author)

	// Add: signed, the key pinned, a payment entry left out and said so.
	out := l.call(http.MethodPost, "/addon-sources", map[string]any{"url": l.url("/index.json")}, http.StatusCreated)
	var v srcView
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Sources) != 1 {
		t.Fatalf("sources: %s", out)
	}
	s := v.Sources[0]
	if !s.Signed || s.Fingerprint == "" || s.Title != "Test author" || s.Error != "" || s.FetchedAt == "" || s.Entries != 2 ||
		len(s.Skipped) != 1 || s.Skipped[0].ID != "yoo" || s.Skipped[0].Reason != "payment" {
		t.Fatalf("source: %+v", s)
	}
	if strings.Contains(string(out), "BEGIN") {
		t.Fatal("a secret in the view")
	}
	row, err := l.h.st.Q.GetAddonSource(context.Background(), s.ID)
	if err != nil || row.Key != base64.StdEncoding.EncodeToString(author.Public().(ed25519.PublicKey)) || row.Error != "" || row.AddedBy.Int64 == 0 {
		t.Fatalf("row: %+v %v", row, err)
	}

	// The marketplace lists the official and the source's entries; the latter are marked.
	m := l.market()
	marked := map[string]string{}
	for _, e := range m.Catalog {
		title := ""
		if e.Origin != nil {
			title = e.Origin.Title
		}
		marked[e.ID] = title
	}
	if len(marked) != 3 || marked["telegram"] != "" || marked["hello"] != "Test author" || marked["robot"] != "Test author" {
		t.Fatalf("catalog: %v", marked)
	}
	// Another source cannot take an id the first holds; the first keeps it.
	l.publish("/second.json", srcEntry("hello", "9.0.0", "e", "generic"), nil)
	l.call(http.MethodPost, "/addon-sources", map[string]any{"url": l.url("/second.json")}, http.StatusUnprocessableEntity, "addon_id_taken", "hello")

	// Install: exactly the permissions shown.
	for _, perms := range [][]string{{}, {"kv", "bot"}, {"users:write"}} {
		l.call(http.MethodPost, "/marketplace/hello/install", approve(perms...), http.StatusConflict, "permissions_changed")
	}
	if _, ok := l.grant("hello"); ok {
		t.Fatal("a refused approval left a grant")
	}
	if _, ok := l.origin("hello"); ok {
		t.Fatal("a refused approval left an origin")
	}
	l.call(http.MethodPost, "/marketplace/hello/install", approve("kv"), http.StatusAccepted)
	if g, ok := l.grant("hello"); !ok || g.Version != "1.0.0" || g.Permissions != `["kv"]` {
		t.Fatalf("grant: %+v %v", g, ok)
	}
	if id, ok := l.origin("hello"); !ok || id != s.ID {
		t.Fatalf("origin: %d %v", id, ok)
	}
	q := l.h.p.Addons.Pending()
	if len(q) != 1 || q[0].ID != "hello" || q[0].Source != "custom" || q[0].Image != "registry.example.com/me/hello" || q[0].Digest != digestOf("b") ||
		q[0].Kind != "generic" || q[0].Mode != "http" || strings.Join(q[0].Permissions, ",") != "kv" || q[0].URL != "" {
		t.Fatalf("the host's request: %+v", q)
	}
	l.call(http.MethodPost, "/marketplace/hello/install", approve("kv"), http.StatusConflict, "addon_busy")
	var audit string
	if err := l.h.st.DB.QueryRow("SELECT details FROM audit_log WHERE action = 'addon.install' ORDER BY id DESC LIMIT 1").Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(audit, "registry.example.com/me/hello") || !strings.Contains(audit, "/index.json") {
		t.Fatalf("audit: %s", audit)
	}

	// The host ran it: the list says where it came from, and the catalog marks it installed.
	l.hostRan(map[string]addons.Installed{"hello": {Image: "registry.example.com/me/hello", Digest: digestOf("b"), Protocol: 2, Source: "custom", Kind: "generic", Status: "running"}})
	m = l.market()
	if len(m.Installed) != 1 || m.Installed[0].ID != "hello" || m.Installed[0].Origin == nil || m.Installed[0].Origin.Title != "Test author" ||
		m.Installed[0].Version != "1.0.0" || m.Installed[0].Source != "custom" {
		t.Fatalf("installed: %+v", m.Installed)
	}
	for _, e := range m.Catalog {
		if e.ID == "hello" && (!e.Installed || e.Update || e.Wider) || e.ID == "robot" && e.Installed {
			t.Fatalf("catalog entry: %+v", e)
		}
	}
	l.call(http.MethodPost, "/marketplace/hello/install", approve("kv"), http.StatusConflict, "addon_current")

	// The source publishes a build asking for more: an update that waits for approval.
	l.publish("/index.json", l.catalogDoc(srcEntry("hello", "1.1.0", "d", "generic", "kv", "users:read"), srcEntry("robot", "2.0.0", "c", "bot", "bot", "kv")), author)
	l.call(http.MethodPost, "/addon-sources/"+itoa(s.ID)+"/refresh", nil, http.StatusOK)
	m = l.market()
	for _, e := range m.Catalog {
		if e.ID == "hello" && (!e.Installed || !e.Update || !e.Wider) {
			t.Fatalf("an update asking for more: %+v", e)
		}
	}
	l.call(http.MethodPost, "/marketplace/hello/install", approve("kv"), http.StatusConflict, "permissions_changed")
	if g, _ := l.grant("hello"); g.Permissions != `["kv"]` {
		t.Fatalf("the grant moved before the approval: %+v", g)
	}
	l.call(http.MethodPost, "/marketplace/hello/install", approve("kv", "users:read"), http.StatusAccepted)
	if g, _ := l.grant("hello"); g.Version != "1.1.0" || g.Permissions != `["kv","users:read"]` {
		t.Fatalf("grant after the update: %+v", g)
	}
	if q := l.h.p.Addons.Pending(); len(q) != 1 || q[0].Digest != digestOf("d") {
		t.Fatalf("update request: %+v", q)
	}
	l.hostRan(map[string]addons.Installed{"hello": {Image: "registry.example.com/me/hello", Digest: digestOf("d"), Protocol: 2, Source: "custom", Kind: "generic", Status: "running"}})

	// The key changes: the source's entries go, and an install of its addon is refused.
	other := newKey(t)
	l.publish("/index.json", l.catalogDoc(srcEntry("hello", "1.2.0", "f", "generic", "kv")), other)
	l.call(http.MethodPost, "/addon-sources/"+itoa(s.ID)+"/refresh", nil, http.StatusOK)
	v = l.sources()
	if e := v.Sources[0]; e.Error != "source_key_changed" || e.NewFingerprint == "" || e.NewFingerprint == e.Fingerprint || e.Entries != 0 || !e.Signed {
		t.Fatalf("after a key change: %+v", e)
	}
	l.call(http.MethodPost, "/marketplace/hello/install", approve("kv"), http.StatusConflict, "source_key_changed")
	// Unsigned is refused as well, and gives no new key to accept.
	l.publish("/index.json", l.catalogDoc(srcEntry("hello", "1.2.0", "f", "generic", "kv")), nil)
	l.call(http.MethodPost, "/addon-sources/"+itoa(s.ID)+"/refresh", nil, http.StatusOK)
	if e := l.sources().Sources[0]; e.Error != "source_key_changed" || e.NewFingerprint != "" {
		t.Fatalf("after losing the signature: %+v", e)
	}
	l.call(http.MethodPost, "/addon-sources/"+itoa(s.ID)+"/refresh?accept="+addons.Fingerprint(base64.StdEncoding.EncodeToString(other.Public().(ed25519.PublicKey))), nil, http.StatusOK)
	if e := l.sources().Sources[0]; e.Error != "source_key_changed" {
		t.Fatalf("a fingerprint accepted an unsigned file: %+v", e)
	}
	// A new key is taken only with its own fingerprint.
	l.publish("/index.json", l.catalogDoc(srcEntry("hello", "1.2.0", "f", "generic", "kv")), other)
	l.call(http.MethodPost, "/addon-sources/"+itoa(s.ID)+"/refresh?accept=0000:0000", nil, http.StatusOK)
	if e := l.sources().Sources[0]; e.Error != "source_key_changed" || e.NewFingerprint == "" {
		t.Fatalf("a wrong fingerprint took the key: %+v", e)
	}
	fp := l.sources().Sources[0].NewFingerprint
	l.call(http.MethodPost, "/addon-sources/"+itoa(s.ID)+"/refresh?accept="+fp, nil, http.StatusOK)
	if e := l.sources().Sources[0]; e.Error != "" || e.Fingerprint != fp || e.Entries != 1 {
		t.Fatalf("after accepting the key: %+v", e)
	}
	var audited int
	if err := l.h.st.DB.QueryRow("SELECT count(*) FROM audit_log WHERE action = 'addon.source.key'").Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("the new key is not audited: %d %v", audited, err)
	}

	// Removing: not while an addon of it is installed.
	l.call(http.MethodDelete, "/addon-sources/"+itoa(s.ID), nil, http.StatusConflict, "source_in_use")
	l.call(http.MethodPost, "/marketplace/hello/remove", nil, http.StatusAccepted)
	if _, ok := l.origin("hello"); ok {
		t.Fatal("the origin outlived the removal")
	}
	if _, ok := l.grant("hello"); ok {
		t.Fatal("the grant outlived the removal")
	}
	l.hostRan(map[string]addons.Installed{})
	l.call(http.MethodDelete, "/addon-sources/"+itoa(s.ID), nil, http.StatusOK)
	if got := l.sources(); len(got.Sources) != 0 {
		t.Fatalf("sources after removal: %+v", got)
	}
	for _, e := range l.market().Catalog {
		if e.Origin != nil {
			t.Fatalf("an entry of a removed source: %+v", e)
		}
	}
	l.call(http.MethodPost, "/marketplace/robot/install", approve("bot", "kv"), http.StatusNotFound, "addon_unknown")
	// The id is free again for another source.
	l.call(http.MethodPost, "/addon-sources", map[string]any{"url": l.url("/second.json")}, http.StatusCreated)
}

// An unsigned source is allowed with the stronger warning; when it later signs, its key is
// pinned from then on.
func TestAddonSourceUnsigned(t *testing.T) {
	l := newSrcLab(t)
	l.publish("/mikan-addon.json", srcEntry("solo", "1.0.0", "b", "ui", "ui"), nil)
	v := l.sources()
	if len(v.Sources) != 0 {
		t.Fatal("sources before adding any")
	}
	l.call(http.MethodPost, "/addon-sources", map[string]any{"url": l.url("/mikan-addon.json")}, http.StatusCreated)
	s := l.sources().Sources[0]
	if s.Signed || s.Fingerprint != "" || s.Entries != 1 || s.Title != "127.0.0.1" {
		t.Fatalf("unsigned source: %+v", s)
	}
	author := newKey(t)
	l.publish("/mikan-addon.json", srcEntry("solo", "1.0.1", "b", "ui", "ui"), author)
	l.call(http.MethodPost, "/addon-sources/"+itoa(s.ID)+"/refresh", nil, http.StatusOK)
	// The key is not pinned behind the admin's back: it waits for the fingerprint, the source
	// stays unsigned (the stronger warning stays) and offers nothing meanwhile.
	s = l.sources().Sources[0]
	if s.Signed || s.Error != "source_key_changed" || s.NewFingerprint == "" || s.Entries != 0 {
		t.Fatalf("a source that began to sign: %+v", s)
	}
	if row, err := l.h.st.Q.GetAddonSource(context.Background(), s.ID); err != nil || row.Key != "" {
		t.Fatalf("the key was pinned unasked: %+v %v", row, err)
	}
	l.call(http.MethodPost, "/addon-sources/"+itoa(s.ID)+"/refresh?accept=0000:0000", nil, http.StatusOK)
	if s = l.sources().Sources[0]; s.Signed || s.Error != "source_key_changed" {
		t.Fatalf("a made-up fingerprint took the key: %+v", s)
	}
	l.call(http.MethodPost, "/addon-sources/"+itoa(s.ID)+"/refresh?accept="+s.NewFingerprint, nil, http.StatusOK)
	if s = l.sources().Sources[0]; !s.Signed || s.Error != "" || s.Entries != 1 {
		t.Fatalf("an accepted key: %+v", s)
	}
	// From now on it is held to the key.
	l.publish("/mikan-addon.json", srcEntry("solo", "1.0.2", "b", "ui", "ui"), nil)
	l.call(http.MethodPost, "/addon-sources/"+itoa(s.ID)+"/refresh", nil, http.StatusOK)
	if s = l.sources().Sources[0]; s.Error != "source_key_changed" {
		t.Fatalf("a pinned source fetched unsigned: %+v", s)
	}
}

// The developer mode: a process of the admin's own on a loopback port, with the admin's word
// that they trust it. The manual form of an image by its digest is gone; an addon installed
// that way before keeps running and can be removed.
func TestMarketplaceExternalAddons(t *testing.T) {
	l := newSrcLab(t)
	author := newKey(t)
	l.publish("/index.json", l.catalogDoc(srcEntry("fromsource", "1.0.0", "b", "generic")), author)
	l.call(http.MethodPost, "/addon-sources", map[string]any{"url": l.url("/index.json")}, http.StatusCreated)

	ext := func(edit func(b map[string]any)) map[string]any {
		b := map[string]any{"id": "devbot", "url": "http://127.0.0.1:45000", "kind": "bot", "mode": "http", "permissions": []string{"kv", "bot"}, "confirm": true}
		if edit != nil {
			edit(b)
		}
		return b
	}
	refused := func(what string, body map[string]any, status int, codes ...string) {
		t.Helper()
		resp, out := l.h.do(http.MethodPost, l.api+"/marketplace/external", body, l.csrf)
		if resp.StatusCode != status {
			t.Fatalf("%s: %d %s, want %d", what, resp.StatusCode, out, status)
		}
		for _, c := range codes {
			if !strings.Contains(string(out), c) {
				t.Fatalf("%s: %s, want %s", what, out, c)
			}
		}
	}
	// The custom form is not there any more.
	resp, _ := l.h.do(http.MethodPost, l.api+"/marketplace/custom", map[string]any{"id": "x"}, l.csrf)
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusCreated {
		t.Fatalf("the custom form still answers: %d", resp.StatusCode)
	}
	// An API key never runs code on the server, however full.
	resp, body := l.asKey(l.key(), http.MethodPost, "/marketplace/external", ext(nil))
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "session_only") {
		t.Fatalf("a full key installs code: %d %s", resp.StatusCode, body)
	}
	l.host(addons.State{Adapters: map[string]addons.Installed{}, HostVersion: ""})
	refused("a host before protocol 2", ext(nil), http.StatusConflict, "host_outdated")
	l.host(addons.State{Adapters: map[string]addons.Installed{}, HostVersion: "0.5.1.0"})
	refused("not confirmed", ext(func(b map[string]any) { b["confirm"] = false }), http.StatusUnprocessableEntity, "addon_not_confirmed")
	refused("an unknown permission", ext(func(b map[string]any) { b["permissions"] = []string{"bot", "root"} }), http.StatusUnprocessableEntity, "addon_permissions_invalid")
	refused("an id of the catalog", ext(func(b map[string]any) { b["id"] = "telegram" }), http.StatusUnprocessableEntity, "addon_id_taken")
	refused("an id of a payment adapter", ext(func(b map[string]any) { b["id"] = "yookassa" }), http.StatusUnprocessableEntity, "addon_id_taken")
	refused("an id of a source", ext(func(b map[string]any) { b["id"] = "fromsource" }), http.StatusUnprocessableEntity, "addon_id_taken")
	for _, url := range []string{"http://10.0.0.5:8080", "http://localhost:8080", "https://127.0.0.1:8080", "http://127.0.0.1:80", "http://0.0.0.0:8080"} {
		refused("external at "+url, ext(func(b map[string]any) { b["url"] = url }), http.StatusUnprocessableEntity, "addon_url_invalid")
	}
	refused("external serving nothing", ext(func(b map[string]any) { delete(b, "url") }), http.StatusUnprocessableEntity, "addon_url_invalid")
	for _, id := range []string{"devbot", "telegram", "yookassa", "fromsource"} {
		if _, ok := l.grant(id); ok {
			t.Fatalf("a refused request left a grant for %s", id)
		}
	}
	if q := l.h.p.Addons.Pending(); len(q) != 0 {
		t.Fatalf("a refused request reached the host: %+v", q)
	}

	l.call(http.MethodPost, "/marketplace/external", ext(nil), http.StatusAccepted)
	if g, ok := l.grant("devbot"); !ok || g.Version != "external" || g.Permissions != `["bot","kv"]` {
		t.Fatalf("grant: %+v %v", g, ok)
	}
	q := l.h.p.Addons.Pending()
	if len(q) != 1 || q[0].ID != "devbot" || q[0].Source != "external" || q[0].URL != "http://127.0.0.1:45000" || q[0].Image != "" || q[0].Digest != "" ||
		q[0].Kind != "bot" || q[0].Mode != "http" || strings.Join(q[0].Permissions, ",") != "kv,bot" {
		t.Fatalf("the host's request: %+v", q)
	}
	refused("again while queued", ext(nil), http.StatusConflict, "addon_busy")
	// A client of the panel needs no address of its own.
	l.call(http.MethodPost, "/marketplace/external", map[string]any{"id": "devclient", "kind": "generic", "mode": "client", "permissions": []string{"kv"}, "confirm": true}, http.StatusAccepted)

	// The host ran them; an addon installed the old way, by its digest, keeps its row and
	// can be removed; it has no source to update from.
	l.hostRan(map[string]addons.Installed{
		"devbot":    {Listen: "127.0.0.1:45000", Protocol: 2, Source: "external", Kind: "bot", Status: "running"},
		"oldcustom": {Image: "ghcr.io/me/mybot", Digest: digestOf("d"), Protocol: 2, Source: "custom", Kind: "bot", Mode: "client", Status: "running"},
	})
	if _, err := l.h.st.DB.Exec("INSERT INTO addon_grants (addon_id, version, permissions, approved_at) VALUES ('oldcustom', 'custom', '[\"kv\"]', 1)"); err != nil {
		t.Fatal(err)
	}
	m := l.market()
	if len(m.Installed) != 2 || m.Installed[0].ID != "devbot" || m.Installed[0].Source != "external" || m.Installed[1].ID != "oldcustom" || m.Installed[1].Source != "custom" || m.Installed[1].Origin != nil {
		t.Fatalf("installed: %+v", m.Installed)
	}
	// The id of the old addon is not a source's to take over.
	l.publish("/index.json", l.catalogDoc(srcEntry("fromsource", "1.0.0", "b", "generic"), srcEntry("oldcustom", "1.0.0", "b", "generic")), author)
	l.call(http.MethodPost, "/addon-sources/1/refresh", nil, http.StatusOK)
	l.call(http.MethodPost, "/marketplace/oldcustom/install", approve(), http.StatusNotFound)
	// The external addon is updated by installing it again; the old one cannot be replaced
	// by a process of the other source.
	refused("the other source under the same id", map[string]any{"id": "oldcustom", "url": "http://127.0.0.1:46000", "kind": "bot", "mode": "http", "permissions": []string{}, "confirm": true},
		http.StatusUnprocessableEntity, "addon_id_taken")
	l.call(http.MethodPost, "/marketplace/oldcustom/remove", nil, http.StatusAccepted)
	if _, ok := l.grant("oldcustom"); ok {
		t.Fatal("the grant outlived the removal")
	}
	if q := l.h.p.Addons.Pending(); len(q) != 1 || q[0].Action != "remove" || q[0].ID != "oldcustom" {
		t.Fatalf("remove request: %+v", q)
	}
}

// The first source to list an id keeps it, however the sources are numbered and whatever the
// panel has in memory; the claim is let go when its source stops listing the id.
func TestAddonSourceIDsAreClaimedByTheFirst(t *testing.T) {
	l := newSrcLab(t)
	l.publish("/a.json", l.catalogDoc(srcEntry("alpha", "1.0.0", "b", "generic")), nil)
	l.publish("/b.json", l.catalogDoc(srcEntry("beta", "1.0.0", "b", "generic")), nil)
	l.call(http.MethodPost, "/addon-sources", map[string]any{"url": l.url("/a.json")}, http.StatusCreated)
	l.call(http.MethodPost, "/addon-sources", map[string]any{"url": l.url("/b.json")}, http.StatusCreated)
	// The older source begins to list what the newer one holds: it does not take it over.
	l.publish("/a.json", l.catalogDoc(srcEntry("alpha", "1.0.0", "b", "generic"), srcEntry("beta", "9.0.0", "e", "generic")), nil)
	l.call(http.MethodPost, "/addon-sources/1/refresh", nil, http.StatusOK)
	owners := map[string]string{}
	for _, e := range l.market().Catalog {
		if e.Origin != nil {
			owners[e.ID] = e.Origin.Title
			if e.ID == "alpha" && e.Origin.ID != 1 {
				t.Fatalf("alpha is not the older source's: %+v", e.Origin)
			}
			if e.ID == "beta" && e.Version != "1.0.0" {
				t.Fatalf("beta offered with the older source's build: %s", e.Version)
			}
		}
	}
	if len(owners) != 2 {
		t.Fatalf("catalog: %v", owners)
	}
	a := l.sources().Sources[0]
	if a.Entries != 1 || len(a.Skipped) != 1 || a.Skipped[0].ID != "beta" || a.Skipped[0].Reason != "id_taken" {
		t.Fatalf("the older source: %+v", a)
	}

	// The panel restarted and the source cannot be read: its claim stands.
	l.h.p.Addons.ForgetSource(1)
	l.mu.Lock()
	delete(l.files, "/a.json")
	l.mu.Unlock()
	l.publish("/c.json", l.catalogDoc(srcEntry("alpha", "2.0.0", "c", "generic")), nil)
	l.call(http.MethodPost, "/addon-sources", map[string]any{"url": l.url("/c.json")}, http.StatusUnprocessableEntity, "addon_id_taken", "alpha")
	if n := len(l.sources().Sources); n != 2 {
		t.Fatalf("a refused source was kept: %d", n)
	}

	// Read in full without the id, its source lets it go.
	l.publish("/a.json", l.catalogDoc(srcEntry("other", "1.0.0", "b", "generic")), nil)
	l.call(http.MethodPost, "/addon-sources/1/refresh", nil, http.StatusOK)
	l.call(http.MethodPost, "/addon-sources", map[string]any{"url": l.url("/c.json")}, http.StatusCreated)
}

// Two adds of the same address at once keep one of them; the other is told it is there.
func TestAddonSourceAddedTwiceAtOnce(t *testing.T) {
	l := newSrcLab(t)
	l.publish("/a.json", l.catalogDoc(srcEntry("alpha", "1.0.0", "b", "generic")), nil)
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := l.h.do(http.MethodPost, l.api+"/addon-sources", map[string]any{"url": l.url("/a.json")}, l.csrf)
			codes[i] = resp.StatusCode
		}()
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusUnprocessableEntity:
		default:
			t.Fatalf("answers: %v", codes)
		}
	}
	if created != 1 || len(l.sources().Sources) != 1 {
		t.Fatalf("answers: %v", codes)
	}
}

// A source that cannot be reached for a while keeps offering what it last offered, so the
// catalog does not flap with the network; an install reads it again and needs it to answer.
func TestAddonSourceUnreachableKeepsItsEntries(t *testing.T) {
	l := newSrcLab(t)
	l.publish("/a.json", l.catalogDoc(srcEntry("alpha", "1.0.0", "b", "generic"), srcEntry("gamma", "1.0.0", "b", "generic")), nil)
	l.call(http.MethodPost, "/addon-sources", map[string]any{"url": l.url("/a.json")}, http.StatusCreated)
	l.mu.Lock()
	l.failing["/a.json"] = true
	l.mu.Unlock()
	l.call(http.MethodPost, "/addon-sources/1/refresh", nil, http.StatusOK)
	s := l.sources().Sources[0]
	if s.Error != "source_unreachable" || s.Entries != 2 {
		t.Fatalf("while down: %+v", s)
	}
	if n := len(l.market().Catalog); n != 3 {
		t.Fatalf("the catalog while the source is down: %d", n)
	}
	l.call(http.MethodPost, "/marketplace/alpha/install", approve(), http.StatusBadGateway, "source_unreachable")
	if _, ok := l.grant("alpha"); ok {
		t.Fatal("a grant for an install the source could not confirm")
	}
}

// A row left by an install from a source that never happened does not make an addon the admin
// installs another way, later, that source's.
func TestAddonOriginRowDoesNotOutliveAFailedInstall(t *testing.T) {
	l := newSrcLab(t)
	l.publish("/a.json", l.catalogDoc(srcEntry("alpha", "1.0.0", "b", "generic")), nil)
	l.call(http.MethodPost, "/addon-sources", map[string]any{"url": l.url("/a.json")}, http.StatusCreated)
	l.call(http.MethodPost, "/marketplace/alpha/install", approve(), http.StatusAccepted)
	if _, ok := l.origin("alpha"); !ok {
		t.Fatal("no origin for a queued install")
	}
	// The host could not pull the image: nothing is installed. The source drops the addon.
	l.hostRan(map[string]addons.Installed{})
	l.publish("/a.json", l.catalogDoc(srcEntry("beta", "1.0.0", "b", "generic")), nil)
	l.call(http.MethodPost, "/addon-sources/1/refresh", nil, http.StatusOK)
	l.call(http.MethodPost, "/marketplace/external", map[string]any{"id": "alpha", "kind": "generic", "mode": "client", "permissions": []string{}, "confirm": true}, http.StatusAccepted)
	if id, ok := l.origin("alpha"); ok {
		t.Fatalf("the developer's own addon is marked as source %d's", id)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
