package routing

import (
	"strings"
	"testing"
)

// The geodata the xray apps download has every category the panel names: one it does not
// have would fail the app's whole routing.
func TestRunetfreedomKnowsThePanelsCategories(t *testing.T) {
	for _, s := range Services {
		for _, m := range s.Rules {
			if m.Type == "GEOSITE" && !RunetfreedomHas("geosite", m.Value) || m.Type == "GEOIP" && !RunetfreedomHas("geoip", m.Value) {
				t.Errorf("%s: %s,%s", s.ID, m.Type, m.Value)
			}
		}
	}
	for list, x := range runetfreedomLists {
		for _, v := range append(append([]string{}, x[0]...), x[1]...) {
			kind, name, _ := strings.Cut(v, ":")
			if !RunetfreedomHas(kind, name) {
				t.Errorf("%s: %s", list, v)
			}
		}
	}
	for _, c := range []string{"category-ru", "ru-blocked", "private"} {
		if !RunetfreedomHas("geosite", c) {
			t.Errorf("geosite %s", c)
		}
	}
	if sites, ips := RunetfreedomList("mikan-telegram-ip"); sites != nil || len(ips) != 1 {
		t.Errorf("telegram: %v %v", sites, ips)
	}
}

// A sing-box rule set is MetaCubeX's category by its lower-case name, escaped for the path.
func TestMetaCubeXRuleSet(t *testing.T) {
	if got := MetaCubeXRuleSet("geosite", "Category-AI-!cn"); got != metaCubeXSing+"geosite/category-ai-%21cn.srs" {
		t.Fatal(got)
	}
}
