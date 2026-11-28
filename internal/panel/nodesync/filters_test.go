package nodesync

import (
	"database/sql"
	"slices"
	"testing"

	"mikan/internal/panel/filters"
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
