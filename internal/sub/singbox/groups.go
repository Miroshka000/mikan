package singbox

import (
	"fmt"
	"maps"
	"slices"
	"strconv"

	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
	"mikan/internal/sub/routing"
)

// The core's name in the notes of the admin panel's preview.
const core = "singbox"

// urltestIdle is sing-box's idle timeout of a urltest group in seconds when none is given.
const urltestIdle = 30 * 60

// note is what sing-box made of the group: a code of model.Note, the preview's words.
func note(group, code string) model.Note { return model.Note{Core: core, Subject: group, Code: code} }

// converted are the groups as sing-box carries them.
type converted struct {
	outbounds []map[string]any
	// copies are the outbounds of the chains' hops past the entry: each a proxy's outbound
	// once more, dialing through the hop before (detour). The proxy's own outbound dials
	// through nothing, wherever else it goes.
	copies []hopCopy
	notes  []model.Note
}

// hopCopy is the outbound Of once more, dialing through the outbound Dialer. Its tag is Of's
// after Prefix, chain<n>-hop<k>-, as xray tags its copies (xray.Copy).
type hopCopy struct {
	Prefix, Of, Dialer string
}

// Tag is the copy's outbound tag.
func (x hopCopy) Tag() string { return x.Prefix + x.Of }

// convertGroups are the groups as sing-box outbounds: a select as a selector, a url-test as
// an urltest. What sing-box has no group for becomes the nearest with a note: a fallback or
// a load-balance an urltest, the fastest that answers; a chain a selector of its exit under
// the chain's name, each hop past the entry made of copies dialing through the hop before
// it (chainer). A member sing-box has no outbound for goes (tags are the proxies'), a group
// left with none goes too, and so do its mentions; a chain goes with any hop of it.
func convertGroups(groups []model.Group, tags map[string]bool) converted {
	byName := map[string]model.Group{}
	alive := map[string]bool{}
	for _, g := range groups {
		byName[g.Name] = g
		alive[g.Name] = true
	}
	member := func(s string) (string, bool) {
		switch {
		case s == "DIRECT":
			return "direct", true
		case tags[s] || alive[s]:
			return s, true
		}
		return "", false // REJECT, PASS, a proxy sing-box cannot carry, a closed group
	}
	for changed := true; changed; {
		changed = false
		for _, g := range groups {
			if !alive[g.Name] {
				continue
			}
			ok := false
			for i, s := range g.Members {
				_, y := member(s)
				if g.Type == model.Chain {
					// Every hop, DIRECT only as the first, and the chain is not a hop of its own.
					if ok = y && s != g.Name && (s != "DIRECT" || i == 0); !ok {
						break
					}
					continue
				}
				if y && s != g.Name {
					ok = true
					break
				}
			}
			if !ok {
				alive[g.Name], changed = false, true
			}
		}
	}
	var out converted
	c := chainer{byName: byName, byTag: map[string]map[string]any{}, copied: map[string]bool{}, busy: map[string]bool{}, out: &out}
	var chains []model.Group
	for _, g := range groups {
		if !alive[g.Name] {
			continue
		}
		var list []string
		seen := map[string]bool{}
		for _, s := range g.Members {
			if t, ok := member(s); ok && !seen[t] {
				seen[t] = true
				list = append(list, t)
			}
		}
		o := map[string]any{"tag": g.Name, "outbounds": list}
		switch g.Type {
		case model.URLTest, model.Fallback, model.LoadBalance:
			o["type"] = "urltest"
			if g.URL != "" {
				o["url"] = g.URL
			}
			if g.Interval > 0 {
				o["interval"] = strconv.Itoa(g.Interval) + "s"
				// sing-box refuses an interval longer than the idle timeout (30 minutes by
				// default), and the panel takes up to a day.
				if g.Interval > urltestIdle {
					o["idle_timeout"] = strconv.Itoa(2*g.Interval) + "s"
				}
			}
			if g.Tolerance > 0 {
				o["tolerance"] = g.Tolerance
			}
			switch g.Type {
			case model.Fallback:
				out.notes = append(out.notes, note(g.Name, "fallback_fastest"))
			case model.LoadBalance:
				out.notes = append(out.notes, note(g.Name, "balance_fastest"))
			}
		case model.Chain:
			chains = append(chains, g)
			o["type"] = "selector" // of its exit, once the hops are made
		default:
			o["type"] = "selector"
		}
		c.byTag[g.Name] = o
		out.outbounds = append(out.outbounds, o)
	}
	for _, g := range chains {
		c.n++
		c.hop = 1 // the entry, which is no copy
		c.byTag[g.Name]["outbounds"] = []string{c.cascade(g, nil)}
	}
	out.outbounds = append(out.outbounds, c.groups...)
	return out
}

// chainer makes the chains' hops past the entry, each of copies of its outbounds (hopCopy)
// and of its groups over them: a proxy dials through a hop only in the chain, its own
// outbound in the other groups through nothing. A chain as a hop is entered by its first
// hop, as model.Entries does.
type chainer struct {
	byName map[string]model.Group
	byTag  map[string]map[string]any // the groups' outbounds, of their members alive
	n, hop int                       // the chain being made, and its hops so far
	copied map[string]bool
	busy   map[string]bool // groups being copied: a group in itself is no member
	groups []map[string]any
	out    *converted
}

// dial is a hop dialed through the outbound tag, its copies tagged prefix and their own tags.
type dial struct{ prefix, tag string }

// cascade is the tag the chain g goes out of, its exit: each hop past the entry dials
// through the one before it (none past DIRECT), the entry through v (nil for none).
func (c *chainer) cascade(g model.Group, v *dial) string {
	prev := ""
	for i, m := range g.Members {
		if i > 0 {
			v = nil
			if prev != "direct" {
				c.hop++
				v = &dial{prefix: fmt.Sprintf("chain%d-hop%d-", c.n, c.hop), tag: prev}
			}
		}
		prev = c.through(m, v)
	}
	return prev
}

// through is the tag of the member m as a hop dialing through v: a copy of a proxy, a copy
// of a group over copies of its members, a chain's exit; m itself when v is nil, and DIRECT
// dials through nothing. Empty for a group in itself.
func (c *chainer) through(m string, v *dial) string {
	if m == "DIRECT" || m == "direct" {
		return "direct"
	}
	if v == nil {
		return m
	}
	tag := v.prefix + m
	if c.copied[tag] {
		return tag
	}
	g, ok := c.byName[m]
	if !ok { // a proxy
		c.copied[tag] = true
		c.out.copies = append(c.out.copies, hopCopy{Prefix: v.prefix, Of: m, Dialer: v.tag})
		return tag
	}
	if c.busy[m] {
		return ""
	}
	c.busy[m] = true
	defer delete(c.busy, m)
	if g.Type == model.Chain {
		return c.cascade(g, v)
	}
	var list []string
	for _, s := range c.byTag[m]["outbounds"].([]string) {
		if t := c.through(s, v); t != "" && !slices.Contains(list, t) {
			list = append(list, t)
		}
	}
	o := maps.Clone(c.byTag[m])
	o["tag"], o["outbounds"] = tag, list
	c.copied[tag] = true
	c.groups = append(c.groups, o)
	return tag
}

// Notes are what sing-box makes of the groups of p's profile it cannot write as they are
// (convertGroups), for the admin panel's preview: of the own Clash profile t, or of the
// built-in groups. An own sing-box template has its groups as the admin wrote them: none.
func Notes(p profile.Profile, g model.GroupNames, m routing.Mode, t model.Template) []model.Note {
	if t.Core == model.CoreSingBox {
		return nil
	}
	pl, err := planOf(p, g, m, t)
	if err != nil {
		return nil
	}
	tags := map[string]bool{"direct": true}
	for _, x := range pl.proxies {
		if o, ok := singboxOutbound(x); ok {
			tags[str(o["tag"])] = true
		}
	}
	return convertGroups(pl.groups, tags).notes
}

// Dropped are what sing-box leaves out of p's profile as Render makes it (config), for the
// admin panel's preview: the proxies it cannot carry (proxy_unsupported), the rules of a
// condition it has no counterpart for or to a group it has none of (rule_unsupported), the
// lists it cannot read (list_unsupported) and the DNS policies it cannot write
// (dns_policy_unsupported); a policy's key that loses only some of its names is not told.
// Nothing is downloaded: a list sing-box could read is taken as
// read, whether or not it would download. An own sing-box template is as the admin wrote it:
// none.
func Dropped(p profile.Profile, g model.GroupNames, m routing.Mode, t model.Template) []model.Note {
	if t.Core == model.CoreSingBox {
		return nil
	}
	pl, err := planOf(p, g, m, t)
	if err != nil {
		return nil
	}
	sets := ruleSets{preview: true}
	config(pl, &sets)
	return sets.dropped
}
