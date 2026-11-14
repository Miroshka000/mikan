package routing

import (
	"slices"
	"testing"
)

// A key of the DNS policy is the names it lists with commas, each of its own kind.
func TestPolicyNames(t *testing.T) {
	geo := func(names ...string) (out []PolicyName) {
		for _, n := range names {
			out = append(out, PolicyName{"geosite", n})
		}
		return out
	}
	for _, c := range []struct {
		key  string
		want []PolicyName
	}{
		{"geosite:cn", geo("cn")},
		{"geosite:CN", geo("cn")},
		{"GEOSITE:cn", geo("cn")}, // the key's prefix in any case, as mihomo reads it
		{"GeoSite:CN,Category-RU", geo("cn", "category-ru")},
		{"geosite: cn , Category-RU ", geo("cn", "category-ru")},
		{"geosite:cn,,category-ru,", geo("cn", "category-ru")},
		{"geosite:!cn", geo("!cn")},
		{"geosite:cn,a.com", geo("cn", "a.com")}, // a plain name after the prefix is a category
		{"geosite:", nil},
		{"rule-set:mikan-ru-core", []PolicyName{{"rule-set", "mikan-ru-core"}}},
		{"rule-set: mikan-ru-core , Mikan-Games", []PolicyName{{"rule-set", "mikan-ru-core"}, {"rule-set", "Mikan-Games"}}}, // a list's name as it is
		{"RULE-SET:mikan-ru-core", []PolicyName{{"rule-set", "mikan-ru-core"}}},
		{"rule-set:", nil},
		// Each name of its own kind: a prefix of its own (in lower case, as mihomo reads it
		// inside a key), a "+." or "*." name a domain.
		{"geosite:cn,+.a.com", []PolicyName{{"geosite", "cn"}, {"", "+.a.com"}}},
		{"geosite:cn,*.a.com,category-ru", []PolicyName{{"geosite", "cn"}, {"", "*.a.com"}, {"geosite", "category-ru"}}},
		{"+.a.com,geosite:CN", []PolicyName{{"", "+.a.com"}, {"geosite", "cn"}}},
		{"rule-set:mikan-games,geosite:cn", []PolicyName{{"rule-set", "mikan-games"}, {"geosite", "cn"}}},
		{"a.com,GEOSITE:cn", []PolicyName{{"", "a.com"}, {"", "GEOSITE:cn"}}}, // a domain to mihomo
		{"a.com, +.B.com ,*.c.com", []PolicyName{{"", "a.com"}, {"", "+.B.com"}, {"", "*.c.com"}}},
		{"a.com,,b.com", []PolicyName{{"", "a.com"}, {"", "b.com"}}},
		// A prefix with no name after it is no name.
		{"+.", nil},
		{"*.", nil},
		{"+.,a.com,*.", []PolicyName{{"", "a.com"}}},
		{"+.a.com,geosite:", []PolicyName{{"", "+.a.com"}}},
		{"geosite:cn,+.", geo("cn")},
		{"", nil},
		{" , ", nil},
	} {
		if got := PolicyNames(c.key); !slices.Equal(got, c.want) {
			t.Errorf("%q: %q, want %q", c.key, got, c.want)
		}
	}
}

// A key mihomo refuses fails every Clash profile, so the panel does not save it; the ones it
// takes are as mihomo's parseNameServerPolicy and trie.ValidAndSplitDomain read them.
func TestPolicyKey(t *testing.T) {
	for _, k := range []string{
		"geosite:cn", "GEOSITE:cn", "geosite:category-ai-!cn", "geosite:cn,category-ru", "GeoSite:CN,Category-RU",
		"rule-set:mikan-ru-core", "rule-set:mikan-ru-core,mikan-games", "RULE-SET:wl",
		"+.a.com", "*.a.com", "a.com", ".a.com", "a.*.com", "*", "localhost", "+.cn",
		"+.a.com,+.b.com", "a.com,+.b.com,*.c.com", "+.a.com,geosite:cn", "a.com,geosite:cn,rule-set:wl",
	} {
		if !policyKey(k) {
			t.Errorf("%q: refused", k)
		}
	}
	for _, k := range []string{
		"", " ", "a.com ", "a.com, b.com", "a.com\r",
		// No name, or a dot at the end.
		"+.", "*.", "x.", "a.com.", "geosite:", "rule-set:", "a.com,,b.com", "a.com,", ",a.com", "geosite:cn,", "geosite:cn,,ru",
		"+.a.com,geosite:", "a.com,rule-set:",
		// A category or a list inside a key is a name, not a domain.
		"+.a.com,geosite:+.x", "a.com,rule-set:*.x",
		// After a prefix with commas mihomo reads every name as of that kind, and cuts the list
		// at its second ":".
		"geosite:cn,+.a.com", "geosite:cn,*.a.com", "geosite:+.a.com", "rule-set:wl,+.a.com",
		"geosite:cn,rule-set:wl", "geosite:cn,geosite:ru", "geosite:a:b",
		// A prefix inside a key in another case is a domain to mihomo, which matches no site.
		"a.com,GEOSITE:cn", "+.a.com,Rule-Set:wl", "a.com,geosite:cn:x",
		// "+" only as the whole first label of several, "*" only as a whole label.
		"+", "+a.com", "a.+.com", "+.+.com", "a+b.com", "*a.com", "a.b*.com", "a..com",
	} {
		if policyKey(k) {
			t.Errorf("%q: taken", k)
		}
	}
}
