package domain

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// What a client stands on comes of its group when the group chose servers, else of its
// tariff: the client's own group over its tariff's, a group that leaves the servers to the
// tariff no different from none, and the card's own protocols over all of it. A tariff's
// clients without a group of their own stand on the tariff's group; a group that is gone
// leaves its clients to their tariffs.
func TestGroupServers(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	q := st.Q
	tariffs, _ := q.ListTariffs(ctx)
	tr := tariffs[1]
	n, err := q.CreateNode(ctx, db.CreateNodeParams{Name: "A", Address: "A:25305", PublicHost: "203.0.113.1", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	inbound := func(name string, port int) db.Inbound {
		t.Helper()
		in, err := q.CreateInbound(ctx, db.CreateInboundParams{NodeID: n.ID, Name: name, Preset: "vless_reality_xhttp", Port: fmt.Sprint(port), Config: "{}", CreatedAt: 1, UpdatedAt: 1})
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	a1, a2 := inbound("a1", 21001), inbound("a2", 21002)
	group := func(name string, servers *ServersInput) int64 {
		t.Helper()
		g, err := q.CreateGroup(ctx, db.CreateGroupParams{Name: name, Color: "gray", CreatedAt: 1})
		if err != nil {
			t.Fatal(err)
		}
		if servers != nil {
			if err := st.Tx(ctx, func(q *db.Queries) error { return SetGroupServers(ctx, q, g.ID, *servers) }); err != nil {
				t.Fatal(err)
			}
		}
		return g.ID
	}
	only1 := group("only a1", &ServersInput{Inbounds: []int64{a1.ID}})
	asTariff := group("as tariff", nil)
	u, err := users.Create(ctx, CreateInput{Name: "u", TariffID: tr.ID})
	if err != nil {
		t.Fatal(err)
	}
	stands := func() (Servers, int64) {
		t.Helper()
		stand, err := UserServers(ctx, q, u)
		if err != nil {
			t.Fatal(err)
		}
		s, g := stand.Servers, stand.Group
		// The tariff here has every server: only a group's own servers are chosen ones.
		if stand.ByGroup == s.All {
			t.Fatalf("the servers are the group's own exactly when they are chosen: %+v", stand)
		}
		// The nodes read every client at once: the book must say what one client's read does.
		b, err := LoadServerBook(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if bg, bs := b.GroupOf(u), b.Of(u); bg != g || bs.All != s.All || bs.Has(a1) != s.Has(a1) || bs.Has(a2) != s.Has(a2) {
			t.Fatalf("the book: group %d %+v, one client's read: group %d %+v", bg, bs, g, s)
		}
		return s, g
	}
	if s, g := stands(); g != 0 || !s.All {
		t.Fatalf("no group: the tariff's servers, every one: group %d %+v", g, s)
	}

	if err := q.SetUserGroup(ctx, db.SetUserGroupParams{UserID: u.ID, GroupID: only1}); err != nil {
		t.Fatal(err)
	}
	if s, g := stands(); g != only1 || s.All || !s.Has(a1) || s.Has(a2) {
		t.Fatalf("the group's own servers over the tariff's: group %d %+v", g, s)
	}
	if acc := AccessOf(u, mustServers(t, ctx, q, u)); !acc.Usable(a1) || acc.Usable(a2) {
		t.Fatal("the access follows the group's servers")
	}

	// The card's own protocols stay over the group's.
	own := u
	own.Inbounds = sql.NullString{String: fmt.Sprintf("[%d]", a2.ID), Valid: true}
	if acc := AccessOf(own, mustServers(t, ctx, q, own)); acc.Usable(a1) || !acc.Usable(a2) {
		t.Fatal("the card's own protocols stay over the group's servers")
	}

	// A tariff's group reaches its clients without a group of their own; their own wins.
	if err := SetTariffGroup(ctx, q, tr.ID, only1); err != nil {
		t.Fatal(err)
	}
	if err := q.SetUserGroup(ctx, db.SetUserGroupParams{UserID: u.ID, GroupID: asTariff}); err != nil {
		t.Fatal(err)
	}
	if s, g := stands(); g != asTariff || !s.All {
		t.Fatalf("an own group that leaves the servers to the tariff: group %d %+v", g, s)
	}
	if err := q.ClearUserGroup(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if s, g := stands(); g != only1 || !s.Has(a1) || s.Has(a2) {
		t.Fatalf("the tariff's group: group %d %+v", g, s)
	}
	if s, err := TariffClientServers(ctx, q, tr); err != nil || s.All || !s.Has(a1) || s.Has(a2) {
		t.Fatalf("a client of the tariff stands on its group's servers: %+v %v", s, err)
	}
	if s, err := TariffServers(ctx, q, sql.NullInt64{Int64: tr.ID, Valid: true}); err != nil || !s.All {
		t.Fatalf("the tariff's own servers stay its own: %+v %v", s, err)
	}

	// A group deleted lets its clients and tariffs go.
	if _, err := q.DeleteGroup(ctx, only1); err != nil {
		t.Fatal(err)
	}
	if s, g := stands(); g != 0 || !s.All {
		t.Fatalf("a deleted group: back to the tariff: group %d %+v", g, s)
	}

	// A group named that does not exist is refused by its field.
	err = st.Tx(ctx, func(q *db.Queries) error { return LockGroups(ctx, q, "group_id", asTariff, only1) })
	if fe, ok := err.(*FieldError); !ok || fe.Field != "group_id" || fe.Code != "group_not_found" {
		t.Fatalf("a deleted group locked: %v", err)
	}
	if err := st.Tx(ctx, func(q *db.Queries) error { return LockGroups(ctx, q, "group_id", asTariff) }); err != nil {
		t.Fatalf("a group there is: %v", err)
	}
}

func mustServers(t *testing.T, ctx context.Context, q *db.Queries, u db.User) Servers {
	t.Helper()
	stand, err := UserServers(ctx, q, u)
	if err != nil {
		t.Fatal(err)
	}
	return stand.Servers
}

// An address goes by group to the clients of its groups alone (only), to every client but
// theirs (never), or to everyone; a client in no group is of none of them, and an address
// "only" for no group is shown to nobody.
func TestHostShowsGroup(t *testing.T) {
	for _, c := range []struct {
		mode   string
		groups []int64
		group  int64
		want   bool
	}{
		{"", nil, 0, true},
		{"", nil, 7, true},
		{HostGroupsOnly, []int64{7, 9}, 7, true},
		{HostGroupsOnly, []int64{7, 9}, 8, false},
		{HostGroupsOnly, []int64{7, 9}, 0, false},
		{HostGroupsOnly, nil, 7, false},
		{HostGroupsNever, []int64{7}, 7, false},
		{HostGroupsNever, []int64{7}, 8, true},
		{HostGroupsNever, []int64{7}, 0, true},
	} {
		if got := HostShowsGroup(c.mode, c.groups, c.group); got != c.want {
			t.Errorf("%q %v to group %d: %v, want %v", c.mode, c.groups, c.group, got, c.want)
		}
	}
}

// A host's groups are saved in id order, once each, and none without a mode; a mode the
// panel does not know, one with no group and a group id that cannot be are refused by field.
func TestHostGroupsChecked(t *testing.T) {
	if got := hostGroups(HostGroupsOnly, []int64{9, 7, 9}); fmt.Sprint(got) != "[7 9]" {
		t.Fatalf("sorted, each once: %v", got)
	}
	if got := hostGroups("", []int64{9}); len(got) != 0 {
		t.Fatalf("no mode, no groups: %v", got)
	}
	for _, c := range []struct {
		mode   string
		groups []int64
		field  string
		code   string
	}{
		{"maybe", []int64{1}, "group_mode", "host_group_mode"},
		{HostGroupsOnly, nil, "groups", "host_groups_empty"},
		{HostGroupsNever, []int64{0}, "groups", "group_not_found"},
	} {
		err := checkHostGroups(c.mode, c.groups)
		e, ok := err.(*EditError)
		if !ok || e.Field != c.field || e.Err.Code != c.code {
			t.Errorf("%q %v: %v, want %s at %s", c.mode, c.groups, err, c.code, c.field)
		}
	}
	if err := checkHostGroups(HostGroupsNever, []int64{3}); err != nil {
		t.Fatalf("a mode with a group: %v", err)
	}
}
