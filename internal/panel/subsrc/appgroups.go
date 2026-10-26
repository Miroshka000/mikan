package subsrc

import (
	"context"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/model"
	"mikan/internal/panel/store/db"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/serve"
)

// Groups are the server groups of the subscription as the Mikan app shows them
// (serve.GroupsHeader), their servers named among ps. It reads what Inbounds found the user
// may use, which a fetch asks for first.
func (s *subscription) Groups(ctx context.Context, ps []submodel.Proxy) (serve.AppGroups, error) {
	rows, err := s.h.st.Q.ListUserPools(ctx, s.u.ID)
	if err != nil {
		return serve.AppGroups{}, err
	}
	pools, err := s.h.st.Q.ListTrafficPools(ctx)
	if err != nil {
		return serve.AppGroups{}, err
	}
	out := serve.AppGroups{V: 1, Groups: appGroupsOf(pools, rows, s.reach.grants, s.u.ID, s.reach.access.Groups(s.reach.all), ps)}
	if t, ok := domain.NextReset(s.u, s.h.now()); ok {
		out.ResetsAt = t.Unix()
	}
	return out, nil
}

// appGroupsOf lists the groups of pools the user reaches (reach: Access.Groups, an
// exhausted one included, which the app must explain) and is not shut out of: an own limit
// always, a group apart without a limit with servers in ps or traffic used, a group on the
// main traffic only with servers in ps to name. rows are the user's modes, left the grants.
func appGroupsOf(pools []db.TrafficPool, rows []db.UserPool, left domain.GrantsLeft, userID int64, reach map[int64]bool, ps []submodel.Proxy) []serve.AppGroup {
	names := map[int64][]string{}
	for _, x := range ps {
		if x.Pool != 0 {
			names[x.Pool] = append(names[x.Pool], x.Name)
		}
	}
	var out []serve.AppGroup
	for _, p := range pools {
		if !reach[p.ID] {
			continue
		}
		row, mode := domain.UserPoolOf(rows, p.ID)
		g := serve.AppGroup{ID: p.ID, Name: p.Name, Mode: mode, State: "active", Proxies: names[p.ID]}
		if g.Proxies == nil {
			g.Proxies = []string{}
		}
		used := row.UsedUp + row.UsedDown
		if mode == domain.PoolOwn && !row.TrafficLimit.Valid {
			mode = domain.PoolUnlimited // a limit of its own with no number: counted apart, never runs out
			g.Mode = mode
		}
		switch mode {
		case domain.PoolClosed:
			continue
		case domain.PoolMain:
			if len(g.Proxies) == 0 {
				continue
			}
		case domain.PoolUnlimited:
			if len(g.Proxies) == 0 && used == 0 {
				continue
			}
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
