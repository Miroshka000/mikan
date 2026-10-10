package tgbot

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// The free trial in the bot: the welcome offers it to a new account, a tap gives the
// subscription and the menu, and the offer is gone after that; a second tap from an old
// message is told why not.
func TestBotTrial(t *testing.T) {
	var svc *billing.Service
	e := setup(t, func(e *env, d *Deps) {
		svc = billing.New(billing.Deps{Store: e.st, Settings: e.set, Users: domain.NewUsers(e.st, domain.NewPool(e.st, e.clock), noChanges{}, e.clock),
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: e.clock, MaxLinks: MaxLinks})
		d.Billing = svc
	})
	svc.SetTelegram(e.bot)
	const visitor = 779
	welcome := func() call {
		t.Helper()
		e.later()
		n := e.tg.count()
		e.say(visitor, "/start")
		send, _ := find(e.tg.wait(t, n, "sendMessage"), "sendMessage")
		return send
	}
	if _, ok := buttons(welcome())["🎁 Попробовать бесплатно"]; ok {
		t.Fatal("a trial nobody offers")
	}
	ts, _ := e.st.Q.ListTariffs(e.ctx)
	// Selling stays off: the trial takes no payment.
	if err := settings.Set(e.ctx, e.set, billing.KeyConfig, billing.Config{TrialTariffID: ts[0].ID}); err != nil {
		t.Fatal(err)
	}
	if buttons(welcome())["🎁 Попробовать бесплатно"] != "tr" {
		t.Fatal("the welcome does not offer the trial")
	}
	press := func(data string) call {
		t.Helper()
		e.later()
		n := e.tg.count()
		e.press(visitor, 1000, data)
		edit, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
		return edit
	}
	got := press("tr")
	if !strings.Contains(text(got), "Пробная подписка готова") || !strings.Contains(text(got), "3 дн.") || buttons(got)["📱 Открыть подписку"] != "m" {
		t.Fatalf("the trial: %q %v", text(got), buttons(got))
	}
	if links, _ := e.st.Q.ListTgLinksOf(e.ctx, visitor); len(links) != 1 || links[0].TariffID.Int64 != ts[0].ID {
		t.Fatalf("links after the trial: %+v", links)
	}
	if _, ok := buttons(welcome())["🎁 Попробовать бесплатно"]; ok {
		t.Fatal("the trial is offered again")
	}
	if again := press("tr"); !strings.Contains(text(again), "один раз") {
		t.Fatalf("a second tap: %q", text(again))
	}
}

// The trial's texts are plain: every one is there in both languages, and none has a dash.
func TestTrialTexts(t *testing.T) {
	for name, w := range map[string]*words{"ru": &ru, "en": &en} {
		for field, s := range map[string]string{"trial": w.trial, "trialDone": w.trialDone, "trialUsed": w.trialUsed, "trialOff": w.trialOff, "trialOpen": w.trialOpen, "trialFail": w.trialFail,
			"trialPaid": w.trialPaid, "trialTitle": w.trialTitle} {
			if s == "" || strings.ContainsAny(s, "—–") {
				t.Errorf("%s.%s is empty or has a dash: %q", name, field, s)
			}
		}
	}
}

// A trial that fails for a reason of ours says so, not "payment is not available".
func TestBotTrialFailure(t *testing.T) {
	var svc *billing.Service
	e := setup(t, func(e *env, d *Deps) {
		svc = billing.New(billing.Deps{Store: e.st, Settings: e.set, Users: domain.NewUsers(e.st, domain.NewPool(e.st, e.clock), noChanges{}, e.clock),
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: e.clock, MaxLinks: MaxLinks})
		d.Billing = svc
	})
	svc.SetTelegram(e.bot)
	ts, _ := e.st.Q.ListTariffs(e.ctx)
	if err := settings.Set(e.ctx, e.set, billing.KeyConfig, billing.Config{TrialTariffID: ts[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DB.ExecContext(e.ctx, "DROP TABLE trials"); err != nil {
		t.Fatal(err)
	}
	e.later()
	n := e.tg.count()
	e.press(780, 1000, "tr")
	edit, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if got := text(edit); got != ru.trialFail {
		t.Fatalf("a failed trial: %q", got)
	}
}

// A priced trial in the bot: the welcome names its price, a tap shows the ways to pay,
// the invoice is for the trial's price, and paying gives the trial subscription once.
func TestBotPaidTrial(t *testing.T) {
	var svc *billing.Service
	e := setup(t, func(e *env, d *Deps) {
		svc = billing.New(billing.Deps{Store: e.st, Settings: e.set, Users: domain.NewUsers(e.st, domain.NewPool(e.st, e.clock), noChanges{}, e.clock),
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: e.clock, MaxLinks: MaxLinks})
		d.Billing = svc
	})
	svc.SetTelegram(e.bot)
	const visitor = 781
	ts, _ := e.st.Q.ListTariffs(e.ctx)
	// Selling is on for the trial alone: no tariff is on sale and new subscriptions are off.
	if err := settings.Set(e.ctx, e.set, billing.KeyConfig, billing.Config{Enabled: true, Stars: true, TrialTariffID: ts[0].ID, TrialStars: 1}); err != nil {
		t.Fatal(err)
	}
	welcome := func() call {
		t.Helper()
		e.later()
		n := e.tg.count()
		e.say(visitor, "/start")
		send, _ := find(e.tg.wait(t, n, "sendMessage"), "sendMessage")
		return send
	}
	got := welcome()
	if buttons(got)["🎁 Попробовать за ⭐ 1"] != "tr" {
		t.Fatalf("the welcome does not offer the priced trial: %v", buttons(got))
	}
	if _, ok := buttons(got)["🎁 Попробовать бесплатно"]; ok {
		t.Fatal("a priced trial is offered for free")
	}
	press := func(data string) call {
		t.Helper()
		e.later()
		n := e.tg.count()
		e.press(visitor, 1000, data)
		edit, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
		return edit
	}
	pay := press("tr")
	if !strings.Contains(text(pay), "Пробный период") || !strings.Contains(text(pay), "3 дн.") || buttons(pay)["⭐ Telegram Stars — ⭐ 1"] != "tp:s" {
		t.Fatalf("the trial's ways to pay: %q %v", text(pay), buttons(pay))
	}
	if links, _ := e.st.Q.ListTgLinksOf(e.ctx, visitor); len(links) != 0 {
		t.Fatalf("a tap gave a priced trial for free: %+v", links)
	}
	n := e.tg.count()
	inv := press("tp:s")
	link, ok := find(e.tg.until(t, n, func(cs []call) bool { _, ok := find(cs, "createInvoiceLink"); return ok }), "createInvoiceLink")
	if !ok || link.body["currency"] != "XTR" || link.body["prices"] == nil {
		t.Fatalf("invoice link: %+v", link)
	}
	if !strings.HasPrefix(buttons(inv)["Оплатить ⭐ 1"], "https://t.me/$") {
		t.Fatalf("pay button: %q %v", text(inv), buttons(inv))
	}
	payload, _ := link.body["payload"].(string)
	n = e.tg.count()
	e.tg.push(Update{PreCheckoutQuery: &PreCheckoutQuery{ID: "pc-trial", From: User{ID: visitor}, Currency: "XTR", TotalAmount: 1, InvoicePayload: payload}})
	if ans, _ := find(e.tg.wait(t, n, "answerPreCheckoutQuery"), "answerPreCheckoutQuery"); ans.body["ok"] != true {
		t.Fatalf("pre-checkout of the trial refused: %v", ans.body)
	}
	n = e.tg.count()
	e.tg.push(Update{Message: &Message{MessageID: 99, From: &User{ID: visitor}, Chat: Chat{ID: visitor, Type: "private"},
		SuccessfulPayment: &SuccessfulPayment{Currency: "XTR", TotalAmount: 1, InvoicePayload: payload, ChargeID: "tg-trial-1"}}})
	e.tg.until(t, n, func(cs []call) bool {
		for _, c := range cs {
			if c.method == "sendMessage" && strings.Contains(text(c), "Оплата получена") {
				return true
			}
		}
		return false
	})
	links, _ := e.st.Q.ListTgLinksOf(e.ctx, visitor)
	if len(links) != 1 || links[0].TariffID.Int64 != ts[0].ID || links[0].Source != domain.UserFromTrial {
		t.Fatalf("links after the paid trial: %+v", links)
	}
	if _, ok := buttons(welcome())["🎁 Попробовать за ⭐ 1"]; ok {
		t.Fatal("the trial is offered again after it was bought")
	}
	if again := press("tp:s"); !strings.Contains(text(again), "один раз") {
		t.Fatalf("a second invoice from an old message: %q", text(again))
	}
}
