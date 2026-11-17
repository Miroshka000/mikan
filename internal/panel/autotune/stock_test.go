package autotune

import (
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
)

// An inbound nobody has used yet leaves the site every installation starts with for one
// next to its server; all of the node's such inbounds take the same site, from one search.
func TestFreshInboundsLeaveTheStockSite(t *testing.T) {
	e := setup(t)
	scans := 0
	e.nodes.onScan = func() { scans++ }
	e.nodes.found = []nodeapi.TargetResult{good("203.0.113.44:443", "shop.example.org"), good("203.0.113.45:443", "news.example.net")}
	e.step(t, 0)
	for _, name := range []string{"vless-xhttp", "vless-vision"} {
		if got := sniOf(t, e.inbound(t, name)); got != "shop.example.org" {
			t.Fatalf("%s: site %s, want the nearest one for both", name, got)
		}
	}
	if scans != 1 {
		t.Fatalf("%d searches for one node's inbounds", scans)
	}
	ev := e.events(t)
	if len(ev) != 2 {
		t.Fatalf("events: %+v", ev)
	}
	for _, x := range ev {
		if x.Kind != "sni" || x.Reason != ReasonStock || x.OldValue != "www.microsoft.com" || x.NewValue != "shop.example.org" {
			t.Fatalf("event: %+v", x)
		}
	}
	// Once moved it is no stock site any more: the next rounds leave it alone.
	e.nodes.found = []nodeapi.TargetResult{good("203.0.113.45:443", "news.example.net")}
	e.step(t, 2*stockRetry)
	if got := sniOf(t, e.inbound(t, "vless-xhttp")); got != "shop.example.org" || scans != 1 {
		t.Fatalf("a site of its own was changed again: %s, %d searches", got, scans)
	}
}

// An inbound clients use keeps its site, the stock one too: a change would cut them off
// until they read their subscription again. One with replacement switched off keeps it as
// well.
func TestUsedInboundKeepsTheStockSite(t *testing.T) {
	e := setup(t)
	vision := e.inbound(t, "vless-vision")
	if err := e.st.Q.SetInboundAuto(e.ctx, db.SetInboundAutoParams{AutoPort: 1, AutoSni: 0, ID: vision.ID}); err != nil {
		t.Fatal(err)
	}
	// The first round writes down who reached what; there is no site to move to yet.
	e.reaches("vless-xhttp", "hysteria2")
	e.step(t, 0)
	e.nodes.found = []nodeapi.TargetResult{good("203.0.113.44:443", "shop.example.org")}
	e.step(t, 2*stockRetry)
	for _, name := range []string{"vless-xhttp", "vless-vision"} {
		if got := sniOf(t, e.inbound(t, name)); got != "www.microsoft.com" {
			t.Fatalf("%s moved to %s", name, got)
		}
	}
	if ev := e.events(t); len(ev) != 0 {
		t.Fatalf("events: %+v", ev)
	}
}

// With no site next to the server the inbound stays as it is, is not marked as stuck, and
// the search is not repeated every round.
func TestNoSiteNearbyLeavesTheStockSiteAlone(t *testing.T) {
	e := setup(t)
	scans := 0
	e.nodes.onScan = func() { scans++ }
	every := DefaultOptions().CheckEvery
	e.step(t, 0)
	e.step(t, every)
	e.step(t, every)
	x := e.inbound(t, "vless-xhttp")
	if got := sniOf(t, x); got != "www.microsoft.com" || presets.DefaultDest != "www.microsoft.com:443" {
		t.Fatalf("site %s", got)
	}
	if s, _ := e.tn.Status(x.ID); s.Stuck != "" || !s.TargetOK {
		t.Fatalf("a working inbound is shown as stuck: %+v", s)
	}
	if scans != 1 {
		t.Fatalf("%d searches in three rounds", scans)
	}
	e.step(t, stockRetry+time.Minute)
	if scans != 2 {
		t.Fatalf("%d searches a day later, want one more", scans)
	}
}
