package build

import (
	"reflect"
	"strings"
	"testing"

	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
)

var testKeys = proto.Slot{Name: "s000001", UUID: "0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1", Secret: "S3cr3t+/="}

// inbounds are two of the default presets on node, their ids from first.
func inbounds(node, first int64) []model.Inbound {
	hy2 := proto.Marshal(proto.Template{"type": "hysteria2", "alpn": []any{"h3"}})
	tuic := proto.Marshal(proto.Template{"type": "tuic", "alpn": []any{"h3"}})
	return []model.Inbound{
		{ID: first, Node: node, Label: "hysteria2", Name: "Hysteria2", Port: 443, PortSpec: "443", Config: hy2},
		{ID: first + 1, Node: node, Label: "tuic", Name: "TUIC", Port: 8443, PortSpec: "8443", Config: tuic},
	}
}

func TestNodePrefix(t *testing.T) {
	for in, want := range map[string]string{"🇳🇱 Нидерланды": "🇳🇱", "🇺🇸США": "🇺🇸", "Германия": "Германия", "": "", " 🇩🇪 DE ": "🇩🇪"} {
		if got := NodePrefix(in); got != want {
			t.Errorf("NodePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// The servers go in the order of the nodes (the admin's order), whatever their ids and the
// order of the inbounds are; the inbounds of one node keep their own, and with several
// nodes a name of a preset gets the node's flag.
func TestProxiesFollowTheNodes(t *testing.T) {
	hosts := map[int64]string{1: "nl.example.com", 2: "us.example.com", 3: "de.example.com"}
	names := map[int64]string{1: "🇳🇱 Нидерланды", 2: "🇺🇸 США", 3: "🇩🇪 Германия"}
	var ins []model.Inbound
	// Inbounds come by node id, as the database lists them.
	for id := int64(1); id <= 3; id++ {
		ins = append(ins, inbounds(id, id*100)...)
	}
	for _, order := range [][]int64{{1, 2, 3}, {3, 1, 2}, {2, 3, 1}} {
		var nodes []model.Node
		var want []string
		for _, id := range order {
			nodes = append(nodes, model.Node{ID: id, Name: names[id], Endpoint: model.Endpoint{Host: hosts[id]}})
			flag := NodePrefix(names[id])
			want = append(want, hosts[id]+" "+flag+" Hysteria2", hosts[id]+" "+flag+" TUIC")
		}
		var got []string
		for _, p := range Proxies(Profile{Keys: testKeys, Inbounds: ins, Nodes: nodes}) {
			got = append(got, p.Server+" "+p.Name)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("proxies for %v:\n%v\nwant\n%v", order, got, want)
		}
	}
}

// Names the admin typed are kept as they are and preset names get the node's flag; a name
// taken already gets a number, since mihomo refuses two proxies of one name.
func TestProxyNames(t *testing.T) {
	nodes := []model.Node{{ID: 1, Name: "🇳🇱 Нидерланды", Endpoint: model.Endpoint{Host: "nl.example.com"}},
		{ID: 2, Name: "🇺🇸 США", Endpoint: model.Endpoint{Host: "us.example.com"}}}
	tuic := proto.Marshal(proto.Template{"type": "tuic", "alpn": []any{"h3"}})
	ins := []model.Inbound{
		{ID: 1, Node: 1, Name: "Fast", Named: true, Port: 443, PortSpec: "443", Config: tuic},
		{ID: 2, Node: 2, Name: "Fast", Named: true, Port: 443, PortSpec: "443", Config: tuic},
		{ID: 3, Node: 2, Name: "TUIC", Port: 8443, PortSpec: "8443", Config: tuic},
	}
	var got []string
	for _, p := range Proxies(Profile{Keys: testKeys, Inbounds: ins, Nodes: nodes}) {
		got = append(got, p.Name)
	}
	if want := []string{"Fast", "Fast 2", "🇺🇸 TUIC"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names %q, want %q", got, want)
	}
}

// A name with control characters, written to the database past the panel, loses them: the
// YAML encoder fails on a name that starts with a line break, and so would the profile.
// One with nothing else in it goes by the inbound's name in the panel.
func TestProxyNamesDropControlCharacters(t *testing.T) {
	nodes := []model.Node{{ID: 1, Name: "🇳🇱 Нидерланды", Endpoint: model.Endpoint{Host: "nl.example.com"}}}
	tuic := proto.Marshal(proto.Template{"type": "tuic", "alpn": []any{"h3"}})
	ins := []model.Inbound{
		{ID: 1, Node: 1, Name: "\nА", Named: true, Port: 443, PortSpec: "443", Config: tuic},
		{ID: 2, Node: 1, Name: "Б\r\n\tВ\u0085", Named: true, Port: 8443, PortSpec: "8443", Config: tuic},
		{ID: 3, Node: 1, Label: "tuic-2", Name: "\n\t", Named: true, Port: 9443, PortSpec: "9443", Config: tuic},
	}
	var got []string
	for _, p := range Proxies(Profile{Keys: testKeys, Inbounds: ins, Nodes: nodes}) {
		got = append(got, p.Name)
	}
	if want := []string{"А", "БВ", "tuic-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names %q, want %q", got, want)
	}
}

// One inbound that cannot be rendered is left out and reported, not the end of everyone's
// subscription.
func TestBadInboundIsSkippedAndReported(t *testing.T) {
	ins := inbounds(1, 1)
	want := len(ins)
	bad := ins[0]
	bad.ID, bad.Label, bad.Port, bad.PortSpec = 99, "broken-port", 0, "not-a-port"
	broken := ins[1]
	broken.ID, broken.Label, broken.Config = 98, "broken-config", "type: [unclosed"
	// It parses, but proto.Validate knows no such type.
	unknown := ins[1]
	unknown.ID, unknown.Label, unknown.Config = 97, "unknown-type", "type: socks\n"
	ins = append(ins, bad, broken, unknown)
	var skipped []string
	p := Profile{Keys: testKeys, Inbounds: ins, Nodes: []model.Node{{ID: 1, Endpoint: model.Endpoint{Host: "203.0.113.7"}}},
		Skip: func(in model.Inbound, err error) { skipped = append(skipped, in.Label+": "+err.Error()) }}
	if got := len(Proxies(p)); got != want {
		t.Fatalf("%d proxies, want the %d good ones", got, want)
	}
	if len(skipped) != 3 || skipped[0] != `broken-port: bad port "not-a-port"` || !strings.HasPrefix(skipped[1], "broken-config: ") ||
		!strings.HasPrefix(skipped[2], "unknown-type: ") {
		t.Fatalf("skipped: %v", skipped)
	}
	// Without a listener nothing is reported and nothing panics.
	p.Skip = nil
	if got := len(Proxies(p)); got != want {
		t.Fatalf("without a listener: %d", got)
	}
}

// Behind the node's SNI router an inbound marked for it is reached on 443.
func TestSNIRouterPort(t *testing.T) {
	tpl := proto.Template{"type": "anytls", "mikan": map[string]any{"sni443": true}}
	in := model.Inbound{ID: 1, Node: 1, Name: "AnyTLS", Port: 2083, PortSpec: "2083", Config: proto.Marshal(tpl)}
	for router, port := range map[bool]int{true: 443, false: 2083} {
		ps := Proxies(Profile{Keys: testKeys, Inbounds: []model.Inbound{in}, Nodes: []model.Node{{ID: 1, Endpoint: model.Endpoint{Host: "203.0.113.7", SNIRouter: router}}}})
		if len(ps) != 1 || ps[0].Port != port {
			t.Fatalf("router %v: %+v", router, ps)
		}
	}
}

func TestParsedTemplatesAreKeptAndCopied(t *testing.T) {
	src := proto.Marshal(proto.Template{"type": "vless", "reality-config": map[string]any{"dest": "www.example.com:443"}})
	a, err := parseTemplate(src)
	if err != nil {
		t.Fatal(err)
	}
	a["type"] = "changed"
	a["reality-config"].(map[string]any)["dest"] = "evil.example:443"
	b, err := parseTemplate(src)
	if err != nil {
		t.Fatal(err)
	}
	if b.Type() == "changed" || b["reality-config"].(map[string]any)["dest"] == "evil.example:443" {
		t.Fatal("a caller's change reached the kept template")
	}
	if _, err := parseTemplate("type: [unclosed"); err == nil {
		t.Fatal("a broken template parses")
	}
	if _, err := parseTemplate("type: [unclosed"); err == nil {
		t.Fatal("and keeps failing")
	}
}

// A key shared by everyone reaches only users whose access is on: the node cannot cut
// anyone off on such an inbound.
func TestForApp(t *testing.T) {
	ins := []model.Inbound{
		{ID: 1, Config: "type: tuic\n"},
		{ID: 2, Config: "type: shadowsocks\ncipher: 2022-blake3-chacha20-poly1305\npassword: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"},
		{ID: 3, Config: "type: trusttunnel\n"},
		// With an AES method every user has a key of its own: no longer shared.
		{ID: 4, Config: "type: shadowsocks\ncipher: 2022-blake3-aes-128-gcm\npassword: AAAAAAAAAAAAAAAAAAAAAA==\n"},
		// A template that no longer parses is kept for Proxies, which reports it.
		{ID: 5, Config: "type: [unclosed"},
	}
	ids := func(list []model.Inbound) []int64 {
		out := []int64{}
		for _, in := range list {
			out = append(out, in.ID)
		}
		return out
	}
	for _, c := range []struct {
		ua     string
		active bool
		want   []int64
	}{
		{"mihomo/1.19.31", true, []int64{1, 2, 3, 4, 5}},
		{"mihomo/1.19.31", false, []int64{1, 3, 4, 5}},
		{"Happ/3.4.1", true, []int64{2, 4, 5}},
		{"Happ/3.4.1", false, []int64{4, 5}},
	} {
		if got := ids(ForApp(ins, apps.DetectApp(c.ua), c.active)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s, active %v: %v, want %v", c.ua, c.active, got, c.want)
		}
	}
}
