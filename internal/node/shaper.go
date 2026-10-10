package node

import (
	"context"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"mikan/internal/nodeapi"
)

// Speed caps and the fair share of a node's channel.
//
// A user's slots (one per bound device) form a group with one limiter each way, so the cap
// is the user's on this node, whatever the devices and connections. Without a cap and
// without the fair share a group is unlimited and its connections are plain countingConns,
// which XTLS Vision may splice in the kernel; a shaped connection gives that up, as
// spliced bytes cannot be held back.
//
// The fair share splits the channel max-min every second: a group that moved less than an
// even share keeps an even share as its ceiling, what it leaves goes to the groups that
// hit their ceilings. Idle groups count for nothing, so one user alone gets the whole
// channel.

const (
	mbit = 1_000_000 / 8 // bytes a second in one Mbit/s

	// shareEvery is how often the fair share is worked out again.
	shareEvery = time.Second
	// activeFor is how long a group counts as active after it last moved traffic.
	activeFor = 3 * time.Second
	// shareFloor keeps a share from sinking to nothing among very many users.
	shareFloor = 1 * mbit
	// hungryAt: a group that used this much of its ceiling wants more.
	hungryAt = 0.85
)

// group is a user's traffic on this node: its cap and its limiters each way.
type group struct {
	own        atomic.Int64 // its own cap, bytes/s each way; 0: none
	up, down   *rate.Limiter
	upN, downN atomic.Int64 // bytes since the last share
	lastActive atomic.Int64 // unix nanoseconds
}

func newGroup() *group {
	return &group{up: rate.NewLimiter(rate.Inf, 0), down: rate.NewLimiter(rate.Inf, 0)}
}

// set gives the group a ceiling each way; 0 or less: none.
func setLimit(l *rate.Limiter, bps float64) {
	if bps <= 0 {
		if l.Limit() != rate.Inf {
			l.SetLimit(rate.Inf)
		}
		return
	}
	// A tenth of a second's worth, so a short burst passes and the average holds.
	burst := int(math.Max(bps/10, 64<<10))
	if l.Limit() != rate.Limit(bps) || l.Burst() != burst {
		l.SetBurst(burst)
		l.SetLimit(rate.Limit(bps))
	}
}

// moved counts bytes the group moved; share turns them into its rate and its activity.
func (g *group) moved(up, down int64) {
	if up != 0 {
		g.upN.Add(up)
	}
	if down != 0 {
		g.downN.Add(down)
	}
}

// shaper keeps the groups and the fair share of the node.
type shaper struct {
	mu      sync.Mutex
	groups  map[string]*group
	channel atomic.Int64 // bytes/s each way; 0: no fair share
	now     func() time.Time
}

func newShaper(now func() time.Time) *shaper {
	return &shaper{groups: map[string]*group{}, now: now}
}

// group is the group of key, made on first use.
func (sh *shaper) group(key string) *group {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	g := sh.groups[key]
	if g == nil {
		g = newGroup()
		sh.groups[key] = g
	}
	return g
}

// keep drops the groups no slot belongs to any more.
func (sh *shaper) keep(keys map[string]bool) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	for k := range sh.groups {
		if !keys[k] {
			delete(sh.groups, k)
		}
	}
}

// on says whether the node shares its channel.
func (sh *shaper) on() bool { return sh.channel.Load() > 0 }

// setChannel turns the fair share on (the channel in Mbit/s) or off (nil).
func (sh *shaper) setChannel(s *nodeapi.Shaping) {
	var bps int64
	if s != nil && s.ChannelMbps > 0 {
		bps = int64(s.ChannelMbps) * mbit
	}
	sh.channel.Store(bps)
	sh.share()
}

// share sets every group's ceilings: its own cap, and with the fair share on its share of
// the channel, whichever is lower.
func (sh *shaper) share() {
	sh.mu.Lock()
	gs := make([]*group, 0, len(sh.groups))
	for _, g := range sh.groups {
		gs = append(gs, g)
	}
	sh.mu.Unlock()
	channel := float64(sh.channel.Load())
	now := sh.now()
	per := shareEvery.Seconds()
	var ups, downs []used
	for _, g := range gs {
		// What it moved since the last share, as a rate.
		up, down := float64(g.upN.Swap(0))/per, float64(g.downN.Swap(0))/per
		if up+down > 0 {
			g.lastActive.Store(now.UnixNano())
		}
		if channel > 0 && now.Sub(time.Unix(0, g.lastActive.Load())) < activeFor {
			ups = append(ups, used{g, up, g.up})
			downs = append(downs, used{g, down, g.down})
		}
	}
	upShare, downShare := fairShares(ups, channel), fairShares(downs, channel)
	for _, g := range gs {
		own := float64(g.own.Load())
		setLimit(g.up, lower(own, upShare[g]))
		setLimit(g.down, lower(own, downShare[g]))
	}
}

// used is what a group moved one way in the last period (bytes/s) and its limiter that way.
type used struct {
	g    *group
	rate float64
	l    *rate.Limiter
}

// lower is the lower of two ceilings, where 0 is none.
func lower(a, b float64) float64 {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	}
	return math.Min(a, b)
}

// fairShares splits channel (bytes/s) between the active groups, max-min: every group gets
// an even share as its ceiling; the groups that hit their ceilings and want more share
// what the others leave unused. A group its own cap holds back wants no more than that.
func fairShares(active []used, channel float64) map[*group]float64 {
	out := make(map[*group]float64, len(active))
	if channel <= 0 || len(active) == 0 {
		return out
	}
	even := math.Max(channel/float64(len(active)), shareFloor)
	left := channel
	var hungry []*group
	for _, u := range active {
		own := float64(u.g.own.Load())
		atCeiling := u.l.Limit() == rate.Inf || u.rate >= hungryAt*float64(u.l.Limit())
		heldByOwn := own > 0 && u.rate >= hungryAt*own
		if atCeiling && !heldByOwn && u.rate >= hungryAt*even {
			hungry = append(hungry, u.g)
			continue
		}
		out[u.g] = even
		left -= math.Min(u.rate, even)
	}
	if len(hungry) > 0 {
		each := math.Max(left/float64(len(hungry)), even)
		for _, g := range hungry {
			out[g] = each
		}
	}
	return out
}

// run works the fair share out every second until ctx ends.
func (sh *shaper) run(ctx context.Context) {
	t := time.NewTicker(shareEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			sh.share()
		case <-ctx.Done():
			return
		}
	}
}

// shapedConn holds a connection to its group's ceilings: what the client sends is taken
// after it is read, what it gets before it is written. It has no Unwrap methods, so the
// copy loop goes through Read and Write and cannot splice past the limiter.
type shapedConn struct {
	net.Conn // the countingConn
	g        *group
}

func (c *shapedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		wait(c.g.up, n)
	}
	return n, err
}

func (c *shapedConn) Write(b []byte) (int, error) {
	written := 0
	for written < len(b) {
		chunk := min(len(b)-written, max(c.g.down.Burst(), 1))
		wait(c.g.down, chunk)
		n, err := c.Conn.Write(b[written : written+chunk])
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// wait takes n bytes from l, in pieces no larger than its burst.
func wait(l *rate.Limiter, n int) {
	for n > 0 {
		if l.Limit() == rate.Inf {
			return
		}
		take := min(n, max(l.Burst(), 1))
		_ = l.WaitN(context.Background(), take)
		n -= take
	}
}
