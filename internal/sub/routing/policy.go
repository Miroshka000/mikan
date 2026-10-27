package routing

import (
	"slices"
	"strings"
)

// PolicyName is one name a key of a DNS policy covers: Kind "geosite" for a category,
// "rule-set" for a rule list, "" for a domain ("+.a.com", "*.a.com", "a.com").
type PolicyName struct{ Kind, Name string }

// PolicyNames reads a key of a DNS policy (DNSPolicy.Match, mihomo's nameserver-policy):
// the names it lists with commas, without their prefixes or spaces, for the cores that
// carry the policy in their own terms. The key's prefix counts in any case, as mihomo reads
// it ("GEOSITE:cn"), and gives its kind to the names after it. A name with a prefix of its
// own is of that kind ("+.a.com,geosite:cn", as mihomo reads it), a prefix inside the key
// counting in lower case only, as in mihomo; a "+." or "*." name is a domain after a prefix
// too ("geosite:cn,+.a.com"). Categories are in lower case; a name left empty, or a prefix
// with nothing after it ("geosite:", "+."), is none.
func PolicyNames(key string) []PolicyName {
	kind, rest := "", key
	if p, r, ok := policyPrefix(key, true); ok {
		kind, rest = p, r
	}
	var out []PolicyName
	for _, v := range strings.Split(rest, ",") {
		k, v := kind, strings.TrimSpace(v)
		if p, r, ok := policyPrefix(v, false); ok {
			k, v = p, strings.TrimSpace(r)
		} else if strings.HasPrefix(v, "+.") || strings.HasPrefix(v, "*.") {
			k = ""
		}
		if k == "geosite" {
			v = strings.ToLower(v)
		}
		if v != "" && (k != "" || v != "+." && v != "*.") {
			out = append(out, PolicyName{k, v})
		}
	}
	return out
}

// policyPrefix is the kind s starts with, "geosite" or "rule-set" (in any case if anyCase),
// and the rest.
func policyPrefix(s string, anyCase bool) (kind, rest string, ok bool) {
	p, rest, ok := strings.Cut(s, ":")
	if anyCase {
		p = strings.ToLower(p)
	}
	if ok && (p == "geosite" || p == "rule-set") {
		return p, rest, true
	}
	return "", "", false
}

// policyKey: mihomo's parseNameServerPolicy takes the key, which it otherwise refuses with
// every Clash profile that has it. After a prefix ("geosite:", "rule-set:" in any case)
// the key is names with commas, none empty: mihomo cuts a list at its second ":" and reads
// a "+." or "*." name as a category or a list. Without one, each name is a domain mihomo
// takes (validDomain), or a category or a list written with its prefix in lower case and a
// name of the same kind after it: a prefix in another case is a domain to mihomo, which
// matches no site.
func policyKey(key string) bool {
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		return false
	}
	if _, rest, ok := policyPrefix(key, true); ok {
		for _, v := range strings.Split(rest, ",") {
			if v == "" || strings.Contains(v, ":") || strings.HasPrefix(v, "+.") || strings.HasPrefix(v, "*.") {
				return false
			}
		}
		return true
	}
	for _, v := range strings.Split(key, ",") {
		if _, rest, ok := policyPrefix(v, false); ok {
			if rest == "" || strings.Contains(rest, ":") || strings.HasPrefix(rest, "+.") || strings.HasPrefix(rest, "*.") {
				return false
			}
		} else if _, _, ok := policyPrefix(v, true); ok || !validDomain(v) {
			return false
		}
	}
	return true
}

// validDomain: a domain mihomo's DomainTrie takes (trie.ValidAndSplitDomain): no empty
// label but a first one (".a.com"), so no dot at the end, "+" only as the whole first label
// of several ("+.a.com"), and "*" only as a whole label.
func validDomain(d string) bool {
	labels := strings.Split(d, ".")
	if d == "" || len(labels) > 1 && slices.Contains(labels[1:], "") {
		return false
	}
	for i, l := range labels {
		if strings.Contains(l, "+") && (l != "+" || i != 0 || len(labels) == 1) {
			return false
		}
		if strings.Contains(l, "*") && l != "*" {
			return false
		}
	}
	return true
}
