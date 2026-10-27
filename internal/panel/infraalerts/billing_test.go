package infraalerts

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// A node's payment is reminded once, from RemindDays before it, and the next payment of the
// series again; the plan's traffic is told once a period when it reaches the alert percent;
// with the event off nothing is told, and a removed node is forgotten.
func TestBillingIsToldOnce(t *testing.T) {
	st, ctx := testMonitorStore(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	m := New(st, settings.New(st.Q), nil, nil, nil, nil, nil, slog.Default(), func() time.Time { return now })
	const gb = 1 << 30
	// Paid every month on the 2nd, reminded 3 days before; 1000 GB a month from the 1st, told at 80%.
	n := db.Node{ID: 1, Name: "NL & co", BillingProvider: "Hetzner <cloud>", PayDay: domain.Day(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)), PayMonths: 1, RemindDays: 3,
		TrafficLimit: 1000 * gb, ResetDay: 1, TrafficAlert: 80}
	carry := func(day time.Time, bytes int64) {
		t.Helper()
		if err := st.Q.AddNodeTraffic(ctx, db.AddNodeTrafficParams{NodeID: 1, Hour: day.Unix() / 3600, Day: domain.Day(day), Up: bytes / 2, Down: bytes - bytes/2}); err != nil {
			t.Fatal(err)
		}
	}
	var state persistentState
	cfg := Default()
	// told runs a check and takes what it queued, as a delivery would: the queue itself drops
	// a message whose key it holds already, the state is what keeps one from coming again.
	told := func(nodes []db.Node, lang string) []string {
		t.Helper()
		m.checkBilling(ctx, &state, cfg, nodes, lang)
		var texts []string
		for _, d := range state.Pending {
			if d.Target != targetAdmin {
				t.Errorf("target %q", d.Target)
			}
			texts = append(texts, d.Text)
		}
		state.Pending = nil
		return texts
	}
	contains := func(text string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(text, w) {
				t.Errorf("no %q in %q", w, text)
			}
		}
	}

	carry(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), 900*gb) // the last period: not counted
	carry(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), 700*gb)
	got := told([]db.Node{n}, "en")
	if len(got) != 1 {
		t.Fatalf("told: %q", got)
	}
	contains(got[0], "Server payment due", "NL &amp; co (Hetzner &lt;cloud&gt;)", "2 Nov 2026")
	// 700 GB of 1000 GB is under 80%; 100 GB more is not
	carry(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), 100*gb)
	got = told([]db.Node{n}, "ru")
	if len(got) != 1 {
		t.Fatalf("told: %q", got)
	}
	contains(got[0], "Трафик сервера на исходе", "800 ГБ из 1000 ГБ (80%)", "01.11.2026")
	// told already: nothing again in the period, with more traffic, nor the same payment
	now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	carry(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), 100*gb)
	if got = told([]db.Node{n}, "en"); len(got) != 0 {
		t.Fatalf("told again: %q", got)
	}
	// the next payment, and a new period that reaches the percent, are told again
	now = time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC)
	carry(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), 850*gb)
	got = told([]db.Node{n}, "en")
	if len(got) != 2 {
		t.Fatalf("told: %q", got)
	}
	contains(got[0], "2 Dec 2026")
	contains(got[1], "850 GB of 1000 GB (85%)", "1 Dec 2026")
	// with the event off, the next payment is not told
	now = time.Date(2026, 12, 30, 12, 0, 0, 0, time.UTC)
	cfg.Events.Billing = false
	if got = told([]db.Node{n}, "en"); len(got) != 0 {
		t.Fatalf("told with the event off: %q", got)
	}
	// a node without a payment or a limit tells nothing, and the removed one is forgotten
	cfg.Events.Billing = true
	if got = told([]db.Node{{ID: 2, Name: "DE", PayMonths: 1, RemindDays: 3, ResetDay: 1, TrafficAlert: 80}}, "en"); len(got) != 0 || len(state.BillingPaid) != 0 || len(state.BillingTraffic) != 0 {
		t.Fatalf("told %q, paid %v, traffic %v", got, state.BillingPaid, state.BillingTraffic)
	}
}
