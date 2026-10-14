package subs

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// The golden tests pin what a subscription looks like byte for byte: the same users, nodes
// and inbounds rendered in every routing mode as the Clash profile, the sing-box config and
// the share links, and compared with the files under testdata/golden. They exist so that
// the renderers (render.go, routes.go, template.go, singbox.go) can be reshaped without a
// profile of any subscriber changing unnoticed. A change that is meant to alter a profile
// regenerates the files (go test ./internal/panel/subs -run Golden -update) and the diff of
// the files is its review.
var update = flag.Bool("update", false, "rewrite the golden files of the subscription profiles")

// goldenDir holds the expected outputs.
const goldenDir = "testdata/golden"

// golden compares got with the file name under goldenDir, or writes the file with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	file := filepath.Join(goldenDir, name)
	if *update {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (run with -update to write the golden files)", err)
	}
	// A checkout on Windows may turn the files' line ends into CRLF.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file (run with -update if the change is meant):\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// goldenKey is a REALITY private key that is the same in every run: the bytes 1 to 32,
// clamped like a generated one, so the public key the profiles carry is fixed as well.
func goldenKey() string {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	return base64.RawURLEncoding.EncodeToString(k)
}

// goldenInbound is a preset's inbound as presets.NewConfig makes it, with the parts it makes
// at random (paths, passwords, keys) fixed.
func goldenInbound(id, node int64, preset, name, port string, tmpl proto.Template) db.Inbound {
	return db.Inbound{ID: id, NodeID: node, Name: name, Preset: preset, Port: port, Enabled: 1, Config: proto.Marshal(tmpl)}
}

func reality(sni string) map[string]any {
	return map[string]any{"dest": sni + ":443", "server-names": []any{sni}, "private-key": goldenKey(), "short-id": []any{"a1b2c3d4"}}
}

// goldenInbounds is one of each kind the profiles carry, on the node: the default presets
// and, with more, the ones of other families.
func goldenInbounds(node, firstID int64, more bool) []db.Inbound {
	out := []db.Inbound{
		goldenInbound(firstID, node, "vless_reality_xhttp", "VLESS XHTTP", "443", proto.Template{"type": "vless", "xhttp-config": map[string]any{"path": "/golden-path", "mode": "stream-one"}, "reality-config": reality("www.example.com")}),
		goldenInbound(firstID+1, node, "hysteria2", "Hysteria2", "443", proto.Template{"type": "hysteria2", "alpn": []any{"h3"}, "obfs": proto.ObfsSalamander, "obfs-password": "golden-obfs-password"}),
		goldenInbound(firstID+2, node, "tuic_v5", "TUIC", "8443", proto.Template{"type": "tuic", "alpn": []any{"h3"}, "congestion-controller": "bbr", "max-idle-time": 15000, "authentication-timeout": 1000}),
		goldenInbound(firstID+3, node, "vless_reality_vision", "VLESS Vision", "8443", proto.Template{"type": "vless", "mikan": map[string]any{"flow": "xtls-rprx-vision"}, "reality-config": reality("www.example.com")}),
	}
	if more {
		out = append(out,
			goldenInbound(firstID+4, node, "trojan_reality", "Trojan", "2087", proto.Template{"type": "trojan", "reality-config": reality("www.example.com")}),
			goldenInbound(firstID+5, node, "anytls", "AnyTLS", "2083", proto.Template{"type": "anytls"}),
			goldenInbound(firstID+6, node, "vless_reality_grpc", "VLESS gRPC", "2053", proto.Template{"type": "vless", "grpc-service-name": "goldengrpc", "reality-config": reality("www.example.com")}),
		)
	}
	return out
}

// goldenSlot is the user's keys, the same in every run.
var goldenSlot = db.Slot{Name: "s000001", Uuid: "0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1", Secret: "S3cr3t+/="}

// goldenRoutes is the admin's routing with a bit of everything: services sent three ways
// and to a node, direct sets, own lists, the DNS, the finer settings.
func goldenRoutes() Routes {
	return Routes{
		Services: map[string]string{"youtube": TargetVPN, "ads": TargetBlock, "torrents": TargetDirect, "ai": NodeTarget(2), "telegram": TargetVPN},
		Direct:   []string{"ru"},
		Lists: []RouteList{
			{Name: "wl", URL: "https://example.com/wl.yaml", Behavior: "classical", Format: "yaml", Target: TargetVPN},
			{Name: "ru-ip", URL: "https://example.com/ru.txt", Behavior: "ipcidr", Format: "text", Target: TargetDirect},
			{Name: "ads", URL: "https://example.com/ads.mrs", Behavior: "domain", Format: "mrs", Target: TargetBlock},
		},
		DNS:     DNS{Nameserver: []string{"https://dns.alidns.com/dns-query"}, Policy: []DNSPolicy{{Match: "+.cn", Servers: []string{"223.5.5.5"}}}},
		Servers: Servers{Auto: "fallback", Interval: 120},
		Tune:    Tune{BlockQUIC: true, Sniffer: true},
	}
}

// goldenRules are the admin's own Clash rules; the DOMAIN-WILDCARD one is younger than the
// cores of some apps (RulesFor).
var goldenRules = []string{"DOMAIN-SUFFIX,sber.ru,DIRECT", "DOMAIN-WILDCARD,*.tinkoff.ru,DIRECT", "IP-CIDR,10.0.0.0/8,DIRECT,no-resolve"}

// goldenSingle is a subscription of one node with a pinned self-signed certificate.
func goldenSingle(t *testing.T) Profile {
	return Profile{
		Slot:     goldenSlot,
		Inbounds: goldenInbounds(1, 1, false),
		Nodes:    []Node{{ID: 1, Endpoint: Endpoint{Host: "203.0.113.7", PinSHA256: "ab12"}}},
		Direct:   []string{"203.0.113.7", "vpn.example.com"},
	}
}

// goldenMulti is a subscription of two nodes: the second with a domain and its own certificate,
// a renamed inbound, a default fingerprint, and an inbound of a node the user is not given.
func goldenMulti(t *testing.T) Profile {
	ins := goldenInbounds(1, 1, true)
	us := goldenInbounds(2, 100, false)
	us[0].DisplayName = "Особый"
	ins = append(ins, us...)
	ins = append(ins, db.Inbound{ID: 200, NodeID: 3, Name: "x", Preset: "hysteria2", Port: "443", Enabled: 1, Config: ins[1].Config})
	return Profile{
		Slot:        goldenSlot,
		Inbounds:    ins,
		Fingerprint: "chrome",
		Nodes: []Node{
			{ID: 1, Name: "🇳🇱 Нидерланды", Endpoint: Endpoint{Host: "203.0.113.7", PinSHA256: "aa11"}},
			{ID: 2, Name: "🇺🇸 США", Endpoint: Endpoint{Host: "usa.example.com", SNI: "usa.example.com", PinSHA256: "bb22"}},
		},
		Direct: []string{"203.0.113.7", "usa.example.com", "vpn.example.com"},
	}
}

// goldenProfile is a case of the user: prof with the admin's settings (custom), in the app.
func goldenProfile(t *testing.T, base func(*testing.T) Profile, custom bool, app App) Profile {
	prof := base(t)
	prof.App = app
	if custom {
		prof.Rules = RulesFor(goldenRules, app)
		prof.Routes = goldenRoutes()
		prof.Lang = "en"
	}
	return prof
}

// goldenModes are the routing modes every profile is rendered in.
var goldenModes = []Routing{RoutingAll, RoutingRUDirect, RoutingBlocked}

// goldenVariants are the users: one node and two, with the admin's settings and without,
// and an app that takes no rule lists.
var goldenVariants = []struct {
	name   string
	base   func(*testing.T) Profile
	custom bool
	app    App
	groups Groups
}{
	{"single-plain", goldenSingle, false, App{}, Groups{}},
	{"single-custom", goldenSingle, true, App{Family: FamilyMihomo, Core: Version{1, 19, 32}}, Groups{Main: "Прокси", Auto: "Fast"}},
	{"multi-plain", goldenMulti, false, App{}, Groups{}},
	{"multi-custom", goldenMulti, true, App{Family: FamilyMihomo, Core: Version{1, 19, 32}}, Groups{Main: "Прокси", Auto: "Fast"}},
	{"multi-old-core", goldenMulti, true, App{Family: FamilyMihomo, Core: Version{1, 18, 0}}, Groups{}},
	{"multi-stash", goldenMulti, true, App{Family: FamilyStash}, Groups{}},
}

// The mihomo profile of every user in every mode.
func TestGoldenClash(t *testing.T) {
	for _, v := range goldenVariants {
		for _, mode := range goldenModes {
			t.Run(v.name+"/"+string(mode), func(t *testing.T) {
				raw, err := Mihomo(goldenProfile(t, v.base, v.custom, v.app), v.groups.WithDefaults("en"), mode)
				if err != nil {
					t.Fatal(err)
				}
				golden(t, v.name+"_"+string(mode)+".clash.yaml", raw)
			})
		}
	}
}

// The sing-box config of every user in every mode, with the lists it converts as small as
// the tests' stand-in for GitHub makes them.
func TestGoldenSingBox(t *testing.T) {
	lists(t)
	for _, v := range goldenVariants {
		for _, mode := range goldenModes {
			t.Run(v.name+"/"+string(mode), func(t *testing.T) {
				raw, err := SingBox(context.Background(), goldenProfile(t, v.base, v.custom, v.app), v.groups, mode, "")
				if err != nil {
					t.Fatal(err)
				}
				golden(t, v.name+"_"+string(mode)+".singbox.json", raw)
			})
		}
	}
}

// The share links, which do not depend on the mode: one list for each user.
func TestGoldenURIs(t *testing.T) {
	for _, v := range goldenVariants {
		t.Run(v.name, func(t *testing.T) {
			links, err := URIs(goldenProfile(t, v.base, v.custom, v.app))
			if err != nil {
				t.Fatal(err)
			}
			golden(t, v.name+".uri.txt", []byte(links+"\n"))
		})
	}
}

// The admin's own Clash profile on the two-node user, as the Clash profile, as the sing-box
// config made of it, and the starter the admin edits from.
func TestGoldenTemplate(t *testing.T) {
	lists(t)
	for _, v := range goldenVariants {
		if strings.HasPrefix(v.name, "single") {
			continue // the template names the second node's group
		}
		t.Run(v.name, func(t *testing.T) {
			prof := goldenProfile(t, v.base, v.custom, v.app)
			raw, err := Template(prof, ownTemplate)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, v.name+".template.clash.yaml", raw)
			sb, err := SingBox(context.Background(), prof, v.groups, RoutingAll, ownTemplate)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, v.name+".template.singbox.json", sb)
			for _, mode := range goldenModes {
				starter, err := Starter(prof, v.groups, mode)
				if err != nil {
					t.Fatal(err)
				}
				golden(t, v.name+"_"+string(mode)+".starter.yaml", starter)
			}
		})
	}
}
