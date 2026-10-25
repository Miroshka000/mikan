package xray

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
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
	if p.GlobalProxy != "false" || p.Name != "VPN" || p.GeositeURL != routing.RunetfreedomGeosite {
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

// The Happ routing profile the settings of every user make in every mode, and the own Clash
// profile's, decoded (subtest has the golden files' story): the panel makes one for all of
// its users.
func TestGoldenHappRouting(t *testing.T) {
	for _, v := range subtest.Variants {
		for _, mode := range subtest.Modes {
			t.Run(v.Name+"/"+string(mode), func(t *testing.T) {
				goldenHapp(t, v.Name+"_"+string(mode)+".happ.json", happRouting(v, mode, ""))
			})
		}
		if !strings.HasPrefix(v.Name, "single") { // the template names the second node's group
			t.Run(v.Name+"/template", func(t *testing.T) {
				goldenHapp(t, v.Name+".template.happ.json", happRouting(v, routing.All, subtest.OwnTemplate))
			})
		}
	}
}

// happRouting is the routing link of the variant's settings, as serve makes it.
func happRouting(v subtest.Variant, m routing.Mode, src string) string {
	u := v.User()
	p := mihomo.Profile{Nodes: u.Build.Nodes, Direct: u.Direct, Rules: u.Rules, Routes: u.Routes, Lang: u.Lang}
	return HappRoutingOf("VPN", mihomo.Rules(p, v.Groups.WithDefaults(u.Lang), m, src))
}

func goldenHapp(t *testing.T, file, link string) {
	t.Helper()
	payload, ok := strings.CutPrefix(link, "happ://routing/onadd/")
	raw, err := base64.StdEncoding.DecodeString(payload)
	var out bytes.Buffer
	if !ok || err != nil || json.Indent(&out, raw, "", "  ") != nil {
		t.Fatalf("not a routing link: %s", link)
	}
	subtest.Golden(t, file, append(out.Bytes(), '\n'))
}
