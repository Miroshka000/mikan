package singbox

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"mikan/internal/sub/routing"
)

// dnsOf is the DNS of r as sing-box servers: the first nameserver for everything, a plain
// one for the proxies' own names (the default resolver, returned too), and the policy's
// first server for its names. Fake IP is mihomo's: sing-box resolves for real.
func dnsOf(r routing.Resolvers, tags map[string]bool, sets *ruleSets) (map[string]any, string) {
	servers := []map[string]any{}
	add := func(tag, s string) bool {
		srv, ok := dnsServer(tag, s, tags)
		if ok {
			servers = append(servers, srv)
		}
		return ok
	}
	remote := ""
	for _, s := range r.Nameserver {
		if add("remote", s) {
			remote = "remote"
			break
		}
	}
	local := ""
	for _, s := range append(slices.Clone(r.ProxyServer), r.Default...) {
		if add("local", s) {
			local = "local"
			break
		}
	}
	if local == "" {
		servers = append(servers, map[string]any{"type": "local", "tag": "local"})
		local = "local"
	}
	if remote == "" {
		remote = local
	}
	// A name the policy gives twice resolves with the last, and the names go in order, as
	// the policy is a map of the Clash profile.
	policy := map[string][]string{}
	for _, p := range r.Policy {
		policy[strings.TrimSpace(p.Match)] = p.Servers
	}
	keys := make([]string, 0, len(policy))
	for k := range policy {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var rules []map[string]any
	for _, k := range keys {
		// A key resolves its names with one server, whatever the names: a rule of their
		// fields, which sing-box ORs.
		fields := map[string][]string{}
		kind, names := routing.PolicyNames(k)
		for _, v := range names {
			switch {
			case kind == "geosite":
				if category(v) {
					fields["rule_set"] = append(fields["rule_set"], v)
				}
			case kind != "":
				// a rule list's names stay mihomo's
			case strings.HasPrefix(v, "+."):
				fields["domain_suffix"] = append(fields["domain_suffix"], v[2:])
			case !strings.ContainsAny(v, ":*"):
				fields["domain"] = append(fields["domain"], v)
			}
		}
		// Each name on its own: a "*." name drops only itself, not the plain names of its key.
		if len(fields) == 0 {
			continue // a pattern of mihomo's own
		}
		tag := "policy-" + strconv.Itoa(len(rules))
		if len(policy[k]) == 0 || !add(tag, policy[k][0]) {
			continue
		}
		rule := map[string]any{"server": tag}
		for f, l := range fields {
			if f == "rule_set" {
				for i, v := range l {
					l[i] = sets.geo("geosite", v)
				}
			}
			rule[f] = l
		}
		rules = append(rules, rule)
	}
	// A server named by a domain needs another to resolve that name, or sing-box refuses
	// the whole config: mihomo's default nameservers, which are there for it.
	var bootstrap map[string]any
	for _, srv := range servers {
		if host, _ := srv["server"].(string); host == "" || isIP(host) {
			continue
		}
		if bootstrap == nil {
			bootstrap = map[string]any{"type": "local", "tag": "bootstrap"}
			for _, s := range r.Default {
				if x, ok := routing.ParseResolver(s); ok && isIP(x.Host) {
					bootstrap, _ = dnsServer("bootstrap", s, nil)
					break
				}
			}
		}
		srv["domain_resolver"] = "bootstrap"
	}
	if bootstrap != nil {
		servers = append(servers, bootstrap)
	}
	dns := map[string]any{"servers": servers, "final": remote}
	if len(rules) > 0 {
		dns["rules"] = rules
	}
	if !r.IPv6 {
		dns["strategy"] = "ipv4_only"
	}
	return dns, local
}

// category says whether a geosite name can be one of MetaCubeX's: their .srs are named
// with a-z, 0-9, "-", "@" and "!" ("category-ai-!cn", "apple@cn"), never "!" first,
// mihomo's negation. Another would make a rule set to fetch that is not there, and sing-box
// does not start without it.
func category(name string) bool {
	if strings.HasPrefix(name, "!") {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("-@!", c)) {
			return false
		}
	}
	return true
}

func isIP(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}

// dnsServer is a server of routing.Resolvers as a sing-box one; one that goes through a
// group sing-box has goes through it (detour).
func dnsServer(tag, s string, tags map[string]bool) (map[string]any, bool) {
	r, ok := routing.ParseResolver(s)
	if !ok {
		return nil, false
	}
	srv := map[string]any{"tag": tag}
	if r.Scheme == "system" {
		srv["type"] = "local"
		return srv, true
	}
	srv["type"], srv["server"] = r.Scheme, r.Host
	if r.Via != "" && tags[r.Via] {
		srv["detour"] = r.Via
	}
	if r.Port != 0 {
		srv["server_port"] = r.Port
	}
	if r.Path != "" {
		srv["path"] = r.Path
	}
	return srv, true
}
