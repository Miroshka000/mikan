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
			if m.Type == "GEOSITE" && !Runetfreedom.Has("geosite", m.Value) || m.Type == "GEOIP" && !Runetfreedom.Has("geoip", m.Value) {
				t.Errorf("%s: %s,%s", s.ID, m.Type, m.Value)
			}
		}
	}
	for list, x := range runetfreedomLists {
		for _, v := range append(append([]string{}, x[0]...), x[1]...) {
			kind, name, _ := strings.Cut(v, ":")
			if !Runetfreedom.Has(kind, name) {
				t.Errorf("%s: %s", list, v)
			}
		}
	}
	for _, c := range []string{"category-ru", "ru-blocked", "private"} {
		if !Runetfreedom.Has("geosite", c) {
			t.Errorf("geosite %s", c)
		}
	}
	if sites, ips := Runetfreedom.List("mikan-telegram-ip"); sites != nil || len(ips) != 1 {
		t.Errorf("telegram: %v %v", sites, ips)
	}
}

// Common is what an app has whichever geodata it keeps: runetfreedom's lists of what is
// blocked in Russia are not in it, the categories every geodata has are.
func TestCommonGeodata(t *testing.T) {
	for _, c := range []string{"category-ru", "private", "youtube", "category-ai-!cn"} {
		if !Common.Has("geosite", c) {
			t.Errorf("geosite %s", c)
		}
	}
	for _, c := range []string{"ru-blocked", "ru-available-only-inside"} {
		if Common.Has("geosite", c) || !Runetfreedom.Has("geosite", c) {
			t.Errorf("geosite %s is runetfreedom's alone", c)
		}
	}
	if !Common.Has("geoip", "ru") || !Common.Has("geoip", "telegram") || Common.Has("geoip", "ru-blocked") {
		t.Error("geoip")
	}
	if sites, _ := Common.List("mikan-ru-sites"); len(sites) != 1 || sites[0] != "geosite:category-ru" {
		t.Errorf("ru sites: %v", sites)
	}
	if sites, ips := Common.List("mikan-blocked"); sites != nil || ips != nil {
		t.Errorf("blocked: %v %v", sites, ips)
	}
}

// A sing-box rule set is MetaCubeX's category by its lower-case name, escaped for the path.
func TestMetaCubeXRuleSet(t *testing.T) {
	if got := MetaCubeXRuleSet("geosite", "Category-AI-!cn"); got != metaCubeXSing+"geosite/category-ai-%21cn.srs" {
		t.Fatal(got)
	}
}
