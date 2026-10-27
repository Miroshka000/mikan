package routing

import (
	_ "embed"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// The catalogs are where each core's apps find the geodata the rules name: a GEOSITE or
// GEOIP category of a service, and the categories nearest to the panel's own lists for a
// core that cannot download a Clash list.

// MetaCubeX meta-rules-dat is the geodata of mihomo and sing-box; where mihomo's apps
// download it from is in mihomo/base.yaml (geox-url).
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
	runetfreedomIPs string
	//go:embed loyalsoldier/geosite.txt
	loyalsoldierSites string
	//go:embed loyalsoldier/geoip.txt
	loyalsoldierIPs string
)

// Geodata is the geodata an Xray config names categories of, as the app has it. The apps
// load runetfreedom's files only when the panel's routing profile names them (Happ and INCY
// with "From the routing settings"); otherwise they keep their own, Loyalsoldier's as a
// rule, and a category those lack (ru-blocked) fails the whole config.
type Geodata struct {
	names func() [2]map[string]bool // sites, IPs
}

var (
	// Runetfreedom is runetfreedom's geodata (RunetfreedomGeosite, RunetfreedomGeoIP).
	Runetfreedom = Geodata{sync.OnceValue(func() [2]map[string]bool {
		return [2]map[string]bool{readNames(runetfreedomSites), readNames(runetfreedomIPs)}
	})}
	// Common is what runetfreedom's and Loyalsoldier's geodata both have: what an app has
	// whichever of the two it loads.
	Common = Geodata{sync.OnceValue(func() [2]map[string]bool {
		rf := Runetfreedom.names()
		both := func(a, b map[string]bool) map[string]bool {
			out := map[string]bool{}
			for k := range a {
				if b[k] {
					out[k] = true
				}
			}
			return out
		}
		return [2]map[string]bool{both(rf[0], readNames(loyalsoldierSites)), both(rf[1], readNames(loyalsoldierIPs))}
	})}
)

// readNames are the categories of a list, a name a line, "#" a comment.
func readNames(s string) map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			m[l] = true
		}
	}
	return m
}

// Has says whether the geodata has the category; kind is "geosite" or "geoip", name in
// lower case.
func (g Geodata) Has(kind, name string) bool {
	names := g.names()
	switch kind {
	case "geosite":
		return names[0][name]
	case "geoip":
		return names[1][name]
	}
	return false
}

// Names are the categories of the geodata, sorted; kind is "geosite" or "geoip".
func (g Geodata) Names(kind string) []string {
	names := g.names()
	switch kind {
	case "geosite":
		return slices.Sorted(maps.Keys(names[0]))
	case "geoip":
		return slices.Sorted(maps.Keys(names[1]))
	}
	return nil
}

// runetfreedomLists are the categories nearest to the panel's rule lists (Services,
// DirectSets, BlockedLists): sites, then IPs. Lists of apps have none, nor the IPs of
// Discord's voice servers (mikan-discord-voice).
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

// List is what the geodata has for a rule list of the panel's: the sites' and the IPs'
// categories ("geosite:category-ru"), none for a list of its own or of apps.
func (g Geodata) List(name string) (sites, ips []string) {
	x := runetfreedomLists[name]
	has := func(kind string, refs []string) []string {
		var out []string
		for _, r := range refs {
			if g.Has(kind, strings.TrimPrefix(r, kind+":")) {
				out = append(out, r)
			}
		}
		return out
	}
	return has("geosite", x[0]), has("geoip", x[1])
}
