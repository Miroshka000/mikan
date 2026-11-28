package nodesync

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/filters"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

func TestFilterAudiencesResolveToAllClientSlots(t *testing.T) {
	cfg := filters.Default()
	cfg.Egress.Enabled = true
	cfg.Egress.Rules = []filters.Rule{
		{Kind: "domain", Value: "example.com", Scope: "only", Folders: []int64{3}, Tariffs: []int64{9}, Users: []int64{1, 4}},
		{Kind: "network", Value: "203.0.113.0/24", Scope: "except", Users: []int64{5}},
	}
	cfg.Egress.Sites = []filters.Site{{UserID: 1, Domain: "sites.example", Allow: true}}
	users := []db.User{
		{ID: 1, FolderID: sql.NullInt64{Int64: 3, Valid: true}},
		{ID: 2, TariffID: sql.NullInt64{Int64: 9, Valid: true}},
		{ID: 3},
		{ID: 4},
		{ID: 5},
	}
	slots := []db.ListSlotUsersRow{
		{UserID: 1, SlotName: "s1-phone"}, {UserID: 1, SlotName: "s1"},
		{UserID: 2, SlotName: "s2"}, {UserID: 3, SlotName: "s3"},
		{UserID: 4, SlotName: "s4"}, {UserID: 5, SlotName: "s5"},
	}
	rules := nodeFilters(cfg, users, slots).Egress.Scoped
	if len(rules) != 3 ||
		!slices.Equal(rules[0].Slots, []string{"s1", "s1-phone", "s2", "s4"}) ||
		!slices.Equal(rules[1].Slots, []string{"s5"}) ||
		!slices.Equal(rules[2].Slots, []string{"s1", "s1-phone"}) {
		t.Fatalf("wrong slots by audience: %+v", rules)
	}
}

func TestTrafficFilterPolicyChanges(t *testing.T) {
	s, node, st, users, now := setup(t)
	ctx := context.Background()
	tariffs, err := st.Q.ListTariffs(ctx)
	if err != nil || len(tariffs) < 3 {
		t.Fatalf("list plans: %v", err)
	}
	client, err := users.Create(ctx, domain.CreateInput{Name: "client", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	cfg := filters.Default()
	cfg.Egress.Enabled = true
	cfg.Egress.Rules = []filters.Rule{{Kind: "domain", Value: "example.com", Scope: "only", Tariffs: []int64{tariffs[1].ID}}}
	if err := settings.Set(ctx, settings.New(st.Q), filters.KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	s.m.SlotsChanged()
	s.applyState(ctx)
	if len(node.applied) != 1 || len(node.applied[0].Policies) != 1 || len(node.applied[0].Filters.Egress.Scoped) != 1 {
		t.Fatalf("initial state: %+v", node.applied)
	}

	quota := node.applied[0].Policies[0].QuotaRemaining + domain.GiB
	if _, err := users.Update(ctx, client.ID, domain.Patch{TrafficLimit: &quota}); err != nil {
		t.Fatal(err)
	}
	s.applyPolicyChange(ctx)
	if len(node.applied) != 1 || len(node.policies) != 1 || node.policies[0][0].QuotaRemaining != quota {
		t.Fatalf("quota change with the same routes: %d states and %+v", len(node.applied), node.policies)
	}

	if _, err := users.Update(ctx, client.ID, domain.Patch{TariffID: &tariffs[2].ID}); err != nil {
		t.Fatal(err)
	}
	rejected := &flaky{fakeNode: node, applyErr: errRefused}
	s.node = rejected
	s.applyPolicyChange(ctx)
	if len(node.applied) != 1 || len(node.policies) != 2 {
		t.Fatalf("a refused route update stopped policy changes: %d states and %d policy pushes", len(node.applied), len(node.policies))
	}

	rejected.applyErr = nil
	*now = now.Add(time.Minute)
	s.applyPolicyChange(ctx)
	if len(node.applied) != 2 || len(node.applied[1].Filters.Egress.Scoped[0].Slots) != 0 {
		t.Fatalf("changing plans did not update the audience: %+v", node.applied)
	}
}
