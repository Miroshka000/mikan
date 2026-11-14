package autotune

import (
	"testing"
	"time"

	"mikan/internal/nodeapi"
)

// A node keeps to one site: of the sites that will do, the one its other inbounds already
// show wins over a faster stranger, a site people open over a weak name, and a weak name
// is taken only when there is nothing else.
func TestChooseTargetKeepsANodeToOneSite(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	w := &world{now: now, ownNames: map[string]bool{"panel.example.com": true}}
	tn := &Tuner{failed: map[string]time.Time{"203.0.113.9:443": now.Add(-time.Hour)}}
	found := []nodeapi.TargetResult{
		good("203.0.113.1:443", "panel.example.com"), // the panel's own name
		good("203.0.113.9:443", "failed.example.org"),
		good("203.0.113.2:443", "home.duckdns.org"),
		good("203.0.113.3:443", "fast.example.org"),
		good("203.0.113.4:443", "shared.example.net"),
		good("203.0.113.5:443", "mail.example.net"),
	}
	choose := func(oldDest string, used ...string) string {
		u := map[string]bool{}
		for _, n := range used {
			u[n] = true
		}
		if p := tn.chooseTarget(w, found, oldDest, nil, u); p != nil {
			return p.SNI
		}
		return ""
	}
	if got := choose(""); got != "fast.example.org" {
		t.Fatalf("nothing shared: the fastest site people open, got %q", got)
	}
	if got := choose("", "shared.example.net"); got != "shared.example.net" {
		t.Fatalf("the node's other inbounds show a site: the same one, got %q", got)
	}
	if got := choose("203.0.113.4:443", "shared.example.net"); got != "fast.example.org" {
		t.Fatalf("the site being left is never taken again, got %q", got)
	}
	if got := choose("", "home.duckdns.org"); got != "fast.example.org" {
		t.Fatalf("a weak name the node shows does not hold it there, got %q", got)
	}
	found = found[:3]
	if got := choose(""); got != "home.duckdns.org" {
		t.Fatalf("only a weak name is left: better than no site, got %q", got)
	}
	found = found[:2]
	if got := choose(""); got != "" {
		t.Fatalf("the panel's own name and a site that failed lately are no choice, got %q", got)
	}
}

// What a round picked for a node counts as the node's site at once, before the snapshot of
// its inbounds shows the move.
func TestSiteNamesCountTheRoundsPick(t *testing.T) {
	w := &world{}
	if len(w.siteNames(1, 0)) != 0 {
		t.Fatal("a node with no inbounds shows no site")
	}
	w.took(1, "Shop.Example.org")
	if got := w.siteNames(1, 0); !got["shop.example.org"] || len(got) != 1 {
		t.Fatalf("the pick of the round: %v", got)
	}
	if len(w.siteNames(2, 0)) != 0 {
		t.Fatal("a pick is the node's alone")
	}
}
