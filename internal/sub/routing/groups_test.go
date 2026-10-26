package routing

import (
	"slices"
	"testing"

	"mikan/internal/sub/model"
)

// The built-in groups: the main one over the automatic choice, the countries and every
// server; a group of one node for a route sent to it; PROXY last.
func TestBuildGroups(t *testing.T) {
	ps := []model.Proxy{{Name: "🇳🇱 VLESS", Node: 1}, {Name: "🇳🇱 TUIC", Node: 1}, {Name: "🇺🇸 VLESS", Node: 2}}
	nodes := []model.Node{{ID: 1, Name: "🇳🇱 Нидерланды"}, {ID: 2, Name: "🇺🇸 США"}, {ID: 3, Name: "Пусто"}}
	g := model.GroupNames{Main: "VPN", Auto: "Авто"}
	in := NewIntent(RUDirect, nil, Routes{Services: map[string]string{"ai": nodeTarget(2), "youtube": nodeTarget(3), "github": nodeTarget(2)},
		Lists: []RouteList{{Name: "wl", URL: "https://example.com/wl.yaml", Behavior: "classical", Format: "yaml", Target: nodeTarget(1)}}})
	b := Balance{Type: model.Fallback, URL: ProbeURL, Interval: 60, Tolerance: 80}
	gs := BuildGroups(ps, nodes, g, Servers{}, b, in.Routes, "en")
	var names []string
	for _, x := range gs.List {
		names = append(names, x.Name)
	}
	if !slices.Equal(names, []string{"VPN", "Авто", "🇳🇱 Нидерланды", "🇺🇸 США", "🤖 ChatGPT · Claude · Gemini", "🐙 GitHub", "📋 wl", "PROXY"}) {
		t.Fatalf("groups: %q", names)
	}
	main, auto, nl := gs.List[0], gs.List[1], gs.List[2]
	if main.Type != model.Select || !slices.Equal(main.Members, []string{"Авто", "🇳🇱 Нидерланды", "🇺🇸 США", "🇳🇱 VLESS", "🇳🇱 TUIC", "🇺🇸 VLESS"}) {
		t.Fatalf("main: %+v", main)
	}
	if auto.Type != model.Fallback || auto.Tolerance != 0 || auto.Interval != 60 || len(auto.Members) != 3 {
		t.Fatalf("a fallback takes no tolerance: %+v", auto)
	}
	if nl.Type != model.URLTest || nl.Tolerance != 80 || nl.Interval != 60 || !slices.Equal(nl.Members, []string{"🇳🇱 VLESS", "🇳🇱 TUIC"}) {
		t.Fatalf("country: %+v", nl)
	}
	if ai := gs.List[4]; !slices.Equal(ai.Members, []string{"🇺🇸 VLESS", "VPN"}) || !gs.List[7].Hidden {
		t.Fatalf("a node's group: %+v", gs.List)
	}
	// The routes: youtube (a node with no servers: the main group), ai, github, wl.
	for i, want := range []string{"VPN", "🤖 ChatGPT · Claude · Gemini", "🐙 GitHub", "📋 wl"} {
		if got := gs.Of(in.Routes[i].To, i); got != want {
			t.Errorf("route %d: %q, want %q", i, got, want)
		}
	}
	if gs.Of(Target{Kind: ToAlias}, -1) != "PROXY" || gs.Of(Target{Kind: ToAuto}, -1) != "Авто" || gs.Of(Target{Kind: ToNode, Node: 2}, -1) != "VPN" || gs.Of(Target{Kind: ToDirect}, -1) != "" {
		t.Fatal("targets of no route")
	}

	one := BuildGroups(ps[:1], nodes[:1], model.GroupNames{Main: "PROXY", Auto: "A"}, Servers{NoCountries: true}, b, nil, "")
	if len(one.List) != 2 || one.List[0].Name != "PROXY" {
		t.Fatalf("a main group named PROXY is its own alias: %+v", one.List)
	}
}

// A node kept out of the automatic choice (a whitelist server, or one that does not answer)
// keeps its country group and its servers in the main group; with every node kept out, the
// choice takes them all rather than none.
func TestAutoLeavesNodesOut(t *testing.T) {
	ps := []model.Proxy{{Name: "🇳🇱 VLESS", Node: 1}, {Name: "🇳🇱 VLESS [WL]", Node: 2}, {Name: "🇺🇸 VLESS", Node: 3}}
	nodes := []model.Node{{ID: 1, Name: "🇳🇱 Нидерланды"}, {ID: 2, Name: "🇳🇱 Нидерланды [WL]", NoAuto: true}, {ID: 3, Name: "🇺🇸 США"}}
	g := model.GroupNames{Main: "VPN", Auto: "Авто"}
	b := Balance{Type: model.URLTest, URL: ProbeURL, Interval: 60, Tolerance: 50}
	gs := BuildGroups(ps, nodes, g, Servers{}, b, nil, "")
	main, auto, wl := gs.List[0], gs.List[1], gs.List[3]
	if !slices.Equal(auto.Members, []string{"🇳🇱 VLESS", "🇺🇸 VLESS"}) {
		t.Fatalf("auto: %v", auto.Members)
	}
	if wl.Name != "🇳🇱 Нидерланды [WL]" || !slices.Equal(wl.Members, []string{"🇳🇱 VLESS [WL]"}) || !slices.Contains(main.Members, "🇳🇱 VLESS [WL]") || !slices.Contains(main.Members, wl.Name) {
		t.Fatalf("the node stays to pick by hand: main %v, its group %+v", main.Members, wl)
	}
	for i := range nodes {
		nodes[i].NoAuto = true
	}
	if all := BuildGroups(ps, nodes, g, Servers{}, b, nil, ""); !slices.Equal(all.List[1].Members, []string{"🇳🇱 VLESS", "🇳🇱 VLESS [WL]", "🇺🇸 VLESS"}) {
		t.Fatalf("every node out: %v", all.List[1].Members)
	}
}
