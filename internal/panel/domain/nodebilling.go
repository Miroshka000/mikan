package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"mikan/internal/panel/store/db"
)

// NodeBilling is what the admin keeps about a node's server at its provider: who hosts it,
// when it is paid for, and how much traffic its plan gives a period. The panel only reminds:
// it pays nothing and limits nothing.
type NodeBilling struct {
	Provider string
	// PayDay is the UTC day (unix time / 86400) of a payment, 0 none; PayMonths repeats it
	// every so many months (0: once). RemindDays before the payment the admin is told, 0 never.
	PayDay     int64
	PayMonths  int64
	RemindDays int64
	// TrafficLimit is the plan's bytes a period, 0 none. A period starts on ResetDay of the
	// month (the month's last day when it is shorter); TrafficAlert is the percent of the
	// limit at which the admin is told, once a period.
	TrafficLimit int64
	ResetDay     int64
	TrafficAlert int64
}

// The bounds of NodeBilling's fields (the API says the same).
const (
	BillingProviderMax = 100
	BillingMonthsMax   = 36
	BillingRemindMax   = 60
)

// BillingOf is the node's billing as it is saved.
func BillingOf(n db.Node) NodeBilling {
	return NodeBilling{Provider: n.BillingProvider, PayDay: n.PayDay, PayMonths: n.PayMonths, RemindDays: n.RemindDays,
		TrafficLimit: n.TrafficLimit, ResetDay: n.ResetDay, TrafficAlert: n.TrafficAlert}
}

// check trims the provider and refuses what is out of bounds (a *FieldError under billing).
func (b *NodeBilling) check() error {
	b.Provider = strings.TrimSpace(b.Provider)
	switch {
	case utf8.RuneCountInString(b.Provider) > BillingProviderMax || strings.ContainsFunc(b.Provider, func(r rune) bool { return r < ' ' }):
		return fieldErr("billing.provider", "bad_billing")
	case b.PayDay < 0:
		return fieldErr("billing.pay_date", "bad_billing")
	case b.PayMonths < 0 || b.PayMonths > BillingMonthsMax:
		return fieldErr("billing.pay_months", "bad_billing")
	case b.RemindDays < 0 || b.RemindDays > BillingRemindMax:
		return fieldErr("billing.remind_days", "bad_billing")
	case b.TrafficLimit < 0:
		return fieldErr("billing.traffic_limit", "bad_billing")
	case b.ResetDay < 1 || b.ResetDay > 31:
		return fieldErr("billing.reset_day", "bad_billing")
	case b.TrafficAlert < 1 || b.TrafficAlert > 100:
		return fieldErr("billing.traffic_alert", "bad_billing")
	}
	return nil
}

// Day is t's UTC day, as PayDay counts them.
func Day(t time.Time) int64 { return t.Unix() / 86400 }

// dayTime is the start of a UTC day.
func dayTime(day int64) time.Time { return time.Unix(day*86400, 0).UTC() }

// NextPayment is the first payment on today or later: PayDay itself, or with PayMonths the
// PayDay so many months on as it takes. The months are counted from PayDay, on its day of the
// month or the month's last day when it is shorter: a payment on 31 January is on 28 February
// and on 31 March. False: no payment is ahead.
func (b NodeBilling) NextPayment(today int64) (int64, bool) {
	if b.PayDay == 0 {
		return 0, false
	}
	if b.PayDay >= today {
		return b.PayDay, true
	}
	if b.PayMonths == 0 {
		return 0, false
	}
	start := dayTime(b.PayDay)
	// Jump close in one step (a month is 31 days at most), then walk.
	for k := max((today-b.PayDay)/31/b.PayMonths, 1); ; k++ {
		if d := monthDay(start.Year(), start.Month()+time.Month(k*b.PayMonths), start.Day()); d >= today {
			return d, true
		}
	}
}

// monthDay is the UTC day of day in the month, or of the month's last day when it is shorter.
// The month may run past December or before January: it is counted on.
func monthDay(year int, month time.Month, day int) int64 {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	return Day(time.Date(year, month, min(day, last), 0, 0, 0, 0, time.UTC))
}

// RemindPayment is the payment the admin is to be told about today: the next one, when it
// is RemindDays away or nearer.
func (b NodeBilling) RemindPayment(today int64) (int64, bool) {
	next, ok := b.NextPayment(today)
	if !ok || b.RemindDays == 0 || next-today > b.RemindDays {
		return 0, false
	}
	return next, true
}

// TrafficPeriod is the plan's period now is in: from its first day (ResetDay of this month
// or the last) to the first day of the next, both UTC days.
func (b NodeBilling) TrafficPeriod(now time.Time) (from, next int64) {
	y, m, _ := now.UTC().Date()
	from = monthDay(y, m, int(b.ResetDay))
	if from > Day(now) {
		return monthDay(y, m-1, int(b.ResetDay)), from
	}
	return from, monthDay(y, m+1, int(b.ResetDay))
}
