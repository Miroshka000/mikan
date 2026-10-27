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
	for name, want := range map[string]Ref{"pick": {Tag: "fast", Balancer: true}, "cascade": {Tag: "chain1-hop2-exits", Balancer: true},
		"through": {Tag: "chain2-hop2-c"}, "off": {Tag: "direct"}, "alias": {Tag: "fast", Balancer: true}} {
		if got := b.Refs[name]; got != want {
			t.Errorf("%s goes to %+v, want %+v", name, got, want)
		}
	}
	if _, ok := b.Refs["none"]; ok || byTag["none"].Tag != "" {
		t.Fatal("a group of nothing xray has is no balancer")
	}
	// The exits are copies that dial through the entry, under a copy of their balancer; the
	// proxies and the exits' own balancer stay as they are. A balancer as a hop before another
	// is its first server: c dials through a in "through", and through nothing elsewhere.
	if !slices.Equal(b.Copies, []Copy{{"chain1-hop2-", "b", "entry"}, {"chain1-hop2-", "c", "entry"}, {"chain2-hop2-", "c", "a"}}) {
		t.Fatalf("copies: %v", b.Copies)
	}
	if exits := byTag["chain1-hop2-exits"]; exits.Strategy.Type != "leastPing" || !slices.Equal(exits.Selector, []string{"chain1-hop2-b", "chain1-hop2-c"}) ||
		!slices.Equal(byTag["exits"].Selector, []string{"b", "c"}) {
		t.Fatalf("the exits' copy: %+v, the exits: %+v", exits, byTag["exits"])
	}
	want := []string{"pick:select_first", "first:fallback_fastest", "hash:balance_random", "through:chain_group_hop"}
	if got := codes(b); !slices.Equal(got, want) {
		t.Fatalf("notes: %q", got)
	}
	if o := b.Observatory; o == nil || o.ProbeURL != routing.ProbeURL || o.ProbeInterval != "60s" || !o.EnableConcurrency || o.SubjectSelector != nil || b.BurstObservatory != nil {
		t.Fatalf("observatory: %+v %+v", o, b.BurstObservatory)
	}
}

// A chain as a hop is entered by its first hop (model.Entries), and its hops are copied once
// more: entry, a, then b, whatever the inner chain dials on its own.
func TestBalanceNestedChain(t *testing.T) {
	groups := []model.Group{
		{Name: "inner", Type: model.Chain, Members: []string{"a", "b"}},
		{Name: "outer", Type: model.Chain, Members: []string{"entry", "inner"}},
		{Name: "mixed", Type: model.URLTest, Members: []string{"inner", "c"}},
		{Name: "wide", Type: model.Chain, Members: []string{"entry", "mixed"}},
	}
	b := Balance(groups, groupTags, routing.Servers{})
	want := []Copy{
		{"chain1-hop2-", "b", "a"},
		{"chain2-hop2-", "a", "entry"}, {"chain2-hop3-", "b", "chain2-hop2-a"},
		{"chain3-hop2-", "a", "entry"}, {"chain3-hop3-", "b", "chain3-hop2-a"}, {"chain3-hop2-", "c", "entry"},
	}
	if !slices.Equal(b.Copies, want) {
		t.Fatalf("copies: %v", b.Copies)
	}
	for name, want := range map[string]Ref{"inner": {Tag: "chain1-hop2-b"}, "outer": {Tag: "chain2-hop3-b"}, "mixed": {Tag: "mixed", Balancer: true},
		"wide": {Tag: "chain3-hop2-mixed", Balancer: true}} {
		if got := b.Refs[name]; got != want {
			t.Errorf("%s goes to %+v, want %+v", name, got, want)
		}
	}
	byTag := map[string]Balancer{}
	for _, x := range b.Balancers {
		byTag[x.Tag] = x
	}
	if !slices.Equal(byTag["mixed"].Selector, []string{"chain1-hop2-b", "c"}) || !slices.Equal(byTag["chain3-hop2-mixed"].Selector, []string{"chain3-hop3-b", "chain3-hop2-c"}) {
		t.Fatalf("balancers: %+v", b.Balancers)
	}
	// What dials through the hop before a chain's second hop is what model.Entries enters it by.
	byName := map[string]model.Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	for chain, hop := range map[string]string{"chain2-hop2-": "inner", "chain3-hop2-": "mixed"} {
		var got []string
		for _, x := range b.Copies {
			if x.Prefix == chain && x.Dialer == "entry" {
				got = append(got, x.Of)
			}
		}
		if want := model.Entries(hop, byName, func(s string) bool { return groupTags[s] }); !slices.Equal(got, want) {
			t.Errorf("%s dials %q through the entry, model.Entries has %q", hop, got, want)
		}
	}
	// No dialer is taken; the copy of a balancer tells nothing again.
	if got := codes(b); !slices.Equal(got, []string{"mixed:balance_flat"}) {
		t.Fatalf("notes: %q", got)
	}
}

// The hops are counted per chain: a chain made while another is cascading (here past DIRECT,
// cascade called before the hops were made) leaves the other's numbers be. Balance makes
// every hop before a chain's cascade, so this is the converter's own guard.
func TestCascadeHopsPerChain(t *testing.T) {
	c := converter{byName: map[string]model.Group{}, tags: groupTags, refs: map[string]Ref{}, busy: map[string]bool{}, hops: map[int]int{}, copied: map[string]bool{},
		out: Balancing{Refs: map[string]Ref{}}}
	long := model.Group{Name: "long", Type: model.Chain, Members: []string{"a", "b", "c", "DIRECT", "short", "c"}}
	c.byName["short"] = model.Group{Name: "short", Type: model.Chain, Members: []string{"entry", "a"}}
	c.chains, c.hops[1] = 1, 1
	r, ok := c.cascade(long, 1, nil)
	want := []Copy{{"chain1-hop2-", "b", "a"}, {"chain1-hop3-", "c", "chain1-hop2-b"}, {"chain2-hop2-", "a", "entry"}, {"chain1-hop5-", "c", "chain2-hop2-a"}}
	if !ok || r != (Ref{Tag: "chain1-hop5-c"}) || !slices.Equal(c.out.Copies, want) {
		t.Fatalf("%+v %v: copies %v", r, ok, c.out.Copies)
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
