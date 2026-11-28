package node

import (
	"net/netip"
	"slices"
	"strings"

	"mikan/internal/nodeapi"
)

// egressRules renders destination filters before cascade and WARP. A scoped rule
// is evaluated on the client-facing node, where the authenticated slot is known.
func egressRules(st nodeapi.DesiredState) (rules []string, sub map[string][]string) {
	if st.Filters == nil {
		return nil, nil
	}
	e := st.Filters.Egress
	slots := make(map[string]string, len(st.Slots))
	for _, slot := range st.Slots {
		if ruleUser(slot.Name) && ruleUser(slot.UUID) {
			slots[slot.Name] = strings.ToLower(slot.UUID)
		}
	}
	relay := "IN-NAME," + nodeapi.RelayListener
	local := "NOT,((" + relay + "))"
	allow := []string{}
	for _, rule := range e.Scoped {
		target := egressTarget(rule.Kind, rule.Value)
		if target == "" {
			continue
		}
		var ids []string
		for _, name := range rule.Slots {
			if uuid, ok := slots[name]; ok {
				ids = append(ids, name, uuid)
			}
		}
		slices.Sort(ids)
		user := "IN-USER," + strings.Join(slices.Compact(ids), "/")
		switch rule.Scope {
		case "only":
			if len(ids) > 0 {
				rules = append(rules, "AND,(("+local+"),("+user+"),("+target+")),REJECT")
			}
		case "except":
			if len(ids) == 0 {
				rules = append(rules, "AND,(("+local+"),("+target+")),REJECT")
			} else {
				rules = append(rules, "AND,(("+local+"),(NOT,(("+user+"))),("+target+")),REJECT")
			}
		case "allow":
			if rule.Kind == "domain" && len(ids) > 0 {
				// A relay can only see the source node. Trust its decision for a
				// destination with an exception, and keep the other global blocks.
				allow = append(allow, "(AND,(("+target+"),(OR,(("+user+"),("+relay+")))))")
			}
		}
	}
	for _, p := range e.Ports {
		if target := egressTarget("port", p); target != "" {
			rules = append(rules, target+",REJECT")
		}
	}
	for _, n := range e.Networks {
		if target := egressTarget("network", n); target != "" {
			rules = append(rules, target+",REJECT")
		}
	}
	var domains []string
	for _, d := range e.Domains {
		if target := egressTarget("domain", d); target != "" {
			domains = append(domains, target+",REJECT")
		}
	}
	if len(allow) == 0 || len(domains) == 0 {
		return append(rules, domains...), nil
	}
	const domainList = "mikan-egress-domains"
	return append(rules, "SUB-RULE,(NOT,((OR,("+strings.Join(allow, ",")+")))),"+domainList),
		map[string][]string{domainList: domains}
}

func ruleUser(s string) bool {
	return safeRuleValue(s) && !strings.ContainsAny(s, "()/")
}

func egressTarget(kind, value string) string {
	v, ok := nodeapi.CanonEgress(kind, value)
	if !ok {
		return ""
	}
	switch kind {
	case "port":
		return "DST-PORT," + v
	case "network":
		if strings.Contains(v, ":") {
			return "IP-CIDR6," + v
		}
		// As with private ranges, let the core resolve domains to check the network.
		return "IP-CIDR," + v
	default:
		return "DOMAIN-SUFFIX," + v
	}
}

// ingress is the ingress filter as the tunnel checks it: the networks as sorted ranges
// that do not overlap, so a check is a binary search. It runs for every UDP packet, and
// a list may hold a thousand networks.
type ingress struct {
	allow  bool
	ranges []addrRange
}

type addrRange struct{ first, last netip.Addr }

func newIngress(allow bool, nets []netip.Prefix) *ingress {
	rs := make([]addrRange, 0, len(nets))
	for _, p := range nets {
		p = p.Masked()
		rs = append(rs, addrRange{p.Addr(), lastAddr(p)})
	}
	// netip.Addr orders IPv4 before IPv6, so one sorted list holds both.
	slices.SortFunc(rs, func(a, b addrRange) int { return a.first.Compare(b.first) })
	in := &ingress{allow: allow}
	for _, r := range rs {
		if n := len(in.ranges); n > 0 && r.first.Compare(in.ranges[n-1].last) <= 0 {
			if r.last.Compare(in.ranges[n-1].last) > 0 {
				in.ranges[n-1].last = r.last
			}
			continue
		}
		in.ranges = append(in.ranges, r)
	}
	return in
}

// lastAddr is the last address of a masked network.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().AsSlice()
	for i := range b {
		switch n := p.Bits() - i*8; {
		case n <= 0:
			b[i] = 0xff
		case n < 8:
			b[i] |= 0xff >> n
		}
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

func (in *ingress) admits(a netip.Addr) bool {
	a = a.Unmap()
	// i is the first range that starts after a: only the one before it can hold a.
	i, _ := slices.BinarySearchFunc(in.ranges, a, func(r addrRange, a netip.Addr) int {
		if r.first.Compare(a) <= 0 {
			return -1
		}
		return 1
	})
	if i > 0 && a.Compare(in.ranges[i-1].last) <= 0 {
		return in.allow
	}
	return !in.allow
}

// SetIngress takes the ingress filter of the state; without networks it lets everyone in.
func (r *Registry) SetIngress(f *nodeapi.Filters) {
	if f == nil {
		r.ingress.Store(nil)
		return
	}
	var nets []netip.Prefix
	for _, n := range f.Ingress.Networks {
		if p, ok := nodeapi.CanonNetwork(n); ok {
			nets = append(nets, p)
		}
	}
	// An allow list with nothing that parses would shut every user out; nothing listed
	// is no filter.
	if len(nets) == 0 {
		r.ingress.Store(nil)
		return
	}
	r.ingress.Store(newIngress(f.Ingress.Allow, nets))
}

// admitFrom says whether the ingress filter lets a connection from the address in.
func (r *Registry) admitFrom(a netip.Addr) bool {
	in := r.ingress.Load()
	return in == nil || in.admits(a)
}
