package tgbot

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
)

// event is what the addon pulls from the panel's feed: the payment or the notice, as JSON.
func event(t *testing.T, typ string, userID, tgID int64, data any) events.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return events.Event{ID: 1, Type: typ, UserID: userID, TgID: tgID, Data: raw}
}

// said handles an event and returns the message to the chat that has want in it (a fresh
// menu follows the payment messages).
func (e *env) said(ev events.Event, chat int64, want string) call {
	e.t.Helper()
	n := e.tg.count()
	if err := e.bot.HandleEvent(e.ctx, ev); err != nil {
		e.t.Fatal(err)
	}
	var got call
	e.tg.until(e.t, n, func(cs []call) bool {
		for _, c := range cs {
			if c.method == "sendMessage" && c.body["chat_id"] == float64(chat) && strings.Contains(text(c), want) {
				got = c
				return true
			}
		}
		return false
	})
	return got
}

// payment is a payment's event as billing writes it.
func payment(kind string) events.Payment {
	return events.Payment{PaymentID: 7, Kind: kind, Provider: "stars", Amount: 100, Currency: "RUB", TariffID: 1, TariffName: "Месяц", TermDays: 30}
}

// The payment messages come to the addon as events and say what the buyer needs to hear: the
// event says what happened, the subscription is fetched over the panel's API.
func TestHandleEventPaymentMessages(t *testing.T) {
	e := setup(t)
	const buyer = 4242
	if err := e.st.Q.UpsertTgChat(e.ctx, db.UpsertTgChatParams{TgID: buyer, FirstName: "Buyer", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}

	// payment.paid: a new subscription gets its link, and becomes the one the chat shows.
	pay := payment("new")
	pay.Created = true
	msg := e.said(event(t, events.PaymentPaid, e.user.ID, buyer, pay), buyer, "готова")
	if !strings.Contains(text(msg), "подписка «Анна» готова") || !strings.Contains(text(msg), e.user.SubToken) {
		t.Fatalf("paid, new: %q", text(msg))
	}
	if ch, err := e.st.Q.GetTgChat(e.ctx, buyer); err != nil || ch.Current != e.user.ID {
		t.Fatalf("the paid subscription is not the chat's current one: %+v %v", ch, err)
	}
	msg = e.said(event(t, events.PaymentPaid, e.user.ID, buyer, payment("renew")), buyer, "продлена")
	if !strings.Contains(text(msg), "продлена") {
		t.Fatalf("paid, renewed: %q", text(msg))
	}
	msg = e.said(event(t, events.PaymentPaid, e.user.ID, buyer, payment(billing.KindPackage)), buyer, "начислен")
	if !strings.Contains(text(msg), "пакет «Месяц» начислен") {
		t.Fatalf("paid, package: %q", text(msg))
	}

	// payment.refunded: what was given is taken back, and the buyer is told which.
	ref := payment("new")
	ref.Disabled = true
	msg = e.said(event(t, events.PaymentRefunded, e.user.ID, buyer, ref), buyer, "отключена")
	if !strings.Contains(text(msg), "Подписка «Анна» отключена") {
		t.Fatalf("refunded, turned off: %q", text(msg))
	}
	msg = e.said(event(t, events.PaymentRefunded, e.user.ID, buyer, payment("renew")), buyer, "продление")
	if !strings.Contains(text(msg), "продление") {
		t.Fatalf("refunded, renewal: %q", text(msg))
	}
	msg = e.said(event(t, events.PaymentRefunded, 0, buyer, payment("new")), buyer, "возвращён.")
	if !strings.Contains(text(msg), "Платёж за «Месяц» возвращён.") {
		t.Fatalf("refunded, subscription gone: %q", text(msg))
	}
	// A subscription deleted since is the same as none: the refund is still told, the thanks
	// are not, and neither is retried for ever.
	msg = e.said(event(t, events.PaymentRefunded, 999999, buyer, payment("new")), buyer, "возвращён.")
	if !strings.Contains(text(msg), "Платёж за «Месяц» возвращён.") {
		t.Fatalf("refunded, subscription deleted: %q", text(msg))
	}
	n := e.tg.count()
	if err := e.bot.HandleEvent(e.ctx, event(t, events.PaymentPaid, 999999, buyer, payment("new"))); err != nil {
		t.Fatalf("paid, subscription deleted: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if e.tg.count() != n {
		t.Fatal("a payment of a deleted subscription was thanked")
	}

	// trial.not_given: why, and where the money comes back, with the support link.
	by := events.NotGiven{PaymentID: 8, Amount: 100, Currency: "RUB", Reason: events.TrialUsed, Refund: events.RefundedManually}
	msg = e.said(event(t, events.TrialNotGiven, 0, buyer, by), buyer, "не засчитан")
	if !strings.Contains(text(msg), "платёж 1 ₽ не засчитан") || buttons(msg)["💬 Поддержка"] != "https://t.me/mikan_support" {
		t.Fatalf("trial not given: %q %v", text(msg), buttons(msg))
	}
	// One that went back by itself is told by its payment.refunded alone.
	n = e.tg.count()
	by.Refund = events.RefundedAuto
	if err := e.bot.HandleEvent(e.ctx, event(t, events.TrialNotGiven, 0, buyer, by)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if e.tg.count() != n {
		t.Fatal("a trial that was refunded by itself was told twice")
	}

	// An event that says nothing readable is let go, not retried for ever.
	for _, typ := range []string{events.PaymentPaid, events.PaymentRefunded, events.TrialNotGiven} {
		if err := e.bot.HandleEvent(e.ctx, events.Event{Type: typ, TgID: buyer, Data: json.RawMessage(`"x"`)}); err != nil {
			t.Fatalf("an unreadable %s: %v", typ, err)
		}
	}
}

// The deprecated bot.* events are for the bot builds released before the payment events
// carried what the bot needs: this build does not subscribe to them and says nothing of one
// that reaches it, so a payment is never told twice.
func TestBotEventsAreNotRead(t *testing.T) {
	e := setup(t)
	const buyer = 4242
	for _, typ := range EventTypes {
		if strings.HasPrefix(typ, "bot.") {
			t.Fatalf("subscribed to the deprecated %s", typ)
		}
	}
	n := e.tg.count()
	pay := db.Payment{ID: 7, TgID: buyer, TariffName: "Месяц", Amount: 100, Currency: "RUB"}
	for _, ev := range []events.Event{
		event(t, events.BotPaid, e.user.ID, buyer, PaymentMessage{Payment: pay, User: e.user, Created: true}),
		event(t, events.BotRefunded, e.user.ID, buyer, PaymentMessage{Payment: pay, User: e.user}),
		event(t, events.BotTrialNotGiven, 0, buyer, PaymentMessage{Payment: pay}),
	} {
		if err := e.bot.HandleEvent(e.ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if e.tg.count() != n {
		t.Fatal("a deprecated bot.* event was said")
	}
}

// The notices come as events too: the addon words them as the admin did and sends them to
// the chat the event names.
func TestHandleEventNotices(t *testing.T) {
	e := setup(t)
	const anna = 555
	e.link(anna)
	e.expireIn(2 * 24 * time.Hour)
	u, _ := e.st.Q.GetUser(e.ctx, e.user.ID)

	msg := e.said(event(t, events.UserExpiring, u.ID, anna, events.Notice{Days: 3, ExpiresAt: u.ExpiresAt.Int64}), anna, "заканчивается")
	if !strings.Contains(text(msg), "Подписка «Анна» заканчивается") || buttons(msg)["💳 Продлить"] != "r" {
		t.Fatalf("expiring: %q %v", text(msg), buttons(msg))
	}
	e.expireIn(-time.Hour)
	msg = e.said(event(t, events.UserExpired, u.ID, anna, events.Notice{ExpiresAt: u.ExpiresAt.Int64}), anna, "закончилась")
	if !strings.Contains(text(msg), "закончилась") || buttons(msg)["💳 Продлить"] != "r" {
		t.Fatalf("expired: %q %v", text(msg), buttons(msg))
	}
	// Traffic: 90% has no renew button, the end of it has; neither offers packages here, as
	// nothing is for sale.
	msg = e.said(event(t, events.Traffic90, u.ID, anna, events.Notice{PeriodFrom: u.PeriodStart}), anna, "90%")
	if !strings.Contains(text(msg), "90% трафика") || len(buttons(msg)) != 0 {
		t.Fatalf("traffic 90: %q %v", text(msg), buttons(msg))
	}
	msg = e.said(event(t, events.Traffic100, u.ID, anna, events.Notice{PeriodFrom: u.PeriodStart}), anna, "на этот период")
	if !strings.Contains(text(msg), "на этот период закончился") || buttons(msg)["💳 Продлить"] != "r" {
		t.Fatalf("traffic 100: %q %v", text(msg), buttons(msg))
	}
	// The night's notices come without a sound.
	e.mu.Lock()
	e.now = time.Date(2027, 1, 15, 2, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	e.mu.Unlock()
	msg = e.said(event(t, events.UserExpiring, u.ID, anna, events.Notice{Days: 1}), anna, "заканчивается")
	if msg.body["disable_notification"] != true {
		t.Fatalf("a night notice rings: %v", msg.body)
	}
	// No account to tell, nothing to send.
	n := e.tg.count()
	if err := e.bot.HandleEvent(e.ctx, event(t, events.UserExpiring, u.ID, 0, events.Notice{Days: 3})); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if e.tg.count() != n {
		t.Fatal("a notice for nobody was sent")
	}
	// A subscription that cannot be read is an error: the event is not acknowledged.
	if err := e.bot.HandleEvent(e.ctx, event(t, events.UserExpiring, 999999, anna, events.Notice{Days: 3})); err == nil {
		t.Fatal("an event about a missing subscription is acknowledged")
	}
}

// The deprecated bot.* events are still written, once per payment and kind, for the bot
// builds released before the payment events carried what the bot needs.
func TestNotificationsStillWriteTheDeprecatedEvents(t *testing.T) {
	e := setup(t)
	const buyer = 4242
	var told []string
	n := Notifications{Log: e.panel.d.Log, Emit: func(ctx context.Context, typ string, userID, tgID int64, data any, dedupe string) error {
		told = append(told, typ)
		if want := typ + ":" + strconv.FormatInt(7, 10); dedupe != want || tgID != buyer {
			t.Errorf("%s: dedupe %q (want %q), tg %d", typ, dedupe, want, tgID)
		}
		return nil
	}}
	pay := db.Payment{ID: 7, TgID: buyer, Amount: 100, Currency: "RUB"}
	n.Paid(e.ctx, pay, e.user, true)
	n.Refunded(e.ctx, pay, e.user, true)
	n.TrialNotGiven(e.ctx, pay)
	if got := strings.Join(told, " "); got != "bot.paid bot.refunded bot.trial_not_given" {
		t.Fatalf("told %q", got)
	}
}
