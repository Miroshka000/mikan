package model

import (
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, ok := range []string{"VPN", "🇳🇱 Нидерланды", "Авто", "PROXY"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " VPN", "a,b", "DIRECT", "global", "x\ny", strings.Repeat("я", 49)} {
		if ValidName(bad) == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// A name next to the groups must not repeat one of them, PROXY included.
func TestCheckName(t *testing.T) {
	g := GroupNames{}.WithDefaults("en")
	if g.Main != "VPN" || g.Auto != "Auto" || (GroupNames{}).WithDefaults("ru").Auto != "Авто" {
		t.Fatalf("defaults: %+v", g)
	}
	for name, want := range map[string]string{"🇳🇱 Нидерланды": "", "vpn": "name_is_group", "AUTO": "name_is_group", "proxy": "name_is_group", "a,b": "name_bad_char"} {
		got := ""
		if err := g.CheckName(name); err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}
