package routing

import (
	"net/netip"
	"strings"
)

// Target is where a rule sends traffic, apart from any core's name for it: each core writes
// the main group, a policy or a group of the admin's own profile as it calls them.
type Target struct {
	Kind TargetKind
	Node int64 // ToNode: the node whose servers the service's group is made of
	// Group is the name of a group of the admin's own Clash profile (ToGroup): a profile of
	// its own names its groups itself.
	Group string
}

// TargetKind is what a Target is.
type TargetKind uint8

const (
	// ToMain is the main group, where the user picks a server.
	ToMain TargetKind = iota
	// ToAlias is the main group by the name Clash apps assume for it (model.AliasGroup): a
	// rule the admin wrote to PROXY stays to PROXY in a Clash profile.
	ToAlias
	ToAuto   // the automatic choice of a server
	ToDirect // past the tunnel
	ToReject // refused
	ToDrop   // dropped without an answer (REJECT-DROP)
	// ToNode is a group of one node's servers with the main group to fall back on: a
	// service sent to one server.
	ToNode
	ToGroup // a group of an own profile, by name
)

// ServiceTarget is the target a service or an own list is stored with (TargetVPN,
// TargetDirect, TargetBlock, "node:<id>"); anything else is the main group.
func ServiceTarget(t string) Target {
	switch t {
	case TargetDirect:
		return Target{Kind: ToDirect}
	case TargetBlock:
		return Target{Kind: ToReject}
	}
	if id, ok := NodeOf(t); ok {
		return Target{Kind: ToNode, Node: id}
	}
	return Target{Kind: ToMain}
}

// Rule is one rule apart from any core: what it takes (all of When; one condition for most
// rules) and where it sends it.
type Rule struct {
	When []Match
	To   Target
}

// Direct is a rule that sends what m takes past the tunnel.
func Direct(m Match) Rule { return Rule{When: []Match{m}, To: Target{Kind: ToDirect}} }

// Clash is r as a Clash profile writes it, sending to target: "TYPE,VALUE,target" with
// no-resolve where the condition has it, "AND,((T1,V1),(T2,V2)),target" for several.
func (r Rule) Clash(target string) string {
	if len(r.When) == 1 {
		return r.When[0].Rule(target)
	}
	parts := make([]string, len(r.When))
	for i, m := range r.When {
		parts[i] = "(" + m.Type + "," + m.Value + ")"
	}
	return "AND,(" + strings.Join(parts, ",") + ")," + target
}

// HostRules keep traffic to the server itself (the panel, SSH, subscription updates) out of
// the tunnel: in TUN mode it would otherwise loop through the node and die with it. hosts
// are domains and IPs; a host given twice makes one rule.
func HostRules(hosts []string) []Rule {
	var out []Rule
	seen := map[string]bool{}
	for _, h := range hosts {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		switch ip, err := netip.ParseAddr(h); {
		case err != nil:
			out = append(out, Direct(Match{Type: "DOMAIN", Value: h}))
		case ip.Is4():
			out = append(out, Direct(Match{Type: "IP-CIDR", Value: ip.String() + "/32", NoResolve: true}))
		default:
			out = append(out, Direct(Match{Type: "IP-CIDR6", Value: ip.String() + "/128", NoResolve: true}))
		}
	}
	return out
}

// SplitRule splits a Clash rule on the commas outside brackets.
func SplitRule(r string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range r {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, r[start:i])
				start = i + 1
			}
		}
	}
	return append(out, r[start:])
}

// RuleParts reads a Clash rule's type, its target and the rule list it uses ("" when
// none). Logic rules (AND, OR, NOT) hold commas in brackets; the target is the last field
// but a trailing no-resolve or src.
func RuleParts(r string) (typ, target, set string) {
	fields := SplitRule(r)
	for len(fields) > 2 && (fields[len(fields)-1] == "no-resolve" || fields[len(fields)-1] == "src") {
		fields = fields[:len(fields)-1]
	}
	if len(fields) < 2 {
		return "", "", ""
	}
	typ, target = strings.TrimSpace(fields[0]), strings.TrimSpace(fields[len(fields)-1])
	if typ == "RULE-SET" && len(fields) == 3 {
		set = strings.TrimSpace(fields[1])
	}
	if typ == "MATCH" && len(fields) != 2 || typ != "MATCH" && len(fields) < 3 {
		return "", "", ""
	}
	return typ, target, set
}

// ReadMatch reads the condition of a Clash rule without its target, as a rule list holds
// them ("DOMAIN-SUFFIX,example.com", "IP-CIDR,10.0.0.0/8,no-resolve") and AND of plain
// conditions ("AND,((NETWORK,UDP),(DST-PORT,443))"); false for OR, NOT and what does not
// read as a condition.
func ReadMatch(s string) ([]Match, bool) {
	f := SplitRule(s)
	typ := strings.ToUpper(strings.TrimSpace(f[0]))
	if len(f) < 2 || typ == "" || typ == "MATCH" || typ == "OR" || typ == "NOT" {
		return nil, false
	}
	value := strings.TrimSpace(f[1])
	if value == "" {
		return nil, false
	}
	if typ == "AND" {
		inner, ok := strings.CutPrefix(value, "(")
		inner, ok2 := strings.CutSuffix(inner, ")")
		if !ok || !ok2 {
			return nil, false
		}
		var out []Match
		for _, part := range SplitRule(inner) {
			part = strings.TrimSpace(part)
			p, ok := strings.CutPrefix(part, "(")
			p, ok2 := strings.CutSuffix(p, ")")
			if !ok || !ok2 {
				return nil, false
			}
			m, ok := ReadMatch(p)
			if !ok || len(m) != 1 {
				return nil, false
			}
			out = append(out, m[0])
		}
		return out, len(out) > 0
	}
	m := Match{Type: typ, Value: value}
	for _, opt := range f[2:] {
		if strings.EqualFold(strings.TrimSpace(opt), "no-resolve") {
			m.NoResolve = true
		}
	}
	return []Match{m}, true
}

// ReadRule reads a rule of an own Clash profile: its conditions and where it sends, the
// built-in policies as targets and any other name as a group of the profile. MATCH reads
// with no conditions. False for a rule ReadMatch cannot read.
func ReadRule(s string) (Rule, bool) {
	typ, target, _ := RuleParts(s)
	if typ == "" {
		return Rule{}, false
	}
	to := Target{Kind: ToGroup, Group: target}
	switch strings.ToUpper(target) {
	case "DIRECT":
		to = Target{Kind: ToDirect}
	case "REJECT":
		to = Target{Kind: ToReject}
	case "REJECT-DROP":
		to = Target{Kind: ToDrop}
	}
	if strings.EqualFold(typ, "MATCH") {
		return Rule{To: to}, true
	}
	// The condition is the rule without its target.
	f := SplitRule(s)
	cut := len(f) - 1
	for cut > 2 && strings.TrimSpace(f[cut]) != target {
		cut--
	}
	when, ok := ReadMatch(strings.Join(append(append([]string{}, f[:cut]...), f[cut+1:]...), ","))
	if !ok {
		return Rule{}, false
	}
	return Rule{When: when, To: to}, true
}
