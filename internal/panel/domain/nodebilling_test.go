package domain

import (
	"testing"
	"time"
)

func utcDay(s string) int64 {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return Day(t)
}

func dayString(d int64) string { return dayTime(d).Format(time.DateOnly) }

// The next payment: the day itself while it is ahead, then the series on the same day of the
// month, or on the month's last day when it is shorter; a payment once is gone once it passed.
func TestNodeBillingNextPayment(t *testing.T) {
	for _, tc := range []struct {
		pay    string
		months int64
		today  string
		want   string // "" none
	}{
		{"", 1, "2026-10-27", ""},
		{"2026-11-02", 0, "2026-10-27", "2026-11-02"},
		{"2026-10-27", 0, "2026-10-27", "2026-10-27"},
		{"2026-10-26", 0, "2026-10-27", ""},
		{"2026-10-26", 1, "2026-10-27", "2026-11-26"},
		{"2026-01-31", 1, "2026-02-01", "2026-02-28"},
		{"2026-01-31", 1, "2026-03-01", "2026-03-31"},
		{"2024-01-31", 1, "2024-02-01", "2024-02-29"},
		{"2026-01-31", 1, "2026-04-30", "2026-04-30"},
		{"2026-01-31", 1, "2026-05-01", "2026-05-31"},
		{"2023-03-15", 12, "2026-10-27", "2027-03-15"},
		{"2023-03-15", 3, "2026-10-27", "2026-12-15"},
		{"2020-10-27", 1, "2026-10-27", "2026-10-27"},
	} {
		b := NodeBilling{PayMonths: tc.months}
		if tc.pay != "" {
			b.PayDay = utcDay(tc.pay)
		}
		got, ok := b.NextPayment(utcDay(tc.today))
		if s := dayString(got); !ok && tc.want != "" || ok && s != tc.want {
			t.Errorf("%s every %d months on %s: %s %v, want %q", tc.pay, tc.months, tc.today, s, ok, tc.want)
		}
	}
}

// The admin is reminded from RemindDays before the payment to its day, and never with 0.
func TestNodeBillingRemindPayment(t *testing.T) {
	b := NodeBilling{PayDay: utcDay("2026-11-02"), PayMonths: 1, RemindDays: 3}
	for today, want := range map[string]string{"2026-10-29": "", "2026-10-30": "2026-11-02", "2026-11-02": "2026-11-02", "2026-11-03": ""} {
		got, ok := b.RemindPayment(utcDay(today))
		if s := dayString(got); !ok && want != "" || ok && s != want {
			t.Errorf("on %s: %s %v, want %q", today, s, ok, want)
		}
	}
	b.RemindDays = 0
	if _, ok := b.RemindPayment(utcDay("2026-11-02")); ok {
		t.Error("reminded with 0 days")
	}
}

// The plan's period runs from the reset day of this month or the last to the next one, on the
// month's last day when it is shorter.
func TestNodeBillingTrafficPeriod(t *testing.T) {
	for _, tc := range []struct {
		reset          int64
		now            string
		from, nextDate string
	}{
		{1, "2026-10-27T15:00:00Z", "2026-10-01", "2026-11-01"},
		{1, "2026-10-01T00:00:00Z", "2026-10-01", "2026-11-01"},
		{15, "2026-10-14T23:59:59Z", "2026-09-15", "2026-10-15"},
		{15, "2026-10-15T00:00:00Z", "2026-10-15", "2026-11-15"},
		{31, "2026-02-28T10:00:00Z", "2026-02-28", "2026-03-31"},
		{31, "2026-02-27T10:00:00Z", "2026-01-31", "2026-02-28"},
		{10, "2026-01-05T10:00:00Z", "2025-12-10", "2026-01-10"},
		{10, "2026-12-20T10:00:00Z", "2026-12-10", "2027-01-10"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		from, next := NodeBilling{ResetDay: tc.reset}.TrafficPeriod(now)
		if dayString(from) != tc.from || dayString(next) != tc.nextDate {
			t.Errorf("reset %d at %s: %s to %s, want %s to %s", tc.reset, tc.now, dayString(from), dayString(next), tc.from, tc.nextDate)
		}
	}
}

func TestNodeBillingCheck(t *testing.T) {
	ok := NodeBilling{Provider: "  Hetzner ", PayMonths: 1, RemindDays: 3, ResetDay: 1, TrafficAlert: 80}
	if err := ok.check(); err != nil || ok.Provider != "Hetzner" {
		t.Fatalf("%v %q", err, ok.Provider)
	}
	for _, bad := range []NodeBilling{
		{Provider: "a\nb", ResetDay: 1, TrafficAlert: 80},
		{PayMonths: 37, ResetDay: 1, TrafficAlert: 80},
		{RemindDays: 61, ResetDay: 1, TrafficAlert: 80},
		{TrafficLimit: -1, ResetDay: 1, TrafficAlert: 80},
		{ResetDay: 0, TrafficAlert: 80},
		{ResetDay: 32, TrafficAlert: 80},
		{ResetDay: 1, TrafficAlert: 0},
		{ResetDay: 1, TrafficAlert: 101},
	} {
		if err := bad.check(); err == nil {
			t.Errorf("%+v passed", bad)
		}
	}
}
