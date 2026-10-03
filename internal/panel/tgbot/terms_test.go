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
)

// A plan sold for several terms in the bot: the list says "from", the plan shows a button
// per term, a term shows the ways to pay for it, and the invoice is for that term. An old
// button without a term still buys the first one.
func TestShopTerms(t *testing.T) {
	var sale db.Tariff
	var svc *billing.Service
	e := setup(t, func(e *env, d *Deps) {
		ts, _ := e.st.Q.ListTariffs(e.ctx)
		var err error
		sale, err = e.st.Q.UpdateTariff(e.ctx, db.UpdateTariffParams{Name: "Std", TrafficLimit: ts[1].TrafficLimit, DurationDays: 30, DeviceLimit: ts[1].DeviceLimit,
			ResetStrategy: ts[1].ResetStrategy, PriceStars: sql.NullInt64{Int64: 150, Valid: true}, OnSale: 1, ID: ts[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		star := func(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }
		if err := domain.SetTariffTerms(e.ctx, e.st.Q, sale.ID, []domain.Term{{Days: 30, PriceStars: star(150)}, {Days: 7, PriceStars: star(50)}, {Days: 90, PriceStars: star(400)}}); err != nil {
			t.Fatal(err)
		}
		svc = billing.New(billing.Deps{Store: e.st, Settings: e.set, Users: domain.NewUsers(e.st, domain.NewPool(e.st, e.clock), noChanges{}, e.clock),
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: e.clock, MaxLinks: MaxLinks})
		d.Billing = svc
	})
	svc.SetTelegram(e.bot)
	if err := settings.Set(e.ctx, e.set, billing.KeyConfig, billing.Config{Enabled: true, Stars: true, AllowNew: true}); err != nil {
		t.Fatal(err)
	}
	const buyer, menu = 778, int64(1000)
	step := func(data string) call {
		t.Helper()
		e.later()
		n := e.tg.count()
		e.press(buyer, menu, data)
		edit, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
		return edit
	}
	id := strconv.FormatInt(sale.ID, 10)
	list := step("b")
	if buttons(list)["Std · от ⭐ 50"] != "tn:"+id || !strings.Contains(text(list), "7 дн. – 90 дн.") {
		t.Fatalf("plans: %q %v", text(list), buttons(list))
	}
	plan := step("tn:" + id)
	if b := buttons(plan); b["7 дн. · ⭐ 50"] != "tn:"+id+".7" || b["90 дн. · ⭐ 400"] != "tn:"+id+".90" || b["⬅️ Назад"] != "b" {
		t.Fatalf("terms: %q %v", text(plan), b)
	}
	term := step("tn:" + id + ".90")
	if b := buttons(term); b["⭐ Telegram Stars — ⭐ 400"] != "pn:"+id+".90:s" || b["⬅️ Назад"] != "tn:"+id {
		t.Fatalf("a term: %q %v", text(term), b)
	}
	inv := step("pn:" + id + ".90:s")
	if !strings.Contains(text(inv), "Std · 90 дн.") || buttons(inv)["Оплатить ⭐ 400"] == "" {
		t.Fatalf("invoice: %q %v", text(inv), buttons(inv))
	}
	// A button sent before terms: the first term.
	old := step("pn:" + id + ":s")
	if buttons(old)["Оплатить ⭐ 150"] == "" {
		t.Fatalf("an old button: %q %v", text(old), buttons(old))
	}
	// A term the plan has not.
	if gone := step("tn:" + id + ".14"); !strings.Contains(text(gone), "не продаётся") {
		t.Fatalf("a missing term: %q", text(gone))
	}
	// The longest callback data fits Telegram's 64 bytes.
	if n := len("py:9223372036854775807.3650:a-" + strings.Repeat("x", 32)); n > 64 {
		t.Fatalf("callback data of %d bytes", n)
	}
}
