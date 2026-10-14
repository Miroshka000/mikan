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

// "Докупить трафик" in the subscription screen lists the packages for the main traffic
// and the subscription's own pools, opens an invoice, and the paid package shows in the
// traffic line.
func TestBuyTraffic(t *testing.T) {
	var svc *billing.Service
	e := setup(t, func(e *env, d *wiring) {
		pool := domain.NewPool(e.st, e.clock)
		svc = billing.New(billing.Deps{Store: e.st, Settings: e.set, Users: domain.NewUsers(e.st, pool, noChanges{}, e.clock),
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: e.clock, MaxLinks: tgate.MaxLinks})
		d.Billing = svc
	})
	svc.SetTelegram(e.gate)
	svc.SetNotifier(e.notifications())
	const owner = 555
	q := e.st.Q
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(settings.Set(e.ctx, e.set, billing.KeyConfig, billing.Config{Enabled: true, Stars: true, AllowNew: true}))
	must(q.LinkTg(e.ctx, db.LinkTgParams{UserID: e.user.ID, TgID: owner, CreatedAt: 1}))
	wl, err := q.CreateTrafficPool(e.ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: 1})
	must(err)
	other, err := q.CreateTrafficPool(e.ctx, db.CreateTrafficPoolParams{Name: "Other", CreatedAt: 1})
	must(err)
	must(q.SetUserPoolLimit(e.ctx, db.SetUserPoolLimitParams{UserID: e.user.ID, PoolID: wl.ID, TrafficLimit: sql.NullInt64{Int64: 100 << 30, Valid: true}}))
	catalog := domain.NewPackages(e.st, e.clock)
	mk := func(name string, gb, pool, stars int64) db.TrafficPackage {
		t.Helper()
		p, err := catalog.Create(e.ctx, domain.PackageInput{Name: name, Bytes: gb << 30, PoolID: pool, Lifetime: domain.LifetimeUsed,
			PriceStars: sql.NullInt64{Int64: stars, Valid: true}, OnSale: true})
		must(err)
		return p
	}
	main := mk("+50 ГБ", 50, 0, 75)
	wlPack := mk("+20 ГБ WL", 20, wl.ID, 30)
	mk("+10 ГБ Other", 10, other.ID, 10) // the subscription has no limit there

	menu := int64(1000)
	step := func(data string) call {
		t.Helper()
		e.later()
		n := e.tg.count()
		e.press(owner, menu, data)
		edit, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
		return edit
	}
	sub := step("s")
	if buttons(sub)["📦 Докупить трафик"] != "x" {
		t.Fatalf("subscription screen: %q %v", text(sub), buttons(sub))
	}
	list := step("x")
	mainID, wlID := strconv.FormatInt(main.ID, 10), strconv.FormatInt(wlPack.ID, 10)
	if b := buttons(list); b["+50 ГБ · ⭐ 75"] != "xk:"+mainID || b["+20 ГБ WL · ⭐ 30"] != "xk:"+wlID || len(b) != 3 {
		t.Fatalf("packages: %q %v", text(list), b)
	}
	if !strings.Contains(text(list), "50 ГБ · основной трафик · пока не израсходован") || !strings.Contains(text(list), "20 ГБ · WL") || strings.Contains(text(list), "Other") {
		t.Fatalf("packages text: %q", text(list))
	}
	one := step("xk:" + mainID)
	if buttons(one)["⭐ Telegram Stars — ⭐ 75"] != "xp:"+mainID+":s" {
		t.Fatalf("package: %q %v", text(one), buttons(one))
	}
	n := e.tg.count()
	inv := step("xp:" + mainID + ":s")
	link, ok := find(e.tg.until(t, n, func(cs []call) bool { _, ok := find(cs, "createInvoiceLink"); return ok }), "createInvoiceLink")
	if !ok || !strings.HasPrefix(buttons(inv)["Оплатить ⭐ 75"], "https://t.me/$") {
		t.Fatalf("invoice: %q %v", text(inv), buttons(inv))
	}
	payload, _ := link.body["payload"].(string)

	n = e.tg.count()
	e.tg.push(tgate.Update{Message: &tgate.Message{MessageID: 99, From: &tgate.User{ID: owner}, Chat: tgate.Chat{ID: owner, Type: "private"},
		SuccessfulPayment: &tgate.SuccessfulPayment{Currency: "XTR", TotalAmount: 75, InvoicePayload: payload, ChargeID: "tg-pack-1"}}})
	e.tg.until(t, n, func(cs []call) bool {
		for _, c := range cs {
			if c.method == "sendMessage" && strings.Contains(text(c), "пакет «+50 ГБ» начислен на подписку «Анна»") {
				return true
			}
		}
		return false
	})
	if gs, _ := q.ListUserGrants(e.ctx, e.user.ID); len(gs) != 1 || gs[0].Bytes != 50<<30 {
		t.Fatalf("grants: %+v", gs)
	}
	sub = step("s")
	if !strings.Contains(text(sub), "150 ГБ + пакеты 50 ГБ") {
		t.Fatalf("traffic line: %q", text(sub))
	}

	// Traffic running out: the notice carries the buy button of this very subscription,
	// and the tap opens its packages.
	// The bought package is used up, and 95% of the tariff's traffic: the 90% notice is due.
	if _, err := e.st.DB.ExecContext(e.ctx, "UPDATE traffic_grants SET remaining = 0 WHERE user_id = $1", e.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DB.ExecContext(e.ctx, "UPDATE users SET used_up = 0, used_down = traffic_limit * 95 / 100 WHERE id = $1", e.user.ID); err != nil {
		t.Fatal(err)
	}
	n = e.tg.count()
	e.panel.Notify(e.ctx)
	var notice call
	e.tg.until(t, n, func(cs []call) bool {
		for _, c := range cs {
			if c.method == "sendMessage" && strings.Contains(text(c), "90%") {
				notice = c
				return true
			}
		}
		return false
	})
	xu := "xu:" + strconv.FormatInt(e.user.ID, 10)
	if buttons(notice)["📦 Докупить трафик"] != xu {
		t.Fatalf("the 90%% notice: %q %v", text(notice), buttons(notice))
	}
	if b := buttons(step(xu)); b["+50 ГБ · ⭐ 75"] != "xk:"+mainID {
		t.Fatalf("the notice's button: %v", b)
	}
	// Somebody else's subscription id opens nothing of theirs.
	if b := buttons(step("xu:999999")); b["+50 ГБ · ⭐ 75"] != "" {
		t.Fatalf("another subscription's packages: %v", b)
	}
}
