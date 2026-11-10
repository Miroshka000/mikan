package autotune

import (
	"database/sql"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
)

func (e *env) relaySite(t *testing.T, node int64) (string, string) {
	t.Helper()
	r, err := e.st.Q.GetNodeRelay(e.ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	return domain.RelayTargetOf(r)
}

func (e *env) relaySiteAudits(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.st.DB.QueryRowContext(e.ctx, `SELECT count(*) FROM audit_log WHERE action = 'auto.relay_sni'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) localRelay(t *testing.T) {
	t.Helper()
	local, err := e.st.Q.GetNode(e.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := domain.EnsureRelay(e.ctx, e.st.Q, local, e.now, nil); err != nil {
		t.Fatal(err)
	}
}

// A relay the panel keeps shows the site the node's protocols show, and follows it in the
// next round with an audit entry and a push to the nodes; the admin's own site stays.
func TestRelayFollowsTheProtocolsSite(t *testing.T) {
	e := setup(t)
	e.localRelay(t)
	if d, _ := e.relaySite(t, 1); d != presets.DefaultDest {
		t.Fatalf("new relay on %s", d)
	}
	x := e.inbound(t, "vless-vision")
	dest, name := "203.0.113.44:443", "shop.example.org"
	if _, _, err := e.tn.inbounds.Update(e.ctx, x.ID, domain.InboundPatch{Dest: &dest, ServerName: &name}); err != nil {
		t.Fatal(err)
	}
	pushes := e.ch.slots
	e.step(t, 0)
	if d, s := e.relaySite(t, 1); d != dest || s != name {
		t.Fatalf("the relay shows %s %s, want the protocol's", d, s)
	}
	if e.ch.slots == pushes || e.relaySiteAudits(t) != 1 {
		t.Fatalf("pushes %d → %d, audits %d", pushes, e.ch.slots, e.relaySiteAudits(t))
	}

	if _, err := domain.SetRelayTarget(e.ctx, e.st.Q, 1, "www.example.net:443", "", false); err != nil {
		t.Fatal(err)
	}
	dest, name = "203.0.113.45:443", "news.example.net"
	if _, _, err := e.tn.inbounds.Update(e.ctx, x.ID, domain.InboundPatch{Dest: &dest, ServerName: &name}); err != nil {
		t.Fatal(err)
	}
	e.step(t, time.Minute)
	if d, _ := e.relaySite(t, 1); d != "www.example.net:443" {
		t.Fatalf("the admin's site was replaced with %s", d)
	}
}

// A relay whose node shows no site of its own gets one next to the server once a node leaves
// through it, never the panel's own name; it is not looked for again within Cooldown, and a
// site that keeps failing is replaced after TargetFails checks and Cooldown.
func TestRelayFindsASiteNextToTheServer(t *testing.T) {
	e := setup(t)
	scans := 0
	e.nodes.onScan = func() { scans++ }
	e.localRelay(t)
	e.nodes.found = []nodeapi.TargetResult{good("203.0.113.10:443", "vpn.example.com"), good("203.0.113.44:443", "shop.example.org")}

	// Nobody leaves through node 1: its relay does not run, and nothing is looked for.
	e.step(t, 0)
	if d, _ := e.relaySite(t, 1); scans != 0 || d != presets.DefaultDest {
		t.Fatalf("an unused relay: %d scans, site %s", scans, d)
	}

	b, err := e.st.Q.CreateNode(e.ctx, db.CreateNodeParams{Name: "B", Address: "198.51.100.20:40000", PublicHost: "198.51.100.20", CreatedAt: e.now.Unix(), UpdatedAt: e.now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	in, err := e.tn.inbounds.Create(e.ctx, domain.NewInbound{NodeID: b.ID, Preset: "vless_reality_xhttp", Port: "443"})
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.UseExit(e.ctx, e.st.Q, b.ID, 1, e.now, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Q.SetInboundExit(e.ctx, db.SetInboundExitParams{ExitNodeID: sql.NullInt64{Int64: 1, Valid: true}, Outbound: "direct", ID: in.ID}); err != nil {
		t.Fatal(err)
	}
	e.step(t, time.Minute)
	if d, s := e.relaySite(t, 1); scans != 1 || d != "203.0.113.44:443" || s != "shop.example.org" {
		t.Fatalf("in use: %d scans, site %s %s", scans, d, s)
	}
	e.step(t, 20*time.Minute)
	if scans != 1 {
		t.Fatalf("a working site was looked for again: %d scans", scans)
	}

	e.nodes.targets["203.0.113.44:443"] = nodeapi.TargetResult{Dest: "203.0.113.44:443", SNI: "shop.example.org", Error: "timeout"}
	e.nodes.found = []nodeapi.TargetResult{good("203.0.113.45:443", "news.example.net")}
	for range 3 {
		e.step(t, 16*time.Minute)
	}
	if d, _ := e.relaySite(t, 1); scans != 1 || d != "203.0.113.44:443" {
		t.Fatalf("replaced within Cooldown of the last pick: %d scans, site %s", scans, d)
	}
	e.step(t, 6*time.Hour)
	if d, s := e.relaySite(t, 1); scans != 2 || d != "203.0.113.45:443" || s != "news.example.net" {
		t.Fatalf("a failing site: %d scans, site %s %s", scans, d, s)
	}
}
