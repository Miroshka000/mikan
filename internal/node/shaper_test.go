package node

import (
	"io"
	"math"
	"net"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"mikan/internal/nodeapi"
)

func near(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > want*0.01 {
		t.Fatalf("%s: %.0f, want %.0f", what, got, want)
	}
}

func usage(g *group, bps float64, l *rate.Limiter) used { return used{g: g, rate: bps, l: l} }

// Two users both pulling as much as they can split the channel in half.
func TestFairShareSplitsEvenly(t *testing.T) {
	a, b := newGroup(), newGroup()
	out := fairShares([]used{usage(a, 0, a.down), usage(b, 0, b.down)}, 100*mbit)
	near(t, "a", out[a], 50*mbit)
	near(t, "b", out[b], 50*mbit)
}

// A light user keeps an even share as its ceiling; what it leaves goes to the heavy one,
// so the channel is not wasted on a cap nobody uses.
func TestFairShareGivesWhatIsLeftToTheHungry(t *testing.T) {
	light, heavy := newGroup(), newGroup()
	setLimit(light.down, 50*mbit)
	setLimit(heavy.down, 50*mbit)
	out := fairShares([]used{usage(light, 10*mbit, light.down), usage(heavy, 50*mbit, heavy.down)}, 100*mbit)
	near(t, "light", out[light], 50*mbit)
	near(t, "heavy", out[heavy], 90*mbit)
}

// A user held by an own cap below the even share takes no more of the channel than that.
func TestFairShareRespectsOwnCap(t *testing.T) {
	capped, free := newGroup(), newGroup()
	capped.own.Store(20 * mbit)
	setLimit(capped.down, 20*mbit)
	out := fairShares([]used{usage(capped, 20*mbit, capped.down), usage(free, 50*mbit, free.down)}, 100*mbit)
	near(t, "free", out[free], 80*mbit)
	if lower(float64(capped.own.Load()), out[capped]) != 20*mbit {
		t.Fatalf("the own cap must stay the ceiling: %.0f", lower(float64(capped.own.Load()), out[capped]))
	}
}

// One user alone gets the whole channel, and a share never sinks under the floor.
func TestFairShareAloneAndFloor(t *testing.T) {
	a := newGroup()
	out := fairShares([]used{usage(a, 0, a.down)}, 100*mbit)
	near(t, "alone", out[a], 100*mbit)

	var many []used
	for range 500 {
		g := newGroup()
		many = append(many, usage(g, 0, g.down))
	}
	for _, s := range fairShares(many, 10*mbit) {
		if s < shareFloor {
			t.Fatalf("share %.0f under the floor", s)
		}
	}
}

// The policy's cap holds the user's devices as one group; without a cap and without the
// fair share nothing is shaped, so Vision may still splice.
func TestPolicyCapsAndGroups(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := NewRegistry("e1", 0, time.Minute, func() time.Time { return now })
	r.SetSlots([]nodeapi.Slot{{Name: "s1", UUID: "u1"}, {Name: "s2", UUID: "u2"}, {Name: "s3", UUID: "u3"}})
	r.SetPolicies("e1", []nodeapi.Policy{
		{Slot: "s1", Allowed: true, QuotaRemaining: -1, SpeedMbps: 50, Group: "7"},
		{Slot: "s2", Allowed: true, QuotaRemaining: -1, SpeedMbps: 50, Group: "7"},
		{Slot: "s3", Allowed: true, QuotaRemaining: -1, Group: "8"},
	})
	s1, s2, s3 := r.lookup("s1"), r.lookup("s2"), r.lookup("s3")
	if s1.group.Load() != s2.group.Load() {
		t.Fatal("a user's devices must share one group")
	}
	g := r.shapedGroup(s1)
	if g == nil || g.down.Limit() != rate.Limit(50*mbit) {
		t.Fatalf("the cap must be 50 Mbit/s: %v", g)
	}
	if r.shapedGroup(s3) != nil {
		t.Fatal("a user without a cap must not be shaped while the fair share is off")
	}
	r.SetShaping(&nodeapi.Shaping{ChannelMbps: 100})
	if r.shapedGroup(s3) == nil {
		t.Fatal("with the fair share on, every user is shaped")
	}
	r.SetShaping(nil)
	r.SetPolicies("e1", []nodeapi.Policy{{Slot: "s1", Allowed: true, QuotaRemaining: -1, Group: "7"}})
	if s1.group.Load().down.Limit() != rate.Inf {
		t.Fatal("a lifted cap must lift the limit")
	}
}

// A cap that comes in closes the connections opened around the limiter: the app opens
// them again, held this time. Shaped ones stay.
func TestCapClosesUnshapedConnections(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := NewRegistry("e1", 0, time.Minute, func() time.Time { return now })
	r.SetSlots([]nodeapi.Slot{{Name: "s1", UUID: "u1"}})
	r.SetPolicies("e1", []nodeapi.Policy{{Slot: "s1", Allowed: true, QuotaRemaining: -1, Group: "7"}})
	s := r.lookup("s1")
	plain, plainPeer := net.Pipe()
	defer plainPeer.Close()
	held, heldPeer := net.Pipe()
	defer heldPeer.Close()
	c1 := &countingConn{Conn: plain, slot: s, now: r.now}
	c2 := &countingConn{Conn: held, slot: s, now: r.now, shaped: true}
	s.addConn(c1)
	s.addConn(c2)

	r.SetPolicies("e1", []nodeapi.Policy{{Slot: "s1", Allowed: true, QuotaRemaining: -1, Group: "7", SpeedMbps: 10}})
	if _, err := plainPeer.Write([]byte("x")); err == nil {
		t.Fatal("the unshaped connection must be closed")
	}
	s.mu.Lock()
	_, kept := s.conns[c2]
	s.mu.Unlock()
	if !kept {
		t.Fatal("the shaped connection must stay")
	}
}

// What the client gets through a shaped connection keeps to the cap.
func TestShapedConnHoldsTheRate(t *testing.T) {
	g := newGroup()
	setLimit(g.down, 4*mbit) // 500 KB/s, a 64 KB burst
	a, b := net.Pipe()
	defer a.Close()
	go func() { _, _ = io.Copy(io.Discard, b) }()
	c := &shapedConn{Conn: a, g: g}
	start := time.Now()
	if _, err := c.Write(make([]byte, 600<<10)); err != nil {
		t.Fatal(err)
	}
	// (600 − 64) KB at 500 KB/s.
	if took := time.Since(start); took < 1000*time.Millisecond {
		t.Fatalf("600 KB at 4 Mbit/s took only %s", took)
	}
}
