package xray

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

var groupTags = map[string]bool{"a": true, "b": true, "c": true, "entry": true}

func codes(b Balancing) []string {
	var out []string
	for _, n := range b.Notes {
		out = append(out, n.Subject+":"+n.Code)
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
		{Name: "exits", Type: model.URLTest, Members: []string{"c"}},
		{Name: "cascade", Type: model.Chain, Members: []string{"entry", "exits"}},
		{Name: "through", Type: model.Chain, Members: []string{"fast", "entry"}},
		{Name: "taken", Type: model.Chain, Members: []string{"a", "c"}},
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
	for name, want := range map[string]Ref{"pick": {Tag: "fast", Balancer: true}, "cascade": {Tag: "exits", Balancer: true},
		"through": {Tag: "entry"}, "taken": {Tag: "c"}, "off": {Tag: "direct"}, "alias": {Tag: "fast", Balancer: true}} {
		if got := b.Refs[name]; got != want {
			t.Errorf("%s goes to %+v, want %+v", name, got, want)
		}
	}
	if _, ok := b.Refs["none"]; ok || byTag["none"].Tag != "" {
		t.Fatal("a group of nothing xray has is no balancer")
	}
	// As in Citrus, the proxies themselves dial through the hop before them: the exits' c
	// through the entry, under the exits' own balancer, and the entry through a, the first
	// server of the balancer before it. c keeps the entry in "taken", which says so.
	if want := map[string]string{"c": "entry", "entry": "a"}; !maps.Equal(b.Dialers, want) {
		t.Fatalf("dialers: %v, want %v", b.Dialers, want)
	}
	if exits := byTag["exits"]; !slices.Equal(exits.Selector, []string{"c"}) || len(b.Balancers) != 5 {
		t.Fatalf("the exits: %+v of %+v", exits, b.Balancers)
	}
	want := []string{"pick:select_first", "first:fallback_fastest", "hash:balance_random", "through:chain_group_hop", "taken:chain_dialer_taken"}
	if got := codes(b); !slices.Equal(got, want) {
		t.Fatalf("notes: %q", got)
	}
	if o := b.Observatory; o == nil || o.ProbeURL != routing.ProbeURL || o.ProbeInterval != "60s" || !o.EnableConcurrency || o.SubjectSelector != nil || b.BurstObservatory != nil {
		t.Fatalf("observatory: %+v %+v", o, b.BurstObservatory)
	}
}

// The chains wired as Citrus wires them (model.Wire): a chain as a hop is entered by its first
// hop, a group as a hop by all its members, of a select too (the app may pick any), and a hop
// past DIRECT dials through nothing; a rule to a chain goes to its exit.
func TestBalanceChainHops(t *testing.T) {
	groups := []model.Group{
		{Name: "inner", Type: model.Chain, Members: []string{"a", "b"}},
		{Name: "outer", Type: model.Chain, Members: []string{"entry", "inner"}},
		{Name: "mixed", Type: model.URLTest, Members: []string{"inner", "c"}},
		{Name: "wide", Type: model.Chain, Members: []string{"entry", "mixed"}},
	}
	b := Balance(groups, groupTags, routing.Servers{})
	if want := map[string]string{"b": "a", "a": "entry", "c": "entry"}; !maps.Equal(b.Dialers, want) {
		t.Fatalf("dialers: %v, want %v", b.Dialers, want)
	}
	for name, want := range map[string]Ref{"inner": {Tag: "b"}, "outer": {Tag: "b"}, "mixed": {Tag: "mixed", Balancer: true}, "wide": {Tag: "mixed", Balancer: true}} {
		if got := b.Refs[name]; got != want {
			t.Errorf("%s goes to %+v, want %+v", name, got, want)
		}
	}
	if len(b.Balancers) != 1 || !slices.Equal(b.Balancers[0].Selector, []string{"b", "c"}) {
		t.Fatalf("balancers: %+v", b.Balancers)
	}
	// What dials through the hop before a chain's second hop is what model.Entries enters it by.
	byName := map[string]model.Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	for _, hop := range []string{"inner", "mixed"} {
		for _, p := range model.Entries(hop, byName, func(s string) bool { return groupTags[s] }) {
			if b.Dialers[p] != "entry" {
				t.Errorf("%s of %s dials through %q, not the entry", p, hop, b.Dialers[p])
			}
		}
	}
	if got := codes(b); !slices.Equal(got, []string{"mixed:balance_flat"}) {
		t.Fatalf("notes: %q", got)
	}

	b = Balance([]model.Group{
		{Name: "pick", Type: model.Select, Members: []string{"a", "b"}},
		{Name: "out", Type: model.Chain, Members: []string{"DIRECT", "c"}},
		{Name: "via", Type: model.Chain, Members: []string{"entry", "pick"}},
		{Name: "again", Type: model.Chain, Members: []string{"entry", "c"}},
	}, groupTags, routing.Servers{})
	if want := map[string]string{"a": "entry", "b": "entry"}; !maps.Equal(b.Dialers, want) {
		t.Fatalf("dialers: %v, want %v", b.Dialers, want)
	}
	if b.Refs["via"] != (Ref{Tag: "a"}) || b.Refs["out"] != (Ref{Tag: "c"}) {
		t.Fatalf("refs: %v", b.Refs)
	}
	// c goes direct in "out" and keeps it in "again".
	if got := codes(b); !slices.Equal(got, []string{"pick:select_first", "again:chain_dialer_taken"}) {
		t.Fatalf("notes: %q", got)
	}
}

// A chain saved before the panel refused loops dials no proxy through itself: the first
// hop is also a member of the group after it, and two chains cross.
func TestBalanceChainNoLoop(t *testing.T) {
	b := Balance([]model.Group{
		{Name: "exits", Type: model.URLTest, Members: []string{"b", "entry"}},
		{Name: "self", Type: model.Chain, Members: []string{"entry", "exits"}},
		{Name: "one", Type: model.Chain, Members: []string{"a", "c"}},
		{Name: "two", Type: model.Chain, Members: []string{"c", "a"}},
	}, groupTags, routing.Servers{})
	if want := map[string]string{"b": "entry", "c": "a"}; !maps.Equal(b.Dialers, want) {
		t.Fatalf("dialers: %v, want %v", b.Dialers, want)
	}
	if got := codes(b); !slices.Equal(got, []string{"self:chain_dialer_taken", "two:chain_dialer_taken"}) {
		t.Fatalf("notes: %q", got)
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
