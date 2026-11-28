package filters

import (
	"context"
	"slices"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/settings"
)

// Rule selects clients by ID, by client folder, or by tariff. Any selected group
// or client is enough for membership.
type Rule struct {
	Kind    string  `json:"kind" enum:"port,network,domain"`
	Value   string  `json:"value" maxLength:"1024"`
	Scope   string  `json:"scope" enum:"only,except"`
	Users   []int64 `json:"users,omitempty" maxItems:"1000"`
	Folders []int64 `json:"folders,omitempty" maxItems:"1000"`
	Tariffs []int64 `json:"tariffs,omitempty" maxItems:"1000"`
}

// The core limits the number of clauses in an expression. Keep the compiled domain
// exceptions below that limit even when every personal rule is an exception.
const MaxSiteOverrides = 500

// Site is an individual override created in the client card.
type Site struct {
	UserID int64  `json:"user_id"`
	Domain string `json:"domain"`
	Allow  bool   `json:"allow"`
}

func (e Egress) HasScoped() bool {
	return e.Enabled && (len(e.Rules) > 0 || len(e.Sites) > 0)
}

func (e *Egress) validateScoped() error {
	if len(e.Rules) > MaxItems || len(e.Sites) > MaxSiteOverrides {
		return ErrConfig
	}
	for i := range e.Rules {
		r := &e.Rules[i]
		value, ok := nodeapi.CanonEgress(r.Kind, r.Value)
		if !ok {
			return &Problem{List: "egress.rules", Index: i, Value: r.Kind}
		}
		r.Value = value
		if r.Scope != "only" && r.Scope != "except" || len(r.Users)+len(r.Folders)+len(r.Tariffs) == 0 {
			return &Problem{List: "egress.rules", Index: i, Value: r.Scope}
		}
		for _, ids := range []*[]int64{&r.Users, &r.Folders, &r.Tariffs} {
			if len(*ids) > MaxItems || slices.ContainsFunc(*ids, func(id int64) bool { return id < 1 }) {
				return &Problem{List: "egress.rules", Index: i, Value: r.Scope}
			}
			*ids = slices.Clone(*ids)
			slices.Sort(*ids)
			*ids = slices.Compact(*ids)
		}
	}
	seen := make(map[struct {
		user int64
		host string
	}]bool, len(e.Sites))
	for i := range e.Sites {
		site := &e.Sites[i]
		host, ok := nodeapi.CanonDomain(site.Domain)
		key := struct {
			user int64
			host string
		}{site.UserID, host}
		if !ok || site.UserID < 1 || seen[key] {
			return ErrConfig
		}
		seen[key] = true
		site.Domain = host
	}
	return nil
}

// DropUserSites removes the overrides of deleted clients in the same transaction.
func DropUserSites(ctx context.Context, set *settings.Settings, ids []int64) error {
	c, err := Load(ctx, set)
	if err != nil || len(c.Egress.Sites) == 0 {
		return err
	}
	before := len(c.Egress.Sites)
	c.Egress.Sites = slices.DeleteFunc(c.Egress.Sites, func(site Site) bool {
		return slices.Contains(ids, site.UserID)
	})
	if len(c.Egress.Sites) == before {
		return nil
	}
	return settings.Set(ctx, set, KeyConfig, c)
}
