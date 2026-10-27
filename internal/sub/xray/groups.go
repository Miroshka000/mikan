package xray

import (
	"slices"
	"strconv"
	"strings"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// The core's name in the notes of the admin panel's preview.
const core = "xray"

// The outbounds of the policies a group may hold.
const (
	directTag = "direct"
	blockTag  = "block"
)

// Balancing is a profile's groups as xray's routing carries them, for its JSON config:
// balancers over the outbounds (tagged by the proxies' names), how the observatories test
// them, what a rule to each group names, and the chains' dialers. What xray has no group
// for becomes the nearest form, with a note for the preview.
type Balancing struct {
	Balancers []Balancer
	// Refs is what a rule to a group names: its balancer, or the outbound a group xray
	// cannot balance (a select, a chain) goes to.
	Refs map[string]Ref
	// Dialers: an outbound to the one it dials through (streamSettings.sockopt.dialerProxy),
	// of the chains.
	Dialers map[string]string
	// Observatory and BurstObservatory are how the outbounds of the leastPing and the
	// leastLoad balancers are tested, none when there is no such balancer. Their
	// subjects are a config's own (observe in json.go): the outbounds of the balancers it has.
	Observatory      *Observatory
	BurstObservatory *BurstObservatory
	Notes            []model.Note
}

// Ref is what a rule or a dialer names: an outbound (outboundTag), or a balancer
// (balancerTag).
type Ref struct {
	Tag      string
	Balancer bool
}

// Balancer is a balancer of xray's routing. Selector holds outbound tags, and xray reads
// each as a prefix: the config must tag its outbounds so that no tag begins with another.
type Balancer struct {
	Tag         string   `json:"tag"`
	Selector    []string `json:"selector"`
	FallbackTag string   `json:"fallbackTag,omitempty"`
	Strategy    Strategy `json:"strategy"`
}

// Strategy is how a balancer picks an outbound: leastPing, leastLoad, random, roundRobin.
type Strategy struct {
	Type     string         `json:"type"`
	Settings map[string]any `json:"settings,omitempty"`
}

// Observatory tests the outbounds of the leastPing balancers.
type Observatory struct {
	SubjectSelector   []string `json:"subjectSelector"`
	ProbeURL          string   `json:"probeUrl"`
	ProbeInterval     string   `json:"probeInterval"`
	EnableConcurrency bool     `json:"enableConcurrency"`
}

// BurstObservatory tests the outbounds of the leastLoad balancers, a few probes each time.
type BurstObservatory struct {
	SubjectSelector []string   `json:"subjectSelector"`
	PingConfig      PingConfig `json:"pingConfig"`
}

// PingConfig is how BurstObservatory probes.
type PingConfig struct {
	Destination string `json:"destination"`
	Interval    string `json:"interval"`
	Sampling    int    `json:"sampling,omitempty"`
}

// Balance converts the groups for xray: a url-test is a leastPing balancer; a fallback a
// leastPing balancer whose fallbackTag is its first member (xray keeps no order, it picks
// the fastest that answers); a load-balance a random or a roundRobin one; a chain the
// dialerProxy of each hop through the one before it, a rule to it going to its exit; a
// select its first member (in xray the app's list of configs is the choice). tags are the
// outbounds xray has of the proxies; s.Xray sets the strategy of the testing groups and the
// observatory.
func Balance(groups []model.Group, tags map[string]bool, s routing.Servers) Balancing {
	c := converter{byName: map[string]model.Group{}, tags: tags, refs: map[string]Ref{}, busy: map[string]bool{}, s: s.Xray,
		out: Balancing{Refs: map[string]Ref{}, Dialers: map[string]string{}}}
	for _, g := range groups {
		c.byName[g.Name] = g
	}
	for _, g := range groups {
		if r, ok := c.ref(g.Name); ok {
			c.out.Refs[g.Name] = r
		}
	}
	c.observe(s.XrayBalance())
	return c.out
}

type converter struct {
	byName map[string]model.Group
	tags   map[string]bool
	refs   map[string]Ref
	busy   map[string]bool // groups being converted: a group in itself is no member
	s      routing.XrayServers
	out    Balancing
}

func (c *converter) note(g model.Group, code string) {
	if !g.Hidden {
		c.out.Notes = append(c.out.Notes, model.Note{Core: core, Group: g.Name, Code: code})
	}
}

// ref is what a rule to the member name goes to: an outbound, a policy's, or a group's
// Ref; false for nothing xray has (a proxy it cannot carry, a group of none).
func (c *converter) ref(name string) (Ref, bool) {
	switch {
	case c.tags[name]:
		return Ref{Tag: name}, true
	case name == "DIRECT":
		return Ref{Tag: directTag}, true
	case strings.HasPrefix(name, "REJECT"):
		return Ref{Tag: blockTag}, true
	}
	if r, ok := c.refs[name]; ok {
		return r, true
	}
	g, ok := c.byName[name]
	if !ok || c.busy[name] {
		return Ref{}, false
	}
	c.busy[name] = true
	defer delete(c.busy, name)
	var r Ref
	switch g.Type {
	case model.URLTest, model.Fallback, model.LoadBalance:
		r, ok = c.balancer(g)
	case model.Chain:
		r, ok = c.chain(g)
	default:
		ok = false
		for _, m := range g.Members {
			if r, ok = c.ref(m); ok {
				break
			}
		}
		if ok && len(g.Members) > 1 {
			c.note(g, "select_first")
		}
	}
	if ok {
		c.refs[name] = r
	}
	return r, ok
}

// outbounds are the outbounds a member is made of: itself, or the members of a group (a
// balancer selects outbounds, not balancers); none for a policy that refuses.
func (c *converter) outbounds(name string, seen map[string]bool) []string {
	switch {
	case c.tags[name]:
		return []string{name}
	case name == "DIRECT":
		return []string{directTag}
	}
	g, ok := c.byName[name]
	if !ok || seen[name] {
		return nil
	}
	seen[name] = true
	if g.Type == model.Chain {
		if r, ok := c.ref(name); ok && !r.Balancer {
			return []string{r.Tag}
		}
		return nil
	}
	var out []string
	for _, m := range g.Members {
		out = append(out, c.outbounds(m, seen)...)
	}
	return out
}

func (c *converter) balancer(g model.Group) (Ref, bool) {
	var sel []string
	flat := false
	for _, m := range g.Members {
		_, group := c.byName[m]
		flat = flat || group
		for _, o := range c.outbounds(m, map[string]bool{g.Name: true}) {
			if !slices.Contains(sel, o) {
				sel = append(sel, o)
			}
		}
	}
	if len(sel) == 0 {
		return Ref{}, false
	}
	b := Balancer{Tag: g.Name, Selector: sel, Strategy: Strategy{Type: routing.XrayLeastPing}}
	switch g.Type {
	case model.Fallback:
		b.FallbackTag = sel[0]
		c.note(g, "fallback_fastest")
	case model.LoadBalance:
		b.Strategy.Type = routing.XrayRandom
		if g.Strategy == "round-robin" {
			b.Strategy.Type = routing.XrayRoundRobin
		} else {
			c.note(g, "balance_random") // xray hashes nothing: no server per site or per user
		}
	}
	if g.Type != model.LoadBalance && c.s.Strategy != "" {
		b.Strategy.Type = c.s.Strategy
	}
	if flat {
		c.note(g, "balance_flat")
	}
	if b.Strategy.Type == routing.XrayLeastLoad {
		set := map[string]any{}
		if c.s.Expected > 0 {
			set["expected"] = c.s.Expected
		}
		if c.s.MaxRTT > 0 {
			set["maxRTT"] = strconv.Itoa(c.s.MaxRTT) + "ms"
		}
		if c.s.Tolerance > 0 {
			set["tolerance"] = c.s.Tolerance
		}
		if len(set) > 0 {
			b.Strategy.Settings = set
		}
	}
	c.out.Balancers = append(c.out.Balancers, b)
	return Ref{Tag: g.Name, Balancer: true}, true
}

// chain makes each hop after the first dial through the one before it: the dialer of the
// outbounds the hop is made of. A dialer is an outbound: a hop before another that is a
// balancer dials through its first outbound.
func (c *converter) chain(g model.Group) (Ref, bool) {
	if len(g.Members) == 0 {
		return Ref{}, false
	}
	for _, m := range g.Members {
		if _, ok := c.ref(m); !ok {
			return Ref{}, false // a hop xray lacks breaks the chain, as in Citrus
		}
	}
	for i := 1; i < len(g.Members); i++ {
		prev, _ := c.ref(g.Members[i-1])
		if prev.Tag == directTag {
			continue
		}
		if prev.Balancer {
			list := c.outbounds(g.Members[i-1], map[string]bool{})
			if len(list) == 0 {
				return Ref{}, false
			}
			prev = Ref{Tag: list[0]}
			c.note(g, "chain_group_hop")
		}
		for _, o := range c.outbounds(g.Members[i], map[string]bool{}) {
			if d, ok := c.out.Dialers[o]; ok && d != prev.Tag {
				c.note(g, "chain_dialer_taken")
				continue
			}
			if o != directTag {
				c.out.Dialers[o] = prev.Tag
			}
		}
	}
	return c.ref(g.Members[len(g.Members)-1])
}

// observe is how the balancers' outbounds are tested, as b says: the observatory for
// leastPing, the burst one for leastLoad.
func (c *converter) observe(b routing.Balance) {
	every := strconv.Itoa(b.Interval) + "s"
	for _, x := range c.out.Balancers {
		switch x.Strategy.Type {
		case routing.XrayLeastPing:
			c.out.Observatory = &Observatory{ProbeURL: b.URL, ProbeInterval: every, EnableConcurrency: true}
		case routing.XrayLeastLoad:
			c.out.BurstObservatory = &BurstObservatory{PingConfig: PingConfig{Destination: b.URL, Interval: every, Sampling: c.s.Sampling}}
		}
	}
}
