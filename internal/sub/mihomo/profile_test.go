package mihomo

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/build"
	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// unmarshalProfile reads a YAML profile into a struct with json tags.
func unmarshalProfile(raw []byte, v any) error {
	var m any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func TestMihomoProfile(t *testing.T) {
	u := subtest.Defaults("ab12")
	u.Direct = []string{"203.0.113.7", "vpn.example.com", ""}
	raw, err := Render(profileOf(u), model.GroupNames{}, routing.All)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []map[string]any `json:"proxies"`
		Groups  []struct {
			Name    string   `json:"name"`
			Proxies []string `json:"proxies"`
			Hidden  bool     `json:"hidden"`
		} `json:"proxy-groups"`
		Rules []string `json:"rules"`
	}
	if err := unmarshalProfile(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Proxies) != 4 || cfg.Groups[0].Name != "VPN" || cfg.Groups[1].Name != "Авто" {
		t.Fatalf("profile: %s", raw)
	}
	// Rules injected by the app itself ("PROCESS-NAME,x.exe,PROXY") need a PROXY group.
	if alias := cfg.Groups[2]; alias.Name != "PROXY" || !alias.Hidden || len(alias.Proxies) != 1 || alias.Proxies[0] != "VPN" {
		t.Fatalf("PROXY alias: %+v", cfg.Groups)
	}
	wantRules := []string{"IP-CIDR,203.0.113.7/32,DIRECT,no-resolve", "DOMAIN,vpn.example.com,DIRECT", "GEOIP,LAN,DIRECT,no-resolve", "MATCH,VPN", "MATCH,REJECT"}
	if strings.Join(cfg.Rules, "|") != strings.Join(wantRules, "|") {
		t.Fatalf("rules: %v", cfg.Rules)
	}
	byType := map[string]map[string]any{}
	for _, p := range cfg.Proxies {
		byType[p["name"].(string)] = p
	}
	if byType["VLESS XHTTP"]["network"] != "xhttp" || byType["VLESS Vision"]["flow"] != "xtls-rprx-vision" {
		t.Fatalf("vless proxies: %v", cfg.Proxies)
	}
	// Without reuse-settings mihomo opens a TLS handshake per app connection (see defaultReuse).
	if opts, _ := byType["VLESS XHTTP"]["xhttp-opts"].(map[string]any); opts["reuse-settings"] == nil {
		t.Fatalf("xhttp must multiplex: %v", byType["VLESS XHTTP"])
	}
	if byType["Hysteria2"]["fingerprint"] != "ab12" || byType["TUIC"]["password"] != "S3cr3t+/=" {
		t.Fatalf("quic proxies: %v", cfg.Proxies)
	}
}

func TestCustomNames(t *testing.T) {
	u := subtest.Defaults("")
	u.Build.Inbounds[0].Name, u.Build.Inbounds[0].Named = "🇳🇱 Нидерланды", true
	u.Build.Inbounds[1].Name, u.Build.Inbounds[1].Named = "🇳🇱 Нидерланды", true // duplicates must not break the profile
	raw, err := Render(profileOf(u), model.GroupNames{Main: "🚀 Мой VPN", Auto: "⚡ Быстрый"}, routing.RUDirect)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []map[string]any `json:"proxies"`
		Groups  []struct {
			Name    string   `json:"name"`
			Proxies []string `json:"proxies"`
		} `json:"proxy-groups"`
		Rules []string `json:"rules"`
	}
	if err := unmarshalProfile(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Proxies[0]["name"] != "🇳🇱 Нидерланды" || cfg.Proxies[1]["name"] != "🇳🇱 Нидерланды 2" {
		t.Fatalf("names: %v %v", cfg.Proxies[0]["name"], cfg.Proxies[1]["name"])
	}
	if cfg.Groups[0].Name != "🚀 Мой VPN" || cfg.Groups[0].Proxies[0] != "⚡ Быстрый" || cfg.Rules[len(cfg.Rules)-2] != "MATCH,🚀 Мой VPN" {
		t.Fatalf("groups: %+v rules: %v", cfg.Groups, cfg.Rules)
	}
}

func TestRouting(t *testing.T) {
	type dns struct {
		Nameserver  []string            `json:"nameserver"`
		ProxyServer []string            `json:"proxy-server-nameserver"`
		Policy      map[string][]string `json:"nameserver-policy"`
	}
	type profileJSON struct {
		Groups []struct {
			Name string `json:"name"`
		} `json:"proxy-groups"`
		Rules   []string          `json:"rules"`
		DNS     dns               `json:"dns"`
		GeoxURL map[string]string `json:"geox-url"`
	}
	render := func(g model.GroupNames, m routing.Mode) profileJSON {
		t.Helper()
		u := subtest.Defaults("")
		u.Direct = []string{"203.0.113.7"}
		raw, err := Render(profileOf(u), g, m)
		if err != nil {
			t.Fatal(err)
		}
		var cfg profileJSON
		if err := unmarshalProfile(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	ru := render(model.GroupNames{}, routing.RUDirect)
	want := []string{"IP-CIDR,203.0.113.7/32,DIRECT,no-resolve", "GEOIP,LAN,DIRECT,no-resolve", "GEOSITE,category-ru,DIRECT", "GEOIP,ru,DIRECT", "MATCH,VPN", "MATCH,REJECT"}
	if strings.Join(ru.Rules, "|") != strings.Join(want, "|") {
		t.Fatalf("ru_direct rules: %v", ru.Rules)
	}
	if !strings.HasPrefix(ru.GeoxURL["geosite"], "https://github.com/MetaCubeX/meta-rules-dat/") || ru.GeoxURL["mmdb"] == "" {
		t.Fatalf("geodata must come from MetaCubeX: %v", ru.GeoxURL)
	}
	// Every domain gets resolved on the client for GEOIP,ru: through the tunnel, except
	// Russian ones and the server's own name (resolving it through itself would deadlock).
	for _, ns := range ru.DNS.Nameserver {
		if !strings.HasSuffix(ns, "#PROXY") {
			t.Errorf("nameserver %q must go through the tunnel", ns)
		}
	}
	if len(ru.DNS.ProxyServer) == 0 || strings.Contains(strings.Join(ru.DNS.ProxyServer, ""), "#") {
		t.Errorf("proxy-server-nameserver must be direct: %v", ru.DNS.ProxyServer)
	}
	if len(ru.DNS.Policy["geosite:category-ru"]) == 0 {
		t.Errorf("Russian domains need a Russian resolver: %v", ru.DNS.Policy)
	}

	// The "#PROXY" suffix must name a group even when the admin calls the main group PROXY.
	named := render(model.GroupNames{Main: "PROXY"}, routing.RUDirect)
	if named.Groups[0].Name != "PROXY" || named.Rules[len(named.Rules)-2] != "MATCH,PROXY" {
		t.Fatalf("main group named PROXY: %+v %v", named.Groups, named.Rules)
	}

	all := render(model.GroupNames{}, routing.All)
	for _, r := range all.Rules {
		if strings.HasPrefix(r, "GEOSITE,") || r == "GEOIP,ru,DIRECT" {
			t.Errorf("all mode needs no geodata: %v", all.Rules)
		}
	}
	if all.GeoxURL != nil || len(all.DNS.Policy) != 0 || strings.Contains(strings.Join(all.DNS.Nameserver, ""), "#") {
		t.Errorf("all mode keeps plain DNS: %+v %v", all.DNS, all.GeoxURL)
	}
}

func TestMultiNodeProfile(t *testing.T) {
	u := subtest.Defaults(subtest.PinNL)
	nl := model.Node{ID: 1, Name: "🇳🇱 Нидерланды", Endpoint: model.Endpoint{Host: "203.0.113.7", PinSHA256: subtest.PinNL}}
	us := model.Node{ID: 2, Name: "🇺🇸 США", Endpoint: model.Endpoint{Host: "usa.example.com", SNI: "usa.example.com", PinSHA256: subtest.PinUS}}
	u.Build.Nodes = []model.Node{nl, us}
	more := subtest.Inbounds(2, 100, false)[:2]
	more[1].Name, more[1].Named = "Особый", true
	u.Build.Inbounds = append(u.Build.Inbounds, more...)
	// An inbound of a node the user is not given (disabled node) stays out.
	other := u.Build.Inbounds[0]
	other.ID, other.Node = 200, 3
	u.Build.Inbounds = append(u.Build.Inbounds, other)

	raw, err := Render(profileOf(u), model.GroupNames{}, routing.All)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []map[string]any `json:"proxies"`
		Groups  []struct {
			Name    string   `json:"name"`
			Type    string   `json:"type"`
			Proxies []string `json:"proxies"`
		} `json:"proxy-groups"`
	}
	if err := unmarshalProfile(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	server := map[string]any{}
	var names []string
	for _, p := range cfg.Proxies {
		name := p["name"].(string)
		names = append(names, name)
		server[name] = p["server"]
	}
	want := []string{"🇳🇱 VLESS XHTTP", "🇳🇱 Hysteria2", "🇳🇱 TUIC", "🇳🇱 VLESS Vision", "🇺🇸 VLESS XHTTP", "Особый"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("proxies: %v", names)
	}
	if server["🇺🇸 VLESS XHTTP"] != "usa.example.com" || server["🇳🇱 VLESS XHTTP"] != "203.0.113.7" {
		t.Fatalf("each node is reached at its own address: %v", server)
	}
	for _, p := range cfg.Proxies {
		if p["name"] == "Особый" && (p["sni"] != "usa.example.com" || p["fingerprint"] != subtest.PinUS) {
			t.Fatalf("Hysteria2 on the US node pins its own certificate: %v", p)
		}
	}
	main, byName := cfg.Groups[0], map[string][]string{}
	for _, g := range cfg.Groups {
		byName[g.Name] = g.Proxies
	}
	if strings.Join(main.Proxies[:3], "|") != "Авто|🇳🇱 Нидерланды|🇺🇸 США" {
		t.Fatalf("main group lists the country groups first: %v", main.Proxies)
	}
	if strings.Join(byName["🇺🇸 США"], "|") != "🇺🇸 VLESS XHTTP|Особый" || len(byName["Авто"]) != len(want) {
		t.Fatalf("country groups: %v", byName)
	}

	// A node named like a group must not break the profile: it just gets no group.
	u.Build.Nodes[1].Name = "VPN"
	raw, err = Render(profileOf(u), model.GroupNames{}, routing.All)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Groups = nil
	if err := unmarshalProfile(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	vpn := 0
	for _, g := range cfg.Groups {
		if g.Name == "VPN" {
			vpn++
		}
	}
	if vpn != 1 {
		t.Fatalf("group names must stay unique: %+v", cfg.Groups)
	}
}

// The panel's default fingerprint reaches every uTLS proxy of the profile; an inbound's
// own one wins.
func TestDefaultFingerprint(t *testing.T) {
	u := subtest.Defaults("ab12")
	u.Build.Fingerprint = "firefox"
	for i, in := range u.Build.Inbounds {
		if in.Name == "VLESS Vision" {
			tpl, err := proto.Parse(in.Config)
			if err != nil || proto.SetFingerprint(tpl, "safari") != nil {
				t.Fatal(err)
			}
			u.Build.Inbounds[i].Config = proto.Marshal(tpl)
		}
	}
	want := map[string]string{"VLESS XHTTP": "firefox", "VLESS Vision": "safari"}
	raw, err := Render(profileOf(u), model.GroupNames{}, routing.All)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []map[string]any `json:"proxies"`
	}
	if err := unmarshalProfile(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.Proxies {
		name := p["name"].(string)
		got, _ := p["client-fingerprint"].(string)
		if got != want[name] {
			t.Errorf("%s: client-fingerprint=%q, want %q", name, got, want[name])
		}
	}
}

// The groups go in the order of the nodes (the admin's order), whatever their ids and the
// order of the inbounds are.
func TestNodeOrderFollowsTheProfile(t *testing.T) {
	base := subtest.Inbounds(1, 1, false)[:2]
	names := map[int64]string{1: "🇳🇱 Нидерланды", 2: "🇺🇸 США", 3: "🇩🇪 Германия"}
	var ins []model.Inbound
	// Inbounds come by node id, as the database lists them.
	for id := int64(1); id <= 3; id++ {
		for _, in := range base {
			in.ID, in.Node = id*100+in.ID, id
			ins = append(ins, in)
		}
	}
	for _, order := range [][]int64{{1, 2, 3}, {3, 1, 2}, {2, 3, 1}} {
		var nodes []model.Node
		var wantGroups []string
		for _, id := range order {
			nodes = append(nodes, model.Node{ID: id, Name: names[id], Endpoint: model.Endpoint{Host: "203.0.113.7"}})
			wantGroups = append(wantGroups, names[id])
		}
		p := profile.Profile{Proxies: build.Proxies(build.Profile{Keys: subtest.Keys, Inbounds: ins, Nodes: nodes}), Nodes: nodes}
		raw, err := Render(p, model.GroupNames{}, routing.All)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Groups []struct {
				Name    string   `json:"name"`
				Proxies []string `json:"proxies"`
			} `json:"proxy-groups"`
		}
		if err := unmarshalProfile(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		// The main group lists the auto group and then the country groups in order.
		if got := cfg.Groups[0].Proxies[1:4]; strings.Join(got, "|") != strings.Join(wantGroups, "|") {
			t.Fatalf("country groups for %v: %v", order, cfg.Groups[0].Proxies)
		}
		var groups []string
		for _, g := range cfg.Groups[2:5] {
			groups = append(groups, g.Name)
		}
		if strings.Join(groups, "|") != strings.Join(wantGroups, "|") {
			t.Fatalf("group list for %v: %v", order, groups)
		}
	}
}

func TestNothingToServeIsAnError(t *testing.T) {
	if _, err := Render(profile.Profile{}, model.GroupNames{}, routing.All); !errors.Is(err, model.ErrNoProxies) {
		t.Fatalf("a profile with empty groups is not served: %v", err)
	}
	if _, _, err := Template(profile.Profile{}, "rules: [MATCH,DIRECT]\n"); !errors.Is(err, model.ErrNoProxies) {
		t.Fatalf("an own profile with no proxies: %v", err)
	}
}

// A rule type younger than an app's core would fail its whole profile: those go only to
// mihomo apps that name a core with them.
func TestRulesFor(t *testing.T) {
	rules := subtest.ParseRules("DOMAIN-SUFFIX,a.com,DIRECT", "DOMAIN-WILDCARD,*.b.com,PROXY", "IP-ASN,13335,PROXY", "GEOSITE,youtube,PROXY")
	for ua, want := range map[string]int{
		"mihomo/1.19.31":     4,
		"mihomo/1.19.11":     3, // no DOMAIN-WILDCARD before 1.19.12
		"mihomo/1.18.1":      2, // nor IP-ASN before 1.18.2
		"clash-verge/v2.4.0": 2, // the core is not named
		"Stash/3.0":          2,
	} {
		if got := RulesFor(rules, apps.DetectApp(ua)); len(got) != want {
			t.Errorf("%s: %v", ua, got)
		}
	}
	if got := RulesFor(rules[:1], apps.DetectApp("Stash/3.0")); &got[0] != &rules[0] {
		t.Error("nothing to drop, yet the rules were copied")
	}
}

// The admin's rules go after the panel's own direct hosts and before the built-in routing.
func TestRulesInProfile(t *testing.T) {
	g := model.GroupNames{Main: "Мой VPN", Auto: "Быстрый"}
	u := subtest.Defaults("")
	u.Direct, u.Rules = []string{"vpn.example.com"}, routing.ServedRules("DOMAIN-SUFFIX,sber.ru,DIRECT\nGEOSITE,youtube,PROXY", g)
	raw, err := Render(profileOf(u), g, routing.RUDirect)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Rules []string `json:"rules"`
	}
	if err := unmarshalProfile(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	at := func(r string) int {
		for i, x := range cfg.Rules {
			if x == r {
				return i
			}
		}
		return -1
	}
	own, lan, geo := at("DOMAIN-SUFFIX,sber.ru,DIRECT"), at("GEOIP,LAN,DIRECT,no-resolve"), at("GEOSITE,category-ru,DIRECT")
	if own < 0 || lan < 0 || geo < 0 || !(lan < own && own < geo) || cfg.Rules[len(cfg.Rules)-2] != "MATCH,Мой VPN" {
		t.Fatalf("order: %q", cfg.Rules)
	}
	if at("DOMAIN,vpn.example.com,DIRECT") > own {
		t.Fatalf("the panel's host must come before the admin's rules: %q", cfg.Rules)
	}
}

// An idle gRPC connection is pinged, so that the app does not stay on one a middlebox cut
// while it was silent; Stash's profile, always the built-in one, goes without.
func TestGRPCKeepalive(t *testing.T) {
	grpc := model.Proxy{Name: "G", Type: "vless", Server: "203.0.113.7", Port: 2053, UUID: subtest.Keys.UUID, Transport: &model.Transport{Network: "grpc", Service: "api"}}
	if o := Proxy(grpc)["grpc-opts"].(map[string]any); o["ping-interval"] != 15 || o["grpc-service-name"] != "api" {
		t.Fatalf("grpc-opts: %v", o)
	}
	for _, app := range []apps.App{{Family: apps.FamilyMihomo}, {Family: apps.FamilyStash}} {
		p := profile.Profile{Proxies: []model.Proxy{grpc}, App: app}
		built, err := renderConfig(p, model.GroupNames{}, routing.DefaultMode)
		if err != nil {
			t.Fatal(err)
		}
		cfgs := map[string]map[string]any{"built-in": built}
		if p.App.TakesOwnProfile() { // Stash gets no own profile
			own, _, err := templateConfig(p, subtest.OwnTemplate)
			if err != nil {
				t.Fatal(err)
			}
			cfgs["own"] = own
		}
		for name, cfg := range cfgs {
			var opts map[string]any
			switch ps := cfg["proxies"].(type) {
			case []map[string]any:
				opts = ps[0]["grpc-opts"].(map[string]any)
			case []any:
				opts = ps[len(ps)-1].(map[string]any)["grpc-opts"].(map[string]any)
			}
			if _, ok := opts["ping-interval"]; ok == (app.Family == apps.FamilyStash) {
				t.Errorf("%s, %s profile: grpc-opts %v", app.Family, name, opts)
			}
		}
	}
}

// Every built-in profile and every starter begins with base.yaml, in its order and with
// its values: the panel only adds to it, the geodata keys go only where nothing names
// geodata, "real IP" turns fake-ip to redir-host with no range, and the sniffer is
// sniffer.yaml's. Rendering leaves base.yaml as it was read.
func TestBaseSkeleton(t *testing.T) {
	if len(baseKeys) == 0 || len(base) != len(baseKeys) || len(sniffer) == 0 {
		t.Fatalf("base.yaml %v, sniffer.yaml %v", baseKeys, sniffer)
	}
	check := func(t *testing.T, raw []byte, mode routing.Mode, tune routing.Tune) {
		t.Helper()
		var n yaml.Node
		var out map[string]any
		if err := yaml.Unmarshal(raw, &n); err != nil {
			t.Fatal(err)
		}
		if err := n.Decode(&out); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for i := 0; i < len(n.Content[0].Content); i += 2 {
			keys = append(keys, n.Content[0].Content[i].Value)
		}
		var want []string
		for _, k := range baseKeys {
			if _, ok := out[k]; !ok && (k == "geodata-mode" || k == "geox-url") && mode == routing.All {
				continue
			}
			want = append(want, k)
		}
		if len(keys) < len(want) || !slices.Equal(keys[:len(want)], want) {
			t.Fatalf("keys %v, base.yaml %v", keys, baseKeys)
		}
		for _, k := range want {
			got, w := out[k], base[k]
			if k == "dns" {
				dns, _ := got.(map[string]any)
				fixed := map[string]any{}
				for dk := range w.(map[string]any) {
					if x, ok := dns[dk]; ok {
						fixed[dk] = x
					}
				}
				if got = fixed; tune.RealIP {
					w = cloneYAML(w)
					w.(map[string]any)["enhanced-mode"] = "redir-host"
					delete(w.(map[string]any), "fake-ip-range")
				}
			}
			if !reflect.DeepEqual(got, w) {
				t.Errorf("%s: %#v, base.yaml %#v", k, got, w)
			}
		}
		if got, ok := out["sniffer"]; ok != tune.Sniffer || ok && !reflect.DeepEqual(got, sniffer) {
			t.Errorf("sniffer %#v (%v), sniffer.yaml %#v", got, tune.Sniffer, sniffer)
		}
	}
	for _, v := range append(slices.Clip(subtest.Variants), subtest.EveryTypeVariant) {
		for _, realIP := range []bool{false, true} {
			p := goldenProfile(v)
			name := v.Name
			if realIP {
				p.Routes.Tune.RealIP, name = true, name+"-real-ip"
			}
			for _, mode := range subtest.Modes {
				t.Run(name+"/"+string(mode), func(t *testing.T) {
					raw, err := Render(p, v.Groups.WithDefaults("en"), mode)
					if err != nil {
						t.Fatal(err)
					}
					check(t, raw, mode, p.Routes.Tune)
					if !p.App.TakesOwnProfile() {
						return
					}
					starter, err := Starter(p, v.Groups, mode)
					if err != nil {
						t.Fatal(err)
					}
					check(t, starter, mode, p.Routes.Tune)
				})
			}
		}
	}
	fresh, _ := embeddedYAML(baseYAML)
	if !reflect.DeepEqual(base, fresh) {
		t.Fatalf("rendering changed base.yaml: %#v", base)
	}
}
