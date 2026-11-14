package singbox

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"mikan/internal/sub/routing"
)

// action is where a rule to t sends traffic: an outbound or a reject; false for a group
// sing-box has none of.
func action(t routing.Target, tags map[string]bool) (map[string]any, bool) {
	switch t.Kind {
	case routing.ToDirect:
		return map[string]any{"outbound": "direct"}, true
	case routing.ToReject:
		return map[string]any{"action": "reject"}, true
	case routing.ToDrop:
		return map[string]any{"action": "reject", "method": "drop"}, true
	case routing.ToGroup:
		if tags[t.Group] {
			return map[string]any{"outbound": t.Group}, true
		}
	}
	return nil, false
}

// rule is the condition of r as a route rule: one condition as it is, several as a logical
// rule of all of them; false when sing-box has no counterpart for one.
func (s *ruleSets) rule(r routing.Rule) (map[string]any, bool) {
	var parts []map[string]any
	for _, m := range r.When {
		c, ok := s.condition(m)
		if !ok {
			if m.Type == "RULE-SET" {
				s.drop("list_unsupported", m.Value)
			} else {
				s.drop("rule_unsupported", m.Type+","+m.Value)
			}
			return nil, false
		}
		parts = append(parts, c)
	}
	switch len(parts) {
	case 0:
		return nil, false
	case 1:
		return parts[0], true
	}
	return map[string]any{"type": "logical", "mode": "and", "rules": parts}, true
}

// condition is m as sing-box writes it; false for what it has no counterpart for (IP-ASN,
// SRC-GEOIP, DOMAIN-WILDCARD, a list it cannot read) and for a value it would refuse.
func (s *ruleSets) condition(m routing.Match) (map[string]any, bool) {
	one := func(k string, v any) (map[string]any, bool) { return map[string]any{k: v}, true }
	v := m.Value
	switch m.Type {
	case "DOMAIN":
		return one("domain", []string{v})
	case "DOMAIN-SUFFIX":
		return one("domain_suffix", []string{v})
	case "DOMAIN-KEYWORD":
		return one("domain_keyword", []string{v})
	case "DOMAIN-REGEX":
		if _, err := regexp.Compile(v); err == nil {
			return one("domain_regex", []string{v})
		}
	case "IP-CIDR", "IP-CIDR6", "SRC-IP-CIDR":
		if _, err := netip.ParsePrefix(v); err == nil {
			k := "ip_cidr"
			if m.Type == "SRC-IP-CIDR" {
				k = "source_ip_cidr"
			}
			return one(k, []string{v})
		}
	case "GEOIP":
		if strings.EqualFold(v, "lan") {
			return one("ip_is_private", true)
		}
		return one("rule_set", []string{s.geo("geoip", v)})
	case "GEOSITE":
		return one("rule_set", []string{s.geo("geosite", v)})
	case "PROCESS-NAME":
		return processRule(v), true
	case "PROCESS-PATH":
		return one("process_path", []string{v})
	case "PROCESS-NAME-REGEX":
		if _, err := regexp.Compile(v); err == nil {
			return one("process_path_regex", []string{v})
		}
	case "DST-PORT", "SRC-PORT":
		prefix := ""
		if m.Type == "SRC-PORT" {
			prefix = "source_"
		}
		return ports(prefix, v)
	case "NETWORK":
		if n := strings.ToLower(v); n == "tcp" || n == "udp" {
			return one("network", []string{n})
		}
	case "RULE-SET":
		if tag, ok := s.list(v); ok {
			return one("rule_set", []string{tag})
		}
	}
	return nil, false
}

// ports are mihomo's "443", "8000-9000" or several joined with "/" as sing-box's port and
// port_range (prefix "source_" for the source port).
func ports(prefix, v string) (map[string]any, bool) {
	var single []int
	var ranges []string
	for _, part := range strings.Split(v, "/") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 1 || a > 65535 {
			return nil, false
		}
		if !isRange {
			single = append(single, a)
			continue
		}
		b, err := strconv.Atoi(hi)
		if err != nil || b < a || b > 65535 {
			return nil, false
		}
		ranges = append(ranges, strconv.Itoa(a)+":"+strconv.Itoa(b))
	}
	out := map[string]any{}
	if len(single) > 0 {
		out[prefix+"port"] = single
	}
	if len(ranges) > 0 {
		out[prefix+"port_range"] = ranges
	}
	return out, true
}

// processRule: an Android package ("com.example.app") or a program's name.
func processRule(name string) map[string]any {
	if strings.Contains(name, ".") && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return map[string]any{"package_name": []string{name}}
	}
	return map[string]any{"process_name": []string{name}}
}
