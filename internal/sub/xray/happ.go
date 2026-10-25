package xray

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/routing"
)

// HappRoutingAuto as Happ's routing (settings.KeyHappRouting): the panel makes the routing
// profile of its routing (HappRoutingOf), so Settings → Routing and an own profile reach
// Happ too, as far as Xray's routing goes: sites and IPs that go direct, through the
// proxy or nowhere. No groups (a service on one server goes through the proxy), no
// process names, no rule lists but the panel's own, as their nearest categories.
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

// HappRoutingOf is Happ's routing link of the profile the settings make, the same for every
// user: of the own Clash profile src when it renders, of the panel's routing (routing.Intent)
// otherwise. A site or IP goes to the list of the first rule that takes it, as in the cores,
// and where the rest goes decides GlobalProxy.
func HappRoutingOf(name string, p mihomo.Profile, m routing.Mode, src string) string {
	rules, final := happRules(p, m, src)
	return happLink(name, rules, final)
}

// happRules are the rules of the profile the settings make, in order, and where what none
// of them takes goes: an own profile's up to its first MATCH (through the proxy without
// one, Happ's own default), or the intent's.
func happRules(p mihomo.Profile, m routing.Mode, src string) ([]routing.Rule, routing.Target) {
	if src != "" {
		if rules, ok := mihomo.TemplateRules(p, src); ok {
			for i, r := range rules {
				if len(r.When) == 0 {
					return rules[:i], r.To
				}
			}
			return rules, routing.Target{Kind: routing.ToMain}
		}
	}
	in := routing.NewIntent(m, p.Rules, p.Routes)
	var rules []routing.Rule
	for _, part := range [][]routing.Rule{routing.HostRules(p.Direct), in.Head(), in.Own} {
		rules = append(rules, part...)
	}
	for _, r := range in.Routes {
		for _, x := range r.Rules {
			rules = append(rules, routing.Rule{When: []routing.Match{x}, To: r.To})
		}
	}
	return append(rules, in.Tail()...), in.Final()
}

// happTypes are the conditions Happ's lists hold: sites and IPs, as xray matches them.
var happTypes = map[string]bool{"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true, "GEOSITE": true,
	"GEOIP": true, "IP-CIDR": true, "IP-CIDR6": true, "RULE-SET": true}

// happLink is the routing link of the rules and final: the sites and IPs of each rule of
// one condition Happ's lists hold go to the lists of its target (direct, blocked, or through
// the proxy for any group).
func happLink(name string, rules []routing.Rule, final routing.Target) string {
	r := happProfile{Name: name, GlobalProxy: "true", UseChunkFiles: "true",
		RemoteDNS: "1.1.1.1", RemoteDNSType: "DoH", RemoteDNSDomain: "https://1.1.1.1/dns-query", RemoteDNSIP: "1.1.1.1",
		DomesticDNS: "77.88.8.8", DomesticDNSType: "DoH", DomesticDNSDomain: "https://77.88.8.8/dns-query", DomesticDNSIP: "77.88.8.8",
		GeoipURL: routing.RunetfreedomGeoIP, GeositeURL: routing.RunetfreedomGeosite, DNSHosts: map[string]string{},
		// The profile sends the blocked and the proxied ahead of the direct.
		RouteOrder: "block-proxy-direct", DomainStrategy: "IPIfNonMatch", FakeDNS: "false",
		DirectSites: []string{}, DirectIP: []string{}, ProxySites: []string{}, ProxyIP: []string{}, BlockSites: []string{}, BlockIP: []string{}}
	if final.Kind == routing.ToDirect {
		r.GlobalProxy = "false"
	}
	seen := map[string]bool{}
	for _, rule := range rules {
		if len(rule.When) != 1 || !happTypes[rule.When[0].Type] {
			continue
		}
		var sites, ips []string
		for _, f := range matchOf(rule.When[0]) {
			s, _ := f["domain"].([]string)
			i, _ := f["ip"].([]string)
			sites, ips = append(sites, s...), append(ips, i...)
		}
		list := [2]*[]string{&r.ProxySites, &r.ProxyIP}
		switch rule.To.Kind {
		case routing.ToDirect:
			list = [2]*[]string{&r.DirectSites, &r.DirectIP}
		case routing.ToReject, routing.ToDrop:
			list = [2]*[]string{&r.BlockSites, &r.BlockIP}
		}
		for i, xs := range [2][]string{sites, ips} {
			for _, x := range xs {
				if !seen[x] { // the first rule that takes it decides, as in the cores
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

// IncyLink is a Happ routing link in INCY's own scheme: INCY takes any, its own is safest.
func IncyLink(link string) string {
	if rest, ok := strings.CutPrefix(link, "happ://"); ok {
		return "incy://" + rest
	}
	return link
}
