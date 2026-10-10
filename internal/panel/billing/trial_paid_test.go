package billing

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// priceTrial turns on the trial on the seeded trial tariff (3 days) at ⭐ 1 or 1 ₽, with
// selling on and new subscriptions off: a priced trial does not need them.
func (e *env) priceTrial() db.Tariff {
	e.t.Helper()
	ctx := context.Background()
	ts, err := e.st.Q.ListTariffs(ctx)
	must(e.t, err)
	trial := ts[0]
	must(e.t, settings.Set(ctx, e.s.d.Settings, KeyConfig, Config{Enabled: true, Stars: true, TrialTariffID: trial.ID, TrialStars: 1, TrialRub: 100}))
	return trial
}

func TestPaidTrialIsBoughtOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	trial := e.priceTrial()
	if _, err := e.s.Trial(ctx, 911); !errors.Is(err, ErrTrialPaid) || e.s.TrialOpen(ctx, 911) {
		t.Fatalf("a priced trial was given for free: %v", err)
	}
	o, av, ok := e.s.TrialOffer(ctx, 911)
	// No adapter runs here: the trial is sold for Stars only.
	if !ok || o.Tariff.ID != trial.ID || o.Stars != 1 || o.Rub != 0 || !av.Stars {
		t.Fatalf("trial offer %+v %+v %v", o, av, ok)
	}
	p, err := e.s.TrialInvoice(ctx, 911, Stars)
	must(t, err)
	if p.Kind != KindTrial || p.Amount != 1 || p.Currency != "XTR" || p.TariffID.Int64 != trial.ID || p.TermDays.Int64 != trial.DurationDays || p.PayUrl == "" {
		t.Fatalf("trial invoice %+v", p)
	}
	again, err := e.s.TrialInvoice(ctx, 911, Stars)
	if err != nil || again.ID != p.ID {
		t.Fatalf("a second tap made another invoice: %v %+v", err, again)
	}
	// The trial tariff is not on sale itself, and need not be.
	must(t, e.s.PreCheckout(ctx, 911, p.Payload, "XTR", 1))
	must(t, e.s.StarsPaid(ctx, 911, p.Payload, "ch-trial", "XTR", 1))
	got := e.payment(p.ID)
	if got.Status != "applied" || !got.UserID.Valid || got.Revert == "" {
		t.Fatalf("trial payment %+v", got)
	}
	u := e.user(got.UserID.Int64)
	if u.TariffID.Int64 != trial.ID || u.Source != domain.UserFromTrial ||
		u.ExpiresAt.Int64 != e.now.Add(time.Duration(trial.DurationDays)*24*time.Hour).Unix() {
		t.Fatalf("trial user %+v", u)
	}
	links, err := e.st.Q.ListTgLinksOf(ctx, 911)
	if err != nil || len(links) != 1 || links[0].ID != u.ID {
		t.Fatalf("the trial is not linked to the account: %+v %v", links, err)
	}
	if taken, _ := e.st.Q.HasTrial(ctx, 911); !taken {
		t.Fatal("the paid trial did not take the account's trial")
	}
	if e.tg.told() != 1 {
		t.Fatalf("the buyer was told %d times", e.tg.told())
	}
	if _, _, ok := e.s.TrialOffer(ctx, 911); ok {
		t.Fatal("the trial is offered again after it was bought")
	}
	if _, err := e.s.TrialInvoice(ctx, 911, Stars); !errors.Is(err, ErrTrialUsed) {
		t.Fatalf("a second paid trial: %v", err)
	}
}

// Customers do not buy the trial: an account with a subscription or a free trial taken.
func TestPaidTrialIsForNewPeople(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.priceTrial()
	u, err := e.s.d.Users.Create(ctx, domain.CreateInput{Name: "linked", TariffID: e.sale.ID})
	must(t, err)
	must(t, e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 912, CreatedAt: e.now.Unix()}))
	if _, err := e.s.TrialInvoice(ctx, 912, Stars); !errors.Is(err, ErrTrialUsed) {
		t.Fatalf("an account with a subscription bought the trial: %v", err)
	}
	if _, err := e.st.Q.TakeTrial(ctx, db.TakeTrialParams{TgID: 913, CreatedAt: e.now.Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := e.s.TrialOffer(ctx, 913); ok {
		t.Fatal("the trial is offered to an account that took it")
	}
	if _, err := e.s.TrialInvoice(ctx, 913, Stars); !errors.Is(err, ErrTrialUsed) {
		t.Fatalf("an account that took the trial bought it: %v", err)
	}
}

// A priced trial is sold: with selling off, or for a currency nobody takes, it is not.
func TestPaidTrialNeedsAProvider(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	trial := e.priceTrial()
	must(t, settings.Set(ctx, e.s.d.Settings, KeyConfig, Config{Stars: true, TrialTariffID: trial.ID, TrialStars: 1}))
	if _, _, ok := e.s.TrialOffer(ctx, 914); ok {
		t.Fatal("a trial offered with selling off")
	}
	if _, err := e.s.TrialInvoice(ctx, 914, Stars); !errors.Is(err, ErrProviderOff) {
		t.Fatalf("a trial invoice with selling off: %v", err)
	}
	// Rubles only, and no adapter takes them.
	must(t, settings.Set(ctx, e.s.d.Settings, KeyConfig, Config{Enabled: true, Stars: true, TrialTariffID: trial.ID, TrialRub: 100}))
	if _, _, ok := e.s.TrialOffer(ctx, 914); ok {
		t.Fatal("a trial offered for rubles nobody takes")
	}
	if _, err := e.s.TrialInvoice(ctx, 914, Stars); !errors.Is(err, ErrProviderOff) {
		t.Fatalf("a rubles trial bought for Stars: %v", err)
	}
	// A free trial has no invoice.
	must(t, settings.Set(ctx, e.s.d.Settings, KeyConfig, Config{Enabled: true, Stars: true, TrialTariffID: trial.ID}))
	if _, err := e.s.TrialInvoice(ctx, 914, Stars); !errors.Is(err, ErrTrialOff) {
		t.Fatalf("an invoice for a free trial: %v", err)
	}
}

// A refunded trial turns its subscription off; the account's trial stays taken.
func TestPaidTrialRefund(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.priceTrial()
	p, err := e.s.TrialInvoice(ctx, 915, Stars)
	must(t, err)
	must(t, e.s.StarsPaid(ctx, 915, p.Payload, "ch-trial-refund", "XTR", 1))
	applied := e.payment(p.ID)
	e.now = e.now.Add(time.Hour)
	rv, err := e.s.Refund(ctx, p.ID)
	must(t, err)
	if rv.Action != "disabled" {
		t.Fatalf("a refunded trial did %q", rv.Action)
	}
	if u := e.user(applied.UserID.Int64); u.Status != "disabled" {
		t.Fatalf("the refunded trial's subscription is %q", u.Status)
	}
	if _, err := e.s.TrialInvoice(ctx, 915, Stars); !errors.Is(err, ErrTrialUsed) {
		t.Fatalf("a refund gave the trial back: %v", err)
	}
}

// Two trial invoices paid at once give one trial: the second is given back. It is paid
// without a pre-checkout on purpose, as an adapter's webhook arrives, or as a pre-checkout
// answered before the first payment landed would have let it through.
func TestPaidTrialPaidTwice(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.priceTrial()
	first, err := e.s.TrialInvoice(ctx, 916, Stars)
	must(t, err)
	// A second invoice of the same account: within the first minutes the open one is
	// shown again instead.
	e.now = e.now.Add(invoiceReuse + time.Minute)
	second, err := e.s.TrialInvoice(ctx, 916, Stars)
	must(t, err)
	if second.ID == first.ID {
		t.Fatal("the second tap reused the open invoice")
	}
	users := e.users()
	must(t, e.s.StarsPaid(ctx, 916, first.Payload, "ch-trial-1", "XTR", 1))
	// Telegram is down for the refund: the payment waits with its reason on it.
	e.tg.refundErr = errors.New("Telegram unavailable")
	if err := e.s.StarsPaid(ctx, 916, second.Payload, "ch-trial-2", "XTR", 1); err == nil {
		t.Fatal("a second trial that could not be refunded passed as done")
	}
	if got := e.payment(second.ID); got.Status != "paid" || got.Error != "trial_refund_failed" {
		t.Fatalf("the second paid trial after a failed refund: %+v", got)
	}
	// The reconcile pass tries the refund again, and Telegram is back.
	e.tg.refundErr = nil
	e.s.Reconcile(ctx)
	if got := e.payment(first.ID); got.Status != "applied" {
		t.Fatalf("the first paid trial is %q", got.Status)
	}
	got := e.payment(second.ID)
	if got.Status != "refunded" || !got.RefundedAt.Valid || got.Error != "" {
		t.Fatalf("the second paid trial: %+v", got)
	}
	if e.users() != users+1 {
		t.Fatalf("two paid trials made %d subscriptions", e.users()-users)
	}
	if refunds := append([]string(nil), e.tg.refunds...); len(refunds) != 1 || refunds[0] != "ch-trial-2" {
		t.Fatalf("refunds %v", refunds)
	}
	// Told once, about the money and not about a subscription: there is none.
	if notes := e.tg.refundNotes(); len(notes) != 1 || notes[0].p.ID != second.ID || notes[0].u.ID != 0 || notes[0].disabled {
		t.Fatalf("the buyer was told %+v", notes)
	}
	if links, err := e.st.Q.ListTgLinksOf(ctx, 916); err != nil || len(links) != 1 {
		t.Fatalf("links %+v %v", links, err)
	}
}

// What was true at the invoice is asked again before the Stars are taken: an account that
// became a customer in between pays nothing.
func TestPaidTrialPreCheckoutAsksAgain(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.priceTrial()
	p, err := e.s.TrialInvoice(ctx, 917, Stars)
	must(t, err)
	must(t, e.s.PreCheckout(ctx, 917, p.Payload, "XTR", 1))
	u, err := e.s.d.Users.Create(ctx, domain.CreateInput{Name: "bought one", TariffID: e.sale.ID})
	must(t, err)
	must(t, e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 917, CreatedAt: e.now.Unix()}))
	if err := e.s.PreCheckout(ctx, 917, p.Payload, "XTR", 1); !errors.Is(err, ErrTrialUsed) {
		t.Fatalf("a customer's trial invoice passed the pre-checkout: %v", err)
	}
}

// An invoice outlives the offer it was made for: the trial turned off, made free, or moved
// to another tariff. The pre-checkout refuses it either way, so no Stars are taken.
func TestPaidTrialPreCheckoutAfterTheOfferChanged(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	trial := e.priceTrial()
	p, err := e.s.TrialInvoice(ctx, 918, Stars)
	must(t, err)
	for _, c := range []struct {
		what string
		cfg  Config
	}{
		{"off", Config{Enabled: true, Stars: true}},
		{"free", Config{Enabled: true, Stars: true, TrialTariffID: trial.ID}},
		{"on another tariff", Config{Enabled: true, Stars: true, TrialTariffID: e.sale.ID, TrialStars: 1}},
	} {
		must(t, settings.Set(ctx, e.s.d.Settings, KeyConfig, c.cfg))
		if err := e.s.PreCheckout(ctx, 918, p.Payload, "XTR", 1); !errors.Is(err, ErrTrialOff) {
			t.Fatalf("the pre-checkout of an invoice for a trial %s: %v", c.what, err)
		}
	}
}

// A priced trial bought through an adapter, with the trial taken in between: the money
// goes back through the adapter under a key of the payment's own, and nothing is given.
// The card is charged on the provider's page, so this is the apply-time check, not the
// pre-checkout, that has to catch it.
func TestPaidTrialThroughAdapterIsRefunded(t *testing.T) {
	e, fa := addonEnv(t)
	ctx := context.Background()
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
		t.Fatal(err)
	}
	e.priceTrial()
	p, err := e.s.TrialInvoice(ctx, 919, "addon:fake")
	must(t, err)
	if p.Currency != "RUB" || p.Amount != 100 {
		t.Fatalf("trial invoice through the adapter: %+v", p)
	}
	// The same account takes the trial while the invoice is open, in another chat.
	if _, err := e.st.Q.TakeTrial(ctx, db.TakeTrialParams{TgID: 919, CreatedAt: e.now.Unix()}); err != nil {
		t.Fatal(err)
	}
	fa.set(p.ExternalID.String, func(s *addons.Status) { s.Status, s.Amount = "paid", p.Amount })
	must(t, e.s.checkAddon(ctx, p.Provider, p.ExternalID.String))
	if got := e.payment(p.ID); got.Status != "refunded" || !got.RefundedAt.Valid || got.Error != "" {
		t.Fatalf("the trial paid through the adapter: %+v", got)
	}
	if len(fa.refunds) != 1 || fa.refunds[0] != p.ExternalID.String {
		t.Fatalf("refunds %v", fa.refunds)
	}
	if len(fa.refundCalls) != 1 || fa.refundCalls[0] != "mikan-trial-"+strconv.FormatInt(p.ID, 10) {
		t.Fatalf("refund keys %v", fa.refundCalls)
	}
	if e.users() != 0 {
		t.Fatal("a trial that may not be given made a subscription")
	}
}
