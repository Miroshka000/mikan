package node

import (
	"reflect"
	"testing"
	"time"

	"mikan/internal/nodeapi"
)

// Activity is what the panel's block detector reads: per device, the last time each
// inbound let it in. Refused connections do not count and old entries drop out.
func TestActivityPerDeviceAndInbound(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := NewRegistry("e1", 0, time.Minute, func() time.Time { return now })
	r.SetSlots([]nodeapi.Slot{{Name: "s1", UUID: "u1"}, {Name: "s2", UUID: "u2"}})
	r.SetPolicies("e1", []nodeapi.Policy{
		{Slot: "s1", Allowed: true, QuotaRemaining: -1},
		{Slot: "s2", Allowed: true, QuotaRemaining: -1, Inbounds: []string{"tuic"}},
	})
	r.admit("u1", "vless-xhttp", "203.0.113.1", false)
	now = now.Add(10 * time.Second)
	r.admit("s1", "hysteria2", "203.0.113.1", false)
	r.admit("u1", "tuic", "203.0.113.2", false)
	if r.admit("u2", "vless-xhttp", "198.51.100.7", false) != nil {
		t.Fatal("s2 is limited to tuic")
	}
	r.admit("u2", "tuic", "198.51.100.7", false)

	want := []nodeapi.ClientActivity{
		{Slot: "s1", IP: "203.0.113.1", Seen: map[string]int64{"vless-xhttp": now.Unix() - 10, "hysteria2": now.Unix()}},
		{Slot: "s1", IP: "203.0.113.2", Seen: map[string]int64{"tuic": now.Unix()}},
		{Slot: "s2", IP: "198.51.100.7", Seen: map[string]int64{"tuic": now.Unix()}},
	}
	if got := r.Activity().Clients; !reflect.DeepEqual(got, want) {
		t.Fatalf("activity:\n got %+v\nwant %+v", got, want)
	}

	now = now.Add(activityKeep + time.Second)
	r.admit("u1", "tuic", "203.0.113.2", false)
	got := r.Activity().Clients
	if len(got) != 1 || got[0].IP != "203.0.113.2" || len(got[0].Seen) != 1 {
		t.Fatalf("entries older than %s must drop out: %+v", activityKeep, got)
	}
}

// A device limit is shared by all nodes of a panel: devices the panel saw on other nodes
// take places here, and those same devices may still connect here.
func TestDeviceLimitCountsOtherNodes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := NewRegistry("e1", 0, time.Minute, func() time.Time { return now })
	r.SetSlots([]nodeapi.Slot{{Name: "s1", UUID: "u1"}})
	policy := nodeapi.Policy{Slot: "s1", Allowed: true, DeviceLimit: 2, QuotaRemaining: -1, OtherIPs: []string{"198.51.100.1"}}
	r.SetPolicies("e1", []nodeapi.Policy{policy})

	if r.admit("u1", "vless", "203.0.113.1", false) == nil {
		t.Fatal("the second device must connect")
	}
	if r.admit("u1", "vless", "203.0.113.2", false) != nil {
		t.Fatal("a third device must be refused: one is already online on another node")
	}
	if r.admit("u1", "vless", "198.51.100.1", false) == nil {
		t.Fatal("the device from the other node must connect here without taking a place")
	}

	// Without devices elsewhere the limit is this node's alone.
	policy.OtherIPs = nil
	r.SetPolicies("e1", []nodeapi.Policy{policy})
	now = now.Add(2 * time.Minute) // both local devices are released
	for _, ip := range []string{"203.0.113.3", "203.0.113.4"} {
		if r.admit("u1", "vless", ip, false) == nil {
			t.Fatalf("%s must connect", ip)
		}
	}
	if r.admit("u1", "vless", "203.0.113.5", false) != nil {
		t.Fatal("the limit still holds on one node")
	}
}

// Listeners with one key for everyone (Shadowsocks-2022, Sudoku, Snell) carry no user:
// the node lets them in without a policy or per-user limits — only on those listeners.
func TestSharedListeners(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := NewRegistry("e1", 0, time.Minute, func() time.Time { return now })
	r.SetSlots([]nodeapi.Slot{{Name: "s1", UUID: "u1"}})
	r.SetPolicies("e1", []nodeapi.Policy{{Slot: "s1", Allowed: true, QuotaRemaining: -1}})
	r.SetShared([]string{"ss"})
	if r.admit("", "ss", "203.0.113.1", false) == nil {
		t.Fatal("a shared listener takes connections without a user")
	}
	if r.admit("", "vless-xhttp", "203.0.113.1", false) != nil {
		t.Fatal("every other listener still needs a user")
	}
	if got := r.Activity().Clients; len(got) != 0 {
		t.Fatalf("shared traffic is nobody's activity: %+v", got)
	}
	r.SetShared(nil)
	if r.admit("", "ss", "203.0.113.1", false) != nil {
		t.Fatal("the shared listener is gone")
	}
}

// admit is admitIn without the traffic pool the connection counts to.
func (r *Registry) admit(user, inName, ip string, tcp bool) *slot {
	s, _ := r.admitIn(user, inName, ip, tcp)
	return s
}
