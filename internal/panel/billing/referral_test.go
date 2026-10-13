package billing

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"mikan/internal/panel/referral"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// referralEnv is the billing fixture with the referral programme wired in: 10% of what an
// invitee pays goes to the balance of the account that invited them, and the balance pays
// part of their own next invoice.
func referralEnv(t *testing.T) (*env, *referral.Service, referral.Program) {
	t.Helper()
	e := newEnv(t)
	refs := referral.New(e.st, e.s.d.Settings, slog.New(slog.NewTextHandler(e.logs, nil)), func() time.Time { return e.now })
	e.s.d.Referral = refs
	cfg := referral.Program{Enabled: true, StarsRate: 100, Spend: true, Notify: true,
		Rules: []referral.Rule{{ID: "p", Kind: referral.KindPercent, Value: 10, TariffIDs: []int64{e.sale.ID}, Enabled: true}}}
	must(t, cfg.Validate())
	must(t, settings.Set(context.Background(), e.s.d.Settings, referral.KeyConfig, cfg))
	return e, refs, cfg
}

// invite ties the buyer's Telegram account to somebody else's code.
func invite(t *testing.T, refs *referral.Service, inviter, invitee int64) {
	t.Helper()
	ctx := context.Background()
	code, err := refs.Code(ctx, inviter)
	must(t, err)
	_, err = refs.Link(ctx, invitee, code)
	must(t, err)
}

func balance(t *testing.T, e *env, tgID int64) int64 {
	t.Helper()
	got, err := e.st.Q.ReferralBalance(context.Background(), tgID)
	if err == sql.ErrNoRows {
		return 0
	}
	must(t, err)
	return got
}

// The buyer's own invoice is cheaper by what their balance holds, and the hold goes with
// it: opening the same purchase again does not take the balance twice.
func TestReferralBalancePaysTheInvoice(t *testing.T) {
	e, refs, _ := referralEnv(t)
	ctx := context.Background()
	invite(t, refs, 101, 555)

	// The inviter has a balance to spend.
	must(t, e.st.Tx(ctx, func(q *db.Queries) error {
		_, err := q.TakeReferralAccount(ctx, db.TakeReferralAccountParams{TgID: 101, Code: "inviter1", CreatedAt: e.now.Unix()})
		return err
	}))
	must(t, e.st.Tx(ctx, func(q *db.Queries) error {
		_, err := q.AddReferralBalance(ctx, db.AddReferralBalanceParams{TgID: 101, Amount: 5000, UpdatedAt: e.now.Unix()})
		return err
	}))

	first, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 101, TariffID: e.sale.ID, Provider: Stars})
	must(t, err)
	if want := e.sale.PriceStars.Int64 - 50; first.Amount != want || first.Currency != "XTR" {
		t.Fatalf("a balance of 50 ₽ off a %d Stars tariff: %+v", e.sale.PriceStars.Int64, first)
	}
	if got := balance(t, e, 101); got != 0 {
		t.Fatalf("%d kopecks left after the invoice took them", got)
	}

	// The same purchase within the reuse window is the same invoice, not a second hold.
	again, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 101, TariffID: e.sale.ID, Provider: Stars})
	must(t, err)
	if again.ID != first.ID {
		t.Fatalf("the invoice was opened again: %d then %d", first.ID, again.ID)
	}
	if got := balance(t, e, 101); got != 0 {
		t.Fatalf("the balance was taken twice: %d left", got)
	}
}

// What the balance held comes back with the refund: a refund undoes the whole payment.
func TestReferralBalanceComesBackWithTheRefund(t *testing.T) {
	e, refs, _ := referralEnv(t)
	ctx := context.Background()
	invite(t, refs, 101, 555)
	must(t, e.st.Tx(ctx, func(q *db.Queries) error {
		_, err := q.TakeReferralAccount(ctx, db.TakeReferralAccountParams{TgID: 101, Code: "inviter2", CreatedAt: e.now.Unix()})
		return err
	}))
	must(t, e.st.Tx(ctx, func(q *db.Queries) error {
		_, err := q.AddReferralBalance(ctx, db.AddReferralBalanceParams{TgID: 101, Amount: 3000, UpdatedAt: e.now.Unix()})
		return err
	}))

	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 101, TariffID: e.sale.ID, Provider: Stars})
	must(t, err)
	if got := balance(t, e, 101); got != 0 {
		t.Fatalf("%d kopecks left after the hold", got)
	}
	// Paid with Stars, then refunded by the admin.
	must(t, e.s.StarsPaid(ctx, 101, p.Payload, "charge1", "XTR", p.Amount))
	latest, err := e.st.Q.GetPayment(ctx, p.ID)
	must(t, err)
	if latest.Status != "applied" {
		t.Fatalf("the payment was not applied: %+v", latest)
	}
	if _, err := e.s.Refund(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got := balance(t, e, 101); got != 3000 {
		t.Fatalf("after the refund the balance is %d", got)
	}
}

// A purchase through somebody else's link earns the inviter, once, on the transaction that
// applied it — and the reward holds even though the buyer never opened the referral screen.
func TestInviteePaymentEarnsTheInviter(t *testing.T) {
	e, refs, _ := referralEnv(t)
	ctx := context.Background()
	invite(t, refs, 101, 555)

	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars})
	must(t, err)
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "charge2", "XTR", p.Amount))

	// The tariff costs 150 Stars; at a hundred kopecks a star that is 150 ₽, and a tenth
	// of it is 15 ₽.
	if got, want := balance(t, e, 101), int64(1500); got != want {
		t.Fatalf("the inviter earned %d kopecks, wanted %d", got, want)
	}
	st, err := refs.StatsOf(ctx, 101)
	must(t, err)
	if st.Invited != 1 || st.Paid != 1 || st.Balance != 1500 {
		t.Fatalf("the inviter's stats: %+v", st)
	}
	// Applying the same payment again pays nothing twice.
	must(t, e.s.Apply(ctx, p.ID))
	if got := balance(t, e, 101); got != 1500 {
		t.Fatalf("a repeated apply earned %d kopecks", got)
	}
}

// An invoice the provider refused gives the hold back: nobody paid for it.
func TestReferralHoldIsReleasedWhenNobodyPays(t *testing.T) {
	e, refs, _ := referralEnv(t)
	ctx := context.Background()
	invite(t, refs, 101, 555)
	must(t, e.st.Tx(ctx, func(q *db.Queries) error {
		_, err := q.TakeReferralAccount(ctx, db.TakeReferralAccountParams{TgID: 101, Code: "inviter3", CreatedAt: e.now.Unix()})
		return err
	}))
	must(t, e.st.Tx(ctx, func(q *db.Queries) error {
		_, err := q.AddReferralBalance(ctx, db.AddReferralBalanceParams{TgID: 101, Amount: 2000, UpdatedAt: e.now.Unix()})
		return err
	}))
	_, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 101, TariffID: e.sale.ID, Provider: Stars})
	must(t, err)
	if got := balance(t, e, 101); got != 0 {
		t.Fatalf("%d kopecks left on the hold", got)
	}
	// The reconcile pass expires invoices nobody paid and gives the hold back.
	e.now = e.now.Add(pendingTTL + time.Minute)
	e.s.Reconcile(ctx)
	if got := balance(t, e, 101); got != 2000 {
		t.Fatalf("an expired invoice kept %d kopecks", 2000-got)
	}
}
