package node

import (
	"sort"
	"sync"
	"time"
)

// fakeCore stands in for Citrus in the registry's tests: it hands out the traffic and the
// addresses a test gives it and records whom the registry keeps out or cuts off.
type fakeCore struct {
	mu     sync.Mutex
	users  map[userKey]*userStat
	held   map[userKey]fakeLimit
	fresh  int      // budgets set to count from now
	passes int      // calls of limits
	closed []string // users whose connections were closed, as "inbound/user"
}

type fakeLimit struct {
	out      bool
	budget   int64
	group    string
	up, down int64
}

func (f *fakeCore) user(inbound, user string) *userStat {
	if f.users == nil {
		f.users = map[userKey]*userStat{}
	}
	k := userKey{inbound, user}
	u := f.users[k]
	if u == nil {
		u = &userStat{Inbound: inbound, User: user}
		f.users[k] = u
	}
	return u
}

// moved counts traffic of a user of an inbound for the next drain.
func (f *fakeCore) moved(inbound, user string, up, down int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.user(inbound, user)
	u.Up += up
	u.Down += down
}

// online lists the user of an inbound as connected from ip (conns > 0) or as gone since
// lastSeen; conns < 0 drops the address.
func (f *fakeCore) online(inbound, user, ip string, conns int64, lastSeen time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.user(inbound, user)
	kept := u.IPs[:0]
	for _, a := range u.IPs {
		if a.IP != ip {
			kept = append(kept, a)
		}
	}
	u.IPs = kept
	if conns >= 0 {
		u.IPs = append(u.IPs, userIP{IP: ip, Conns: conns, LastSeen: lastSeen})
	}
	u.Conns = 0
	for _, a := range u.IPs {
		u.Conns += a.Conns
	}
}

func (f *fakeCore) drain() []userStat {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]userStat, 0, len(f.users))
	for _, u := range f.users {
		out = append(out, *u)
		u.Up, u.Down = 0, 0
	}
	return out
}

func (f *fakeCore) limits(set map[userKey]coreLimit, remove, fresh []userKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held == nil {
		f.held = map[userKey]fakeLimit{}
	}
	f.passes++
	f.fresh += len(fresh)
	for _, k := range remove {
		delete(f.held, k)
	}
	for k, l := range set {
		fl := fakeLimit{out: l.out, budget: l.budget, up: l.up, down: l.down}
		if l.up > 0 || l.down > 0 {
			fl.group = l.group
		}
		f.held[k] = fl
	}
}

// closeConns closes what a user has open: its connections drop to none, its addresses stay
// listed.
func (f *fakeCore) closeConns(keys []userKey) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, k := range keys {
		f.closed = append(f.closed, k.inbound+"/"+k.user)
		u := f.users[k]
		if u == nil {
			continue
		}
		n += int(u.Conns)
		u.Conns = 0
		for i := range u.IPs {
			u.IPs[i].Conns = 0
		}
	}
	sort.Strings(f.closed)
	return n
}

// keptOut lists the users the core keeps out, as "inbound/user", sorted.
func (f *fakeCore) keptOut() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for k, l := range f.held {
		if l.out {
			out = append(out, k.inbound+"/"+k.user)
		}
	}
	sort.Strings(out)
	return out
}
