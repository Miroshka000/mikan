package tgbot

import (
	"database/sql"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// The admin's description of a plan follows the line built from its limits on every screen
// of the plan: the list, the terms and the way to pay. It is plain text, so markup in it
// reaches the buyer as typed, and its line breaks stay; a plan without one looks as before.
func TestShopDescription(t *testing.T) {
	var sale db.Tariff
	var svc *billing.Service
	e := setup(t, func(e *env, d *wiring) {
		ts, _ := e.st.Q.ListTariffs(e.ctx)
		var err error
		sale, err = e.st.Q.UpdateTariff(e.ctx, db.UpdateTariffParams{Name: "Std", TrafficLimit: ts[1].TrafficLimit, DurationDays: 30, DeviceLimit: ts[1].DeviceLimit,
			ResetStrategy: ts[1].ResetStrategy, PriceStars: sql.NullInt64{Int64: 150, Valid: true}, OnSale: 1, ID: ts[1].ID,
			Description: "С белыми списками\n<b>без</b> рекламы & блокировок"})
		if err != nil {
			t.Fatal(err)
		}
		star := func(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }
		if err := domain.SetTariffTerms(e.ctx, e.st.Q, sale.ID, []domain.Term{{Days: 30, PriceStars: star(150)}, {Days: 90, PriceStars: star(400)}}); err != nil {
			t.Fatal(err)
		}
		svc = billing.New(billing.Deps{Store: e.st, Settings: e.set, Users: domain.NewUsers(e.st, domain.NewPool(e.st, e.clock), noChanges{}, e.clock),
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: e.clock, MaxLinks: tgate.MaxLinks})
		d.Billing = svc
	})
	svc.SetTelegram(e.gate)
	svc.SetNotifier(e.notifications())
	if err := settings.Set(e.ctx, e.set, billing.KeyConfig, billing.Config{Enabled: true, Stars: true, AllowNew: true}); err != nil {
		t.Fatal(err)
	}
	const buyer, menu = 781, int64(1000)
	step := func(data string) call {
		t.Helper()
		e.later()
		n := e.tg.count()
		e.press(buyer, menu, data)
		edit, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
		return edit
	}
	note := "\nС белыми списками\n&lt;b&gt;без&lt;/b&gt; рекламы &amp; блокировок"
	id := strconv.FormatInt(sale.ID, 10)
	if list := step("b"); !strings.Contains(text(list), "30 дн. – 90 дн.") || !strings.Contains(text(list), note) {
		t.Fatalf("the list: %q", text(list))
	}
	if plan := step("tn:" + id); !strings.Contains(text(plan), note+"\n\n") {
		t.Fatalf("the terms: %q", text(plan))
	}
	if term := step("tn:" + id + ".90"); !strings.Contains(text(term), note+"\n\n") {
		t.Fatalf("a term: %q", text(term))
	}

	// Without a description nothing is added.
	if _, err := e.st.Q.UpdateTariff(e.ctx, db.UpdateTariffParams{Name: "Std", TrafficLimit: sale.TrafficLimit, DurationDays: 30, DeviceLimit: sale.DeviceLimit,
		ResetStrategy: sale.ResetStrategy, PriceStars: sale.PriceStars, OnSale: 1, ID: sale.ID}); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"b", "tn:" + id, "tn:" + id + ".90"} {
		if got := text(step(data)); strings.Contains(got, "белыми") {
			t.Fatalf("%s without a description: %q", data, got)
		}
	}
}
