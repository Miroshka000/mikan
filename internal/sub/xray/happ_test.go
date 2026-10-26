package xray

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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

// happOf is the routing link of Clash rules: the rules up to the first MATCH, and where
// it sends the rest.
func happOf(lines ...string) string {
	var rules []routing.Rule
	final := routing.Target{Kind: routing.ToMain}
	for _, l := range lines {
		r, ok := routing.ReadRule(l)
		if ok && len(r.When) == 0 {
			final = r.To
			break
		}
		if ok {
			rules = append(rules, r)
		}
	}
	return happLink("VPN", rules, final)
}

// Rules land in Happ's lists by their target, the first rule that takes a site deciding,
// as in the cores; what Xray has no list for is left out.
func TestHappRoutingOf(t *testing.T) {
	p := decodeHapp(t, happOf(
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
	))
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
	if all := decodeHapp(t, happOf("MATCH,VPN")); all.GlobalProxy != "true" || !slices.Equal(all.DirectSites, []string{"geosite:private"}) {
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
	return HappRoutingOf("VPN", variantProfile(v), m, src)
}

func variantProfile(v subtest.Variant) mihomo.Profile {
	u := v.User()
	return mihomo.Profile{Nodes: u.Build.Nodes, Direct: u.Direct, Rules: u.Rules, Routes: u.Routes, Lang: u.Lang}
}

// Both apps keep the geo files whole and take the profile again for a later stamp; INCY
// gets the same profile in its own scheme, with UseChunkFiles in its own spelling too. The
// stamp is no older than the geodata it loads.
func TestIncyRoutingOf(t *testing.T) {
	for _, mode := range subtest.Modes {
		p := variantProfile(subtest.Variants[0])
		link := IncyRoutingOf("VPN", p, mode, "")
		rest, ok := strings.CutPrefix(link, "incy://")
		if !ok {
			t.Fatalf("not INCY's scheme: %s", link)
		}
		incy, happ := decodeHapp(t, "happ://"+rest), decodeHapp(t, HappRoutingOf("VPN", p, mode, ""))
		if happ.UseChunkFiles != "false" || happ.ChunkFiles != nil || happ.LastUpdated != strconv.Itoa(routingStamp) {
			t.Fatalf("%s: Happ's profile: %+v", mode, happ)
		}
		if incy.ChunkFiles == nil || *incy.ChunkFiles {
			t.Fatalf("%s: INCY's spelling: %+v", mode, incy)
		}
		happ.ChunkFiles = incy.ChunkFiles
		if !reflect.DeepEqual(incy, happ) {
			t.Fatalf("%s: INCY's profile is not Happ's:\n%+v\n%+v", mode, incy, happ)
		}
	}
	pin := path.Base(path.Dir(routing.RunetfreedomGeoIP))
	if at, err := time.Parse("200601021504", pin); err != nil || routingStamp < at.Unix() {
		t.Fatalf("routingStamp %d is older than the geodata release %s (%v): move it forward", routingStamp, pin, err)
	}
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
