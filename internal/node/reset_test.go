package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
)

// A reset closes the slot's connections on every inbound, by the name the core knows it
// there (TUIC: the UUID), and changes nothing of its access: the slot is let in again at
// once, whom the core keeps out stays the same, and a torrent ban, the node's own one
// included, holds on.
func TestResetSlotsClosesConnectionsAndKeepsAccess(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	core := &fakeCore{}
	r := NewRegistry("e1", 0, time.Minute, func() time.Time { return now }, core)
	r.SetSlots([]nodeapi.Slot{{Name: "s1", UUID: "U1"}, {Name: "s2", UUID: "u2"}, {Name: "s3", UUID: "u3"}})
	r.SetInbounds(map[string]bool{"vless": false, "tuic": true})
	r.SetPolicies("e1", []nodeapi.Policy{
		{Slot: "s1", Allowed: true, QuotaRemaining: 1000},
		{Slot: "s2", Allowed: true, QuotaRemaining: -1},
		{Slot: "s3", Allowed: true, QuotaRemaining: -1},
	})
	r.SetTorrent(&nodeapi.TorrentBlock{BanSeconds: 3600})
	core.online("vless", "s1", "203.0.113.1", 3, now)
	core.online("tuic", "u1", "203.0.113.1", 1, now)
	core.online("vless", "s2", "203.0.113.2", 2, now)
	core.online("vless", "s3", "203.0.113.3", 5, now)
	r.Counters()
	// s2 is caught at a torrent: the node's own short ban keeps it out.
	s2 := r.admitIn("s2", "vless", "203.0.113.2")
	r.caught(s2, "203.0.113.2", "vless", "tcp", nodeapi.TorrentHandshake, "198.51.100.9:6881")
	if r.admitIn("s2", "vless", "203.0.113.2") != nil {
		t.Fatal("a caught slot is kept out")
	}
	out, passes := core.keptOut(), core.passes
	local, policy := s2.localBan.Load(), s2.policyBan.Load()

	if n := r.ResetSlots([]string{"s1", "s2", "gone"}); n != 6 {
		t.Fatalf("closed %d, want the 4 of s1 and the 2 of s2", n)
	}
	if want := []string{"tuic/u1", "tuic/u2", "vless/s1", "vless/s2"}; !reflect.DeepEqual(core.closed, want) {
		t.Fatalf("closed %v, want %v", core.closed, want)
	}
	if core.passes != passes || !reflect.DeepEqual(core.keptOut(), out) {
		t.Fatalf("a reset must not touch the core's limits: %d passes, kept out %v", core.passes-passes, core.keptOut())
	}
	if r.admitIn("s1", "vless", "203.0.113.1") == nil || r.admitIn("s1", "tuic", "203.0.113.1") == nil {
		t.Fatal("the reset slot is let in again at once")
	}
	if r.admitIn("s2", "vless", "203.0.113.2") != nil || s2.localBan.Load() != local || s2.policyBan.Load() != policy {
		t.Fatal("a reset must leave the torrent ban as it was")
	}
	if c := r.Counters(); c.Online["s3"].Conns != 5 {
		t.Fatalf("another slot keeps its connections: %+v", c.Online["s3"])
	}
	if n := r.ResetSlots(nil); n != 0 || len(core.closed) != 4 {
		t.Fatal("nothing named, nothing closed")
	}
}

// The Node API takes the slots by name and answers how many connections it closed; a slot
// the node does not have is no error, which the panel would take for an old node's 404.
func TestResetSlotsAPI(t *testing.T) {
	core := &fakeCore{}
	reg := NewRegistry("e1", 0, time.Minute, time.Now, core)
	reg.SetSlots([]nodeapi.Slot{{Name: "s1", UUID: "u1"}})
	reg.SetInbounds(map[string]bool{"vless": false})
	reg.SetPolicies("e1", []nodeapi.Policy{{Slot: "s1", Allowed: true, QuotaRemaining: -1}})
	core.online("vless", "s1", "203.0.113.1", 2, time.Now())
	h := Handler(&Engine{log: quiet(), Reg: reg}, quiet())
	for _, c := range []struct {
		body string
		want int
	}{{`{"slots":["s1"]}`, 2}, {`{"slots":["nobody"]}`, 0}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/slots/reset", strings.NewReader(c.body)))
		var res nodeapi.ResetResult
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.Closed != c.want {
			t.Fatalf("%s: %d %s", c.body, w.Code, w.Body)
		}
	}
}
