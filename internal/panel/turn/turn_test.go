package turn

import (
	"strings"
	"testing"

	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// inbound is a saved inbound of a node with a generated config of the given preset.
func inbound(t *testing.T, id int64, preset, port, listen string) db.Inbound {
	t.Helper()
	cfg, err := presets.NewConfig(preset, "")
	if err != nil {
		t.Fatalf("preset %s: %v", preset, err)
	}
	return db.Inbound{ID: id, NodeID: 2, Name: preset, Preset: preset, Port: port, Listen: listen, Config: cfg}
}

func good() Config {
	return Config{Enabled: true, InboundID: 7, ListenPort: "56000", ClientHost: "127.0.0.1",
		ClientPort: "9000", Streams: DefaultStreams, UDP: true,
		Links: []string{"https://vk.com/call/join/abc"}}
}

func TestValidate(t *testing.T) {
	if err := good().Validate(); err != nil {
		t.Fatalf("a good config was refused: %v", err)
	}
	cases := map[string]func(*Config){
		"empty listen port":  func(c *Config) { c.ListenPort = "" },
		"listen port 70000":  func(c *Config) { c.ListenPort = "70000" },
		"no client host":     func(c *Config) { c.ClientHost = "" },
		"client host as URL": func(c *Config) { c.ClientHost = "127.0.0.1:9000" },
		"client port 0":      func(c *Config) { c.ClientPort = "0" },
		"no streams":         func(c *Config) { c.Streams = 0 },
		"too many streams":   func(c *Config) { c.Streams = MaxStreams + 1 },
		"a bare word as link": func(c *Config) {
			c.Links = []string{"not-a-link"}
		},
	}
	for name, breakIt := range cases {
		c := good()
		breakIt(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// An IPv6 client host is fine; the address is dialed by the phone itself.
	c := good()
	c.ClientHost = "::1"
	if err := c.Validate(); err != nil {
		t.Fatalf("::1 refused: %v", err)
	}
}

func TestServerCarriesConnectionsOrDatagrams(t *testing.T) {
	c := good()
	tcp := inbound(t, 7, "vless_reality_xhttp", "443", "")
	srv, err := c.Server(tcp)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Listen != "0.0.0.0:56000" || srv.Target != "127.0.0.1:443" || !srv.VLESS {
		t.Fatalf("a TCP inbound: %+v", srv)
	}
	udp := inbound(t, 7, "hysteria2", "443", "")
	srv, err = c.Server(udp)
	if err != nil {
		t.Fatal(err)
	}
	if srv.VLESS {
		t.Fatalf("a UDP inbound must carry datagrams: %+v", srv)
	}
}

func TestServerSpeaksToTheInboundsOwnAddress(t *testing.T) {
	c := good()
	in := inbound(t, 7, "vless_reality_xhttp", "8443-8450", "10.0.0.5")
	srv, err := c.Server(in)
	if err != nil {
		t.Fatal(err)
	}
	// The low end of a range, and the address the inbound really listens on.
	if srv.Target != "10.0.0.5:8443" {
		t.Fatalf("target = %q", srv.Target)
	}
}

func TestNameAndMode(t *testing.T) {
	c := good()
	if got := c.Name("Германия (XHTTP)"); got != "Германия (XHTTP) WL" {
		t.Fatalf("name = %q", got)
	}
	c.Title = "Германия (XHTTP WL)"
	if got := c.Name("Германия (XHTTP)"); got != "Германия (XHTTP WL)" {
		t.Fatalf("the admin's title must be used as is, got %q", got)
	}
	if mode, err := c.Mode(inbound(t, 7, "vless_reality_xhttp", "443", "")); err != nil || mode != "vless" {
		t.Fatalf("mode = %q, %v", mode, err)
	}
	if mode, err := c.Mode(inbound(t, 7, "tuic_v5", "8443", "")); err != nil || mode != "udp" {
		t.Fatalf("mode = %q, %v", mode, err)
	}
}

func TestLinkSpreadsAndSticks(t *testing.T) {
	c := good()
	c.Links = []string{"https://vk.com/call/join/a", "https://vk.com/call/join/b", "https://vk.com/call/join/c"}
	seen := map[string]bool{}
	for id := int64(1); id <= 40; id++ {
		l := c.Link(id)
		if l == "" {
			t.Fatalf("user %d got no link", id)
		}
		if l != c.Link(id) {
			t.Fatalf("user %d got different links on two calls", id)
		}
		seen[l] = true
	}
	if len(seen) != 3 {
		t.Fatalf("the list is not spread over: %v", seen)
	}
	// A panel with one link gives it to everybody; with none, the entry has no link.
	c.Links = nil
	if got := c.Link(3); got != "" {
		t.Fatalf("no links must give none, got %q", got)
	}
}

func TestDirectDropsWhateverStandsInFront(t *testing.T) {
	in := inbound(t, 7, "vless_reality_xhttp", "443", "")
	parsed, err := proto.Parse(in.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.SetClientEndpoint(parsed, "cdn.example.com", 8443, ""); err != nil {
		t.Fatal(err)
	}
	direct, err := Direct(parsed)
	if err != nil {
		t.Fatal(err)
	}
	c, err := proto.ClientConfig(direct, proto.ClientInput{Name: "Германия (XHTTP) WL", Host: "127.0.0.1", Port: 9000,
		PortSpec: "9000", Slot: proto.Slot{Name: "u", UUID: "6f9c0e0a-6c2a-4b7e-9f3a-0f1c2d3e4f50"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Mihomo["server"]; got != "127.0.0.1" {
		t.Fatalf("server = %v", got)
	}
	if !strings.Contains(c.URI, "@127.0.0.1:9000") {
		t.Fatalf("the share link still points elsewhere: %s", c.URI)
	}
	// The same inbound without the override keeps working as before.
	plain, err := Direct(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Ext().Client.Server != "" {
		t.Fatalf("the override survived: %q", plain.Ext().Client.Server)
	}
}

func TestLinksRoundTrip(t *testing.T) {
	raw := "https://vk.com/call/join/a\n\n  # a comment\nhttps://vk.com/call/join/b  \n"
	links := splitLinks(raw)
	if len(links) != 2 || links[0] != "https://vk.com/call/join/a" || links[1] != "https://vk.com/call/join/b" {
		t.Fatalf("split = %q", links)
	}
	if got := JoinLinks(links); got != "https://vk.com/call/join/a\nhttps://vk.com/call/join/b" {
		t.Fatalf("join = %q", got)
	}
	if got := JoinLinks([]string{" ", "https://vk.com/call/join/c"}); got != "https://vk.com/call/join/c" {
		t.Fatalf("join with blanks = %q", got)
	}
}

func TestFromRow(t *testing.T) {
	c := FromRow(db.NodeTurn{Enabled: 1, InboundID: 9, ListenPort: "56000", ClientHost: "127.0.0.1",
		ClientPort: "9001", Streams: 4, Udp: 0, Title: " WL", VkLinks: "https://vk.com/call/join/x"})
	if !c.Enabled || c.InboundID != 9 || c.Streams != 4 || c.UDP || len(c.Links) != 1 {
		t.Fatalf("from row: %+v", c)
	}
}
