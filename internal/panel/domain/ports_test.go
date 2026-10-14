package domain

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The node cannot open ports in the host's firewall: the installer opens the pool ahead.
func TestInstallerOpensThePool(t *testing.T) {
	raw, err := os.ReadFile("../../../installer/src/host.rs")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`POOL: \[u16; \d+\] = \[([0-9, ]+)\]`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("the installer has no POOL")
	}
	var want []string
	for _, p := range PortPool {
		want = append(want, strconv.Itoa(p))
	}
	if got := strings.Fields(strings.ReplaceAll(string(m[1]), ",", " ")); !slices.Equal(got, want) {
		t.Fatalf("the installer opens %v, the pool is %v", got, want)
	}
}

// A map like a node's: XHTTP 443/tcp, Hysteria2 hopping over 20000-30000/udp, TUIC
// 8443/udp, a disabled Trojan on 2087/tcp, the relay on 2053/tcp and the panel on 21355.
func testPorts() PortMap {
	var m PortMap
	m.add(PortHolder{Kind: PortPanel}, "21355", "tcp", false)
	m.add(PortHolder{Kind: PortRelay}, "2053", "tcp", false)
	m.add(PortHolder{Kind: PortInbound, Name: "vless-xhttp", ID: 1}, "443", "tcp", false)
	m.add(PortHolder{Kind: PortInbound, Name: "hysteria2", ID: 2}, "20000-30000", "udp", false)
	m.add(PortHolder{Kind: PortInbound, Name: "tuic", ID: 3}, "8443", "udp", false)
	m.add(PortHolder{Kind: PortInbound, Name: "off", ID: 4}, "2087", "tcp", true)
	return m
}

func TestPortMapBusy(t *testing.T) {
	m := testPorts()
	hy := PortHolder{Kind: PortInbound, Name: "hysteria2", ID: 2}
	for _, c := range []struct {
		name, spec, network string
		self                PortHolder
		want                string // the holder's kind or inbound name; "" free
	}{
		{"same port and network", "443", "tcp", PortHolder{}, "vless-xhttp"},
		{"same port, other network", "443", "udp", PortHolder{}, ""},
		{"its own port", "443", "tcp", PortHolder{Kind: PortInbound, ID: 1}, ""},
		{"another inbound with the same id is not it", "2053", "tcp", PortHolder{Kind: PortInbound, ID: 0}, PortRelay},
		{"the relay", "2053", "tcp", PortHolder{}, PortRelay},
		{"the relay is TCP only", "2053", "udp", PortHolder{}, ""},
		{"the panel", "21355", "tcp", PortHolder{}, PortPanel},
		{"a disabled inbound holds nothing", "2087", "tcp", PortHolder{}, ""},
		{"a port inside a range", "25000", "udp", PortHolder{}, "hysteria2"},
		{"a range's first port", "20000", "udp", PortHolder{}, "hysteria2"},
		{"a range's last port", "30000", "udp", PortHolder{}, "hysteria2"},
		{"next to a range", "30001", "udp", PortHolder{}, ""},
		{"a range over a port", "8000-9000", "udp", PortHolder{}, "tuic"},
		{"a range over a TCP port", "400-500", "tcp", PortHolder{}, "vless-xhttp"},
		{"a range over a range's start", "19000-20000", "udp", PortHolder{}, "hysteria2"},
		{"a range inside a range", "21000-22000", "udp", PortHolder{}, "hysteria2"},
		{"a range around a range", "10000-40000", "udp", PortHolder{}, "hysteria2"},
		{"a range past a range", "30001-31000", "udp", PortHolder{}, ""},
		{"a range moving over its own", "25000-35000", "udp", hy, ""},
		{"not a port", "0", "tcp", PortHolder{}, ""},
	} {
		h, busy := m.Busy(c.spec, c.network, c.self)
		got := ""
		if busy {
			got = h.Kind
			if h.Kind == PortInbound {
				got = h.Name
			}
		}
		if got != c.want {
			t.Errorf("%s: %s/%s held by %q, want %q", c.name, c.spec, c.network, got, c.want)
		}
	}
}

func TestPortMapFree(t *testing.T) {
	m := testPorts()
	for _, c := range []struct {
		port    int
		network string
		want    bool
	}{
		{2083, "tcp", true},
		{2053, "tcp", false}, // the relay
		{2087, "tcp", false}, // a disabled inbound may come back on
		{2087, "udp", true},
		{25000, "udp", false}, // inside the hopping range
		{25000, "tcp", true},
		{22, "tcp", false}, // SSH is never picked
		{21355, "tcp", false},
	} {
		if got := m.Free(c.port, c.network); got != c.want {
			t.Errorf("%d/%s free: %v, want %v", c.port, c.network, got, c.want)
		}
	}
}

// What NodePorts reads: the panel's and the subscription port on the panel's node, the
// node API's port on a remote one, the relay and the inbounds of that node only.
func TestNodePorts(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	set := settings.New(st.Q)
	for k, v := range map[string]int{settings.KeyPanelPort: 21355, settings.KeySubPort: 2096} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	remote, _, err := AddNode(ctx, st, panel, NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRelay(ctx, st.Q, remote, now, nil); err != nil {
		t.Fatal(err)
	}
	relay, err := st.Q.GetNodeRelay(ctx, remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	local, err := st.Q.GetNode(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	own, err := NodePorts(ctx, st.Q, local)
	if err != nil {
		t.Fatal(err)
	}
	for spec, want := range map[string]string{"21355": PortPanel, "2096": PortSub, "40000": "", relay.Port: ""} {
		if h, _ := own.Busy(spec, "tcp", PortHolder{}); h.Kind != want {
			t.Errorf("own node %s: %q, want %q", spec, h.Kind, want)
		}
	}
	if h, _ := own.Busy("443", "tcp", PortHolder{}); h.Name != "vless-xhttp" {
		t.Errorf("own node 443: %+v", h)
	}
	b, err := NodePorts(ctx, st.Q, remote)
	if err != nil {
		t.Fatal(err)
	}
	for spec, want := range map[string]string{"21355": "", "2096": "", "40000": PortNodeAPI, relay.Port: PortRelay} {
		if h, _ := b.Busy(spec, "tcp", PortHolder{}); h.Kind != want {
			t.Errorf("node B %s: %q, want %q", spec, h.Kind, want)
		}
	}
	// A read that fails is an error, never a free port.
	st.Close()
	if _, err := NodePorts(ctx, st.Q, remote); err == nil {
		t.Fatal("a closed database gave a port map")
	}
	var busy *PortInUseError
	if err := CheckPort(ctx, st.Q, remote, "2083", "tcp", PortHolder{}); err == nil || errors.As(err, &busy) {
		t.Fatalf("a closed database: %v", err)
	}
}

// The relay and the hopping ranges count for inbounds the CLI and the automatic moves add
// and move too: they go through the same service, without a node to ask.
func TestPortsWithoutTheNode(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, s := inbounds(t, &now, nil)
	ctx := context.Background()
	local, err := st.Q.GetNode(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := EnsureRelay(ctx, st.Q, local, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	var busy *PortInUseError
	if _, err := s.Create(ctx, NewInbound{Preset: "trojan_reality", Port: relay.Port}); !errors.As(err, &busy) || busy.Kind != PortRelay {
		t.Fatalf("add on the relay's port: %v", err)
	}
	if _, _, err := s.Update(ctx, find(t, s, 1, "vless-xhttp").ID, InboundPatch{Port: &relay.Port}); !errors.As(err, &busy) || busy.Kind != PortRelay {
		t.Fatalf("move to the relay's port: %v", err)
	}
	config, err := presets.NewConfig("hysteria2", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q.CreateInbound(ctx, db.CreateInboundParams{NodeID: 1, Name: "hopping", Preset: "hysteria2", Port: "20000-30000", Config: config,
		CreatedAt: now.Unix(), UpdatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Update(ctx, find(t, s, 1, "tuic").ID, InboundPatch{Port: ptr("25000")}); !errors.As(err, &busy) || busy.Name != "hopping" {
		t.Fatalf("move into a hopping range: %v", err)
	}
	if _, err := s.Create(ctx, NewInbound{Preset: "tuic_v5", Port: "29000-31000"}); !errors.As(err, &busy) || busy.Name != "hopping" {
		t.Fatalf("a range over a hopping range: %v", err)
	}
}

// What the node's server holds is not for an automatic pick: a foreign Xray on 2083/tcp
// keeps the relay and a moved inbound off it, over that network only. Busy, which refuses
// the admin's choice, still names only what mikan knows, so an inbound keeps its own port
// although the node reports it listening too.
func TestPortMapFreeSkipsWhatTheServerHolds(t *testing.T) {
	host := &nodeapi.HostPorts{TCP: []int{443, 2053, 2083, 8443}, UDP: []int{2096, 8443}}
	m := testPorts().WithHost(host)
	for _, c := range []struct {
		port    int
		network string
		want    bool
	}{
		{2083, "tcp", false}, // a foreign program
		{2083, "udp", true},
		{2096, "udp", false},
		{2096, "tcp", true},
		{8443, "tcp", false},
		{2087, "tcp", false}, // a disabled inbound's, as before
		{4443, "tcp", true},
		{443, "tcp", false}, // mikan's own, listening: not free either
		{2053, "tcp", false},
	} {
		if got := m.Free(c.port, c.network); got != c.want {
			t.Errorf("%d/%s free: %v, want %v", c.port, c.network, got, c.want)
		}
	}
	xhttp := PortHolder{Kind: PortInbound, Name: "vless-xhttp", ID: 1}
	if _, busy := m.Busy("443", "tcp", xhttp); busy {
		t.Error("an inbound's own port, which the node reports listening, is busy for it")
	}
	if _, busy := m.Busy("2083", "tcp", PortHolder{}); busy {
		t.Error("a foreign program's port is named a holder of mikan's")
	}
	if h, _ := m.Busy("2053", "tcp", PortHolder{}); h.Kind != PortRelay {
		t.Errorf("the relay's port, reported listening: %+v", h)
	}
	// A node that said nothing: the map as it was.
	if !testPorts().WithHost(nil).Free(2083, "tcp") {
		t.Error("an unknown host holds ports")
	}
}

// Every preset on one node, each on its default port, still leaves autotune a TCP port of
// the pool to move a blocked inbound to.
func TestPresetsLeaveThePoolAPort(t *testing.T) {
	taken := map[string]bool{}
	for _, p := range presets.All {
		if p.Network == "tcp" && p.Port != "" {
			taken[p.Port] = true
		}
	}
	free := 0
	for _, p := range PortPool {
		if !taken[strconv.Itoa(p)] {
			free++
		}
	}
	if free == 0 {
		t.Fatalf("the presets' default TCP ports fill the whole pool %v", PortPool)
	}
}

// Picks stay in PortPool; past it only with any port allowed and the pool full, and then
// on the spare range.
func TestPickPorts(t *testing.T) {
	var m PortMap
	for _, p := range PortPool {
		m.add(PortHolder{Kind: PortInbound, Name: strconv.Itoa(p), ID: int64(p)}, strconv.Itoa(p), "tcp", false)
	}
	if got := m.PickPorts("tcp", false, nil); len(got) != 0 {
		t.Fatalf("a full pool without any port: %v", got)
	}
	got := m.PickPorts("tcp", true, map[string]bool{strconv.Itoa(SpareLo): true})
	if len(got) != maxSpare || got[0] != strconv.Itoa(SpareLo+1) {
		t.Fatalf("spare ports: %d, first %v", len(got), got[:1])
	}
	if got := m.PickPorts("udp", true, nil); len(got) != len(PortPool) || got[0] != strconv.Itoa(PortPool[0]) {
		t.Fatalf("the pool comes first while it has room: %v", got)
	}
}
