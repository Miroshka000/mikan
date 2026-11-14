package domain

import (
	"context"
	"errors"
	"testing"
	"time"

	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// realityConfig is a REALITY template that shows dest; sni is the name for a dest given by IP.
func realityConfig(t *testing.T, dest, sni string) string {
	t.Helper()
	r, err := presets.NewReality(presets.DefaultDest)
	if err != nil {
		t.Fatal(err)
	}
	tpl := proto.Template{"type": "vless", "reality-config": r}
	if err := presets.SetDest(tpl, dest, sni); err != nil {
		t.Fatal(err)
	}
	return proto.Marshal(tpl)
}

func rtInbound(t *testing.T, on bool, dest, sni string) db.Inbound {
	t.Helper()
	in := db.Inbound{NodeID: 1, Config: realityConfig(t, dest, sni)}
	if on {
		in.Enabled = 1
	}
	return in
}

func rtRelay(t *testing.T, dest, sni string, auto bool) db.NodeRelay {
	t.Helper()
	r := db.NodeRelay{NodeID: 1, Config: realityConfig(t, dest, sni)}
	if auto {
		r.AutoSni = 1
	}
	return r
}

// A relay shows the site the node's protocols show: the server's own domain first, then the
// first enabled one; the untouched default and switched-off protocols say nothing.
func TestRelayTargetTakesTheNodesSite(t *testing.T) {
	hy := db.Inbound{NodeID: 1, Enabled: 1, Config: "type: hysteria2\n"}
	near := rtInbound(t, true, "203.0.113.5:443", "near.example")
	own := rtInbound(t, true, "127.0.0.1:21355", "vpn.example.com")
	for _, c := range []struct {
		name      string
		ins       []db.Inbound
		dest, sni string
		ok        bool
	}{
		{"the server's own domain first", []db.Inbound{hy, rtInbound(t, true, presets.DefaultDest, ""), near, own}, "127.0.0.1:21355", "vpn.example.com", true},
		{"else the first site", []db.Inbound{rtInbound(t, false, "203.0.113.9:443", "off.example"), near}, "203.0.113.5:443", "near.example", true},
		{"the default says nothing", []db.Inbound{hy, rtInbound(t, true, presets.DefaultDest, "")}, "", "", false},
		{"no protocols", nil, "", "", false},
	} {
		dest, sni, ok := RelayTarget(c.ins)
		if dest != c.dest || sni != c.sni || ok != c.ok {
			t.Errorf("%s: %q %q %v", c.name, dest, sni, ok)
		}
	}
	near2 := []db.Inbound{near}
	for _, c := range []struct {
		name  string
		relay db.NodeRelay
		ins   []db.Inbound
		want  string
	}{
		{"set by the admin", rtRelay(t, "203.0.113.5:443", "near.example", false), near2, RelayTargetManual},
		{"the server's own", rtRelay(t, "127.0.0.1:21355", "vpn.example.com", true), nil, RelayTargetOwn},
		{"as the protocols", rtRelay(t, "203.0.113.5:443", "near.example", true), near2, RelayTargetProtocol},
		{"the default", rtRelay(t, presets.DefaultDest, "", true), nil, RelayTargetDefault},
		{"found next to the server", rtRelay(t, "198.51.100.7:443", "other.example", true), near2, RelayTargetPicked},
	} {
		if got := RelayTargetSource(c.relay, c.ins); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func realityKeys(t *testing.T, r db.NodeRelay) (string, string) {
	t.Helper()
	tpl, err := proto.Parse(r.Config)
	if err != nil {
		t.Fatal(err)
	}
	rc, _ := tpl["reality-config"].(map[string]any)
	key, _ := rc["private-key"].(string)
	sid, _ := rc["short-id"].([]any)
	if key == "" || len(sid) != 1 {
		t.Fatalf("relay keys: %v", rc)
	}
	return key, sid[0].(string)
}

// A new relay shows the site of its node's protocols; handed back to the panel it follows
// them; the admin's own site stays; the keys never change with the site.
func TestRelayTargetFollowsTheNodeAndKeepsTheKeys(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	q := st.Q
	clock := func() time.Time { return now }
	local, err := q.GetNode(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := EnsureRelay(ctx, q, local, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The seeded protocols show the default site: so does the relay, for the tuner to replace.
	if dest, _ := RelayTargetOf(r); dest != presets.DefaultDest || r.AutoSni != 1 {
		t.Fatalf("new relay: %s auto %d", dest, r.AutoSni)
	}
	key, sid := realityKeys(t, r)

	ins, err := q.ListNodeInbounds(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var reality db.Inbound
	for _, in := range ins {
		if tpl, err := proto.Parse(in.Config); err == nil && in.Enabled != 0 {
			if d, _ := presets.Dest(tpl); d != "" {
				reality = in
				break
			}
		}
	}
	if reality.ID == 0 {
		t.Fatal("no seeded REALITY protocol")
	}
	inbounds := NewInbounds(st, nil, clock)
	dest, name := "203.0.113.5:443", "near.example"
	if _, _, err := inbounds.Update(ctx, reality.ID, InboundPatch{Dest: &dest, ServerName: &name}); err != nil {
		t.Fatal(err)
	}
	if changed, err := SetRelayAuto(ctx, q, 1); err != nil || !changed {
		t.Fatalf("auto: %v %v", changed, err)
	}
	r, err = q.GetNodeRelay(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if d, s := RelayTargetOf(r); d != dest || s != name {
		t.Fatalf("the relay shows %s %s, want the protocol's", d, s)
	}
	if k, s := realityKeys(t, r); k != key || s != sid {
		t.Fatal("the relay's keys changed with its site")
	}

	if _, err := SetRelayTarget(ctx, q, 1, "www.example.org:443", "", false); err != nil {
		t.Fatal(err)
	}
	r, err = q.GetNodeRelay(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	ins, _ = q.ListNodeInbounds(ctx, 1)
	if d, s := RelayTargetOf(r); d != "www.example.org:443" || s != "www.example.org" || RelayTargetSource(r, ins) != RelayTargetManual {
		t.Fatalf("the admin's site: %s %s %s", d, s, RelayTargetSource(r, ins))
	}
	if changed, err := SetRelayTarget(ctx, q, 1, "www.example.org:443", "", false); err != nil || changed {
		t.Fatalf("the same site again: %v %v", changed, err)
	}
	if _, err := SetRelayTarget(ctx, q, 1, "203.0.113.9:443", "", false); err == nil {
		t.Fatal("a site by IP without a name was taken")
	}

	// A relay made on a node whose protocols show a site takes it at once.
	x, err := q.CreateNode(ctx, db.CreateNodeParams{Name: "B", Address: "198.51.100.20:40000", PublicHost: "198.51.100.20", CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	in, err := inbounds.Create(ctx, NewInbound{NodeID: x.ID, Preset: "vless_reality_xhttp", Port: "443"})
	if err != nil {
		t.Fatal(err)
	}
	bDest, bName := "198.51.100.30:443", "b-near.example"
	if _, _, err := inbounds.Update(ctx, in.ID, InboundPatch{Dest: &bDest, ServerName: &bName}); err != nil {
		t.Fatal(err)
	}
	rb, err := EnsureRelay(ctx, q, x, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d, s := RelayTargetOf(rb); d != bDest || s != bName {
		t.Fatalf("B's relay shows %s %s, want its protocol's", d, s)
	}
}

// The admin's own relay port: one free TCP port, which turns the panel's moves off for it;
// a malformed one or one an inbound holds is refused; handed back, the port stays.
func TestSetRelayPort(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	q := st.Q
	local, err := q.GetNode(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRelay(ctx, q, local, now, nil); err != nil {
		t.Fatal(err)
	}
	relay := func() db.NodeRelay {
		t.Helper()
		r, err := q.GetNodeRelay(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := relay(); r.AutoPort != 1 {
		t.Fatalf("a new relay keeps its port by itself: %d", r.AutoPort)
	}
	for _, bad := range []string{"0", "70000", "2000-3000", "port"} {
		if err := SetRelayPort(ctx, q, local, bad, false); err != ErrBadPort {
			t.Errorf("%s: %v", bad, err)
		}
	}
	ins, err := q.ListNodeInbounds(ctx, 1)
	if err != nil || len(ins) == 0 {
		t.Fatal(err)
	}
	var tcp string
	for _, in := range ins {
		if InboundNetwork(in) == "tcp" {
			tcp = in.Port
			break
		}
	}
	var busy *PortInUseError
	if err := SetRelayPort(ctx, q, local, tcp, false); !errors.As(err, &busy) {
		t.Fatalf("an inbound's port %s: %v", tcp, err)
	}
	if err := SetRelayPort(ctx, q, local, "9443", false); err != nil {
		t.Fatal(err)
	}
	if r := relay(); r.Port != "9443" || r.AutoPort != 0 {
		t.Fatalf("the admin's port: %s auto %d", r.Port, r.AutoPort)
	}
	if err := SetRelayPort(ctx, q, local, "", true); err != nil {
		t.Fatal(err)
	}
	if r := relay(); r.Port != "9443" || r.AutoPort != 1 {
		t.Fatalf("handed back: %s auto %d", r.Port, r.AutoPort)
	}
}

// The admin's own site is checked as an inbound's: a private address and a site by IP without
// a name are refused, and only the panel's own node may show the panel's HTTPS.
func TestCheckRelayTarget(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	s := NewInbounds(st, nil, func() time.Time { return now })
	local, err := st.Q.GetNode(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	remote := db.Node{ID: 9, Address: "198.51.100.20:40000"}
	for _, c := range []struct {
		node      db.Node
		dest, sni string
		code      string
	}{
		{local, "www.example.org:443", "", ""},
		{local, "203.0.113.5:443", "near.example", ""},
		{local, "203.0.113.5:443", "", "reality_sni"},
		{local, "10.0.0.5:443", "x.example", "reality_dest_private"},
		{local, "nohost", "", "dest_format"},
		{remote, "127.0.0.1:21355", "vpn.example.com", "reality_dest_private"},
	} {
		err := s.CheckRelayTarget(ctx, c.node, c.dest, c.sni)
		if code(err) != c.code {
			t.Errorf("%s %s: %v, want %q", c.dest, c.sni, err, c.code)
		}
	}
}

func code(err error) string {
	if err == nil {
		return ""
	}
	if pe, ok := err.(*proto.Error); ok {
		return pe.Code
	}
	return err.Error()
}
