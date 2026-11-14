package domain

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
)

// The removal reaches a subscription the rules' days after both its end and its last
// connection, and keeps one that holds something of its subscriber's: money on the linked
// account's balance, a payment not applied yet, a top-up waiting for the admin, traffic
// packages still running. One whose term waits, one a next tariff follows and one without
// an end are not inactive at all. The list is the same whether the rules are on or not;
// only on do they remove, oldest first, at most so many a round, each with its line in the
// journal and its event.
func TestRemoveInactive(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	day := int64(24 * 3600)
	at := func(daysAgo int64) sql.NullInt64 { return sql.NullInt64{Int64: now.Unix() - daysAgo*day, Valid: true} }
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := st.DB.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	ids := map[string]int64{}
	mk := func(name string, expires, online sql.NullInt64) int64 {
		t.Helper()
		u, err := users.Create(ctx, CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		exec("UPDATE users SET expires_at = $1, online_at = $2 WHERE id = $3", expires, online, u.ID)
		ids[name] = u.ID
		return u.ID
	}
	link := func(id, tg int64) {
		t.Helper()
		if err := st.Q.LinkTg(ctx, db.LinkTgParams{UserID: id, TgID: tg, CreatedAt: now.Unix()}); err != nil {
			t.Fatal(err)
		}
	}
	payment := func(id int64, status string) {
		t.Helper()
		exec(`INSERT INTO payments (provider, payload, tg_id, kind, user_id, tariff_id, tariff_name, amount, currency, status, created_at)
			VALUES ('stars', $1, 555, 'renew', $2, $3, 'Standard', 100, 'XTR', $4, $5)`, "payload-"+strconv.FormatInt(id, 10), id, tariffs[1].ID, status, now.Unix())
	}

	mk("due", at(40), sql.NullInt64{})
	mk("seen long ago", at(40), at(35))
	mk("seen lately", at(40), at(10))
	mk("ended lately", at(20), sql.NullInt64{})
	link(mk("with money", at(41), sql.NullInt64{}), 101)
	exec("INSERT INTO balances (tg_id, amount, updated_at) VALUES (101, 500, $1)", now.Unix())
	link(mk("with an empty balance", at(41), sql.NullInt64{}), 102)
	exec("INSERT INTO balances (tg_id, amount, updated_at) VALUES (102, 0, $1)", now.Unix())
	payment(mk("with an invoice open", at(42), sql.NullInt64{}), "pending")
	payment(mk("paid, not applied", at(42), sql.NullInt64{}), "paid")
	payment(mk("paid and applied", at(42), sql.NullInt64{}), "applied")
	link(mk("with a top-up waiting", at(43), sql.NullInt64{}), 103)
	exec("INSERT INTO topups (tg_id, amount, currency, status, created_at) VALUES (103, 50000, 'RUB', 'pending', $1)", now.Unix())
	granted := mk("with a package running", at(44), sql.NullInt64{})
	u, _ := users.Get(ctx, granted)
	if _, err := GrantTx(ctx, st.Q, u, GrantSpec{Bytes: GiB, Lifetime: LifetimeUsed, Source: SourceAdmin}, now); err != nil {
		t.Fatal(err)
	}
	spent := mk("with a package over", at(44), sql.NullInt64{})
	u, _ = users.Get(ctx, spent)
	if _, err := GrantTx(ctx, st.Q, u, GrantSpec{Bytes: GiB, Lifetime: LifetimeDays, Days: 1, Source: SourceAdmin}, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	mk("endless", sql.NullInt64{}, sql.NullInt64{})
	waits := mk("waits", at(45), sql.NullInt64{})
	if err := waitForConnection(ctx, st.Q, waits, now); err != nil {
		t.Fatal(err)
	}
	followed := mk("followed", at(45), sql.NullInt64{})
	if err := st.Q.SetNextTariff(ctx, db.SetNextTariffParams{UserID: followed, TariffID: tariffs[2].ID, CreatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}

	rules := RemovalRules{Days: 30}
	list, err := users.Inactive(ctx, rules)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"due": "", "seen long ago": "", "with an empty balance": "", "paid and applied": "", "with a package over": "",
		"with money": KeepBalance, "with an invoice open": KeepPayment, "paid, not applied": KeepPayment,
		"with a top-up waiting": KeepPayment, "with a package running": KeepGrant,
	}
	got := map[string]string{}
	for i, iu := range list {
		got[iu.Name] = iu.Keep
		if i > 0 && iu.ExpiresAt.Before(list[i-1].ExpiresAt) {
			t.Errorf("%s comes after a later end", iu.Name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("listed %v, want %v", got, want)
	}
	for name, keep := range want {
		if k, ok := got[name]; !ok || k != keep {
			t.Errorf("%s: listed %v, kept for %q, want %q", name, ok, k, keep)
		}
	}

	// Off, nothing goes.
	if n, err := users.RemoveInactive(ctx, rules, 100); err != nil || n != 0 {
		t.Fatalf("off: removed %d %v", n, err)
	}
	rules.On = true
	pushes := ch.policies
	if n, err := users.RemoveInactive(ctx, rules, 2); err != nil || n != 2 {
		t.Fatalf("a round of two: %d %v", n, err)
	}
	if n, err := users.RemoveInactive(ctx, rules, 100); err != nil || n != 3 {
		t.Fatalf("the rest: %d %v", n, err)
	}
	if ch.policies != pushes+2 {
		t.Fatalf("the nodes were told %d times, want once a round", ch.policies-pushes)
	}
	for name, id := range ids {
		_, err := users.Get(ctx, id)
		keep, listed := want[name]
		if gone := err != nil; gone != (listed && keep == "") {
			t.Errorf("%s: gone %v (%v)", name, gone, err)
		}
	}
	var lines int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM audit_log WHERE action = 'user.remove_inactive' AND admin_id IS NULL").Scan(&lines); err != nil || lines != 5 {
		t.Fatalf("journal lines %d %v", lines, err)
	}
	var removed []db.AddonEvent
	for _, e := range feedOf(t, st) {
		if e.Type == events.UserRemoved {
			removed = append(removed, e)
		}
	}
	if len(removed) != 5 {
		t.Fatalf("told %d removals", len(removed))
	}
	for _, e := range removed {
		var r events.Removed
		if err := json.Unmarshal([]byte(e.Data), &r); err != nil || r.By != events.RemovedInactive || r.Days != 30 || r.ExpiredAt >= now.Unix()-30*day {
			t.Errorf("removal told %s: %+v %v", e.Data, r, err)
		}
		if e.UserID.Int64 == ids["with an empty balance"] && e.TgID.Int64 != 102 {
			t.Errorf("the linked account is not told: %+v", e)
		}
		if e.UserID.Int64 == ids["seen long ago"] && r.OnlineAt != now.Unix()-35*day {
			t.Errorf("the last connection is not told: %+v", r)
		}
	}
	// What is kept stays kept round after round.
	if n, err := users.RemoveInactive(ctx, rules, 100); err != nil || n != 0 {
		t.Fatalf("again: %d %v", n, err)
	}
}
