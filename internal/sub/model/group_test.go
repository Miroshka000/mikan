package model

import (
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
