package mihomo

import (
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

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

func renderRouted(t *testing.T, prof profile.Profile, m routing.Mode) (routedProfile, []byte) {
	t.Helper()
	raw, err := Render(prof, model.GroupNames{}, m)
	if err != nil {
		t.Fatal(err)
	}
	coreAccepts(t, raw)
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
	prof := profileOf(subtest.TwoNodes())
	prof.Rules = subtest.ParseRules("DOMAIN-SUFFIX,sber.ru,DIRECT")
	prof.Routes = routing.Routes{
		Services: map[string]string{"youtube": routing.TargetVPN, "ads": routing.TargetBlock, "torrents": routing.TargetDirect, "ai": subtest.NodeTarget(2)},
		Direct:   []string{"ru"},
	}
	cfg, raw := renderRouted(t, prof, routing.RUDirect)
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

	prof.App = apps.App{Family: apps.FamilyMihomo, Core: apps.Version{1, 19, 20}}
	if cfg, _ := renderRouted(t, prof, routing.RUDirect); cfg.Providers["mikan-ru-apps-wildcard"] == nil {
		t.Fatalf("a known new core takes the wildcard list: %v", cfg.Providers)
	}
}

// Only what is blocked goes through the tunnel; the rest is direct.
func TestRoutingBlocked(t *testing.T) {
	prof := profileOf(subtest.Defaults(""))
	prof.Routes = routing.Routes{Services: map[string]string{"telegram": routing.TargetVPN}}
	cfg, _ := renderRouted(t, prof, routing.Blocked)
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
	prof.App = apps.App{Family: apps.FamilyStash}
	cfg, _ = renderRouted(t, prof, routing.Blocked)
	if len(cfg.Providers) != 0 || cfg.Rules[len(cfg.Rules)-2] != "MATCH,VPN" || !slices.Contains(cfg.Rules, "GEOIP,ru,DIRECT") || slices.Contains(cfg.Rules, "GEOSITE,telegram,VPN") {
		t.Fatalf("stash:\n%s", strings.Join(cfg.Rules, "\n"))
	}
}

// A service on a node this app has nothing of goes to the main group.
func TestServiceOnAMissingNode(t *testing.T) {
	prof := profileOf(subtest.Defaults(""))
	prof.Routes = routing.Routes{Services: map[string]string{"youtube": subtest.NodeTarget(7)}}
	cfg, _ := renderRouted(t, prof, routing.All)
	if !slices.Contains(cfg.Rules, "GEOSITE,youtube,VPN") || len(cfg.Groups) != 3 {
		t.Fatalf("groups %+v rules %v", cfg.Groups, cfg.Rules)
	}
}

func TestRoutesDNS(t *testing.T) {
	prof := profileOf(subtest.Defaults(""))
	prof.Routes.DNS = routing.DNS{Nameserver: []string{"https://dns.alidns.com/dns-query"}, Policy: []routing.DNSPolicy{{Match: "+.cn", Servers: []string{"223.5.5.5"}}}}
	cfg, _ := renderRouted(t, prof, routing.RUDirect)
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

// Lists of the admin's own go where they are sent, through the tunnel to download; one in
// .mrs only to an app whose core reads it.
func TestOwnLists(t *testing.T) {
	prof := profileOf(subtest.TwoNodes())
	prof.Routes = routing.Routes{Lists: []routing.RouteList{
		{Name: "wl", URL: "https://example.com/wl.yaml", Behavior: "classical", Format: "yaml", Target: routing.TargetVPN},
		{Name: "ru-ip", URL: "https://example.com/ru.txt", Behavior: "ipcidr", Format: "text", Target: routing.TargetDirect},
		{Name: "games", URL: "https://example.com/games.yaml", Behavior: "domain", Format: "yaml", Target: subtest.NodeTarget(2)},
		{Name: "ads", URL: "https://example.com/ads.mrs", Behavior: "domain", Format: "mrs", Target: routing.TargetBlock},
	}}
	cfg, raw := renderRouted(t, prof, routing.All)
	for _, want := range []string{"RULE-SET,own-wl,VPN", "RULE-SET,own-ru-ip,DIRECT,no-resolve", "RULE-SET,own-games,📋 games"} {
		if !slices.Contains(cfg.Rules, want) {
			t.Errorf("no %s:\n%s", want, strings.Join(cfg.Rules, "\n"))
		}
	}
	if p := cfg.Providers["own-wl"]; p == nil || p["proxy"] != "VPN" || p["url"] != "https://example.com/wl.yaml" {
		t.Errorf("provider: %v", cfg.Providers)
	}
	if cfg.Providers["own-ads"] != nil || strings.Contains(string(raw), "own-ads") {
		t.Error("an .mrs list went to a core that may not read it")
	}
	prof.App = apps.App{Family: apps.FamilyMihomo, Core: apps.Version{1, 19, 32}}
	if cfg, _ := renderRouted(t, prof, routing.All); !slices.Contains(cfg.Rules, "RULE-SET,own-ads,REJECT") {
		t.Errorf("a new core takes .mrs: %v", cfg.Rules)
	}
}

// How servers are picked: no group per country, the first that works, the interval.
func TestServerChoice(t *testing.T) {
	prof := profileOf(subtest.TwoNodes())
	prof.Routes.Servers = routing.Servers{NoCountries: true, Auto: "fallback", Interval: 120}
	cfg, raw := renderRouted(t, prof, routing.All)
	if len(cfg.Groups) != 3 || strings.Contains(string(raw), "🇺🇸 США\n") {
		t.Fatalf("groups: %+v", cfg.Groups)
	}
	if !strings.Contains(string(raw), "type: fallback") || !strings.Contains(string(raw), "interval: 120") || strings.Contains(string(raw), "tolerance") {
		t.Fatalf("auto:\n%s", raw)
	}
}

// The Clash apps' own balance goes over the common one: the kind of the automatic choice,
// the interval and the tolerance of the groups that test; the other cores' leave it be.
func TestMihomoBalance(t *testing.T) {
	prof := profileOf(subtest.TwoNodes())
	prof.Routes.Servers = routing.Servers{Auto: "fallback", Interval: 120, Mihomo: routing.MihomoServers{Type: "load-balance", Interval: 600, Tolerance: 150},
		SingBox: routing.SingBoxServers{Interval: 60}}
	_, raw := renderRouted(t, prof, routing.All)
	for _, want := range []string{"name: Авто\n    type: load-balance", "interval: 600", "tolerance: 150"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("no %q:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), "interval: 120") || strings.Contains(string(raw), "interval: 60\n") {
		t.Fatalf("the common or another core's interval:\n%s", raw)
	}
}

// QUIC blocked ahead of the admin's rules, the sniffer, and DNS without fake addresses.
func TestTune(t *testing.T) {
	prof := profileOf(subtest.Defaults(""))
	prof.Rules = subtest.ParseRules("DOMAIN-SUFFIX,sber.ru,DIRECT")
	prof.Routes.Tune = routing.Tune{BlockQUIC: true, Sniffer: true, RealIP: true}
	cfg, raw := renderRouted(t, prof, routing.RUDirect)
	quic, own := slices.Index(cfg.Rules, "AND,((NETWORK,UDP),(DST-PORT,443)),REJECT"), slices.Index(cfg.Rules, "DOMAIN-SUFFIX,sber.ru,DIRECT")
	if quic < 0 || own < quic {
		t.Fatalf("rules:\n%s", strings.Join(cfg.Rules, "\n"))
	}
	if cfg.DNS["enhanced-mode"] != "redir-host" || cfg.DNS["fake-ip-range"] != nil || !strings.Contains(string(raw), "sniffer:") {
		t.Fatalf("dns %v\n%s", cfg.DNS, raw)
	}
	if _, raw := renderRouted(t, profileOf(subtest.Defaults("")), routing.RUDirect); strings.Contains(string(raw), "sniffer") || strings.Contains(string(raw), "DST-PORT") {
		t.Fatal("left alone, the profile is as it was")
	}
}
