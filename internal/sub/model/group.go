package model

// Group is a group of proxies apart from any core: how the app picks one of its members.
// Each core writes it as its own kind of group, or as the nearest one it has with a Note.
type Group struct {
	Name string
	Type GroupType
	// Members are proxies, groups and the built-in policies (DIRECT, REJECT) by name; a
	// chain's are its hops in order, the entry first and the exit last.
	Members []string
	// URL, Interval (seconds) and Tolerance (milliseconds) are of the groups that test their
	// members; zero values are the core's own defaults.
	URL       string
	Interval  int
	Tolerance int
	// Strategy is a load-balance's: round-robin, consistent-hashing or sticky-sessions
	// (mihomo's names); "" is the core's default.
	Strategy string
	// Hidden keeps a group the app needs out of its list of groups.
	Hidden bool
}

// GroupType is how a group picks a member, by mihomo's names for it.
type GroupType string

const (
	Select      GroupType = "select"       // the user does
	URLTest     GroupType = "url-test"     // the fastest that answers
	Fallback    GroupType = "fallback"     // the first that answers, in order
	LoadBalance GroupType = "load-balance" // spread over the members
	// Chain is a cascade of hops (Citrus): every hop after the first is dialed through the
	// hop before it, and the group goes out of the last.
	Chain GroupType = "chain"
)

// Tests says whether the group tests its members: its URL, Interval and Tolerance apply.
func (t GroupType) Tests() bool { return t == URLTest || t == Fallback || t == LoadBalance }

// Known says whether the core takes the type: Citrus knows no other group (relay is gone,
// and the types of other forks, smart and the like, are not in it).
func (t GroupType) Known() bool { return t == Select || t == Chain || t.Tests() }

// Note is what a core made of what it cannot write as it is, which the admin panel's preview
// tells: the nearest form of a group, or a rule, a list, a DNS policy, a proxy or a category
// left out. Code names it for the panel's words; Subject is what it is of (a group's name, a
// condition "TYPE,VALUE", a list's name, a policy's key, a proxy's name, "geosite:NAME"),
// under the JSON key it had when notes were of groups only.
type Note struct {
	Core    string `json:"core" enum:"mihomo,singbox,xray"`
	Subject string `json:"group" doc:"О чём заметка: группа, условие TYPE,VALUE, список, ключ политики DNS, сервер или geosite:NAME"`
	Code    string `json:"code" enum:"fallback_fastest,balance_fastest,balance_random,balance_flat,select_first,chain_group_hop,chain_dialer_taken,proxy_unsupported,rule_unsupported,list_unsupported,dns_policy_unsupported,category_missing"`
}

// Entries are the proxies a hop of a chain is entered by, which dial through the hop before
// it: the proxy itself, the members of a group, the first hop of a chain. byName are the
// groups by name; isProxy tells a proxy from the rest (a policy, a member of nothing).
func Entries(hop string, byName map[string]Group, isProxy func(string) bool) []string {
	return entries(hop, byName, isProxy, map[string]bool{})
}

func entries(hop string, byName map[string]Group, isProxy func(string) bool, seen map[string]bool) []string {
	if isProxy(hop) {
		return []string{hop}
	}
	g, ok := byName[hop]
	if !ok || seen[hop] {
		return nil
	}
	seen[hop] = true
	if g.Type == Chain {
		if len(g.Members) == 0 {
			return nil
		}
		return entries(g.Members[0], byName, isProxy, seen)
	}
	var out []string
	for _, m := range g.Members {
		out = append(out, entries(m, byName, isProxy, seen)...)
	}
	return out
}

// Wire makes every hop of the chain g after the first dial through the hop before it, as
// Citrus does on loading: via[p] is the hop (a proxy, a group, a chain, DIRECT) the proxy p
// dials through, for the proxies each hop is entered by (Entries). The proxy dials through it
// wherever it goes, not only in g. Citrus refuses a proxy two chains need through different
// hops, and a dialer that would loop back to the proxy; the panel refuses such a template on
// saving, and for one saved before Wire keeps the dialer the proxy has and tells (taken).
func Wire(g Group, byName map[string]Group, isProxy func(string) bool, via map[string]string) (taken bool) {
	if g.Type != Chain {
		return false
	}
	for i := 1; i < len(g.Members); i++ {
		prev := g.Members[i-1]
		for _, p := range Entries(g.Members[i], byName, isProxy) {
			if d, ok := via[p]; ok {
				taken = taken || d != prev
				continue
			}
			if reaches(prev, p, byName, via, map[string]bool{}) {
				taken = true
				continue
			}
			via[p] = prev
		}
	}
	return taken
}

// reaches says whether dialing name goes through the proxy p: name is p, or a group (a
// chain) of a member that reaches it, or a proxy that dials through one (via).
func reaches(name, p string, byName map[string]Group, via map[string]string, seen map[string]bool) bool {
	if name == p {
		return true
	}
	if seen[name] {
		return false
	}
	seen[name] = true
	if d, ok := via[name]; ok {
		return reaches(d, p, byName, via, seen)
	}
	for _, m := range byName[name].Members {
		if reaches(m, p, byName, via, seen) {
			return true
		}
	}
	return false
}
