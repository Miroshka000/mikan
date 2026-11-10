package domain

import (
	"context"
	"slices"
	"testing"
	"time"

	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

func realityNames(t *testing.T, config string) (string, []string) {
	t.Helper()
	tpl, err := proto.Parse(config)
	if err != nil {
		t.Fatal(err)
	}
	return presets.Dest(tpl)
}

// A change of the panel's domain moves the self-steal of the panel's own node to the new
// name, the old one kept after it for the clients that have not fetched their subscription
// since; a site of the server's network stays; a relay the admin pointed at the panel's
// HTTPS follows. A former name let go leaves the inbounds it is not the first name of.
func TestFollowPanelDomain(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	q := st.Q
	if err := settings.Set(ctx, settings.New(q), settings.KeyPanelPort, 2053); err != nil {
		t.Fatal(err)
	}
	inbounds := NewInbounds(st, nil, func() time.Time { return now })
	ins, err := q.ListNodeInbounds(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var reality []db.Inbound
	for _, in := range ins {
		if dest, _ := realityNames(t, in.Config); dest != "" {
			reality = append(reality, in)
		}
	}
	if len(reality) < 2 {
		t.Fatalf("seeded REALITY protocols: %d", len(reality))
	}
	own, near := reality[0], reality[1]
	ownDest, ownName, nearDest, nearName := "127.0.0.1:2053", "a.example.com", "203.0.113.5:443", "near.example"
	if _, _, err := inbounds.Update(ctx, own.ID, InboundPatch{Dest: &ownDest, ServerName: &ownName}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbounds.Update(ctx, near.ID, InboundPatch{Dest: &nearDest, ServerName: &nearName}); err != nil {
		t.Fatal(err)
	}
	local, err := q.GetNode(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRelay(ctx, q, local, now, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := SetRelayTarget(ctx, q, 1, ownDest, ownName, false); err != nil {
		t.Fatal(err)
	}

	changed, err := inbounds.FollowPanelDomain(ctx, "A.example.com", "b.example.com")
	if err != nil || len(changed) != 1 || changed[0].ID != own.ID {
		t.Fatalf("changed %d: %v", len(changed), err)
	}
	if dest, names := realityNames(t, changed[0].Config); dest != ownDest || !slices.Equal(names, []string{"b.example.com", "a.example.com"}) {
		t.Fatalf("self-steal: %s %v", dest, names)
	}
	row, err := q.GetInbound(ctx, near.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dest, names := realityNames(t, row.Config); dest != nearDest || !slices.Equal(names, []string{nearName}) {
		t.Fatalf("a site of the server's network moved: %s %v", dest, names)
	}
	r, err := q.GetNodeRelay(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dest, sni := RelayTargetOf(r); dest != ownDest || sni != "b.example.com" || r.AutoSni != 0 {
		t.Fatalf("the admin's relay: %s %s auto %d", dest, sni, r.AutoSni)
	}
	if changed, err := inbounds.FollowPanelDomain(ctx, "a.example.com", "b.example.com"); err != nil || len(changed) != 0 {
		t.Fatalf("again: %d %v", len(changed), err)
	}

	if changed, err := inbounds.ForgetPanelName(ctx, "a.example.com"); err != nil || len(changed) != 1 {
		t.Fatalf("forget: %d %v", len(changed), err)
	}
	row, _ = q.GetInbound(ctx, own.ID)
	if _, names := realityNames(t, row.Config); !slices.Equal(names, []string{"b.example.com"}) {
		t.Fatalf("after the old name was let go: %v", names)
	}
	if changed, err := inbounds.ForgetPanelName(ctx, "b.example.com"); err != nil || len(changed) != 0 {
		t.Fatalf("the only name: %d %v", len(changed), err)
	}
}
