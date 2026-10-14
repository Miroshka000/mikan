package billing

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

// Two trial invoices paid before either applies (two webhooks at once, or Stars while an
// adapter's report is on its way): each sees the other paid. One of them still gives the
// trial and only the other goes back: a paid trial is not refused for another trial
// payment, TakeTrial decides between them.
func TestPaidTrialBothPaidBeforeApply(t *testing.T) {
	for _, together := range []bool{false, true} {
		name := "one after the other"
		if together {
			name = "at once"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			e.priceTrial()
			first, err := e.s.TrialInvoice(ctx, 918, Stars)
			must(t, err)
			e.now = e.now.Add(invoiceReuse + time.Minute)
			second, err := e.s.TrialInvoice(ctx, 918, Stars)
			must(t, err)
			pays := []db.Payment{first, second}
			users := e.users()
			for i, p := range pays {
				n, err := e.st.Q.MarkPaymentPaid(ctx, db.MarkPaymentPaidParams{ExternalID: sql.NullString{String: "ch-both-" + strconv.Itoa(i), Valid: true},
					PaidAt: sql.NullInt64{Int64: e.now.Unix(), Valid: true}, ID: p.ID})
				must(t, err)
				if n != 1 {
					t.Fatalf("payment %d not marked paid", p.ID)
				}
			}
			if together {
				errs := make([]error, len(pays))
				var wg sync.WaitGroup
				for i, p := range pays {
					wg.Add(1)
					go func() {
						defer wg.Done()
						errs[i] = e.s.Apply(ctx, p.ID)
					}()
				}
				wg.Wait()
				must(t, errors.Join(errs...))
			} else {
				must(t, e.s.Apply(ctx, first.ID))
				if got := e.payment(first.ID); got.Status != "applied" {
					t.Fatalf("the first paid trial, with another one paid: %q %q", got.Status, got.Error)
				}
				must(t, e.s.Apply(ctx, second.ID))
			}
			statuses := map[string]int{}
			for _, p := range pays {
				statuses[e.payment(p.ID).Status]++
			}
			if statuses["applied"] != 1 || statuses["refunded"] != 1 {
				t.Fatalf("two trials paid before either applied ended as %v", statuses)
			}
			if e.users() != users+1 {
				t.Fatalf("two paid trials made %d subscriptions", e.users()-users)
			}
			if refunds := e.tg.refundNotes(); len(refunds) != 1 {
				t.Fatalf("the buyer was told of %d refunds", len(refunds))
			}
		})
	}
}

// A paid trial is sold through every payment method that takes its price, refunds or not.
// One that cannot be given when it applies goes back where the method refunds. Through an
// adapter without refunds the adapter is not asked: the payment stays paid with
// trial_refund_manual, the buyer is told once, the reconcile pass leaves it alone, and the
// admin closes it with RefundedByHand once the money is back. Nothing is given either way.
func TestPaidTrialThroughAnyAdapter(t *testing.T) {
	for _, refunds := range []bool{true, false} {
		name := "an adapter that refunds"
		if !refunds {
			name = "an adapter that cannot refund"
		}
		t.Run(name, func(t *testing.T) {
			e, fa, dir := adapterEnv(t, "fake")
			fa.noRefund = !refunds
			runAdapter(t, dir, "fake", fa)
			ctx := context.Background()
			if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
				t.Fatal(err)
			}
			e.priceTrial()
			o, av, ok := e.s.TrialOffer(ctx, 921)
			if !ok || o.Stars != 1 || o.Rub != 100 || len(av.Addons) != 1 {
				t.Fatalf("the trial through the adapter: %+v %+v %v", o, av, ok)
			}
			p, err := e.s.TrialInvoice(ctx, 921, "addon:fake")
			must(t, err)
			// The account takes its trial in another chat while the invoice is open.
			_, err = e.st.Q.TakeTrial(ctx, db.TakeTrialParams{TgID: 921, CreatedAt: e.now.Unix()})
			must(t, err)
			fa.set(p.ExternalID.String, func(s *addons.Status) { s.Status, s.Amount = "paid", p.Amount })
			err = e.s.checkAddon(ctx, p.Provider, p.ExternalID.String)
			got := e.payment(p.ID)
			if e.users() != 0 {
				t.Fatal("a trial that may not be given made a subscription")
			}
			if refunds {
				if err != nil || got.Status != "refunded" || len(fa.refundCalls) != 1 {
					t.Fatalf("refunded through the adapter: %v %+v %v", err, got, fa.refundCalls)
				}
				return
			}
			if err != nil || got.Status != "paid" || got.Error != TrialRefundByHand {
				t.Fatalf("the trial through an adapter without refunds: %v %+v", err, got)
			}
			if len(fa.refundCalls) != 0 {
				t.Fatalf("an adapter without refunds was asked to refund: %v", fa.refundCalls)
			}
			// The reconcile pass and a repeated report leave it alone: told once.
			e.s.Reconcile(ctx)
			must(t, e.s.Apply(ctx, p.ID))
			e.tg.mu.Lock()
			told := len(e.tg.notGiven)
			e.tg.mu.Unlock()
			if told != 1 || e.payment(p.ID).Error != TrialRefundByHand || len(fa.refundCalls) != 0 {
				t.Fatalf("after another pass: told %d times, %+v", told, e.payment(p.ID))
			}
			// The admin refunds it in the provider's dashboard and closes it.
			must(t, e.s.RefundedByHand(ctx, p.ID))
			got = e.payment(p.ID)
			if got.Status != "refunded" || !got.RefundedAt.Valid || got.Error != "" {
				t.Fatalf("refunded by hand: %+v", got)
			}
			if notes := e.tg.refundNotes(); len(notes) != 1 || notes[0].p.ID != p.ID || notes[0].u.ID != 0 {
				t.Fatalf("the buyer was told %+v", notes)
			}
			if err := e.s.RefundedByHand(ctx, p.ID); !errors.Is(err, ErrNotRefunable) {
				t.Fatalf("refunded by hand twice: %v", err)
			}
		})
	}
}

// An adapter that is not running when the money should go back is not one without
// refunds: the payment waits for the next pass instead of being left for a refund by hand,
// and goes back once the adapter runs again.
func TestPaidTrialRefundWaitsForTheAdapter(t *testing.T) {
	e, fa, dir := adapterEnv(t, "fake")
	runAdapter(t, dir, "fake", fa)
	ctx := context.Background()
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
		t.Fatal(err)
	}
	e.priceTrial()
	p, err := e.s.TrialInvoice(ctx, 927, "addon:fake")
	must(t, err)
	_, err = e.st.Q.TakeTrial(ctx, db.TakeTrialParams{TgID: 927, CreatedAt: e.now.Unix()})
	must(t, err)
	n, err := e.st.Q.MarkPaymentPaid(ctx, db.MarkPaymentPaidParams{ExternalID: p.ExternalID, PaidAt: sql.NullInt64{Int64: e.now.Unix(), Valid: true}, ID: p.ID})
	must(t, err)
	if n != 1 {
		t.Fatal("the trial payment not marked paid")
	}
	state := filepath.Join(dir, "addons", "state.json")
	running, err := os.ReadFile(state)
	must(t, err)
	must(t, os.WriteFile(state, bytes.Replace(running, []byte(`"status":"running"`), []byte(`"status":"failed"`), 1), 0o600))
	if err := e.s.Apply(ctx, p.ID); err == nil {
		t.Fatal("a refund through an adapter that is not running passed as done")
	}
	if got := e.payment(p.ID); got.Status != "paid" || got.Error == "trial_refund_failed" || got.Error == "" {
		t.Fatalf("the payment while the adapter is down: %+v", got)
	}
	must(t, os.WriteFile(state, running, 0o600))
	must(t, e.s.Apply(ctx, p.ID))
	if got := e.payment(p.ID); got.Status != "refunded" || len(fa.refundCalls) != 1 {
		t.Fatalf("the refund once the adapter runs: %+v %v", got, fa.refundCalls)
	}
}

// "Refunded by hand" is for a payment through an adapter that was paid and never applied:
// Stars go back through the panel, and what was applied or never paid is not closed so.
func TestRefundedByHandOnlyPaidAdapterPayments(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.priceTrial()
	stars, err := e.s.TrialInvoice(ctx, 928, Stars)
	must(t, err)
	if err := e.s.RefundedByHand(ctx, stars.ID); !errors.Is(err, ErrNotRefunable) {
		t.Fatalf("an open Stars invoice closed by hand: %v", err)
	}
	must(t, e.s.StarsPaid(ctx, 928, stars.Payload, "ch-928", "XTR", 1))
	if err := e.s.RefundedByHand(ctx, stars.ID); !errors.Is(err, ErrNotRefunable) {
		t.Fatalf("an applied Stars payment closed by hand: %v", err)
	}
	p, err := e.st.Q.CreatePayment(ctx, db.CreatePaymentParams{Provider: "addon:fake", Payload: "pending-929", TgID: 929, Kind: "new",
		TariffID: sql.NullInt64{Int64: e.sale.ID, Valid: true}, TariffName: e.sale.Name, Amount: 100, Currency: "RUB", CreatedAt: e.now.Unix()})
	must(t, err)
	if err := e.s.RefundedByHand(ctx, p.ID); !errors.Is(err, ErrNotRefunable) {
		t.Fatalf("an unpaid invoice closed by hand: %v", err)
	}
	if got := e.payment(p.ID); got.Status != "pending" {
		t.Fatalf("the unpaid invoice: %+v", got)
	}
}

// An open invoice of the last minutes is given again only to an account that may still
// take the trial: one that became a customer, or took its trial, meanwhile gets no pay link.
func TestPaidTrialOpenInvoiceAsksAgain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		become func(e *env, tg int64)
	}{
		{"a subscription linked", func(e *env, tg int64) {
			u, err := e.s.d.Users.Create(context.Background(), domain.CreateInput{Name: "bought one", TariffID: e.sale.ID})
			must(e.t, err)
			must(e.t, e.st.Q.LinkTg(context.Background(), db.LinkTgParams{UserID: u.ID, TgID: tg, CreatedAt: e.now.Unix()}))
		}},
		{"the trial taken", func(e *env, tg int64) {
			_, err := e.st.Q.TakeTrial(context.Background(), db.TakeTrialParams{TgID: tg, CreatedAt: e.now.Unix()})
			must(e.t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			e.priceTrial()
			first, err := e.s.TrialInvoice(ctx, 922, Stars)
			must(t, err)
			again, err := e.s.TrialInvoice(ctx, 922, Stars)
			must(t, err)
			if again.ID != first.ID {
				t.Fatal("the open invoice was not given again")
			}
			tc.become(e, 922)
			if p, err := e.s.TrialInvoice(ctx, 922, Stars); !errors.Is(err, ErrTrialUsed) {
				t.Fatalf("the open invoice given again: %+v %v", p, err)
			}
		})
	}
}

// What the account did between the invoice and the payment counts when the trial applies,
// not only a trial taken: a plan paid for, or a subscription linked to it, and the trial's
// money goes back with nothing made.
func TestPaidTrialAppliedToACustomer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		become func(e *env, tg int64)
	}{
		{"a plan paid for", func(e *env, tg int64) {
			ctx := context.Background()
			p, err := e.st.Q.CreatePayment(ctx, db.CreatePaymentParams{Provider: Stars, Payload: "plan-923", TgID: tg, Kind: "new",
				TariffID: sql.NullInt64{Int64: e.sale.ID, Valid: true}, TariffName: e.sale.Name, Amount: 50, Currency: "XTR", CreatedAt: e.now.Unix()})
			must(e.t, err)
			n, err := e.st.Q.MarkPaymentPaid(ctx, db.MarkPaymentPaidParams{ExternalID: sql.NullString{String: "ch-plan", Valid: true},
				PaidAt: sql.NullInt64{Int64: e.now.Unix(), Valid: true}, ID: p.ID})
			must(e.t, err)
			if n != 1 {
				e.t.Fatal("the plan's payment not marked paid")
			}
		}},
		{"a subscription linked", func(e *env, tg int64) {
			u, err := e.s.d.Users.Create(context.Background(), domain.CreateInput{Name: "bought one", TariffID: e.sale.ID})
			must(e.t, err)
			must(e.t, e.st.Q.LinkTg(context.Background(), db.LinkTgParams{UserID: u.ID, TgID: tg, CreatedAt: e.now.Unix()}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			e.priceTrial()
			p, err := e.s.TrialInvoice(ctx, 923, Stars)
			must(t, err)
			must(t, e.s.PreCheckout(ctx, 923, p.Payload, "XTR", 1))
			tc.become(e, 923)
			users := e.users()
			must(t, e.s.StarsPaid(ctx, 923, p.Payload, "ch-trial-923", "XTR", 1))
			if got := e.payment(p.ID); got.Status != "refunded" || got.Error != "" {
				t.Fatalf("the trial paid by a customer: %+v", got)
			}
			if e.users() != users {
				t.Fatal("the trial made a subscription for a customer")
			}
			if has, err := e.st.Q.HasTrial(ctx, 923); err != nil || has {
				t.Fatalf("the trial taken by a payment that went back: %v %v", has, err)
			}
			if refunds := append([]string(nil), e.tg.refunds...); len(refunds) != 1 || refunds[0] != "ch-trial-923" {
				t.Fatalf("refunds %v", refunds)
			}
		})
	}
}

// Telegram reports the Stars it has just returned (refunded_payment) before the trial's
// refund is recorded: the payment ends refunded all the same, without an error, and the
// buyer is told once.
func TestPaidTrialRefundReportedMeanwhile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.priceTrial()
	p, err := e.s.TrialInvoice(ctx, 924, Stars)
	must(t, err)
	_, err = e.st.Q.TakeTrial(ctx, db.TakeTrialParams{TgID: 924, CreatedAt: e.now.Unix()})
	must(t, err)
	e.tg.onRefund = func() {
		// The update for an unapplied payment asks nothing of the bot, so it may run here.
		if err := e.s.StarsRefunded(ctx, 924, "ch-meanwhile"); err != nil {
			t.Error(err)
		}
	}
	must(t, e.s.StarsPaid(ctx, 924, p.Payload, "ch-meanwhile", "XTR", 1))
	if got := e.payment(p.ID); got.Status != "refunded" || !got.RefundedAt.Valid || got.Error != "" {
		t.Fatalf("the trial refunded meanwhile: %+v", got)
	}
	if notes := e.tg.refundNotes(); len(notes) != 1 || notes[0].p.ID != p.ID || notes[0].u.ID != 0 {
		t.Fatalf("the buyer was told %+v", notes)
	}
}

// The trial's money went back, but the payment moved on meanwhile (another pass applied
// it): the error is the admin's to settle, so the payment carries it for the Payments page.
func TestPaidTrialChangedWhileRefunded(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.priceTrial()
	p, err := e.s.TrialInvoice(ctx, 926, Stars)
	must(t, err)
	_, err = e.st.Q.TakeTrial(ctx, db.TakeTrialParams{TgID: 926, CreatedAt: e.now.Unix()})
	must(t, err)
	e.tg.onRefund = func() {
		if _, err := e.st.Q.SetPaymentStatus(ctx, db.SetPaymentStatusParams{NewStatus: PaymentApplied, ID: p.ID, OldStatus: PaymentPaid}); err != nil {
			t.Error(err)
		}
	}
	if err := e.s.StarsPaid(ctx, 926, p.Payload, "ch-changed", "XTR", 1); err == nil {
		t.Fatal("a trial payment that changed while refunded passed as done")
	}
	if got := e.payment(p.ID); got.Status != "applied" || got.Error != "the trial payment changed while its money was going back" {
		t.Fatalf("the payment that changed: %+v", got)
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
