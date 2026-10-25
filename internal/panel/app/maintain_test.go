package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The housekeeping cuts what only grew: daily traffic after 400 days, the audit journal
// after 180; what is younger stays.
func TestMaintainCutsWhatOnlyGrows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	today := h.now.Unix() / 86400
	for _, day := range []int64{today - 500, today - 401, today - 399, today - 1} {
		if err := h.st.Q.AddTrafficDaily(ctx, db.AddTrafficDailyParams{UserID: u.ID, Day: day, Up: 1, Down: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// The nodes' days go with the users' days.
	for _, day := range []int64{today - 500, today - 399} {
		if _, err := h.st.DB.ExecContext(ctx, "INSERT INTO node_traffic_daily(node_id,day,up,down) VALUES (1,$1,1,1)", day); err != nil {
			t.Fatal(err)
		}
	}
	for _, age := range []time.Duration{200 * 24 * time.Hour, 181 * 24 * time.Hour, 179 * 24 * time.Hour, time.Hour} {
		if err := h.st.Q.InsertAudit(ctx, db.InsertAuditParams{Ts: h.now.Add(-age).Unix(), Action: "test"}); err != nil {
			t.Fatal(err)
		}
	}

	h.p.maintain(ctx)

	var days, entries int
	if err := h.st.DB.QueryRow("SELECT count(*) FROM traffic_daily").Scan(&days); err != nil || days != 2 {
		t.Fatalf("daily traffic rows: %d %v, want the two within 400 days", days, err)
	}
	if err := h.st.DB.QueryRow("SELECT count(*) FROM node_traffic_daily").Scan(&days); err != nil || days != 1 {
		t.Fatalf("daily node traffic rows: %d %v, want the one within 400 days", days, err)
	}
	if err := h.st.DB.QueryRow("SELECT count(*) FROM audit_log WHERE action = 'test'").Scan(&entries); err != nil || entries != 2 {
		t.Fatalf("audit rows: %d %v, want the two within 180 days", entries, err)
	}
}

// The housekeeping's round moves the lifecycle on: a waiting term keeps its whole term, a
// next tariff takes over from a term that ends before the next round, and an inactive
// subscription goes by the admin's rules, only once they are on.
func TestMaintainMovesTheLifecycleOn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	yes := true
	waits, err := h.p.users.Create(ctx, domain.CreateInput{Name: "waits", TariffID: tariffs[1].ID, StartsOnConnect: &yes})
	if err != nil {
		t.Fatal(err)
	}
	next, err := h.p.users.Create(ctx, domain.CreateInput{Name: "next", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.p.users.SetNextTariff(ctx, next.ID, domain.NextTariff{TariffID: tariffs[2].ID}); err != nil {
		t.Fatal(err)
	}
	gone, err := h.p.users.Create(ctx, domain.CreateInput{Name: "gone", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.DB.ExecContext(ctx, "UPDATE users SET expires_at = $1 WHERE id = $2", h.now.Add(-60*24*time.Hour).Unix(), gone.ID); err != nil {
		t.Fatal(err)
	}

	h.now = h.now.Add(30*24*time.Hour - 5*time.Minute)
	h.p.maintain(ctx)
	if u, _ := h.p.users.Get(ctx, waits.ID); u.ExpiresAt.Int64 != h.now.Add(30*24*time.Hour).Unix() {
		t.Fatalf("the waiting term ends %v, want its whole term from now", u.ExpiresAt)
	}
	if u, _ := h.p.users.Get(ctx, next.ID); u.TariffID.Int64 != tariffs[2].ID {
		t.Fatalf("the next tariff did not take over: %+v", u)
	}
	if _, err := h.p.users.Get(ctx, gone.ID); err != nil {
		t.Fatalf("removed with the rules off: %v", err)
	}
	if err := settings.Set(ctx, h.p.Settings, domain.KeyLifecycle, domain.Lifecycle{Carry: domain.DefaultLifecycle.Carry, Removal: domain.RemovalRules{On: true, Days: 30}}); err != nil {
		t.Fatal(err)
	}
	h.p.maintain(ctx)
	if _, err := h.p.users.Get(ctx, gone.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an inactive subscription is still there: %v", err)
	}
}

// The journal is readable page by page, newest first, by the session only: it holds the
// admins' addresses.
func TestAuditJournalOverHTTP(t *testing.T) {
	k := newKeyHarness(t)
	for i := 0; i < 5; i++ {
		if err := k.st.Q.InsertAudit(context.Background(), db.InsertAuditParams{Ts: k.now.Unix() + int64(i), Action: "test." + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	type page struct {
		Items []struct {
			ID     int64  `json:"id"`
			Action string `json:"action"`
		} `json:"items"`
		Next int64 `json:"next"`
	}
	get := func(query string) page {
		t.Helper()
		resp, body := k.do(http.MethodGet, k.api+"/audit"+query, nil, nil)
		var p page
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &p) != nil {
			t.Fatalf("audit%s: %d %s", query, resp.StatusCode, body)
		}
		return p
	}
	first := get("?limit=3")
	if len(first.Items) != 3 || first.Items[0].Action != "test.4" || first.Next == 0 || first.Items[0].ID <= first.Items[1].ID {
		t.Fatalf("first page: %+v", first)
	}
	second := get("?limit=3&before=" + strconv.FormatInt(first.Next, 10))
	if len(second.Items) != 3 || second.Items[0].ID >= first.Next {
		t.Fatalf("second page: %+v", second)
	}
	if len(get("?limit=200").Items) < 5 {
		t.Fatal("the journal is missing entries")
	}
	for _, key := range []string{k.read, k.full} {
		if resp, body := k.asKey(key, http.MethodGet, "/audit", nil); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "session_only") {
			t.Fatalf("an API key reads the journal: %d %s", resp.StatusCode, body)
		}
	}
}
