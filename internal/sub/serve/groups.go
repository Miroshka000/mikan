package serve

import "mikan/internal/sub/model"

// AppGroups is the JSON of X-Mikan-Groups. V changes only when an app that reads version 1
// would read the new one wrong; fields are only ever added.
type AppGroups struct {
	V int `json:"v"`
	// ResetsAt is when the traffic of the period starts over, the groups' with the main
	// traffic (unix seconds); absent when it never does.
	ResetsAt int64      `json:"resets_at,omitempty"`
	Groups   []AppGroup `json:"groups"`
}

// The modes of a group in AppGroup.Mode.
const (
	GroupOwn       = "own"       // a limit of its own apart from the main traffic
	GroupUnlimited = "unlimited" // apart from the main traffic, without a limit
	GroupMain      = "main"      // its servers spend the main traffic; the app only names them
)

// AppGroup is one server group of the subscription, in the order the admin panel lists them.
type AppGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Mode is GroupOwn, GroupUnlimited or GroupMain.
	Mode string `json:"mode" enum:"own,unlimited,main"`
	Used int64  `json:"used,omitempty"` // bytes of the period; not for "main"
	// Limit, Extra and Left are of an own limit alone: the base limit, what is left of the
	// traffic bought or given for the group, and what is left of both.
	Limit *int64 `json:"limit,omitempty"`
	Extra int64  `json:"extra,omitempty"`
	Left  *int64 `json:"left,omitempty"`
	// State is "exhausted" once an own limit has nothing left: its servers are out of the
	// profile until the period starts over or traffic is bought. "active" otherwise.
	State string `json:"state" enum:"active,exhausted"`
	// Proxies are the names of the group's servers in this very profile; null when the names
	// did not fit in the header.
	Proxies []string `json:"proxies"`
}

// named is g with the servers of each group named among ps, the proxies of the profile the
// app gets, and without the groups left with nothing to show: one on the main traffic with
// no servers to name, one apart without a limit with no servers and no traffic used. An own
// limit stays with no servers: the app must explain that it ran out.
func (g AppGroups) named(ps []model.Proxy) AppGroups {
	names := map[int64][]string{}
	for _, x := range ps {
		if x.Pool != 0 {
			names[x.Pool] = append(names[x.Pool], x.Name)
		}
	}
	groups := g.Groups
	g.Groups = nil
	for _, x := range groups {
		x.Proxies = names[x.ID]
		if x.Proxies == nil {
			x.Proxies = []string{} // no servers; null is for names that did not fit
		}
		if len(x.Proxies) == 0 && (x.Mode == GroupMain || x.Mode == GroupUnlimited && x.Used == 0) {
			continue
		}
		g.Groups = append(g.Groups, x)
	}
	return g
}
