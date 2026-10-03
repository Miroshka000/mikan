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
