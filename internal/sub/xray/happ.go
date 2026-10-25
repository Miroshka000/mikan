package xray

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"strings"
	"sync"

	"mikan/internal/sub/mihomo"
)

// HappRoutingAuto as Happ's routing (settings.KeyHappRouting): the panel makes the routing
// profile of the Clash profile's rules, so Settings → Routing and an own profile reach
// Happ too, as far as Xray's routing goes: sites and IPs that go direct, through the
// proxy or nowhere. No groups (a service on one server goes through the proxy), no
// process names, no rule lists.
const HappRoutingAuto = "auto"

// HappRoutingMax bounds the routing link: a profile with geo lists stays far below it.
const HappRoutingMax = 64 << 10

// ValidHappRouting accepts happ://routing/off, HappRoutingAuto, and the add/onadd links
// whose payload is the base64 of a JSON profile: what Happ itself exports.
func ValidHappRouting(s string) bool {
	if s == "happ://routing/off" || s == HappRoutingAuto {
		return true
	}
	if len(s) > HappRoutingMax || strings.ContainsAny(s, " \r\n\t") {
		return false
	}
	var payload string
	for _, p := range []string{"happ://routing/onadd/", "happ://routing/add/"} {
		if rest, ok := strings.CutPrefix(s, p); ok {
			payload = rest
		}
	}
	if payload == "" {
		return false
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := enc.DecodeString(payload); err == nil {
			return json.Valid(raw)
		}
	}
	return false
}

// The geodata Happ downloads for it: runetfreedom's, made for Xray in Russia, with the
// categories of mihomo's (MetaCubeX) and ru-blocked. Pinned to the release whose
// categories are listed in happ/: a category the file lacks fails Happ's whole routing.
const (
	happRelease = "https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/download/202610071030/"
	happGeosite = happRelease + "geosite.dat"
	happGeoip   = happRelease + "geoip.dat"
)

var (
	//go:embed happ/geosite.txt
	happSitesList string
	//go:embed happ/geoip.txt
	happIPsList string
	happNames   = sync.OnceValue(func() [2]map[string]bool {
		read := func(s string) map[string]bool {
			m := map[string]bool{}
			for _, l := range strings.Split(s, "\n") {
				if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
					m[l] = true
				}
			}
			return m
		}
		return [2]map[string]bool{read(happSitesList), read(happIPsList)}
	})
)

// happProfile is Happ's routing profile (happ.su/main/dev-docs).
type happProfile struct {
	Name              string            `json:"Name"`
	GlobalProxy       string            `json:"GlobalProxy"` // "true": what no list takes goes through the proxy
	UseChunkFiles     string            `json:"UseChunkFiles"`
	RemoteDNS         string            `json:"RemoteDns"`
	DomesticDNS       string            `json:"DomesticDns"`
	RemoteDNSType     string            `json:"RemoteDNSType"`
	RemoteDNSDomain   string            `json:"RemoteDNSDomain"`
	RemoteDNSIP       string            `json:"RemoteDNSIP"`
	DomesticDNSType   string            `json:"DomesticDNSType"`
	DomesticDNSDomain string            `json:"DomesticDNSDomain"`
	DomesticDNSIP     string            `json:"DomesticDNSIP"`
	GeoipURL          string            `json:"Geoipurl"`
	GeositeURL        string            `json:"Geositeurl"`
	DNSHosts          map[string]string `json:"DnsHosts"`
	RouteOrder        string            `json:"RouteOrder"`
	DirectSites       []string          `json:"DirectSites"`
	DirectIP          []string          `json:"DirectIp"`
	ProxySites        []string          `json:"ProxySites"`
	ProxyIP           []string          `json:"ProxyIp"`
	BlockSites        []string          `json:"BlockSites"`
	BlockIP           []string          `json:"BlockIp"`
	DomainStrategy    string            `json:"DomainStrategy"`
	FakeDNS           string            `json:"FakeDNS"`
}

// HappRoutingOf turns Clash rules into Happ's routing link: a site or IP goes to the list
// of the first rule that takes it (mihomo's first match), MATCH decides the rest.
func HappRoutingOf(name string, rules []string) string {
	names := happNames()
	r := happProfile{Name: name, GlobalProxy: "true", UseChunkFiles: "true",
		RemoteDNS: "1.1.1.1", RemoteDNSType: "DoH", RemoteDNSDomain: "https://1.1.1.1/dns-query", RemoteDNSIP: "1.1.1.1",
		DomesticDNS: "77.88.8.8", DomesticDNSType: "DoH", DomesticDNSDomain: "https://77.88.8.8/dns-query", DomesticDNSIP: "77.88.8.8",
		GeoipURL: happGeoip, GeositeURL: happGeosite, DNSHosts: map[string]string{},
		// The profile sends the blocked and the proxied ahead of the direct.
		RouteOrder: "block-proxy-direct", DomainStrategy: "IPIfNonMatch", FakeDNS: "false",
		DirectSites: []string{}, DirectIP: []string{}, ProxySites: []string{}, ProxyIP: []string{}, BlockSites: []string{}, BlockIP: []string{}}
	seen := map[string]bool{}
	for _, rule := range rules {
		typ, target, _ := mihomo.RuleParts(rule)
		if typ == "MATCH" {
			if target == "DIRECT" {
				r.GlobalProxy = "false"
			}
			break // nothing after it is reached
		}
		f := mihomo.SplitRule(rule)
		if typ == "" || len(f) < 3 {
			continue
		}
		value := strings.TrimSpace(f[1])
		var sites, ips []string
		switch typ {
		case "DOMAIN":
			sites = []string{"full:" + value}
		case "DOMAIN-SUFFIX":
			sites = []string{"domain:" + value}
		case "DOMAIN-KEYWORD":
			sites = []string{"keyword:" + value}
		case "GEOSITE":
			if v := strings.ToLower(value); names[0][v] {
				sites = []string{"geosite:" + v}
			}
		case "GEOIP":
			switch v := strings.ToLower(value); {
			case v == "lan":
				ips = []string{"geoip:private"}
			case names[1][v]:
				ips = []string{"geoip:" + v}
			}
		case "IP-CIDR", "IP-CIDR6":
			if _, err := netip.ParsePrefix(value); err == nil {
				ips = []string{value}
			}
		case "RULE-SET":
			// The panel's own lists, by the geodata categories nearest to them.
			x := happLists[value]
			sites, ips = x[0], x[1]
		}
		var list *[2]*[]string
		switch {
		case target == "DIRECT":
			list = &[2]*[]string{&r.DirectSites, &r.DirectIP}
		case strings.HasPrefix(target, "REJECT"):
			list = &[2]*[]string{&r.BlockSites, &r.BlockIP}
		default:
			list = &[2]*[]string{&r.ProxySites, &r.ProxyIP}
		}
		for i, xs := range [2][]string{sites, ips} {
			for _, x := range xs {
				if !seen[x] { // the first rule that takes it decides, as in mihomo
					seen[x] = true
					*list[i] = append(*list[i], x)
				}
			}
		}
	}
	if !seen["geosite:private"] {
		r.DirectSites = append([]string{"geosite:private"}, r.DirectSites...)
	}
	b, _ := json.Marshal(r)
	return "happ://routing/onadd/" + base64.StdEncoding.EncodeToString(b)
}

// happLists are the geodata categories of the panel's rule lists (routing.Services,
// routing.DirectSets, routing.BlockedLists): sites, then IPs. Lists of apps have none.
var happLists = map[string][2][]string{
	"mikan-blocked":     {{"geosite:ru-blocked"}, {"geoip:ru-blocked"}},
	"mikan-ru-sites":    {{"geosite:category-ru", "geosite:ru-available-only-inside"}, nil},
	"mikan-ru-keywords": {{"geosite:category-ru"}, nil},
	"mikan-ru-core":     {{"geosite:category-ru"}, nil},
	"mikan-telegram-ip": {nil, {"geoip:telegram"}},
	"mikan-ai":          {{"geosite:category-ai-!cn"}, nil},
	"mikan-gemini":      {{"geosite:google-gemini"}, nil},
	"mikan-games":       {{"geosite:category-games"}, nil},
}

// IncyLink is a Happ routing link in INCY's own scheme: INCY takes any, its own is safest.
func IncyLink(link string) string {
	if rest, ok := strings.CutPrefix(link, "happ://"); ok {
		return "incy://" + rest
	}
	return link
}
