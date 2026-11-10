package settings

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

func names(list []Former) []string {
	var out []string
	for _, f := range list {
		out = append(out, f.Name)
	}
	return out
}

// A domain left behind becomes a former name, and so does the address of a panel that gets
// its first domain; a change of the address alone does not, and a name taken back leaves
// the list.
func TestNextFormers(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ip := Address{PublicHost: "203.0.113.7"}
	a := Address{Domain: "a.example.com", PublicHost: "203.0.113.7"}
	b := Address{Domain: "B.example.com.", PublicHost: "203.0.113.7"}

	list := NextFormers(nil, ip, a, now)
	if got := names(list); !slices.Equal(got, []string{"203.0.113.7"}) {
		t.Fatalf("the first domain: %v", got)
	}
	list = NextFormers(list, a, b, now.Add(time.Hour))
	if got := names(list); !slices.Equal(got, []string{"203.0.113.7", "a.example.com"}) || list[1].Since != now.Add(time.Hour).Unix() {
		t.Fatalf("a new domain: %+v", list)
	}
	// The server moved: the address it left leads nowhere.
	moved := b
	moved.PublicHost = "198.51.100.9"
	if got := names(NextFormers(list, b, moved, now)); !slices.Equal(got, []string{"203.0.113.7", "a.example.com"}) {
		t.Fatalf("a move: %v", got)
	}
	// Back to the old domain: it is the panel's again.
	if got := names(NextFormers(list, b, a, now)); !slices.Equal(got, []string{"203.0.113.7", "b.example.com"}) {
		t.Fatalf("back: %v", got)
	}
	// No domain any more: the panel answers at its address, the domain it had stays.
	if got := names(NextFormers(list, b, ip, now)); !slices.Equal(got, []string{"a.example.com", "b.example.com"}) {
		t.Fatalf("no domain: %v", got)
	}
	// The subscription domain is a former name of its own kind.
	sub := b
	sub.SubDomain = "sub.example.com"
	moved = sub
	moved.SubDomain = "links.example.com"
	got := NextFormers(nil, sub, moved, now)
	if len(got) != 1 || got[0].Name != "sub.example.com" || !got[0].Sub {
		t.Fatalf("the subscription domain: %+v", got)
	}
}

// What no CA certifies is never kept, and the list keeps its newest names.
func TestNextFormersBounds(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	if got := NextFormers(nil, Address{PublicHost: "10.0.0.5"}, Address{Domain: "vpn.example.com"}, now); len(got) != 0 {
		t.Fatalf("a private address: %+v", got)
	}
	if got := NextFormers(nil, Address{Domain: "vpn.test"}, Address{Domain: "vpn.example.com"}, now); len(got) != 0 {
		t.Fatalf("a special-use name: %+v", got)
	}
	var list []Former
	prev := Address{Domain: "d0.example.com"}
	for i := 1; i <= MaxFormers+2; i++ {
		next := Address{Domain: "d" + strconv.Itoa(i) + ".example.com"}
		list = NextFormers(list, prev, next, now.Add(time.Duration(i)*time.Minute))
		prev = next
	}
	if len(list) != MaxFormers || slices.Contains(names(list), "d0.example.com") || !slices.Contains(names(list), "d8.example.com") {
		t.Fatalf("bounded: %v", names(list))
	}
	if got := WithoutFormer(list, "D8.example.com."); len(got) != MaxFormers-1 || slices.Contains(names(got), "d8.example.com") {
		t.Fatalf("without: %v", names(got))
	}
}
