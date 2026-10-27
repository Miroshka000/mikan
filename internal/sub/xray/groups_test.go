package xray

import (
	"encoding/json"
	"slices"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

var groupTags = map[string]bool{"a": true, "b": true, "c": true, "entry": true}

func codes(b Balancing) []string {
	var out []string
	for _, n := range b.Notes {
		out = append(out, n.Group+":"+n.Code)
	}
	return out
}

// Each kind of group as xray's routing carries it, the nearest form with a note where xray
// has none of its own.
func TestBalance(t *testing.T) {
	b := Balance([]model.Group{
		{Name: "pick", Type: model.Select, Members: []string{"fast", "a"}},
		{Name: "fast", Type: model.URLTest, Members: []string{"a", "b"}},
		{Name: "first", Type: model.Fallback, Members: []string{"b", "a"}},
		{Name: "turn", Type: model.LoadBalance, Members: []string{"a", "c"}, Strategy: "round-robin"},
		{Name: "hash", Type: model.LoadBalance, Members: []string{"a", "c"}},
		{Name: "exits", Type: model.URLTest, Members: []string{"b", "c"}},
		{Name: "cascade", Type: model.Chain, Members: []string{"entry", "exits"}},
		{Name: "through", Type: model.Chain, Members: []string{"fast", "c"}},
		{Name: "off", Type: model.Select, Members: []string{"DIRECT"}},
		{Name: "alias", Type: model.Select, Members: []string{"pick", "a"}, Hidden: true},
		{Name: "none", Type: model.URLTest, Members: []string{"REJECT", "gone"}},
	}, groupTags, routing.Servers{Interval: 60})

	byTag := map[string]Balancer{}
	for _, x := range b.Balancers {
		byTag[x.Tag] = x
	}
	if fast := byTag["fast"]; fast.Strategy.Type != "leastPing" || !slices.Equal(fast.Selector, []string{"a", "b"}) || fast.FallbackTag != "" {
		t.Fatalf("url-test: %+v", fast)
	}
	if first := byTag["first"]; first.Strategy.Type != "leastPing" || first.FallbackTag != "b" {
		t.Fatalf("fallback: %+v", first)
	}
	if byTag["turn"].Strategy.Type != "roundRobin" || byTag["hash"].Strategy.Type != "random" {
		t.Fatalf("load-balance: %+v %+v", byTag["turn"], byTag["hash"])
	}
	for name, want := range map[string]Ref{"pick": {Tag: "fast", Balancer: true}, "cascade": {Tag: "exits", Balancer: true}, "through": {Tag: "c"},
		"off": {Tag: "direct"}, "alias": {Tag: "fast", Balancer: true}} {
		if got := b.Refs[name]; got != want {
			t.Errorf("%s goes to %+v, want %+v", name, got, want)
		}
	}
	if _, ok := b.Refs["none"]; ok || byTag["none"].Tag != "" {
		t.Fatal("a group of nothing xray has is no balancer")
	}
	// The exits dial through the entry; a balancer as a hop before another is its first server.
	if b.Dialers["b"] != "entry" || b.Dialers["c"] != "entry" || len(b.Dialers) != 2 {
		t.Fatalf("dialers: %v", b.Dialers)
	}
	want := []string{"pick:select_first", "first:fallback_fastest", "hash:balance_random", "through:chain_group_hop", "through:chain_dialer_taken"}
	if got := codes(b); !slices.Equal(got, want) {
		t.Fatalf("notes: %q", got)
	}
	if o := b.Observatory; o == nil || o.ProbeURL != routing.ProbeURL || o.ProbeInterval != "60s" || !o.EnableConcurrency || o.SubjectSelector != nil || b.BurstObservatory != nil {
		t.Fatalf("observatory: %+v %+v", o, b.BurstObservatory)
	}
}

// The admin's xray balance: leastLoad with its settings and the burst observatory, the
// check address and interval.
func TestBalanceLeastLoad(t *testing.T) {
	s := routing.Servers{Xray: routing.XrayServers{Strategy: "leastLoad", ProbeURL: "https://cp.cloudflare.com/", Interval: 600, Sampling: 3, Tolerance: 0.05, Expected: 2, MaxRTT: 1500}}
	b := Balance([]model.Group{{Name: "auto", Type: model.Fallback, Members: []string{"a", "b"}}}, groupTags, s)
	raw, _ := json.Marshal(b.Balancers)
	if string(raw) != `[{"tag":"auto","selector":["a","b"],"fallbackTag":"a","strategy":{"type":"leastLoad","settings":{"expected":2,"maxRTT":"1500ms","tolerance":0.05}}}]` {
		t.Fatalf("balancers: %s", raw)
	}
	// The subjects are a config's own (observe): Balance has how they are probed.
	if o := b.BurstObservatory; o == nil || o.PingConfig != (PingConfig{Destination: "https://cp.cloudflare.com/", Interval: "600s", Sampling: 3}) || o.SubjectSelector != nil || b.Observatory != nil {
		t.Fatalf("burst observatory: %+v %+v", o, b.Observatory)
	}
}

// The built-in groups: the main group goes to the automatic choice, the countries and the
// automatic choice are balancers; PROXY, hidden, says nothing.
func TestBalanceBuiltin(t *testing.T) {
	ps := []model.Proxy{{Name: "a", Node: 1}, {Name: "b", Node: 2}}
	nodes := []model.Node{{ID: 1, Name: "🇳🇱 NL"}, {ID: 2, Name: "🇺🇸 US"}}
	s := routing.Servers{}
	gs := routing.BuildGroups(ps, nodes, model.GroupNames{Main: "VPN", Auto: "Auto"}, s, s.XrayBalance(), nil, "en")
	b := Balance(gs.List, map[string]bool{"a": true, "b": true}, s)
	if b.Refs["VPN"] != (Ref{Tag: "Auto", Balancer: true}) || b.Refs["PROXY"] != b.Refs["VPN"] || len(b.Balancers) != 3 {
		t.Fatalf("refs %v, balancers %+v", b.Refs, b.Balancers)
	}
	if got := codes(b); !slices.Equal(got, []string{"VPN:select_first"}) {
		t.Fatalf("notes: %q", got)
	}
}
