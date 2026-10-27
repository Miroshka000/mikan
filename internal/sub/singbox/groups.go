package singbox

import (
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
	// detours: a proxy's outbound to the one it dials through, of the chains.
	detours map[string]string
	notes   []model.Note
}

// convertGroups are the groups as sing-box outbounds: a select as a selector, a url-test as
// an urltest. What sing-box has no group for becomes the nearest with a note: a fallback or
// a load-balance an urltest, the fastest that answers; a chain the detour of each hop
// through the one before it, and a selector of the exit under the chain's name. A member
// sing-box has no outbound for goes (tags are the proxies'), a group left with none goes
// too, and so do its mentions; a chain goes with any hop of it.
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
	out := converted{detours: map[string]string{}}
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
			out.chain(g, byName, tags)
			exit, _ := member(g.Members[len(g.Members)-1])
			o["type"], o["outbounds"] = "selector", []string{exit}
		default:
			o["type"] = "selector"
		}
		out.outbounds = append(out.outbounds, o)
	}
	return out
}

// chain makes every hop of g after the first dial through the hop before it: the detour of
// the proxies the hop is entered by (model.Entries). A proxy that dials through another hop
// already keeps it.
func (c *converted) chain(g model.Group, byName map[string]model.Group, tags map[string]bool) {
	isProxy := func(s string) bool { return tags[s] }
	for i := 1; i < len(g.Members); i++ {
		prev := g.Members[i-1]
		if prev == "DIRECT" {
			continue // the first hop past the tunnel is no detour at all
		}
		for _, p := range model.Entries(g.Members[i], byName, isProxy) {
			if d, ok := c.detours[p]; ok && d != prev {
				c.notes = append(c.notes, note(g.Name, "chain_dialer_taken"))
				continue
			}
			c.detours[p] = prev
		}
	}
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
// (dns_policy_unsupported). Nothing is downloaded: a list sing-box could read is taken as
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
	sets := ruleSets{lists: pl.lists, used: map[string]map[string]any{}, preview: true}
	tags := map[string]bool{"direct": true}
	for _, x := range pl.proxies {
		if o, ok := singboxOutbound(x); ok {
			tags[str(o["tag"])] = true
		} else {
			sets.drop("proxy_unsupported", str(x["name"]))
		}
	}
	for _, gr := range convertGroups(pl.groups, tags).outbounds {
		tags[str(gr["tag"])] = true
	}
	// The rules as config takes them: up to the first MATCH.
	for _, r := range pl.rules {
		if len(r.When) == 0 {
			break
		}
		if _, ok := action(r.To, tags); !ok {
			sets.drop("rule_unsupported", r.Clash(r.To.Group))
			continue
		}
		sets.rule(r)
	}
	dnsOf(pl.dns, tags, &sets, "")
	return sets.dropped
}
