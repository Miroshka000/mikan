package billing

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
)

func (e *env) events() []db.AddonEvent {
	e.t.Helper()
	evs, err := e.st.Q.ListAddonEvents(context.Background(), db.ListAddonEventsParams{Types: []string{}, Lim: 100})
	must(e.t, err)
	return evs
}

func typesOf(evs []db.AddonEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func ofType(evs []db.AddonEvent, typ string) []db.AddonEvent {
	var out []db.AddonEvent
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// The addons hear of an applied payment and of its refund once each, from the transaction
// that made the change, with who paid and what.
func TestPaymentEvents(t *testing.T) {
	e := newEnv(t)
	hub := events.NewHub()
	e.s.d.Events = hub
	woken := hub.Wait()
	p := e.buy(0, "ch-events")
	select {
	case <-woken:
	default:
		t.Fatal("a waiting reader is not woken")
	}
	// The buyer's new subscription is told first, then the payment that made it.
	evs := e.events()
	if got := typesOf(evs); !slices.Equal(got, []string{events.UserCreated, events.PaymentPaid}) {
		t.Fatalf("events after the payment: %v", got)
	}
	var made events.User
	must(t, json.Unmarshal([]byte(evs[0].Data), &made))
	if made.Source != domain.UserFromBot || made.TariffID != e.sale.ID || evs[0].TgID.Int64 != 555 || evs[0].UserID.Int64 != p.UserID.Int64 {
		t.Fatalf("user.created: %+v %+v", evs[0], made)
	}
	if evs[1].TgID.Int64 != 555 || evs[1].UserID.Int64 != p.UserID.Int64 {
		t.Fatalf("events after the payment: %+v", evs)
	}
	var got events.Payment
	must(t, json.Unmarshal([]byte(evs[1].Data), &got))
	if got.PaymentID != p.ID || got.Amount != p.Amount || got.Currency != p.Currency || got.Provider != Stars || !got.Created ||
		got.Kind != "new" || got.TariffID != e.sale.ID || got.TariffName != e.sale.Name || got.TermDays != p.TermDays.Int64 || got.TermDays == 0 || got.Disabled {
		t.Fatalf("payment event: %+v (payment %+v)", got, p)
	}

	// Applying it again (reconcile, a retried update) tells nothing new.
	must(t, e.s.Apply(context.Background(), p.ID))
	if n := len(e.events()); n != 2 {
		t.Fatalf("%d events after a second apply", n)
	}

	e.now = e.now.Add(time.Hour)
	_, err := e.s.Refund(context.Background(), p.ID)
	must(t, err)
	_, _ = e.s.Refund(context.Background(), p.ID) // a second refund changes nothing, whatever it answers
	evs = e.events()
	if got := typesOf(evs); !slices.Equal(got, []string{events.UserCreated, events.PaymentPaid, events.PaymentRefunded}) {
		t.Fatalf("events after the refund: %v", got)
	}
	// A new subscription goes off with its refund: the bot says so.
	var refunded events.Payment
	must(t, json.Unmarshal([]byte(evs[2].Data), &refunded))
	if !refunded.Disabled || refunded.PaymentID != p.ID || refunded.TariffName != e.sale.Name || refunded.TermDays != got.TermDays || refunded.Created {
		t.Fatalf("refund event: %+v", refunded)
	}
}

// A refund of a renewal takes days back and leaves the subscription on: no disabled flag.
func TestRefundEventOfARenewalKeepsTheSubscription(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.subscriber(10 * 24 * time.Hour)
	p := e.buy(u.ID, "ch-renew")
	e.now = e.now.Add(time.Hour)
	rv, err := e.s.Refund(ctx, p.ID)
	must(t, err)
	if rv.Action == "disabled" {
		t.Fatalf("a renewal refund turned the subscription off: %+v", rv)
	}
	evs := ofType(e.events(), events.PaymentRefunded)
	if len(evs) != 1 {
		t.Fatalf("refund events: %+v", evs)
	}
	var got events.Payment
	must(t, json.Unmarshal([]byte(evs[0].Data), &got))
	if got.Disabled || got.Kind != "renew" || got.PaymentID != p.ID || evs[0].UserID.Int64 != u.ID {
		t.Fatalf("refund event: %+v", got)
	}
	// The renewal made no subscription: only the subscriber's own was told.
	if n := len(ofType(e.events(), events.UserCreated)); n != 1 {
		t.Fatalf("%d user.created events", n)
	}
}

// A trial taken for free is told once: the subscription and the trial, in one transaction.
func TestTrialEvents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	trial := e.offerTrial()
	u, err := e.s.Trial(ctx, 901)
	must(t, err)
	evs := e.events()
	if got := typesOf(evs); !slices.Equal(got, []string{events.UserCreated, events.TrialStarted}) {
		t.Fatalf("events of a trial: %v", got)
	}
	var made events.User
	must(t, json.Unmarshal([]byte(evs[0].Data), &made))
	if made.Source != domain.UserFromTrial || made.TariffID != trial.ID || evs[0].UserID.Int64 != u.ID || evs[0].TgID.Int64 != 901 {
		t.Fatalf("user.created: %+v %+v", evs[0], made)
	}
	var started events.Trial
	must(t, json.Unmarshal([]byte(evs[1].Data), &started))
	if started.TariffID != trial.ID || started.Paid || started.PaymentID != 0 || evs[1].UserID.Int64 != u.ID || evs[1].TgID.Int64 != 901 {
		t.Fatalf("trial.started: %+v %+v", evs[1], started)
	}
	// A second tap is refused and tells nothing.
	if _, err := e.s.Trial(ctx, 901); !errors.Is(err, ErrTrialUsed) {
		t.Fatalf("a second trial: %v", err)
	}
	if n := len(e.events()); n != 2 {
		t.Fatalf("%d events after a refused trial", n)
	}
}

// A bought trial: the subscription, the payment, and the trial it is.
func TestPaidTrialEvents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	trial := e.priceTrial()
	p, err := e.s.TrialInvoice(ctx, 911, Stars)
	must(t, err)
	must(t, e.s.StarsPaid(ctx, 911, p.Payload, "ch-trial-ev", "XTR", 1))
	evs := e.events()
	if got := typesOf(evs); !slices.Equal(got, []string{events.UserCreated, events.PaymentPaid, events.TrialStarted}) {
		t.Fatalf("events of a bought trial: %v", got)
	}
	var made events.User
	must(t, json.Unmarshal([]byte(evs[0].Data), &made))
	var paid events.Payment
	must(t, json.Unmarshal([]byte(evs[1].Data), &paid))
	var started events.Trial
	must(t, json.Unmarshal([]byte(evs[2].Data), &started))
	if made.Source != domain.UserFromTrial || made.TariffID != trial.ID || evs[0].TgID.Int64 != 911 ||
		paid.Kind != KindTrial || !paid.Created || paid.PaymentID != p.ID ||
		started.TariffID != trial.ID || !started.Paid || started.PaymentID != p.ID {
		t.Fatalf("events of a bought trial: %+v %+v %+v", made, paid, started)
	}
	// Applied again: nothing new.
	must(t, e.s.Apply(ctx, p.ID))
	if n := len(e.events()); n != 3 {
		t.Fatalf("%d events after a second apply", n)
	}
}

// The free plan makes a subscription the addons hear of, but is not a trial.
func TestFreePlanEvents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	free := e.offerFree(FreeNoCondition)
	u, err := e.s.FreePlan(ctx, 911)
	must(t, err)
	evs := e.events()
	if len(evs) != 1 || evs[0].Type != events.UserCreated || evs[0].UserID.Int64 != u.ID || evs[0].TgID.Int64 != 911 {
		t.Fatalf("events of the free plan: %+v", evs)
	}
	var made events.User
	must(t, json.Unmarshal([]byte(evs[0].Data), &made))
	if made.Source != domain.UserFromFree || made.TariffID != free.ID {
		t.Fatalf("user.created: %+v", made)
	}
}

// A paid trial the account may not take goes back by itself: the addons hear the refund (the
// money is back, no subscription to turn off) and that the trial was not given, once.
func TestTrialNotGivenEvents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.priceTrial()
	first, err := e.s.TrialInvoice(ctx, 916, Stars)
	must(t, err)
	e.now = e.now.Add(invoiceReuse + time.Minute)
	second, err := e.s.TrialInvoice(ctx, 916, Stars)
	must(t, err)
	must(t, e.s.StarsPaid(ctx, 916, first.Payload, "ch-ev-1", "XTR", 1))
	must(t, e.s.StarsPaid(ctx, 916, second.Payload, "ch-ev-2", "XTR", 1))
	e.s.Reconcile(ctx) // a second look changes nothing
	refunds := ofType(e.events(), events.PaymentRefunded)
	gave := ofType(e.events(), events.TrialNotGiven)
	if len(refunds) != 1 || len(gave) != 1 {
		t.Fatalf("events after the second paid trial: %v", typesOf(e.events()))
	}
	var r events.Payment
	must(t, json.Unmarshal([]byte(refunds[0].Data), &r))
	var n events.NotGiven
	must(t, json.Unmarshal([]byte(gave[0].Data), &n))
	if r.PaymentID != second.ID || r.Disabled || refunds[0].UserID.Valid || refunds[0].TgID.Int64 != 916 {
		t.Fatalf("refund of the trial: %+v %+v", refunds[0], r)
	}
	if n.PaymentID != second.ID || n.Refund != events.RefundedAuto || n.Reason != events.TrialUsed || n.Amount != second.Amount ||
		n.Currency != "XTR" || n.TariffID == 0 || gave[0].TgID.Int64 != 916 || gave[0].UserID.Valid {
		t.Fatalf("trial.not_given: %+v %+v", gave[0], n)
	}
}

// Through a method that cannot refund the admin does it: told once as such, and the refund
// event comes only when the admin closes the payment.
func TestTrialNotGivenByHandEvents(t *testing.T) {
	e, fa, dir := adapterEnv(t, "fake")
	fa.noRefund = true
	runAdapter(t, dir, "fake", fa)
	ctx := context.Background()
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
		t.Fatal(err)
	}
	e.priceTrial()
	p, err := e.s.TrialInvoice(ctx, 921, "addon:fake")
	must(t, err)
	_, err = e.st.Q.TakeTrial(ctx, db.TakeTrialParams{TgID: 921, CreatedAt: e.now.Unix()})
	must(t, err)
	fa.set(p.ExternalID.String, func(s *addons.Status) { s.Status, s.Amount = "paid", p.Amount })
	must(t, e.s.checkAddon(ctx, p.Provider, p.ExternalID.String))
	e.s.Reconcile(ctx)
	must(t, e.s.Apply(ctx, p.ID))
	gave := ofType(e.events(), events.TrialNotGiven)
	if len(gave) != 1 || len(ofType(e.events(), events.PaymentRefunded)) != 0 {
		t.Fatalf("events while the admin has not refunded: %v", typesOf(e.events()))
	}
	var n events.NotGiven
	must(t, json.Unmarshal([]byte(gave[0].Data), &n))
	if n.Refund != events.RefundedManually || n.PaymentID != p.ID || n.Provider != "addon:fake" || n.Currency != "RUB" {
		t.Fatalf("trial.not_given: %+v", n)
	}
	must(t, e.s.RefundedByHand(ctx, p.ID))
	if err := e.s.RefundedByHand(ctx, p.ID); !errors.Is(err, ErrNotRefunable) {
		t.Fatalf("closed by hand twice: %v", err)
	}
	refunds := ofType(e.events(), events.PaymentRefunded)
	if len(refunds) != 1 || refunds[0].UserID.Valid || refunds[0].TgID.Int64 != 921 {
		t.Fatalf("refund events after the admin closed it: %+v", refunds)
	}
}
