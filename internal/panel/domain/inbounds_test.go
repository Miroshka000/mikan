package domain

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// inbounds is the service over the seeded store: node 1 with vless-xhttp 443/tcp,
// hysteria2 443/udp, tuic 8443/udp and vless-vision 8443/tcp.
func inbounds(t *testing.T, now *time.Time, dry DryRun) (*store.Store, *Inbounds) {
	t.Helper()
	st, _, _ := setup(t, now)
	return st, NewInbounds(st, dry, func() time.Time { return *now })
}

func find(t *testing.T, s *Inbounds, node int64, name string) db.Inbound {
	t.Helper()
	in, err := s.Find(context.Background(), node, name)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func ptr[T any](v T) *T { return &v }

func TestCreate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, s := inbounds(t, &now, nil)
	ctx := context.Background()
	info, _ := presets.Get("trojan_reality")

	in, err := s.Create(ctx, NewInbound{Preset: "trojan_reality"})
	if err != nil {
		t.Fatal(err)
	}
	if in.NodeID != 1 || in.Port != info.Port || in.Name != info.Name || in.Config == "" || in.Enabled == 0 || in.UpdatedAt != now.Unix() {
		t.Fatalf("inbound: %+v", in)
	}
	// The preset's port is taken now: a port the admin names is refused, and with none
	// named the next inbound takes the first free port of the pool.
	var busy *PortInUseError
	if _, err := s.Create(ctx, NewInbound{Preset: "trojan_reality", Port: info.Port}); !errors.As(err, &busy) || busy.Name != info.Name {
		t.Fatalf("the preset's port is taken now: %v", err)
	}
	moved, err := s.Create(ctx, NewInbound{Preset: "trojan_reality"})
	if err != nil || moved.Port != strconv.Itoa(PortPool[0]) || moved.Name != info.Name+"-2" {
		t.Fatalf("second inbound on the preset's port: %+v %v", moved, err)
	}
	second, err := s.Create(ctx, NewInbound{NodeID: 1, Preset: "trojan_reality", Port: "20000"})
	if err != nil || second.Name != info.Name+"-3" {
		t.Fatalf("third inbound: %+v %v", second, err)
	}
	// TCP and UDP listeners share a port number without conflict.
	if _, err := s.Create(ctx, NewInbound{Preset: "tuic_v5", Port: "20000"}); err != nil {
		t.Fatalf("udp next to tcp: %v", err)
	}
	custom, err := s.Create(ctx, NewInbound{Preset: presets.Custom, Port: "30000", Config: "type: anytls\n"})
	if err != nil || custom.Name != "anytls" {
		t.Fatalf("a custom template is named by its type: %+v %v", custom, err)
	}
	var pe *proto.Error
	if _, err := s.Create(ctx, NewInbound{Preset: presets.Custom, Port: "30001", Config: "type: nope\n"}); !errors.As(err, &pe) {
		t.Fatalf("a broken template: %v", err)
	}
	// Clients would talk plain TLS to the masking site: REALITY has no WebSocket.
	if _, err := s.Create(ctx, NewInbound{Preset: presets.Custom, Port: "30002", Config: in.Config + "\nws-path: /ws\n"}); !errors.As(err, &pe) || pe.Code != "config_ws_reality" {
		t.Fatalf("ws over reality: %v", err)
	}
	for _, c := range []struct {
		in   NewInbound
		want error
	}{
		{NewInbound{Preset: "nope"}, ErrUnknownPreset},
		{NewInbound{Preset: "anytls", NodeID: 9}, ErrUnknownNode},
		{NewInbound{Preset: "anytls", Port: "70000"}, ErrBadPort},
		{NewInbound{Preset: "anytls", Port: "2083-2000"}, ErrBadPort},
	} {
		if _, err := s.Create(ctx, c.in); !errors.Is(err, c.want) {
			t.Errorf("%+v: got %v, want %v", c.in, err, c.want)
		}
	}
}

// An automatic move names the port it moves from: a row moved meanwhile keeps its port.
func TestUpdateFromPort(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, s := inbounds(t, &now, nil)
	ctx := context.Background()
	x := find(t, s, 1, "vless-xhttp")
	if _, _, err := s.Update(ctx, x.ID, InboundPatch{Port: ptr("2053"), FromPort: ptr("8443")}); !errors.Is(err, ErrInboundChanged) {
		t.Fatalf("moved from a port it is not on: %v", err)
	}
	if got := find(t, s, 1, "vless-xhttp").Port; got != "443" {
		t.Fatalf("port %s", got)
	}
	if _, next, err := s.Update(ctx, x.ID, InboundPatch{Port: ptr("2053"), FromPort: ptr("443")}); err != nil || next.Port != "2053" {
		t.Fatalf("move: %+v %v", next, err)
	}
}

func TestUpdatePort(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, s := inbounds(t, &now, nil)
	ctx := context.Background()
	x := find(t, s, 1, "vless-xhttp")

	now = now.Add(time.Hour)
	prev, next, err := s.Update(ctx, x.ID, InboundPatch{Port: ptr("2443")})
	if err != nil {
		t.Fatal(err)
	}
	if prev != x || next.Port != "2443" || next.Config != x.Config || next.Enabled != x.Enabled || next.UpdatedAt != now.Unix() {
		t.Fatalf("moved: %+v -> %+v", prev, next)
	}
	// TCP and UDP listeners share a port number without conflict.
	if _, next, err := s.Update(ctx, find(t, s, 1, "hysteria2").ID, InboundPatch{Port: ptr("2443")}); err != nil || next.Port != "2443" {
		t.Fatalf("udp next to tcp: %+v %v", next, err)
	}
	var busy *PortInUseError
	if _, _, err := s.Update(ctx, find(t, s, 1, "tuic").ID, InboundPatch{Port: ptr("2443")}); !errors.As(err, &busy) || busy.Name != "hysteria2" {
		t.Fatalf("udp 2443 is taken by hysteria2: %v", err)
	}
	vision := find(t, s, 1, "vless-vision")
	if _, _, err := s.Update(ctx, vision.ID, InboundPatch{Port: ptr("2443")}); !errors.As(err, &busy) || busy.Name != "vless-xhttp" {
		t.Fatalf("tcp 2443 is taken by vless-xhttp: %v", err)
	}
	// A disabled inbound holds no port, and is checked when it comes back on.
	if _, _, err := s.Update(ctx, vision.ID, InboundPatch{Enabled: ptr(false), Port: ptr("2443")}); err != nil {
		t.Fatalf("a disabled inbound moves anywhere: %v", err)
	}
	if _, _, err := s.Update(ctx, vision.ID, InboundPatch{Enabled: ptr(true)}); !errors.As(err, &busy) || busy.Name != "vless-xhttp" {
		t.Fatalf("back on over vless-xhttp: %v", err)
	}
	// A name alone does not ask about the port.
	if _, _, err := s.Update(ctx, vision.ID, InboundPatch{DisplayName: ptr("Vision")}); err != nil {
		t.Fatalf("rename a disabled inbound: %v", err)
	}

	// Ports and names are per node: node 2 has its own vless-xhttp.
	n, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "🇺🇸 США", Address: "203.0.113.7:25305", PublicHost: "203.0.113.7", CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := s.Create(ctx, NewInbound{NodeID: n.ID, Preset: "vless_reality_xhttp", Port: "443"})
	if err != nil {
		t.Fatal(err)
	}
	if _, next, err := s.Update(ctx, remote.ID, InboundPatch{Port: ptr("2443")}); err != nil || next.Port != "2443" {
		t.Fatalf("node 2 inbound: %+v %v", next, err)
	}
	if _, _, err := s.Update(ctx, remote.ID, InboundPatch{Port: ptr("25305")}); !errors.As(err, &busy) || busy.Kind != PortNodeAPI {
		t.Fatalf("node 2's API port: %v", err)
	}

	for _, c := range []struct {
		id   int64
		port string
		want error
	}{
		{999, "3000", ErrUnknownInbound},
		{x.ID, "0", ErrBadPort},
		{x.ID, "70000", ErrBadPort},
		{x.ID, "", ErrBadPort},
	} {
		if _, _, err := s.Update(ctx, c.id, InboundPatch{Port: &c.port}); !errors.Is(err, c.want) {
			t.Errorf("%d %q: got %v, want %v", c.id, c.port, err, c.want)
		}
	}
	if _, err := s.Find(ctx, 9, "vless-xhttp"); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("find on no node: %v", err)
	}
	if _, err := s.Find(ctx, 1, "nope"); !errors.Is(err, ErrUnknownInbound) {
		t.Fatalf("find no inbound: %v", err)
	}
}

// The installer and the admin point REALITY inbounds at a site next to the server; the
// panel's own HTTPS (self-steal) is for the panel's own node only.
func TestUpdateTarget(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, s := inbounds(t, &now, nil)
	ctx := context.Background()
	if err := settings.Set(ctx, settings.New(st.Q), settings.KeyPanelPort, 21355); err != nil {
		t.Fatal(err)
	}
	x := find(t, s, 1, "vless-xhttp")
	prev, next, err := s.Update(ctx, x.ID, InboundPatch{Dest: ptr(" 203.0.113.20:443 "), ServerName: ptr("www.example.org")})
	if err != nil {
		t.Fatal(err)
	}
	tpl, _ := proto.Parse(next.Config)
	dest, names := presets.Dest(tpl)
	if dest != "203.0.113.20:443" || len(names) != 1 || names[0] != "www.example.org" || next.Port != prev.Port {
		t.Fatalf("retargeted: %s %v %+v", dest, names, next)
	}
	// The keys stay: clients keep working after they refresh the subscription.
	old, _ := proto.Parse(prev.Config)
	if old["reality-config"].(map[string]any)["private-key"] != tpl["reality-config"].(map[string]any)["private-key"] {
		t.Fatal("the REALITY key changed")
	}
	if _, _, err := s.Update(ctx, find(t, s, 1, "vless-vision").ID, InboundPatch{Dest: ptr("127.0.0.1:21355"), ServerName: ptr("vpn.example.com")}); err != nil {
		t.Fatalf("self-steal on the panel's node: %v", err)
	}

	n, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "🇺🇸 США", Address: "203.0.113.7:25305", PublicHost: "203.0.113.7", CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	trojan, err := s.Create(ctx, NewInbound{NodeID: n.ID, Preset: "trojan_reality", Port: "2087"})
	if err != nil {
		t.Fatal(err)
	}
	var pe *proto.Error
	var edit *EditError
	if _, _, err := s.Update(ctx, trojan.ID, InboundPatch{Dest: ptr("127.0.0.1:21355"), ServerName: ptr("vpn.example.com")}); !errors.As(err, &pe) || errors.As(err, &edit) {
		t.Fatalf("a remote node cannot borrow the panel's HTTPS: %v", err)
	}
	for _, c := range []struct {
		name string
		p    InboundPatch
		code string
	}{
		{"vless-xhttp", InboundPatch{Dest: ptr("203.0.113.20:443")}, "reality_sni"},
		{"vless-xhttp", InboundPatch{Dest: ptr("nope")}, "dest_format"},
		{"hysteria2", InboundPatch{Dest: ptr("www.example.org:443")}, "dest_no_reality"},
		{"hysteria2", InboundPatch{Fingerprint: ptr("chrome")}, "fingerprint_no_tls"},
	} {
		in := find(t, s, 1, c.name)
		if _, _, err := s.Update(ctx, in.ID, c.p); !errors.As(err, &edit) || edit.Err.Code != c.code {
			t.Errorf("%s %+v: %v, want %s", c.name, c.p, err, c.code)
		}
	}
}

// The rules of one Update: every field is checked before anything is written, and a
// change refused on any field leaves the inbound and the exit node as they were.
func TestUpdateAllOrNothing(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, s := inbounds(t, &now, nil)
	ctx := context.Background()
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	b, _, err := AddNode(ctx, st, panel, NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, now)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.Q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	x, vision := find(t, s, 1, "vless-xhttp"), find(t, s, 1, "vless-vision")
	var busy *PortInUseError
	var name *NameInUseError
	for _, c := range []struct {
		name string
		p    InboundPatch
		is   func(error) bool
	}{
		{"busy port with a pool", InboundPatch{PoolID: &pool.ID, Port: ptr(vision.Port)}, func(err error) bool { return errors.As(err, &busy) }},
		{"busy port with an exit", InboundPatch{Outbound: ptr("node"), ExitNodeID: &b.ID, Port: ptr(vision.Port)}, func(err error) bool { return errors.As(err, &busy) }},
		{"busy port with node-side fields", InboundPatch{Listen: ptr("127.0.0.1"), AutoSNI: ptr(false), Port: ptr(vision.Port)}, func(err error) bool { return errors.As(err, &busy) }},
		{"taken name with an exit", InboundPatch{Outbound: ptr("node"), ExitNodeID: &b.ID, DisplayName: ptr(ProxyName(vision))}, func(err error) bool { return errors.As(err, &name) }},
		{"no pool with a port", InboundPatch{PoolID: ptr(int64(999)), Port: ptr("2443")}, func(err error) bool { return errors.Is(err, ErrUnknownPool) }},
		{"itself as the exit with a pool", InboundPatch{Outbound: ptr("node"), ExitNodeID: ptr(int64(1)), PoolID: &pool.ID}, func(err error) bool { return errors.Is(err, ErrExitSelf) }},
		{"no exit node named", InboundPatch{Outbound: ptr("node"), PoolID: &pool.ID}, func(err error) bool { return errors.Is(err, ErrNotFound) }},
		{"auto port behind a proxy", InboundPatch{Listen: ptr("127.0.0.1"), AutoPort: ptr(true), PoolID: &pool.ID}, func(err error) bool { return errors.Is(err, ErrAutoPortListen) }},
		{"bad listen with a pool", InboundPatch{Listen: ptr("localhost"), PoolID: &pool.ID}, func(err error) bool { return errors.Is(err, ErrBadListen) }},
	} {
		if _, _, err := s.Update(ctx, x.ID, c.p); !c.is(err) {
			t.Errorf("%s: %v", c.name, err)
		}
		if after, err := st.Q.GetInbound(ctx, x.ID); err != nil || after != x {
			t.Errorf("%s changed the inbound:\n%+v\n%+v", c.name, x, after)
		}
		if _, err := st.Q.GetNodeRelay(ctx, b.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("%s gave node B a relay: %v", c.name, err)
		}
	}

	// Only the node's side: clients get nothing new, updated_at stays.
	now = now.Add(time.Hour)
	_, next, err := s.Update(ctx, x.ID, InboundPatch{PoolID: &pool.ID, Outbound: ptr("node"), ExitNodeID: &b.ID, Listen: ptr("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	if next.PoolID.Int64 != pool.ID || next.ExitNodeID.Int64 != b.ID || next.Outbound != "direct" || next.Listen != "127.0.0.1" || next.AutoPort != 0 || next.UpdatedAt != x.UpdatedAt {
		t.Fatalf("node side: %+v", next)
	}
	if _, err := st.Q.GetNodeRelay(ctx, b.ID); err != nil {
		t.Fatalf("node B's relay: %v", err)
	}
	// Back to the main traffic and straight out.
	if _, next, err = s.Update(ctx, x.ID, InboundPatch{PoolID: ptr(int64(0)), Outbound: ptr("direct")}); err != nil || next.PoolID.Valid || next.ExitNodeID.Valid {
		t.Fatalf("back: %+v %v", next, err)
	}
}

// The listen address the admin types: every address, or one IP literal.
func TestParseListen(t *testing.T) {
	for in, want := range map[string]string{"": "", " ": "", "0.0.0.0": "", "::": "", "127.0.0.1": "127.0.0.1", " 10.0.0.5 ": "10.0.0.5",
		"::1": "::1", "2001:DB8::1": "2001:db8::1", "::ffff:127.0.0.1": "127.0.0.1"} {
		if got, err := ParseListen(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"localhost", "127.0.0.1:444", "fe80::1%eth0", "224.0.0.1", "10.0.0.0/8", "0.0.0.0\nlisten: x"} {
		if _, err := ParseListen(bad); !errors.Is(err, ErrBadListen) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
	if ListenPinsPort("") || !ListenPinsPort("127.0.0.1") {
		t.Fatal("only an address of its own pins the port")
	}
}

// One port number per node and network, whatever address the inbounds listen on.
func TestPortTakenOnAnotherAddress(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, s := inbounds(t, &now, nil)
	ctx := context.Background()
	x := find(t, s, 1, "vless-xhttp")
	if _, _, err := s.Update(ctx, x.ID, InboundPatch{Port: ptr("444"), Listen: ptr("127.0.0.1")}); err != nil {
		t.Fatal(err)
	}
	var busy *PortInUseError
	if _, _, err := s.Update(ctx, find(t, s, 1, "vless-vision").ID, InboundPatch{Port: ptr("444")}); !errors.As(err, &busy) || busy.Name != "vless-xhttp" {
		t.Fatalf("tcp 444 is taken by vless-xhttp on 127.0.0.1: %v", err)
	}
}

type fakeDryRun struct {
	err   error
	ports []string
}

func (f *fakeDryRun) Validate(_ context.Context, _ int64, req nodeapi.ValidateRequest) error {
	f.ports = append(f.ports, req.Inbound.Port)
	return f.err
}

// The node tries a new template, or the template on a new port, before it is saved; a
// node it cannot reach does not stop the change.
func TestDryRunOnTheNode(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	dry := &fakeDryRun{err: &nodeapi.Error{Code: "invalid_config", Message: "bad listener"}}
	_, s := inbounds(t, &now, dry)
	ctx := context.Background()
	x := find(t, s, 1, "vless-xhttp")
	var ne *nodeapi.Error
	if _, _, err := s.Update(ctx, x.ID, InboundPatch{Port: ptr("2443")}); !errors.As(err, &ne) {
		t.Fatalf("the node refuses: %v", err)
	}
	if _, err := s.Create(ctx, NewInbound{Preset: "anytls"}); !errors.As(err, &ne) {
		t.Fatalf("the node refuses a new one: %v", err)
	}
	dry.err = nodeapi.ErrUnavailable
	if _, next, err := s.Update(ctx, x.ID, InboundPatch{Port: ptr("2443")}); err != nil || next.Port != "2443" {
		t.Fatalf("a node down does not stop it: %+v %v", next, err)
	}
	// A name or a pool is not the node's business.
	dry.ports = nil
	if _, _, err := s.Update(ctx, x.ID, InboundPatch{DisplayName: ptr("XHTTP")}); err != nil || len(dry.ports) != 0 {
		t.Fatalf("a rename asked the node: %v %v", dry.ports, err)
	}
}
