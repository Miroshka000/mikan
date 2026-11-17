package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// A node the panel lost says hello from a new address: the panel moves it there once the
// node answers there, keeps its API port, writes it down and says so. Not a node known by a
// name, nor to a private address, nor one that does not answer there, nor with the switch
// off, nor when the node's domain leads elsewhere.
func TestReaddressNode(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Unix(1_800_000_000, 0)
	o := Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
	node := func(name, host, dom string) db.Node {
		t.Helper()
		addr := ""
		if host != "" {
			addr = host + ":40000"
		}
		n, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: name, Address: addr, PublicHost: host, Domain: dom, CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	answers := func(context.Context, db.Node, string) error { return nil }
	var dialed []string
	reach := func(_ context.Context, _ db.Node, addr string) error {
		dialed = append(dialed, addr)
		return nil
	}
	var told [][2]string
	moved := func(_ context.Context, _ db.Node, from, to string) { told = append(told, [2]string{from, to}) }

	a := node("A", "203.0.113.5", "")
	if r, ok := readdressNode(ctx, st, nil, o, a.ID, "198.51.100.9", reach, moved); !ok || !r.OK {
		t.Fatalf("moved: %+v %v", r, ok)
	}
	got, err := st.Q.GetNode(ctx, a.ID)
	if err != nil || got.Address != "198.51.100.9:40000" || got.PublicHost != "198.51.100.9" || len(dialed) != 1 || dialed[0] != "198.51.100.9:40000" ||
		len(told) != 1 || told[0] != [2]string{"203.0.113.5", "198.51.100.9"} {
		t.Fatalf("the node: %+v %v %v %v", got, err, dialed, told)
	}

	b := node("B", "203.0.113.6", "")
	for name, c := range map[string]struct {
		id   int64
		seen string
		ok   func(context.Context, db.Node, string) error
	}{
		"a private address":    {b.ID, "10.0.0.9", answers},
		"the same address":     {b.ID, "203.0.113.6", answers},
		"no answer there":      {b.ID, "198.51.100.10", func(context.Context, db.Node, string) error { return errors.New("timeout") }},
		"a node by its name":   {node("C", "node-c.example.com", "").ID, "198.51.100.11", answers},
		"the panel's own node": {node("D", "", "").ID, "198.51.100.12", answers},
	} {
		if _, ok := readdressNode(ctx, st, nil, o, c.id, c.seen, c.ok, moved); ok {
			t.Errorf("%s: moved", name)
		}
	}

	// The node's domain must lead to the new address.
	doh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ans := map[string]any{"Status": 0, "Answer": []any{}}
		if r.URL.Query().Get("type") == "A" {
			ans["Answer"] = []any{map[string]any{"type": 1, "data": "203.0.113.7"}}
		}
		_ = json.NewEncoder(w).Encode(ans)
	}))
	t.Cleanup(doh.Close)
	o.DNS = &dnscheck.Checker{Resolvers: []string{doh.URL}, HTTP: http.DefaultClient}
	e := node("E", "203.0.113.7", "node-e.example.com")
	if _, ok := readdressNode(ctx, st, nil, o, e.ID, "198.51.100.13", answers, moved); ok {
		t.Error("moved while its domain leads to the old address")
	}

	if err := settings.Set(ctx, settings.New(st.Q), settings.KeyAutoNodeAddress, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := readdressNode(ctx, st, nil, o, b.ID, "198.51.100.14", answers, moved); ok {
		t.Error("moved with the switch off")
	}
	if len(told) != 1 {
		t.Fatalf("told: %v", told)
	}
}
