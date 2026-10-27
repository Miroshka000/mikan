package subsrc

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subpage"
	"mikan/internal/sub/apps"
	submodel "mikan/internal/sub/model"
)

// The admin's word keeps a node out at once; a node that does not answer the panel, only
// after nodeDownForAuto; one never checked, or answering, stays in.
func TestOutOfAuto(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	down := func(d time.Duration) NodeHealth { return NodeHealth{Since: now.Add(-d)} }
	for _, c := range []struct {
		name   string
		noAuto int64
		hv     NodeHealth
		known  bool
		want   bool
	}{
		{"the admin's word", 1, NodeHealth{OK: true}, true, true},
		{"answering", 0, NodeHealth{OK: true}, true, false},
		{"not checked yet", 0, NodeHealth{}, false, false},
		{"silent for a minute", 0, down(time.Minute), true, false},
		{"silent long enough", 0, down(nodeDownForAuto), true, true},
	} {
		if got := outOfAuto(c.noAuto, c.hv, c.known, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// fakeNodes is what the node syncers would say of the nodes, by id.
type fakeNodes struct {
	health map[int64]NodeHealth
	served map[int64]string
}

func (f fakeNodes) Health(id int64) (NodeHealth, bool) { h, ok := f.health[id]; return h, ok }
func (f fakeNodes) ServedPin(id int64) (string, bool)  { p, ok := f.served[id]; return p, ok }

// pickByName pins a node's certificate by the node's name.
type pickByName struct{}

func (pickByName) Pick(n db.Node) (string, error) { return "picked-" + n.Name, nil }

// The config carries the panel's settings, templates, rules and tariffs, and the nodes
// turned on: a remote node's addresses go direct, the pin it serves wins over the one
// picked now, and one older than the key per user gets one key.
func TestLoadConfig(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	set := settings.New(st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyDomain: "panel.example.com", settings.KeySubDomain: "sub.example.com"} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	node := func(name, host, domain string) db.Node {
		t.Helper()
		n, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: name, Address: name + ":1", PublicHost: host, Domain: domain, CreatedAt: 1, UpdatedAt: 1})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	old, fresh := node("old", "198.51.100.1", "old.example.com"), node("fresh", "198.51.100.2", "")
	off := node("off", "198.51.100.3", "off.example.com")
	if _, err := st.Q.UpdateNode(ctx, db.UpdateNodeParams{Name: off.Name, Address: off.Address, PublicHost: off.PublicHost, Domain: off.Domain, Enabled: 0, UpdatedAt: 1, ID: off.ID}); err != nil {
		t.Fatal(err)
	}
	tpl, err := st.Q.CreateSubTemplate(ctx, db.CreateSubTemplateParams{Core: "mihomo", Name: "Clash", Body: "rules: [MATCH,DIRECT]\n", At: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRules(ctx, st.Q, []apps.Rule{testRule("named", apps.Action{Template: tpl.ID})}); err != nil {
		t.Fatal(err)
	}
	tariff, err := st.Q.CreateTariff(ctx, db.CreateTariffParams{Name: "Pro", DurationDays: 30, ResetStrategy: "none", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	nodes := fakeNodes{
		health: map[int64]NodeHealth{old.ID: {OK: true, Version: "0.5.1.0"}, fresh.ID: {OK: true, Version: "0.5.2.0"}},
		served: map[int64]string{fresh.ID: "served"},
	}
	now := time.Unix(1_800_000_000, 0)
	cfg, err := LoadConfig(ctx, ConfigDeps{Settings: set, Q: st.Q, Pages: subpage.NewService(st.Q), Now: func() time.Time { return now },
		SubBase: func(context.Context) string { return "" }, Nodes: nodes, TLS: pickByName{}})
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]submodel.Node{}
	for _, n := range cfg.Nodes {
		byName[n.Name] = n
	}
	if _, ok := byName["off"]; ok {
		t.Errorf("a node turned off is served: %+v", cfg.Nodes)
	}
	o, f := byName["old"], byName["fresh"]
	if o.Endpoint.Host != "old.example.com" || o.Endpoint.SNI != "old.example.com" || f.Endpoint.Host != "198.51.100.2" {
		t.Errorf("endpoints: old %+v, fresh %+v", o.Endpoint, f.Endpoint)
	}
	if !o.SSOneKey || f.SSOneKey {
		t.Errorf("one key: old %v, fresh %v", o.SSOneKey, f.SSOneKey)
	}
	if o.Endpoint.PinSHA256 != "picked-old" || f.Endpoint.PinSHA256 != "served" {
		t.Errorf("pins: old %q, fresh %q", o.Endpoint.PinSHA256, f.Endpoint.PinSHA256)
	}
	want := []string{"203.0.113.10", "panel.example.com", "sub.example.com"}
	for _, n := range cfg.Nodes {
		if n.Name == "old" {
			want = append(want, "198.51.100.1", "old.example.com")
		} else if n.Name == "fresh" {
			want = append(want, "198.51.100.2", "")
		}
	}
	if !reflect.DeepEqual(cfg.Direct, want) {
		t.Errorf("direct %q, want %q", cfg.Direct, want)
	}

	if len(cfg.Templates) != 1 || cfg.Templates[0].ID != tpl.ID {
		t.Errorf("templates %+v", cfg.Templates)
	}
	if d := cfg.Responses.Decide(http.Header{"User-Agent": {"named/1.0"}}); d.Rule != "named" {
		t.Errorf("the admin's rule does not decide: %+v", d)
	}
	if cfg.Tariffs[tariff.ID] != "Pro" {
		t.Errorf("tariffs %v", cfg.Tariffs)
	}
}
