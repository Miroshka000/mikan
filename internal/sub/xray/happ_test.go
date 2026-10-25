package xray

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/routing"
)

func TestValidHappRouting(t *testing.T) {
	profile := base64.StdEncoding.EncodeToString([]byte(`{"Name":"RU direct","GlobalProxy":"true"}`))
	for s, want := range map[string]bool{
		"happ://routing/onadd/" + profile:        true,
		"happ://routing/add/" + profile:          true,
		"happ://routing/off":                     true,
		HappRoutingAuto:                          true,
		"happ://routing/onadd/bm90IGpzb24=":      false, // "not json"
		"happ://routing/onadd/":                  false,
		"https://example.com/":                   false,
		"happ://routing/onadd/" + profile + " x": false,
	} {
		if got := ValidHappRouting(s); got != want {
			t.Errorf("ValidHappRouting(%.40q) = %v, want %v", s, got, want)
		}
	}
	if IncyLink("happ://routing/off") != "incy://routing/off" || IncyLink("auto") != "auto" {
		t.Fatal("INCY's scheme")
	}
}

func decodeHapp(t *testing.T, link string) happProfile {
	t.Helper()
	payload, ok := strings.CutPrefix(link, "happ://routing/onadd/")
	if !ok || !ValidHappRouting(link) {
		t.Fatalf("not a routing link: %s", link)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var p happProfile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// A Clash profile's rules land in Happ's lists by their target, the first rule that takes
// a site deciding, as in mihomo; what Xray has no list for is left out.
func TestHappRoutingOf(t *testing.T) {
	p := decodeHapp(t, HappRoutingOf("VPN", []string{
		"IP-CIDR,203.0.113.7/32,DIRECT,no-resolve",
		"GEOIP,LAN,DIRECT,no-resolve",
		"DOMAIN-SUFFIX,sber.ru,DIRECT",
		"GEOSITE,category-ads-all,REJECT",
		"GEOSITE,youtube,📺 YouTube",
		"GEOSITE,youtube,REJECT", // a twin of the rule above: never reached
		"GEOIP,telegram,VPN,no-resolve",
		"GEOSITE,no-such-category,VPN",
		"PROCESS-NAME,Telegram.exe,VPN",
		"AND,((NETWORK,UDP),(DST-PORT,443)),REJECT",
		"RULE-SET,mikan-blocked,VPN",
		"GEOSITE,category-ru,DIRECT",
		"MATCH,DIRECT",
		"MATCH,REJECT",
	}))
	if p.GlobalProxy != "false" || p.Name != "VPN" || !strings.HasPrefix(p.GeositeURL, happRelease) {
		t.Fatalf("profile: %+v", p)
	}
	want := map[string][]string{
		"DirectSites": {"geosite:private", "domain:sber.ru", "geosite:category-ru"},
		"DirectIp":    {"203.0.113.7/32", "geoip:private"},
		"BlockSites":  {"geosite:category-ads-all"},
		"ProxySites":  {"geosite:youtube", "geosite:ru-blocked"},
		"ProxyIp":     {"geoip:telegram", "geoip:ru-blocked"},
	}
	got := map[string][]string{"DirectSites": p.DirectSites, "DirectIp": p.DirectIP, "BlockSites": p.BlockSites, "ProxySites": p.ProxySites, "ProxyIp": p.ProxyIP}
	for k, w := range want {
		if !slices.Equal(got[k], w) {
			t.Errorf("%s: %v, want %v", k, got[k], w)
		}
	}
	if len(p.BlockIP) != 0 {
		t.Errorf("BlockIp: %v", p.BlockIP)
	}
	if all := decodeHapp(t, HappRoutingOf("VPN", []string{"MATCH,VPN"})); all.GlobalProxy != "true" || !slices.Equal(all.DirectSites, []string{"geosite:private"}) {
		t.Fatalf("all through the proxy: %+v", all)
	}
}

// Every category the panel's own routing can send is in the pinned geodata: one that is
// not would fail Happ's whole routing.
func TestHappKnowsThePanelsCategories(t *testing.T) {
	names := happNames()
	for _, s := range routing.Services {
		for _, m := range s.Rules {
			if m.Type == "GEOSITE" && !names[0][m.Value] || m.Type == "GEOIP" && !names[1][m.Value] {
				t.Errorf("%s: %s,%s", s.ID, m.Type, m.Value)
			}
		}
	}
	for list, x := range happLists {
		for _, v := range append(append([]string{}, x[0]...), x[1]...) {
			cat := strings.SplitN(v, ":", 2)
			if cat[0] == "geosite" && !names[0][cat[1]] || cat[0] == "geoip" && !names[1][cat[1]] {
				t.Errorf("%s: %s", list, v)
			}
		}
	}
	for _, c := range []string{"category-ru", "ru-blocked", "private"} {
		if !names[0][c] {
			t.Errorf("geosite %s", c)
		}
	}
}
