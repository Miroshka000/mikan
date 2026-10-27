package routing

import "strings"

// PolicyNames reads a key of a DNS policy (DNSPolicy.Match, mihomo's nameserver-policy):
// kind "geosite" for categories, "rule-set" for rule lists, "" for domains ("+.a.com",
// "*.a.com", "a.com"), and the names one key lists with commas, without the prefix or
// spaces. Categories are in lower case; a name left empty is none. A prefix counts only in
// lower case and at the start: "geosite:cn,+.a.com" lists two categories.
func PolicyNames(key string) (kind string, names []string) {
	rest := key
	for _, p := range []string{"geosite", "rule-set"} {
		if r, ok := strings.CutPrefix(key, p+":"); ok {
			kind, rest = p, r
			break
		}
	}
	for _, v := range strings.Split(rest, ",") {
		if v = strings.TrimSpace(v); kind == "geosite" {
			v = strings.ToLower(v)
		}
		if v != "" {
			names = append(names, v)
		}
	}
	return kind, names
}
