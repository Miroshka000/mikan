package model

import (
	"maps"
	"slices"
	"testing"
)

// A hop is entered by its proxies: a group's members, flattened, and a chain's first hop,
// not its exit.
func TestEntries(t *testing.T) {
	byName := map[string]Group{}
	for _, g := range []Group{
		{Name: "fast", Type: URLTest, Members: []string{"a", "b"}},
		{Name: "pick", Type: Select, Members: []string{"fast", "c", "DIRECT", "gone"}},
		{Name: "inner", Type: Chain, Members: []string{"a", "b"}},
		{Name: "outer", Type: Chain, Members: []string{"x", "inner"}},
		{Name: "deep", Type: Chain, Members: []string{"outer", "c"}},
		{Name: "mixed", Type: LoadBalance, Members: []string{"inner", "c"}},
		{Name: "loop", Type: Select, Members: []string{"loop", "a"}},
		{Name: "empty", Type: Chain},
	} {
		byName[g.Name] = g
	}
	isProxy := func(s string) bool { return s == "a" || s == "b" || s == "c" || s == "x" }
	for hop, want := range map[string][]string{
		"a":     {"a"},
		"fast":  {"a", "b"},
		"pick":  {"a", "b", "c"},
		"inner": {"a"},
		"outer": {"x"},
		"deep":  {"x"},
		"mixed": {"a", "c"},
		"loop":  {"a"},
		"empty": nil,
		"gone":  nil,
	} {
		if got := Entries(hop, byName, isProxy); !slices.Equal(got, want) {
			t.Errorf("%s is entered by %q, want %q", hop, got, want)
		}
	}
}

// The core knows five types of group; mihomo's relay is gone from it, and Clash's spelling
// url_test is not mihomo's.
func TestGroupTypeKnown(t *testing.T) {
	for typ, want := range map[GroupType][2]bool{ // Known, Tests
		Select:      {true, false},
		URLTest:     {true, true},
		Fallback:    {true, true},
		LoadBalance: {true, true},
		Chain:       {true, false},
		"relay":     {false, false},
		"url_test":  {false, false},
		"":          {false, false},
	} {
		if got := [2]bool{typ.Known(), typ.Tests()}; got != want {
			t.Errorf("%q: Known, Tests = %v, want %v", typ, got, want)
		}
	}
}

// Wire is Citrus's wiring of a chain: every hop past the first is entered by its proxies,
// which dial through the hop before it (a proxy, a group, a chain, DIRECT). A proxy keeps
// the hop it dials through already, and a hop that would loop back is no dialer; both are
// told.
func TestWire(t *testing.T) {
	byName := map[string]Group{}
	for _, g := range []Group{
		{Name: "exits", Type: URLTest, Members: []string{"b", "c"}},
		{Name: "tail", Type: Chain, Members: []string{"x", "c"}},
		{Name: "back", Type: Select, Members: []string{"a", "b"}},
	} {
		byName[g.Name] = g
	}
	isProxy := func(s string) bool { return s == "a" || s == "b" || s == "c" || s == "x" }
	for _, tc := range []struct {
		chains []Group
		want   map[string]string
		taken  []bool
	}{
		{[]Group{{Name: "C", Type: Chain, Members: []string{"a", "b", "c"}}}, map[string]string{"b": "a", "c": "b"}, []bool{false}},
		{[]Group{{Name: "C", Type: Chain, Members: []string{"a", "exits"}}}, map[string]string{"b": "a", "c": "a"}, []bool{false}},
		{[]Group{{Name: "C", Type: Chain, Members: []string{"exits", "a"}}}, map[string]string{"a": "exits"}, []bool{false}},
		{[]Group{{Name: "C", Type: Chain, Members: []string{"a", "tail"}}}, map[string]string{"x": "a"}, []bool{false}},
		{[]Group{{Name: "C", Type: Chain, Members: []string{"DIRECT", "a"}}, {Name: "D", Type: Chain, Members: []string{"b", "a"}}},
			map[string]string{"a": "DIRECT"}, []bool{false, true}},
		{[]Group{{Name: "C", Type: Chain, Members: []string{"a", "b"}}, {Name: "D", Type: Chain, Members: []string{"a", "b"}}},
			map[string]string{"b": "a"}, []bool{false, false}},
		{[]Group{{Name: "C", Type: Chain, Members: []string{"a", "back"}}}, map[string]string{"b": "a"}, []bool{true}},
		{[]Group{{Name: "C", Type: Chain, Members: []string{"a", "b"}}, {Name: "D", Type: Chain, Members: []string{"b", "a"}}},
			map[string]string{"b": "a"}, []bool{false, true}},
		{[]Group{{Name: "S", Type: Select, Members: []string{"a", "b"}}}, map[string]string{}, []bool{false}},
	} {
		via := map[string]string{}
		var taken []bool
		for _, g := range tc.chains {
			taken = append(taken, Wire(g, byName, isProxy, via))
		}
		if !maps.Equal(via, tc.want) || !slices.Equal(taken, tc.taken) {
			t.Errorf("%v: %v taken %v, want %v taken %v", tc.chains, via, taken, tc.want, tc.taken)
		}
	}
}
