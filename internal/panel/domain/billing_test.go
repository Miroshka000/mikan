package domain

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func bday(d int64) sql.NullInt64 { return sql.NullInt64{Int64: d, Valid: true} }

func TestBillingDates(t *testing.T) {
	for _, c := range []struct {
		name string
		got  time.Time
		want string
	}{
		{"next billing day this month", NextBillingDate(at("2026-09-05 10:45"), 10), "2026-09-10 10:45"},
		{"the billing moment itself is not next", NextBillingDate(at("2026-10-10 10:45"), 10), "2026-11-10 10:45"},
		{"the 31st in February is its last day", NextBillingDate(at("2027-01-31 08:00"), 31), "2027-02-28 08:00"},
		{"and back to the 31st after it", NextBillingDate(at("2027-02-28 08:00"), 31), "2027-03-31 08:00"},
		{"leap year", NextBillingDate(at("2028-01-31 08:00"), 30), "2028-02-29 08:00"},
		{"december to january", NextBillingDate(at("2026-12-20 00:00"), 5), "2027-01-05 00:00"},

		{"first term: nearest billing day 20 days away", AddMonths(at("2026-09-20 12:00"), 1, bday(10)), "2026-10-10 12:00"},
		{"first term: 11 days is too short, a month more", AddMonths(at("2026-09-29 13:45"), 1, bday(10)), "2026-11-10 13:45"},
		{"+1 month from an expiry on the billing day", AddMonths(at("2026-10-10 13:45"), 1, bday(10)), "2026-11-10 13:45"},
		{"+3 months", AddMonths(at("2026-10-10 13:45"), 3, bday(10)), "2027-01-10 13:45"},
		{"no billing day: same day next month", AddMonths(at("2026-10-15 09:00"), 1, sql.NullInt64{}), "2026-11-15 09:00"},
		{"no billing day: the 31st to a shorter month", AddMonths(at("2027-01-31 09:00"), 1, sql.NullInt64{}), "2027-02-28 09:00"},

		{"monthly period began on the last billing day", MonthPeriodStart(at("2026-09-29 13:45"), bday(10)), "2026-09-10 00:00"},
		{"before this month's billing day: last month's", MonthPeriodStart(at("2026-09-05 13:45"), bday(10)), "2026-08-10 00:00"},
		{"without a billing day: the 1st", MonthPeriodStart(at("2026-09-29 13:45"), sql.NullInt64{}), "2026-09-01 00:00"},
		{"the 31st in February", MonthPeriodStart(at("2027-03-10 00:00"), bday(31)), "2027-02-28 00:00"},
	} {
		if got := c.got.Format("2006-01-02 15:04"); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	for days, months := range map[int64]int{30: 1, 31: 1, 90: 3, 180: 6, 365: 12, 7: 1} {
		if got := TermMonths(days); got != months {
			t.Errorf("%d days: %d months, want %d", days, got, months)
		}
	}
	if e := tariffExpiry(at("2026-09-29 13:45"), durationTariff{90, bday(10)}); time.Unix(e.Int64, 0).UTC().Format("2006-01-02") != "2027-01-10" {
		t.Errorf("quarter with a billing day: %v", time.Unix(e.Int64, 0).UTC())
	}
	if e := tariffExpiry(at("2026-09-29 13:45"), durationTariff{0, bday(10)}); e.Valid {
		t.Error("an unlimited term stays unlimited")
	}
}

func TestUsersWithBillingDay(t *testing.T) {
	now := at("2026-09-29 13:45")
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	std := tariffs[1] // Стандарт: 30 days
	if _, err := st.Q.UpdateTariff(ctx, db.UpdateTariffParams{Name: std.Name, TrafficLimit: std.TrafficLimit, DurationDays: std.DurationDays, DeviceLimit: std.DeviceLimit,
		ResetStrategy: "month_start", PriceLabel: std.PriceLabel, Sort: std.Sort, BillingDay: bday(10), ID: std.ID}); err != nil {
		t.Fatal(err)
	}
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: std.ID})
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Unix(u.ExpiresAt.Int64, 0).UTC()
	if !u.BillingDay.Valid || u.BillingDay.Int64 != 10 || exp.Format("2006-01-02 15:04") != "2026-11-10 13:45" {
		t.Fatalf("created on the 29th, billing day 10: expiry %s", exp)
	}
	if r, _ := NextReset(u, now); r.Format("2006-01-02") != "2026-10-10" {
		t.Fatalf("traffic resets on the billing day: %s", r)
	}
	u, err = users.ExtendMonths(ctx, u.ID, 1)
	if err != nil || time.Unix(u.ExpiresAt.Int64, 0).UTC().Format("2006-01-02") != "2026-12-10" {
		t.Fatalf("+1 month: %v %v", time.Unix(u.ExpiresAt.Int64, 0).UTC(), err)
	}
	// Lapsed: a new term from today, at least half a month.
	now = at("2027-01-25 10:00")
	if u, err = users.ExtendMonths(ctx, u.ID, 1); err != nil || time.Unix(u.ExpiresAt.Int64, 0).UTC().Format("2006-01-02") != "2027-02-10" {
		t.Fatalf("lapsed, renewed on the 25th: %v %v", time.Unix(u.ExpiresAt.Int64, 0).UTC(), err)
	}
	// One period (bulk, dashboard): to the next billing day, or 30 days without one.
	if u, err = users.ExtendPeriod(ctx, u.ID); err != nil || time.Unix(u.ExpiresAt.Int64, 0).UTC().Format("2006-01-02") != "2027-03-10" {
		t.Fatalf("one period with a billing day: %v %v", time.Unix(u.ExpiresAt.Int64, 0).UTC(), err)
	}
	bad := int64(32)
	if _, err := users.Update(ctx, u.ID, Patch{BillingDay: &bad}); !errors.Is(err, ErrBadBillingDay) {
		t.Fatalf("day 32: %v", err)
	}
	if u, err = users.Update(ctx, u.ID, Patch{ClearBillingDay: true}); err != nil || u.BillingDay.Valid {
		t.Fatalf("cleared: %v %v", u.BillingDay, err)
	}
	if u, err = users.ExtendPeriod(ctx, u.ID); err != nil || time.Unix(u.ExpiresAt.Int64, 0).UTC().Format("2006-01-02") != "2027-04-09" {
		t.Fatalf("one period without a billing day: %v %v", time.Unix(u.ExpiresAt.Int64, 0).UTC(), err)
	}
}

// ExtendPeriod adds one paid period: a month up to the billing day, or 30 days without one.
func (s *Users) ExtendPeriod(ctx context.Context, id int64) (db.User, error) {
	return s.Update(ctx, id, Patch{Extend: &Extension{Period: true}})
}
