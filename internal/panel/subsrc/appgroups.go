package subsrc

import (
	"context"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/model"
	"mikan/internal/panel/store/db"
	"mikan/internal/sub/serve"
)

// Groups are the server groups of the subscription as the Mikan app shows them
// (serve.GroupsHeader), without their servers: the handler names those in the profile. It
// reads what Inbounds found the user may use, which a fetch asks for first.
func (s *subscription) Groups(ctx context.Context) (serve.AppGroups, error) {
	rows, err := s.h.st.Q.ListUserPools(ctx, s.u.ID)
	if err != nil {
		return serve.AppGroups{}, err
	}
	pools, err := s.h.st.Q.ListTrafficPools(ctx)
	if err != nil {
		return serve.AppGroups{}, err
	}
	out := serve.AppGroups{V: 1, Groups: appGroupsOf(pools, rows, s.reach.grants, s.u.ID, s.reach.access.Groups(s.reach.all))}
	if t, ok := domain.NextReset(s.u, s.h.now()); ok {
		out.ResetsAt = t.Unix()
	}
	return out, nil
}

// appGroupsOf lists the groups of pools the user reaches (reach: Access.Groups, an
// exhausted one included, which the app must explain) and is not shut out of, with the
// traffic of the period: an own limit, a group apart without a limit, a group on the main
// traffic. rows are the user's modes, left the grants.
func appGroupsOf(pools []db.TrafficPool, rows []db.UserPool, left domain.GrantsLeft, userID int64, reach map[int64]bool) []serve.AppGroup {
	var out []serve.AppGroup
	for _, p := range pools {
		if !reach[p.ID] {
			continue
		}
		row, mode := domain.UserPoolOf(rows, p.ID)
		if mode == domain.PoolOwn && !row.TrafficLimit.Valid {
			mode = domain.PoolUnlimited // a limit of its own with no number: counted apart, never runs out
		}
		g := serve.AppGroup{ID: p.ID, Name: p.Name, Mode: mode, State: "active"}
		used := row.UsedUp + row.UsedDown
		switch mode {
		case domain.PoolClosed:
			continue
		case domain.PoolUnlimited:
			g.Used = used
		case domain.PoolOwn:
			extra := left.Pool(userID, p.ID)
			limit, rest := row.TrafficLimit.Int64, model.TrafficLeft(row.TrafficLimit, used, extra)
			g.Used, g.Limit, g.Extra, g.Left = used, &limit, extra, &rest
			if domain.PoolExhausted(row, extra) {
				g.State = "exhausted"
			}
		}
		out = append(out, g)
	}
	return out
}
