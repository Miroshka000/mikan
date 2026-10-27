package xray

import (
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"mikan/internal/sub/routing"
)

// The routing of an xray JSON config is the panel's (routing.Intent) as xray matches it:
// sites by xray's own prefixes (full:, domain:, keyword:, regexp:), IPs, ports, networks,
// and the categories of the geodata the app has (routing.Geodata). What xray has no match
// for is left out: a category that geodata lacks (it would fail the whole config), process
// names, a rule list it cannot download (the panel's own ones become their nearest
// categories).

// fields are the conditions of a rule as xray's routing rules carry them, nil when xray
// cannot match them all with the categories geo has. xray takes the fields of a rule
// together and the values of a field one or another, so several conditions make one rule,
// and a list of sites and IPs two.
func fields(when []routing.Match, geo routing.Geodata) []map[string]any {
	if len(when) == 0 {
		return nil
	}
	out := []map[string]any{{}}
	for _, m := range when {
		alts := matchOf(m, geo)
		if len(alts) == 0 {
			return nil
		}
		var next []map[string]any
		for _, have := range out {
			for _, alt := range alts {
				f := make(map[string]any, len(have)+len(alt))
				for k, v := range have {
					f[k] = v
				}
				for k, v := range alt {
					if _, taken := f[k]; taken {
						return nil // two conditions on one field would read as either of them
					}
					f[k] = v
				}
				next = append(next, f)
			}
		}
		out = next
	}
	return out
}

// resolves says whether a rule of the conditions makes the app resolve a name to match an
// IP: an IP condition without no-resolve.
func resolves(when []routing.Match, geo routing.Geodata) bool {
	for _, m := range when {
		if m.NoResolve {
			continue
		}
		switch m.Type {
		case "GEOIP", "IP-CIDR", "IP-CIDR6":
			return true
		case "RULE-SET":
			if _, ips := geo.List(m.Value); len(ips) > 0 {
				return true
			}
		}
	}
	return false
}

var portList = regexp.MustCompile(`^\d+(-\d+)?(,\d+(-\d+)?)*$`)

// matchOf is one condition as xray matches it: one set of fields, or two for a list of
// sites and IPs; none for what xray has no form of, or a category geo lacks.
func matchOf(m routing.Match, geo routing.Geodata) []map[string]any {
	one := func(k string, v any) []map[string]any { return []map[string]any{{k: v}} }
	v := strings.TrimSpace(m.Value)
	switch m.Type {
	case "DOMAIN":
		return one("domain", []string{"full:" + v})
	case "DOMAIN-SUFFIX":
		return one("domain", []string{"domain:" + v})
	case "DOMAIN-KEYWORD":
		return one("domain", []string{"keyword:" + v})
	case "DOMAIN-REGEX":
		if _, err := regexp.Compile(v); err == nil {
			return one("domain", []string{"regexp:" + v})
		}
	case "DOMAIN-WILDCARD":
		return one("domain", []string{"regexp:" + wildcard(v, ".*")})
	case "GEOSITE":
		if v = strings.ToLower(v); geo.Has("geosite", v) {
			return one("domain", []string{"geosite:" + v})
		}
	case "GEOIP":
		switch v = strings.ToLower(v); {
		case v == "lan":
			return one("ip", []string{"geoip:private"})
		case geo.Has("geoip", v):
			return one("ip", []string{"geoip:" + v})
		}
	case "IP-CIDR", "IP-CIDR6":
		if _, err := netip.ParsePrefix(v); err == nil {
			return one("ip", []string{v})
		}
	case "SRC-IP-CIDR":
		if _, err := netip.ParsePrefix(v); err == nil {
			return one("source", []string{v})
		}
	case "DST-PORT", "SRC-PORT":
		// mihomo lists ports with "/", xray with ",".
		if ports := strings.ReplaceAll(v, "/", ","); portList.MatchString(ports) {
			if m.Type == "DST-PORT" {
				return one("port", ports)
			}
			return one("sourcePort", ports)
		}
	case "NETWORK":
		if v = strings.ToLower(v); v == "tcp" || v == "udp" {
			return one("network", v)
		}
	case "RULE-SET":
		sites, ips := geo.List(v)
		var out []map[string]any
		if len(sites) > 0 {
			out = append(out, map[string]any{"domain": sites})
		}
		if len(ips) > 0 {
			out = append(out, map[string]any{"ip": ips})
		}
		return out
	}
	return nil
}

// wildcard is a mihomo wildcard of names as a regular expression: "?" one character, "*"
// as many as any (star).
func wildcard(s, star string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, c := range s {
		switch c {
		case '*':
			b.WriteString(star)
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
}

// dnsTag names the DNS of a config among the inbounds its rules match: its own queries go
// out by the rules that name it, past the tunnel or through a group.
const dnsTag = "dns"

// dnsOf is the DNS of r as xray's, and the rules its queries go by: each server is asked
// past the tunnel or through the group after its "#" (via says what a group goes to in
// this config), any other query of it through rest. A server xray has no form of is left
// out: DNS over TLS, and DNS over QUIC through the tunnel.
func dnsOf(r routing.Resolvers, geo routing.Geodata, via func(group string) (Ref, bool), rest Ref) (map[string]any, []map[string]any) {
	type route struct {
		to          Ref
		ips, domain []string
	}
	var routes []*route
	routeOf := func(to Ref) *route {
		for _, x := range routes {
			if x.to == to {
				return x
			}
		}
		x := &route{to: to}
		routes = append(routes, x)
		return x
	}
	var servers []any
	add := func(s string, domains []string) {
		res, ok := routing.ParseResolver(s)
		if !ok {
			return
		}
		to := Ref{Tag: directTag}
		if res.Via != "" {
			if to, ok = via(res.Via); !ok {
				return
			}
		}
		host := res.Host
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		port := func(def int) string {
			if res.Port != 0 {
				return host + ":" + strconv.Itoa(res.Port)
			}
			if def != 0 {
				return host + ":" + strconv.Itoa(def)
			}
			return host
		}
		var addr any
		routed := true
		switch res.Scheme {
		case "system":
			addr, routed = "localhost", false
		case "udp":
			addr = res.Host
			if res.Port != 0 && res.Port != 53 {
				addr = map[string]any{"address": res.Host, "port": res.Port}
			}
		case "tcp":
			addr = "tcp://" + port(53)
		case "https", "h3":
			// DNS over HTTP/3 as DNS over HTTPS of the same server: xray has no HTTP/3 one.
			path := res.Path
			if path == "" {
				path = "/dns-query"
			}
			addr = "https://" + port(0) + path
		case "quic":
			if res.Via != "" {
				return // xray asks over QUIC only past the tunnel
			}
			addr, routed = "quic+local://"+port(0), false
		default:
			return
		}
		if domains != nil {
			entry, ok := addr.(map[string]any)
			if !ok {
				entry = map[string]any{"address": addr}
			}
			entry["domains"], entry["skipFallback"] = domains, true
			addr = entry
		}
		servers = append(servers, addr)
		if routed {
			x := routeOf(to)
			if _, err := netip.ParseAddr(res.Host); err == nil {
				if !slices.Contains(x.ips, res.Host) {
					x.ips = append(x.ips, res.Host)
				}
			} else if d := "full:" + res.Host; !slices.Contains(x.domain, d) {
				x.domain = append(x.domain, d)
			}
		}
	}
	for _, p := range r.Policy {
		if domains := policyDomains(p.Match, geo); len(domains) > 0 {
			for _, s := range p.Servers {
				add(s, domains)
			}
		}
	}
	for _, s := range r.Nameserver {
		add(s, nil)
	}
	if len(servers) == 0 {
		servers = []any{"localhost"}
	}
	strategy := "UseIPv4"
	if r.IPv6 {
		strategy = "UseIP"
	}
	dns := map[string]any{"tag": dnsTag, "servers": servers, "queryStrategy": strategy}
	var rules []map[string]any
	for _, x := range routes {
		if len(x.ips) > 0 {
			rules = append(rules, withRef(map[string]any{"inboundTag": []string{dnsTag}, "ip": x.ips}, x.to))
		}
		if len(x.domain) > 0 {
			rules = append(rules, withRef(map[string]any{"inboundTag": []string{dnsTag}, "domain": x.domain}, x.to))
		}
	}
	return dns, append(rules, withRef(map[string]any{"inboundTag": []string{dnsTag}}, rest))
}

// policyDomains are the names a DNS policy's key covers (mihomo's nameserver-policy) as
// xray's DNS reads them (routing.PolicyNames): categories, the panel's lists as their
// nearest categories, and domains ("+." with every subdomain, "*." one label deep); none
// for what xray has no form of, or a category geo lacks.
func policyDomains(key string, geo routing.Geodata) []string {
	var out []string
	kind, names := routing.PolicyNames(key)
	for _, v := range names {
		switch {
		case kind == "geosite":
			if geo.Has("geosite", v) {
				out = append(out, "geosite:"+v)
			}
		case kind == "rule-set":
			sites, _ := geo.List(v)
			out = append(out, sites...)
		case strings.HasPrefix(v, "+."):
			out = append(out, "domain:"+v[2:])
		case strings.HasPrefix(v, "*."):
			out = append(out, "regexp:"+wildcard(v, "[^.]+"))
		case !strings.ContainsAny(v, "*:"):
			out = append(out, "full:"+v)
		}
	}
	return out
}

// withRef is a rule of the fields f that sends to r: an outbound, or a balancer.
func withRef(f map[string]any, r Ref) map[string]any {
	out := make(map[string]any, len(f)+1)
	for k, v := range f {
		out[k] = v
	}
	if r.Balancer {
		out["balancerTag"] = r.Tag
	} else {
		out["outboundTag"] = r.Tag
	}
	return out
}
