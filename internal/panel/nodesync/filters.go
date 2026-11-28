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
	folders := map[int64][]int64{}
	tariffs := map[int64][]int64{}
	for _, user := range users {
		if user.FolderID.Valid {
			folders[user.FolderID.Int64] = append(folders[user.FolderID.Int64], user.ID)
		}
		if user.TariffID.Valid {
			tariffs[user.TariffID.Int64] = append(tariffs[user.TariffID.Int64], user.ID)
		}
	}
	for _, rule := range cfg.Egress.Rules {
		selected := map[int64]bool{}
		for _, id := range rule.Users {
			selected[id] = true
		}
		for _, folder := range rule.Folders {
			for _, id := range folders[folder] {
				selected[id] = true
			}
		}
		for _, tariff := range rule.Tariffs {
			for _, id := range tariffs[tariff] {
				selected[id] = true
			}
		}
		r := nodeapi.ScopedEgress{Kind: rule.Kind, Value: rule.Value, Scope: rule.Scope}
		for id := range selected {
			r.Slots = append(r.Slots, slots[id]...)
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
