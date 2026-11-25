package domain

import (
	"slices"
	"strings"

	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// Inbounds behind a node's SNI router are told apart by the name a client asks for. Two that
// ask for one name are one too many unless the router tells them apart by key (byKey): a
// REALITY client is known by the key it made its ClientHello with, anyone else by the name
// alone, so a name may then hold any number of REALITY inbounds and one that is no REALITY
// one. Otherwise the router hands the name to one of them and the others get nothing.

// SNIClash is what an inbound's name shares with others behind the router.
type SNIClash struct {
	// With: the node's other inbounds that ask for one of its names.
	With []string
	// Fix: "by_key" when the router telling REALITY inbounds apart would do, "name" when one
	// of them needs a name of its own (two that are no REALITY ones share it).
	Fix string
}

// SNIClashes finds the inbounds of one node behind its router that the router cannot tell
// from others asking for the same name, by inbound id. byKey: the node's router tells REALITY
// inbounds that share a name apart.
func SNIClashes(inbounds []db.Inbound, nodeDomain string, byKey bool) map[int64]SNIClash {
	type member struct {
		in      db.Inbound
		reality bool
	}
	byName := map[string][]member{}
	for _, in := range inbounds {
		if in.Enabled == 0 {
			continue
		}
		t, err := proto.Parse(in.Config)
		if err != nil || !proto.SNI443(t) || !proto.CanSNI443(t) {
			continue
		}
		seen := map[string]bool{}
		for _, n := range proto.SNINames(t, nodeDomain) {
			n = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n), "."))
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			byName[n] = append(byName[n], member{in: in, reality: proto.Reality(t)})
		}
	}
	with := map[int64]map[string]bool{}
	fix := map[int64]string{}
	for _, group := range byName {
		if len(group) < 2 {
			continue
		}
		plain := 0
		for _, m := range group {
			if !m.reality {
				plain++
			}
		}
		if byKey && plain < 2 {
			continue
		}
		f := "by_key"
		if plain >= 2 {
			f = "name"
		}
		for _, m := range group {
			// With the key telling REALITY ones apart, only those that are no REALITY ones
			// stand in each other's way.
			if byKey && m.reality {
				continue
			}
			for _, o := range group {
				if o.in.ID == m.in.ID || byKey && o.reality {
					continue
				}
				if with[m.in.ID] == nil {
					with[m.in.ID] = map[string]bool{}
				}
				with[m.in.ID][o.in.Name] = true
			}
			if fix[m.in.ID] != "name" {
				fix[m.in.ID] = f
			}
		}
	}
	out := make(map[int64]SNIClash, len(with))
	for id, names := range with {
		c := SNIClash{Fix: fix[id]}
		for n := range names {
			c.With = append(c.With, n)
		}
		slices.Sort(c.With)
		out[id] = c
	}
	return out
}
