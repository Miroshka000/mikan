package routing

import (
	"slices"
	"testing"
)

// A key of the DNS policy is a kind and the names it lists with commas.
func TestPolicyNames(t *testing.T) {
	for _, c := range []struct {
		key, kind string
		names     []string
	}{
		{"geosite:cn", "geosite", []string{"cn"}},
		{"geosite:CN", "geosite", []string{"cn"}},
		{"geosite: cn , Category-RU ", "geosite", []string{"cn", "category-ru"}},
		{"geosite:cn,,category-ru,", "geosite", []string{"cn", "category-ru"}},
		{"geosite:!cn", "geosite", []string{"!cn"}},
		{"geosite:cn,+.a.com", "geosite", []string{"cn", "+.a.com"}},
		{"geosite:", "geosite", nil},
		{"rule-set:mikan-ru-core", "rule-set", []string{"mikan-ru-core"}},
		{"rule-set: mikan-ru-core , Mikan-Games", "rule-set", []string{"mikan-ru-core", "Mikan-Games"}}, // a list's name as it is
		{"rule-set:", "rule-set", nil},
		{"GEOSITE:cn", "", []string{"GEOSITE:cn"}},
		{"+.a.com,geosite:cn", "", []string{"+.a.com", "geosite:cn"}},
		{"a.com, +.B.com ,*.c.com", "", []string{"a.com", "+.B.com", "*.c.com"}},
		{"a.com,,b.com", "", []string{"a.com", "b.com"}},
		{"+.", "", []string{"+."}},
		{"", "", nil},
		{" , ", "", nil},
	} {
		kind, names := PolicyNames(c.key)
		if kind != c.kind || !slices.Equal(names, c.names) {
			t.Errorf("%q: %q %q, want %q %q", c.key, kind, names, c.kind, c.names)
		}
	}
}
