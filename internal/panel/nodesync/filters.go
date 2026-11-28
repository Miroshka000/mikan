package nodesync

import (
	"slices"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/filters"
	"mikan/internal/panel/store/db"
)

// nodeFilters resolves audiences using the same users and devices as the node state's
// policies. The node only needs the names of the authenticated slots.
func nodeFilters(cfg filters.Config, users []db.User, owners []db.ListSlotUsersRow) *nodeapi.Filters {
	f := cfg.State()
	if !cfg.Egress.HasScoped() {
		return f
	}
	slots := make(map[int64][]string, len(users))
	for _, owner := range owners {
		slots[owner.UserID] = append(slots[owner.UserID], owner.SlotName)
	}
	for _, rule := range cfg.Egress.Rules {
		r := nodeapi.ScopedEgress{Kind: rule.Kind, Value: rule.Value, Scope: rule.Scope}
		for _, u := range users {
			if rule.Selects(u.ID, u.FolderID.Int64, u.TariffID.Int64) {
				r.Slots = append(r.Slots, slots[u.ID]...)
			}
		}
		slices.Sort(r.Slots)
		f.Egress.Scoped = append(f.Egress.Scoped, r)
	}
	for _, site := range cfg.Egress.Sites {
		names := slices.Clone(slots[site.UserID])
		if len(names) == 0 {
			continue
		}
		slices.Sort(names)
		scope := "only"
		if site.Allow {
			scope = "allow"
		}
		f.Egress.Scoped = append(f.Egress.Scoped, nodeapi.ScopedEgress{
			Kind: "domain", Value: site.Domain, Scope: scope, Slots: names,
		})
	}
	return f
}
