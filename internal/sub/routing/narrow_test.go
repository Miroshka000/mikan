package routing

import (
	"reflect"
	"testing"
)

// A list the core cannot read goes with the RULE-SET conditions that read it, the other
// conditions stay, and a route left with none goes.
func TestNarrow(t *testing.T) {
	keep, drop := Provider{Name: "keep"}, Provider{Name: "drop"}
	mixed := Route{Name: "mixed", To: Target{Kind: ToMain}, Lists: []Provider{keep, drop},
		Rules: []Match{{Type: "RULE-SET", Value: "drop"}, {Type: "GEOSITE", Value: "x"}, {Type: "RULE-SET", Value: "keep"}}}
	gone := Route{Name: "gone", To: Target{Kind: ToDirect}, Lists: []Provider{drop}, Rules: []Match{{Type: "RULE-SET", Value: "drop"}}}
	plain := Route{Name: "plain", To: Target{Kind: ToReject}, Rules: []Match{{Type: "DOMAIN", Value: "a.example"}}}
	got := Narrow([]Route{mixed, gone, plain}, func(pr Provider) bool { return pr.Name != "drop" })
	want := []Route{
		{Name: "mixed", To: Target{Kind: ToMain}, Lists: []Provider{keep}, Rules: []Match{{Type: "GEOSITE", Value: "x"}, {Type: "RULE-SET", Value: "keep"}}},
		plain,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("narrowed:\n%+v\nwant\n%+v", got, want)
	}
	if len(mixed.Lists) != 2 || len(mixed.Rules) != 3 {
		t.Fatalf("the routes given changed: %+v", mixed)
	}
	if got := Narrow([]Route{mixed, gone}, func(Provider) bool { return true }); !reflect.DeepEqual(got, []Route{mixed, gone}) {
		t.Fatalf("a core that reads every list: %+v", got)
	}
}
