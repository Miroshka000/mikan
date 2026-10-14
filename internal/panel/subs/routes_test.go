package subs

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/config"

	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
)

// NodeTarget sends a service to one node, the way the admin panel writes the target.
func NodeTarget(id int64) string { return targetNode + strconv.FormatInt(id, 10) }

// twoNodes: the test profile's node 1 and a second node in the US.
func twoNodes(t *testing.T) Profile {
	t.Helper()
	prof := profile(t, "")
	prof.Nodes = []Node{{ID: 1, Name: "🇳🇱 Нидерланды", Endpoint: Endpoint{Host: "203.0.113.7"}}, {ID: 2, Name: "🇺🇸 США", Endpoint: Endpoint{Host: "198.51.100.9"}}}
	info, _ := presets.Get("vless_reality_xhttp")
	c, err := presets.NewConfig("vless_reality_xhttp", "www.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	prof.Inbounds = append(prof.Inbounds, db.Inbound{ID: 100, NodeID: 2, Name: info.Name, Preset: info.ID, Port: info.Port, Enabled: 1, Config: c})
	return prof
}

type routedProfile struct {
	Groups []struct {
		Name    string   `json:"name"`
		Proxies []string `json:"proxies"`
	} `json:"proxy-groups"`
	Providers map[string]map[string]any `json:"rule-providers"`
	Rules     []string                  `json:"rules"`
	DNS       map[string]any            `json:"dns"`
	GeoxURL   map[string]string         `json:"geox-url"`
}

func renderRouted(t *testing.T, prof Profile, r Routing) (routedProfile, []byte) {
	t.Helper()
	raw, err := Mihomo(prof, Groups{}, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.UnmarshalRawConfig(raw); err != nil {
		t.Fatalf("mihomo refuses the profile: %v\n%s", err, raw)
	}
	var cfg routedProfile
	if err := unmarshalProfile(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg, raw
}

// Services go where the admin sends them, after the admin's own rules and before the
// mode's; a service on one server gets a group of that server, the main group to fall
// back on.
func TestRoutesInProfile(t *testing.T) {
	prof := twoNodes(t)
	prof.Rules = []string{"DOMAIN-SUFFIX,sber.ru,DIRECT"}
	prof.Routes = Routes{
		Services: map[string]string{"youtube": TargetVPN, "ads": TargetBlock, "torrents": TargetDirect, "ai": NodeTarget(2)},
		Direct:   []string{"ru"},
	}
	cfg, raw := renderRouted(t, prof, RoutingRUDirect)
	at := func(r string) int { return slices.Index(cfg.Rules, r) }
	own, ads, yt, ai, bank, ru := at("DOMAIN-SUFFIX,sber.ru,DIRECT"), at("GEOSITE,category-ads-all,REJECT"), at("GEOSITE,youtube,VPN"),
		at("GEOSITE,category-ai-!cn,🤖 ChatGPT · Claude · Gemini"), at("RULE-SET,mikan-ru-apps,DIRECT"), at("GEOSITE,category-ru,DIRECT")
	if own < 0 || ads < 0 || yt < 0 || ai < 0 || bank < 0 || ru < 0 || !(own < ads && ads < yt && yt < ai && ai < bank && bank < ru) {
		t.Fatalf("order:\n%s", strings.Join(cfg.Rules, "\n"))
	}
	if at("RULE-SET,mikan-torrent-apps,DIRECT") < 0 || cfg.Rules[len(cfg.Rules)-2] != "MATCH,VPN" || cfg.Rules[len(cfg.Rules)-1] != "MATCH,REJECT" {
		t.Fatalf("torrents direct, the rest through the tunnel:\n%s", strings.Join(cfg.Rules, "\n"))
	}
	i := slices.IndexFunc(cfg.Groups, func(g struct {
		Name    string   `json:"name"`
		Proxies []string `json:"proxies"`
	}) bool {
		return g.Name == "🤖 ChatGPT · Claude · Gemini"
	})
	if i < 0 || len(cfg.Groups[i].Proxies) != 2 || !strings.HasPrefix(cfg.Groups[i].Proxies[0], "🇺🇸") || cfg.Groups[i].Proxies[1] != "VPN" {
		t.Fatalf("the AI group: %+v", cfg.Groups)
	}
	for _, name := range []string{"mikan-ai", "mikan-ru-apps", "mikan-torrent-apps"} {
		if p := cfg.Providers[name]; p == nil || p["proxy"] != "VPN" || !strings.HasPrefix(p["url"].(string), "https://") {
			t.Fatalf("provider %s: %v", name, cfg.Providers[name])
		}
	}
	// An app that does not name its core gets no lists of a younger core.
	if cfg.Providers["mikan-discord-voice"] != nil || cfg.Providers["mikan-ru-apps-wildcard"] != nil || strings.Contains(string(raw), "wildcard") {
		t.Fatalf("lists for younger cores went to an unknown one: %v", cfg.Providers)
	}

	prof.App = App{Family: FamilyMihomo, Core: Version{1, 19, 20}}
	if cfg, _ := renderRouted(t, prof, RoutingRUDirect); cfg.Providers["mikan-ru-apps-wildcard"] == nil {
		t.Fatalf("a known new core takes the wildcard list: %v", cfg.Providers)
	}
}

// Only what is blocked goes through the tunnel; the rest is direct.
func TestRoutingBlocked(t *testing.T) {
	prof := profile(t, "")
	prof.Routes = Routes{Services: map[string]string{"telegram": TargetVPN}}
	cfg, _ := renderRouted(t, prof, RoutingBlocked)
	if cfg.Rules[len(cfg.Rules)-1] != "MATCH,DIRECT" || !slices.Contains(cfg.Rules, "RULE-SET,mikan-blocked,VPN") || !slices.Contains(cfg.Rules, "GEOSITE,telegram,VPN") {
		t.Fatalf("rules:\n%s", strings.Join(cfg.Rules, "\n"))
	}
	// What falls through goes direct here: UDP the tunnel cannot carry stops at a twin.
	for _, r := range []string{"RULE-SET,mikan-blocked", "GEOSITE,telegram"} {
		if i := slices.Index(cfg.Rules, r+",VPN"); i < 0 || i+1 >= len(cfg.Rules) || cfg.Rules[i+1] != r+",REJECT" {
			t.Fatalf("%s has no REJECT twin:\n%s", r, strings.Join(cfg.Rules, "\n"))
		}
	}
	if slices.Contains(cfg.Rules, "GEOIP,LAN,REJECT,no-resolve") {
		t.Fatal("a DIRECT rule got a twin")
	}
	if cfg.GeoxURL["geosite"] == "" {
		t.Fatal("the lists name sites by GEOSITE: geodata is needed")
	}

	// Stash takes no rule lists: the mode it can do, and no services.
	prof.App = App{Family: FamilyStash}
	cfg, _ = renderRouted(t, prof, RoutingBlocked)
	if len(cfg.Providers) != 0 || cfg.Rules[len(cfg.Rules)-2] != "MATCH,VPN" || !slices.Contains(cfg.Rules, "GEOIP,ru,DIRECT") || slices.Contains(cfg.Rules, "GEOSITE,telegram,VPN") {
		t.Fatalf("stash:\n%s", strings.Join(cfg.Rules, "\n"))
	}
}

// A service on a node this app has nothing of goes to the main group.
func TestServiceOnAMissingNode(t *testing.T) {
	prof := profile(t, "")
	prof.Routes = Routes{Services: map[string]string{"youtube": NodeTarget(7)}}
	cfg, _ := renderRouted(t, prof, RoutingAll)
	if !slices.Contains(cfg.Rules, "GEOSITE,youtube,VPN") || len(cfg.Groups) != 3 {
		t.Fatalf("groups %+v rules %v", cfg.Groups, cfg.Rules)
	}
}

func TestRoutesDNS(t *testing.T) {
	prof := profile(t, "")
	prof.Routes.DNS = DNS{Nameserver: []string{"https://dns.alidns.com/dns-query"}, Policy: []DNSPolicy{{Match: "+.cn", Servers: []string{"223.5.5.5"}}}}
	cfg, _ := renderRouted(t, prof, RoutingRUDirect)
	if ns := cfg.DNS["nameserver"].([]any); len(ns) != 1 || ns[0] != "https://dns.alidns.com/dns-query" {
		t.Fatalf("nameserver: %v", cfg.DNS)
	}
	if pol := cfg.DNS["nameserver-policy"].(map[string]any); len(pol) != 1 || pol["+.cn"] == nil {
		t.Fatalf("policy: %v", cfg.DNS)
	}
	// Left alone, the panel's own.
	if cfg.DNS["proxy-server-nameserver"] == nil {
		t.Fatalf("proxy-server-nameserver: %v", cfg.DNS)
	}
}

func TestRoutesCheck(t *testing.T) {
	exists := func(id int64) bool { return id == 2 }
	good := Routes{Services: map[string]string{"youtube": TargetVPN, "ai": NodeTarget(2)}, Direct: []string{"ru"},
		DNS: DNS{Nameserver: []string{"https://1.1.1.1/dns-query#VPN", "8.8.8.8", "1.1.1.1:53", "system", "dhcp://en0"}, Policy: []DNSPolicy{{Match: "geosite:cn", Servers: []string{"tls://223.5.5.5"}}}}}
	if err := good.Check(exists); err != nil {
		t.Fatal(err)
	}
	for code, r := range map[string]Routes{
		"routes_service": {Services: map[string]string{"nope": TargetVPN}},
		"routes_target":  {Services: map[string]string{"youtube": NodeTarget(9)}},
		"routes_direct":  {Direct: []string{"nope"}},
		"routes_dns":     {DNS: DNS{Nameserver: []string{"ftp://x"}}},
	} {
		if err := r.Check(exists); err == nil || err.Error() != code {
			t.Errorf("%s: %v", code, err)
		}
	}
	if err := (Routes{DNS: DNS{Policy: []DNSPolicy{{Match: "+.cn"}}}}).Check(exists); err == nil {
		t.Error("a policy without servers")
	}
}
