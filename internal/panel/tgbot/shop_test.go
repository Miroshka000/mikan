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

// A stranger buys a subscription for Stars in the bot: welcome → plans → plan → invoice;
// Telegram's pre-checkout is answered, the payment creates and links the subscription
// once, and the buyer gets the link.
func TestShopStars(t *testing.T) {
	var svc *billing.Service
	var sale db.Tariff
	e := setup(t, func(e *env, d *wiring) {
		ts, _ := e.st.Q.ListTariffs(e.ctx)
		var err error
		sale, err = e.st.Q.UpdateTariff(e.ctx, db.UpdateTariffParams{Name: "Месяц", TrafficLimit: ts[1].TrafficLimit, DurationDays: 30, DeviceLimit: ts[1].DeviceLimit,
			ResetStrategy: ts[1].ResetStrategy, PriceStars: sql.NullInt64{Int64: 50, Valid: true}, OnSale: 1, ID: ts[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		pool := domain.NewPool(e.st, e.clock)
		svc = billing.New(billing.Deps{Store: e.st, Settings: e.set, Users: domain.NewUsers(e.st, pool, noChanges{}, e.clock),
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: e.clock, MaxLinks: tgate.MaxLinks})
		d.Billing = svc
	})
	svc.SetTelegram(e.gate)
	svc.SetNotifier(e.notifications())
	const buyer = 777

	// Selling is off on a panel that never turned it on: the welcome has no shop.
	n := e.tg.count()
	e.say(buyer, "/start")
	send, _ := find(e.tg.wait(t, n, "sendMessage"), "sendMessage")
	if _, ok := buttons(send)["🛒 Купить подписку"]; ok {
		t.Fatalf("shop with selling off: %v", buttons(send))
	}
	if err := settings.Set(e.ctx, e.set, billing.KeyConfig, billing.Config{Enabled: true, Stars: true, AllowNew: true, RenewResetsTraffic: true}); err != nil {
		t.Fatal(err)
	}
	e.later()
	n = e.tg.count()
	e.say(buyer, "/start")
	send, _ = find(e.tg.wait(t, n, "sendMessage"), "sendMessage")
	if buttons(send)["🛒 Купить подписку"] != "b" {
		t.Fatalf("welcome without the shop: %v", buttons(send))
	}
	menu := int64(1000) // any message id: the edits land on it
	step := func(data, want string) call {
		t.Helper()
		e.later()
		n := e.tg.count()
		e.press(buyer, menu, data)
		edit, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
		if !strings.Contains(text(edit)+strings.Join(keys(buttons(edit)), " "), want) {
			t.Fatalf("%s: %q %v", data, text(edit), buttons(edit))
		}
		return edit
	}
	list := step("b", "Месяц")
	id := strconv.FormatInt(sale.ID, 10)
	if buttons(list)["Месяц · ⭐ 50"] != "tn:"+id {
		t.Fatalf("plans: %v", buttons(list))
	}
	plan := step("tn:"+id, "Stars")
	if buttons(plan)["⭐ Telegram Stars — ⭐ 50"] != "pn:"+id+":s" {
		t.Fatalf("plan: %v", buttons(plan))
	}
	n = e.tg.count()
	inv := step("pn:"+id+":s", "Оплатить")
	link, ok := find(e.tg.until(t, n, func(cs []call) bool { _, ok := find(cs, "createInvoiceLink"); return ok }), "createInvoiceLink")
	if !ok || link.body["currency"] != "XTR" {
		t.Fatalf("invoice link: %+v", link)
	}
	payload, _ := link.body["payload"].(string)
	if buttons(inv)["Оплатить ⭐ 50"] == "" || !strings.HasPrefix(buttons(inv)["Оплатить ⭐ 50"], "https://t.me/$") {
		t.Fatalf("pay button: %v", buttons(inv))
	}

	// Telegram asks first: a wrong sum is refused, the right one goes ahead.
	n = e.tg.count()
	e.tg.push(tgate.Update{PreCheckoutQuery: &tgate.PreCheckoutQuery{ID: "pc1", From: tgate.User{ID: buyer}, Currency: "XTR", TotalAmount: 1, InvoicePayload: payload}})
	ans, _ := find(e.tg.wait(t, n, "answerPreCheckoutQuery"), "answerPreCheckoutQuery")
	if ans.body["ok"] != false || ans.body["error_message"] == "" {
		t.Fatalf("a wrong sum went ahead: %v", ans.body)
	}
	n = e.tg.count()
	e.tg.push(tgate.Update{PreCheckoutQuery: &tgate.PreCheckoutQuery{ID: "pc2", From: tgate.User{ID: buyer}, Currency: "XTR", TotalAmount: 50, InvoicePayload: payload}})
	ans, _ = find(e.tg.wait(t, n, "answerPreCheckoutQuery"), "answerPreCheckoutQuery")
	if ans.body["ok"] != true {
		t.Fatalf("pre-checkout refused: %v", ans.body)
	}

	paid := tgate.Update{Message: &tgate.Message{MessageID: 99, From: &tgate.User{ID: buyer}, Chat: tgate.Chat{ID: buyer, Type: "private"},
		SuccessfulPayment: &tgate.SuccessfulPayment{Currency: "XTR", TotalAmount: 50, InvoicePayload: payload, ChargeID: "tg-charge-1"}}}
	n = e.tg.count()
	e.tg.push(paid)
	got := e.tg.until(t, n, func(cs []call) bool {
		for _, c := range cs {
			if c.method == "sendMessage" && strings.Contains(text(c), "Оплата получена") {
				return true
			}
		}
		return false
	})
	var told string
	for _, c := range got {
		if c.method == "sendMessage" && strings.Contains(text(c), "Оплата получена") {
			told = text(c)
		}
	}
	links, _ := e.st.Q.ListTgLinksOf(e.ctx, buyer)
	if len(links) != 1 || !strings.Contains(told, "https://vpn.example.com:21355/sub/"+links[0].SubToken) {
		t.Fatalf("subscription: %d links, told %q", len(links), told)
	}
	// Telegram repeating the update changes nothing.
	e.tg.push(paid)
	e.later()
	n = e.tg.count()
	e.say(buyer, "ещё")
	e.tg.wait(t, n, "sendMessage")
	if links, _ := e.st.Q.ListTgLinksOf(e.ctx, buyer); len(links) != 1 {
		t.Fatalf("a repeated payment made %d subscriptions", len(links))
	}

	// Telegram refunds the payment (the buyer asked it): the subscription is turned off and
	// the buyer is told, once, however often the update comes.
	refunded := tgate.Update{Message: &tgate.Message{MessageID: 100, From: &tgate.User{ID: buyer}, Chat: tgate.Chat{ID: buyer, Type: "private"},
		RefundedPayment: &tgate.RefundedPayment{Currency: "XTR", TotalAmount: 50, InvoicePayload: payload, ChargeID: "tg-charge-1"}}}
	told = ""
	n = e.tg.count()
	e.tg.push(refunded)
	for _, c := range e.tg.until(t, n, func(cs []call) bool {
		for _, c := range cs {
			if c.method == "sendMessage" && strings.Contains(text(c), "Платёж за тариф «Месяц» возвращён") {
				return true
			}
		}
		return false
	}) {
		if c.method == "sendMessage" && strings.Contains(text(c), "возвращён") {
			told = text(c)
		}
	}
	if !strings.Contains(told, "отключена") {
		t.Fatalf("buyer told %q", told)
	}
	links, _ = e.st.Q.ListTgLinksOf(e.ctx, buyer)
	if u, err := e.st.Q.GetUser(e.ctx, links[0].ID); err != nil || u.Status != "disabled" {
		t.Fatalf("the refunded subscription: %+v %v", u, err)
	}
	e.tg.push(refunded)
	e.later()
	n = e.tg.count()
	e.say(buyer, "ещё")
	again := 0
	for _, c := range e.tg.wait(t, n, "sendMessage") {
		if c.method == "sendMessage" && strings.Contains(text(c), "возвращён") {
			again++
		}
	}
	if again != 0 {
		t.Fatalf("a repeated refund update told the buyer again")
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
