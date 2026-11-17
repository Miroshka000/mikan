package node

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mikan/internal/frontpage"
	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/proto"
)

// Apply writes the chosen page under the core's home, named after the template and the
// page's own bytes; a name that is none of the templates writes no file, there or above it.
// The installation's seed makes another page, and so another file: the core reads a page
// when an inbound starts and not again, and only a new name starts the inbounds anew.
func TestFrontPageFile(t *testing.T) {
	dir := t.TempDir()
	e, err := Start(Options{DataDir: dir, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.Apply(nodeapi.DesiredState{Revision: 99})
		frontpage.SetSeed("")
	})
	front := filepath.Join(dir, "mihomo", "front")
	only := func() string {
		t.Helper()
		files, err := os.ReadDir(front)
		if err != nil || len(files) != 1 {
			t.Fatalf("front pages: %v %v", files, err)
		}
		return files[0].Name()
	}
	if _, err := e.Apply(nodeapi.DesiredState{Revision: 1, FrontPage: "cats"}); err != nil {
		t.Fatal(err)
	}
	plain := only()
	if got, err := os.ReadFile(filepath.Join(front, plain)); err != nil || !bytes.Equal(got, frontpage.Page("cats")) || plain != frontFile("cats", got) || !strings.HasPrefix(plain, "cats-") {
		t.Fatalf("the page's file %s: %v", plain, err)
	}
	// The seed comes with a later state, as after an update: the page is another, and so is
	// its file, and the file of before is gone.
	if _, err := e.Apply(nodeapi.DesiredState{Revision: 2, FrontPage: "cats", FrontSeed: "the-installations-own"}); err != nil {
		t.Fatal(err)
	}
	seeded := only()
	got, err := os.ReadFile(filepath.Join(front, seeded))
	if err != nil || seeded == plain || !bytes.Equal(got, frontpage.Page("cats")) || bytes.Contains(got, []byte("[[")) {
		t.Fatalf("the seeded page's file %s (was %s): %v", seeded, plain, err)
	}
	if _, err := e.Apply(nodeapi.DesiredState{Revision: 3, FrontPage: "cats", FrontSeed: "the-installations-own"}); err != nil {
		t.Fatal(err)
	}
	if again := only(); again != seeded {
		t.Fatalf("the same page got another file: %s, then %s", seeded, again)
	}
	// The admin's own page comes whole with the state, and another one is another file.
	own := "<!doctype html><title>Bakery</title>"
	if _, err := e.Apply(nodeapi.DesiredState{Revision: 4, FrontPage: frontpage.Custom, FrontHTML: own}); err != nil {
		t.Fatal(err)
	}
	custom := only()
	if got, err := os.ReadFile(filepath.Join(front, custom)); err != nil || string(got) != own || !strings.HasPrefix(custom, "custom-") {
		t.Fatalf("the admin's page's file %s: %q %v", custom, got, err)
	}
	if _, err := e.Apply(nodeapi.DesiredState{Revision: 5, FrontPage: frontpage.Custom, FrontHTML: own + "<p>open on Sundays</p>"}); err != nil {
		t.Fatal(err)
	}
	if next := only(); next == custom {
		t.Fatalf("a changed page kept its file %s", custom)
	}
	if _, err := e.Apply(nodeapi.DesiredState{Revision: 6, FrontPage: "cats", FrontSeed: "the-installations-own"}); err != nil {
		t.Fatal(err)
	}
	if back := only(); back != seeded || frontpage.Page(frontpage.Custom) != nil {
		t.Fatalf("back to the built-in page: %s, want %s", back, seeded)
	}
	if _, err := e.Apply(nodeapi.DesiredState{Revision: 7, FrontPage: "../../escape"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.html")); err == nil {
		t.Fatal("a file out of the front pages")
	}
	if left := only(); left != seeded {
		t.Fatalf("front pages after a name that is no template: %s", left)
	}
}

// The front page goes to the inbounds on an HTTP transport, VLESS and Trojan, as their
// fallback-page; a plain VLESS and the other protocols take none, and with no page none has
// it. The core's parser takes them all.
func TestFrontPageInTheConfig(t *testing.T) {
	tpl := func(y string) json.RawMessage {
		t.Helper()
		tm, err := proto.Parse(y)
		if err != nil {
			t.Fatal(err)
		}
		return tm.JSON()
	}
	st := nodeapi.DesiredState{Inbounds: []nodeapi.Inbound{
		{Name: "ws", Port: "8443", Config: tpl("type: vless\nws-path: /ws\nmikan:\n  tls: node\n")},
		{Name: "xhttp", Port: "8444", Config: tpl("type: vless\nxhttp-config:\n  path: /x\nmikan:\n  tls: node\n")},
		{Name: "grpc", Port: "8445", Config: tpl("type: trojan\ngrpc-service-name: GunService\nmikan:\n  tls: node\n")},
		{Name: "plain", Port: "8446", Config: tpl("type: vless\nmikan:\n  tls: node\n")},
		{Name: "hy2", Port: "8447", Config: tpl("type: hysteria2\n")},
	}}
	cert := proto.Cert{CertPath: "/tmp/c.pem", KeyPath: "/tmp/k.pem"}
	pages := func(front string) map[string]any {
		t.Helper()
		raw, rejected, err := buildConfig(st, cert, false, front)
		if err != nil || len(rejected) != 0 {
			t.Fatalf("config: %v %+v", err, rejected)
		}
		var cfg struct {
			Listeners []map[string]any `json:"listeners"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		out := map[string]any{}
		for _, l := range cfg.Listeners {
			out[l["name"].(string)] = l["fallback-page"]
		}
		return out
	}
	got := pages("/var/lib/mikan-node/front/cats.html")
	for name, want := range map[string]bool{"ws": true, "xhttp": true, "grpc": true, "plain": false, "hy2": false} {
		if has := got[name] == "/var/lib/mikan-node/front/cats.html"; has != want || !want && got[name] != nil {
			t.Errorf("%s: fallback-page %v", name, got[name])
		}
	}
	for name, page := range pages("") {
		if page != nil {
			t.Errorf("%s: a page with none chosen: %v", name, page)
		}
	}
}

// What a stranger gets on a protocol's port follows the page: the core reads the page when
// an inbound starts and not again, so a page that changes after the start (the
// installation's seed arriving with a later state, as right after an update) starts the
// inbound anew, and the port answers with the new page and not the one of before.
func TestFrontPageOnTheWireFollowsTheSeed(t *testing.T) {
	e, err := Start(Options{DataDir: t.TempDir(), Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.Apply(nodeapi.DesiredState{Revision: 99})
		frontpage.SetSeed("")
	})
	pair, err := nodetls.Generate("node.test", x509.ExtKeyUsageServerAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	state := func(rev int64, seed string) nodeapi.DesiredState {
		return nodeapi.DesiredState{
			Revision:  rev,
			Inbounds:  []nodeapi.Inbound{{Name: "ws", Port: port, Config: []byte(`{"type":"vless","ws-path":"/w","mikan":{"tls":"node"}}`)}},
			Slots:     []nodeapi.Slot{{Name: "s1", UUID: "00000000-0000-4000-8000-000000000001", Secret: "a"}},
			TLS:       &nodeapi.TLSFiles{CertPEM: pair.CertPEM, KeyPEM: pair.KeyPEM},
			FrontPage: "cats",
			FrontSeed: seed,
		}
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DisableKeepAlives: true}}
	served := func() []byte {
		t.Helper()
		resp, err := client.Get("https://127.0.0.1:" + port + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("the page: %d %v", resp.StatusCode, err)
		}
		return body
	}

	if _, err := e.Apply(state(1, "")); err != nil {
		t.Fatal(err)
	}
	plain := served()
	if !bytes.Equal(plain, frontpage.Page("cats")) {
		t.Fatal("the port does not answer with the page")
	}
	res, err := e.Apply(state(2, "the-installations-own"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Recreated, []string{"ws"}) {
		t.Fatalf("the inbound kept running with the page of before: recreated %v", res.Recreated)
	}
	seeded := served()
	if bytes.Equal(seeded, plain) || !bytes.Equal(seeded, frontpage.Page("cats")) {
		t.Fatal("the port still answers with the page of before the seed")
	}
	// The same page again starts nothing anew.
	if res, err = e.Apply(state(3, "the-installations-own")); err != nil || len(res.Recreated) != 0 {
		t.Fatalf("the same page recreated %v: %v", res.Recreated, err)
	}
}
