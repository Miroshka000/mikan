package subs

import (
	"net/url"
	"strings"
	"testing"

	"mikan/internal/panel/store/db"
	"mikan/internal/panel/turn"
)

// withTurn adds a node's TURN setup mirroring one of the profile's inbounds.
func withTurn(t *testing.T, p Profile, in db.Inbound, edit func(*turn.Config)) Profile {
	t.Helper()
	cfg := turn.Config{Enabled: true, InboundID: in.ID, ListenPort: "56000", ClientHost: "127.0.0.1",
		ClientPort: "9000", Streams: 10, UDP: true, Links: []string{"https://vk.com/call/join/goodturn"}}
	if edit != nil {
		edit(&cfg)
	}
	p.Turn = map[int64]turn.Config{in.NodeID: cfg}
	p.UserID = 7
	return p
}

func inboundNamed(t *testing.T, p Profile, name string) db.Inbound {
	t.Helper()
	for _, in := range p.Inbounds {
		if in.Name == name {
			return in
		}
	}
	t.Fatalf("no inbound %q in the fixture", name)
	return db.Inbound{}
}

func TestTurnCopyStandsNextToItsInbound(t *testing.T) {
	mirror := inboundNamed(t, profile(t, "ab12"), "vless-xhttp")
	p := withTurn(t, profile(t, "ab12"), mirror, nil)
	links, err := URIs(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(links, "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d links, want the four and the WL copy:\n%s", len(lines), links)
	}
	first, err := url.Parse(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	wl, err := url.Parse(lines[1])
	if err != nil {
		t.Fatal(err)
	}
	if wl.Fragment != first.Fragment+" WL" {
		t.Fatalf("the copy is named %q, next to %q", wl.Fragment, first.Fragment)
	}
	if wl.Host != "127.0.0.1:9000" {
		t.Fatalf("the copy must stand at the client's own address, got %s", wl.Host)
	}
	// The key, the transport and the camouflage are the same server's.
	if wl.User.Username() != first.User.Username() || wl.Query().Get("type") != "xhttp" || wl.Query().Get("pbk") == "" {
		t.Fatalf("the copy is not the same server: %s", wl)
	}
}

func TestTurnCopyOfADatagramInboundDropsPortHopping(t *testing.T) {
	mirror := inboundNamed(t, profile(t, "ab12"), "hysteria2")
	p := withTurn(t, profile(t, "ab12"), mirror, nil)
	links, err := URIs(p)
	if err != nil {
		t.Fatal(err)
	}
	var wl *url.URL
	for _, l := range strings.Split(links, "\n") {
		u, err := url.Parse(l)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(u.Fragment, " WL") {
			wl = u
		}
	}
	if wl == nil {
		t.Fatalf("no WL copy of the Hysteria2 inbound:\n%s", links)
	}
	if wl.Host != "127.0.0.1:9000" || wl.Query().Get("mport") != "" {
		t.Fatalf("the copy must be one port of the client's, not a hopping range: %s", wl)
	}
}

func TestTurnCopyTakesTheAdminsName(t *testing.T) {
	mirror := inboundNamed(t, profile(t, "ab12"), "vless-xhttp")
	p := withTurn(t, profile(t, "ab12"), mirror, func(c *turn.Config) { c.Title = "Германия (XHTTP WL)" })
	links, err := URIs(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(links, "Германия (XHTTP WL)") {
		// The share link carries the name percent-encoded, so it is parsed, not searched.
		named := false
		for _, l := range strings.Split(links, "\n") {
			u, err := url.Parse(l)
			if err != nil {
				t.Fatal(err)
			}
			if u.Fragment == "Германия (XHTTP WL)" && u.Host == "127.0.0.1:9000" {
				named = true
			}
		}
		if !named {
			t.Fatalf("the admin's name is not in the list:\n%s", links)
		}
	}
}

func TestNoTurnCopyForAnInboundTheUserCannotUse(t *testing.T) {
	mirror := inboundNamed(t, profile(t, "ab12"), "vless-xhttp")
	p := withTurn(t, profile(t, "ab12"), mirror, nil)
	// The user's allow-list leaves the mirrored inbound out: nothing to copy.
	var kept []db.Inbound
	for _, in := range p.Inbounds {
		if in.ID != mirror.ID {
			kept = append(kept, in)
		}
	}
	p.Inbounds = kept
	links, err := URIs(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(links, "127.0.0.1") {
		t.Fatalf("a copy appeared for an inbound the user cannot use:\n%s", links)
	}
	if got := TurnSubscription(p); got != "" {
		t.Fatalf("the client parameters must be empty too, got %q", got)
	}
}

func TestTurnSubscriptionParameters(t *testing.T) {
	mirror := inboundNamed(t, profile(t, "ab12"), "vless-xhttp")
	p := withTurn(t, profile(t, "ab12"), mirror, nil)
	got := TurnSubscription(p)
	for _, want := range []string{
		"peer: 203.0.113.7:56000",
		"vk: https://vk.com/call/join/goodturn",
		"listen: 127.0.0.1:9000",
		"mode: vless",
		"n: 10",
		"udp: on",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in:\n%s", want, got)
		}
	}
	// A datagram inbound is carried as datagrams, not as connections.
	hy := withTurn(t, profile(t, "ab12"), inboundNamed(t, profile(t, "ab12"), "hysteria2"), nil)
	if !strings.Contains(TurnSubscription(hy), "mode: udp") {
		t.Fatalf("a Hysteria2 mirror must say udp:\n%s", TurnSubscription(hy))
	}
}

func TestTurnFormatIsChosenByQuery(t *testing.T) {
	if got := Format("curl/8.9", "*/*", "turn"); got != "turn" {
		t.Fatalf("?format=turn → %s", got)
	}
	if got := Format("curl/8.9", "*/*", "wl"); got != "turn" {
		t.Fatalf("?format=wl → %s", got)
	}
}
