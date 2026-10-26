package routing

import (
	_ "embed"
	"net/url"
	"strings"
	"sync"
)

// The catalogs are where each core's apps find the geodata the rules name: a GEOSITE or
// GEOIP category of a service, and the categories nearest to the panel's own lists for a
// core that cannot download a Clash list.

// MetaCubeX meta-rules-dat is the geodata of mihomo and sing-box. MetaCubeXGeoX is what
// Koala Clash ships, mihomo's own default: apps without the files download them from here
// before the profile starts.
var MetaCubeXGeoX = map[string]string{
	"geoip":   "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip-lite.dat",
	"geosite": "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
	"mmdb":    "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.metadb",
	"asn":     "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/GeoLite2-ASN.mmdb",
}

const metaCubeXSing = "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/"

// MetaCubeXRuleSet is the sing-box rule set (.srs) of a category of MetaCubeX's geodata;
// kind is "geosite" or "geoip".
func MetaCubeXRuleSet(kind, name string) string {
	return metaCubeXSing + kind + "/" + url.PathEscape(strings.ToLower(name)) + ".srs"
}

// runetfreedom russia-v2ray-rules-dat is the geodata of the xray apps (Happ, INCY): made for
// Xray in Russia, with the categories of MetaCubeX's and ru-blocked. Pinned to the release
// whose categories are listed in runetfreedom/: a category the file lacks fails the app's
// whole routing.
const (
	runetfreedomRelease = "https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/download/202610071030/"
	RunetfreedomGeosite = runetfreedomRelease + "geosite.dat"
	RunetfreedomGeoIP   = runetfreedomRelease + "geoip.dat"
)

var (
	//go:embed runetfreedom/geosite.txt
	runetfreedomSites string
	//go:embed runetfreedom/geoip.txt
	runetfreedomIPs   string
	runetfreedomNames = sync.OnceValue(func() [2]map[string]bool {
		read := func(s string) map[string]bool {
			m := map[string]bool{}
			for _, l := range strings.Split(s, "\n") {
				if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
					m[l] = true
				}
			}
			return m
		}
		return [2]map[string]bool{read(runetfreedomSites), read(runetfreedomIPs)}
	})
)

// RunetfreedomHas says whether runetfreedom's geodata has the category; kind is "geosite"
// or "geoip", name in lower case.
func RunetfreedomHas(kind, name string) bool {
	names := runetfreedomNames()
	switch kind {
	case "geosite":
		return names[0][name]
	case "geoip":
		return names[1][name]
	}
	return false
}

// runetfreedomLists are the categories nearest to the panel's rule lists (Services,
// DirectSets, BlockedLists): sites, then IPs. Lists of apps have none.
var runetfreedomLists = map[string][2][]string{
	"mikan-blocked":     {{"geosite:ru-blocked"}, {"geoip:ru-blocked"}},
	"mikan-ru-sites":    {{"geosite:category-ru", "geosite:ru-available-only-inside"}, nil},
	"mikan-ru-keywords": {{"geosite:category-ru"}, nil},
	"mikan-ru-core":     {{"geosite:category-ru"}, nil},
	"mikan-telegram-ip": {nil, {"geoip:telegram"}},
	"mikan-ai":          {{"geosite:category-ai-!cn"}, nil},
	"mikan-gemini":      {{"geosite:google-gemini"}, nil},
	"mikan-games":       {{"geosite:category-games"}, nil},
}

// RunetfreedomList is what runetfreedom's geodata has for a rule list of the panel's: the
// sites' and the IPs' categories ("geosite:category-ru"), none for a list of its own or of
// apps.
func RunetfreedomList(name string) (sites, ips []string) {
	x := runetfreedomLists[name]
	return x[0], x[1]
}
