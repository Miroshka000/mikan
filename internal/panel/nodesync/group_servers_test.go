package nodesync

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// A group's servers reach the node as a tariff's do, and the node holds them, not only the
// subscription: a client of a group that chose one inbound here gets in only by it, a client
// of a group with nothing here is turned away, the tariff's group reaches its clients without
// one of their own, a client's own group wins over its tariff's, the card's own protocols
// over both, and a deleted group lets its clients back to their tariffs' servers.
func TestGroupServersOnTheNode(t *testing.T) {
	s, _, st, users, _ := setup(t)
	ctx := context.Background()
	q := st.Q
	ins, _ := q.ListInbounds(ctx)
	if len(ins) < 2 {
		t.Fatalf("the seeded node needs two inbounds: %d", len(ins))
	}
	tariffs, _ := q.ListTariffs(ctx)
	tr := tariffs[1]
	a, _ := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tr.ID})
	b, _ := users.Create(ctx, domain.CreateInput{Name: "b", TariffID: tr.ID})
	group := func(name string, servers *domain.ServersInput) int64 {
		t.Helper()
		g, err := q.CreateGroup(ctx, db.CreateGroupParams{Name: name, Color: "gray", CreatedAt: 1})
		if err != nil {
			t.Fatal(err)
		}
		if servers != nil {
			if err := st.Tx(ctx, func(q *db.Queries) error { return domain.SetGroupServers(ctx, q, g.ID, *servers) }); err != nil {
				t.Fatal(err)
			}
		}
		return g.ID
	}
	join := func(user, group int64) {
		t.Helper()
		var err error
		if group == 0 {
			err = q.ClearUserGroup(ctx, user)
		} else {
			err = q.SetUserGroup(ctx, db.SetUserGroupParams{UserID: user, GroupID: group})
		}
		if err != nil {
			t.Fatal(err)
		}
		s.m.PoliciesChanged() // what the API says after a client's group changes
	}
	one := group("one", &domain.ServersInput{Inbounds: []int64{ins[1].ID}})
	free := group("free", nil)

	join(a.ID, one)
	if p := policyOf(t, s, a.ID); !p.Allowed || !slices.Equal(p.Inbounds, []string{ins[1].Name}) {
		t.Fatalf("the group's one inbound here: %+v", p)
	}
	if p := policyOf(t, s, b.ID); !p.Allowed || len(p.Inbounds) != 0 {
		t.Fatalf("a client in no group keeps the tariff's every server: %+v", p)
	}

	other, err := q.CreateNode(ctx, db.CreateNodeParams{Name: "B", Address: "203.0.113.2:25305", PublicHost: "203.0.113.2", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := group("elsewhere", &domain.ServersInput{Nodes: []int64{other.ID}})
	join(a.ID, elsewhere)
	if p := policyOf(t, s, a.ID); p.Allowed {
		t.Fatalf("a group with nothing here: the node turns its clients away: %+v", p)
	}

	// The tariff's group reaches b, who has none of its own; a's own group wins over it.
	if err := domain.SetTariffGroup(ctx, q, tr.ID, one); err != nil {
		t.Fatal(err)
	}
	join(a.ID, free)
	if p := policyOf(t, s, b.ID); !p.Allowed || !slices.Equal(p.Inbounds, []string{ins[1].Name}) {
		t.Fatalf("the tariff's group: %+v", p)
	}
	if p := policyOf(t, s, a.ID); !p.Allowed || len(p.Inbounds) != 0 {
		t.Fatalf("an own group that leaves the servers to the tariff wins over the tariff's group: %+v", p)
	}

	raw := "[" + strconv.FormatInt(ins[0].ID, 10) + "]"
	if _, err := st.DB.ExecContext(ctx, "UPDATE users SET inbounds = $1 WHERE id = $2", raw, b.ID); err != nil {
		t.Fatal(err)
	}
	s.m.PoliciesChanged()
	if p := policyOf(t, s, b.ID); !p.Allowed || !slices.Equal(p.Inbounds, []string{ins[0].Name}) {
		t.Fatalf("the client's own protocols stay over its group's: %+v", p)
	}
	if _, err := st.DB.ExecContext(ctx, "UPDATE users SET inbounds = NULL WHERE id = $1", b.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := q.DeleteGroup(ctx, one); err != nil {
		t.Fatal(err)
	}
	s.m.PoliciesChanged()
	if p := policyOf(t, s, b.ID); !p.Allowed || len(p.Inbounds) != 0 {
		t.Fatalf("a deleted group: back to the tariff's every server: %+v", p)
	}
}
