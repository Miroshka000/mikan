package billing

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
)

const term = 30 * 24 * time.Hour

// subscriber is buyer 555's subscription on the first tariff (not the one on sale) that
// ends in left.
func (e *env) subscriber(left time.Duration) db.User {
	e.t.Helper()
	ctx := context.Background()
	ts, err := e.st.Q.ListTariffs(ctx)
	must(e.t, err)
	u, err := e.s.d.Users.Create(ctx, domain.CreateInput{Name: "mine", TariffID: ts[0].ID})
	must(e.t, err)
	must(e.t, e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 555, CreatedAt: 1}))
	_, err = e.st.DB.ExecContext(ctx, "UPDATE users SET expires_at = $1 WHERE id = $2", e.now.Add(left).Unix(), u.ID)
	must(e.t, err)
	return e.user(u.ID)
}

func (e *env) user(id int64) db.User {
	e.t.Helper()
	u, err := e.st.Q.GetUser(context.Background(), id)
	must(e.t, err)
	return u
}

// buy pays an invoice of the tariff on sale (user 0: a new subscription) and returns the
// applied payment.
func (e *env) buy(user int64, charge string) db.Payment {
	e.t.Helper()
	p := e.invoice(555, user, Stars)
	must(e.t, e.s.StarsPaid(context.Background(), 555, p.Payload, charge, p.Currency, p.Amount))
	got := e.payment(p.ID)
	if got.Status != "applied" {
		e.t.Fatalf("payment %s is %q", charge, got.Status)
	}
	return got
}

func TestRefundNewSubscriptionDisablesIt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.buy(0, "ch-new")
	if p.Revert == "" {
		t.Fatal("what the payment did is not recorded")
	}
	e.now = e.now.Add(time.Hour)
	rv, err := e.s.Refund(ctx, p.ID)
	must(t, err)
	u := e.user(p.UserID.Int64)
	if rv.Action != "disabled" || u.Status != "disabled" || u.ExpiresAt.Int64 != e.now.Unix() {
		t.Fatalf("reverted %+v, user %q expires %d (now %d)", rv, u.Status, u.ExpiresAt.Int64, e.now.Unix())
	}
	// The subscription is turned off, not deleted: the buyer's link stays.
	if link, err := e.st.Q.GetTgLink(ctx, u.ID); err != nil || link.TgID != 555 {
		t.Fatalf("link: %+v %v", link, err)
	}
	notes := e.tg.refundNotes()
	if len(notes) != 1 || !notes[0].disabled || notes[0].u.ID != u.ID || notes[0].p.Status != "refunded" {
		t.Fatalf("buyer told: %+v", notes)
	}
}

func TestRefundRenewalTakesBackTheTermAndRestoresTheTariff(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ts, _ := e.st.Q.ListTariffs(ctx)
	prior := e.subscriber(10 * 24 * time.Hour)
	p := e.buy(prior.ID, "ch-r")
	if got := e.user(prior.ID); got.ExpiresAt.Int64 != prior.ExpiresAt.Int64+int64(term/time.Second) || got.TariffID.Int64 != e.sale.ID {
		t.Fatalf("renewed: %+v", got)
	}
	e.now = e.now.Add(2 * time.Hour)
	rv, err := e.s.Refund(ctx, p.ID)
	must(t, err)
	got := e.user(prior.ID)
	if rv.Action != "term" || !rv.TariffRestored || !rv.Exact || got.ExpiresAt != prior.ExpiresAt || got.TariffID.Int64 != ts[0].ID ||
		got.TrafficLimit != ts[0].TrafficLimit || got.DeviceLimit != ts[0].DeviceLimit || got.ResetStrategy != ts[0].ResetStrategy || got.Status != prior.Status {
		t.Fatalf("reverted %+v\nuser %+v\nwas  %+v", rv, got, prior)
	}
	if notes := e.tg.refundNotes(); len(notes) != 1 || notes[0].disabled || notes[0].u.ExpiresAt != prior.ExpiresAt {
		t.Fatalf("buyer told: %+v", notes)
	}
}

// A subscription that ended before the renewal gets back to ended: now, not the old end.
func TestRefundRenewalOfAnEndedSubscriptionEndsNow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	prior := e.subscriber(-24 * time.Hour)
	p := e.buy(prior.ID, "ch-ended")
	if got := e.user(prior.ID); got.ExpiresAt.Int64 != e.now.Add(term).Unix() {
		t.Fatalf("renewed from now: %d", got.ExpiresAt.Int64)
	}
	e.now = e.now.Add(time.Hour)
	_, err := e.s.Refund(ctx, p.ID)
	must(t, err)
	if got := e.user(prior.ID); got.ExpiresAt.Int64 != e.now.Unix() {
		t.Fatalf("expires %d, want now %d", got.ExpiresAt.Int64, e.now.Unix())
	}
}

// Only the refunded term goes: what a later payment added and the tariff it set stay.
func TestRefundOfAnEarlierRenewalKeepsTheLaterOne(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	prior := e.subscriber(10 * 24 * time.Hour)
	first := e.buy(prior.ID, "ch-1")
	e.now = e.now.Add(time.Hour)
	second := e.buy(prior.ID, "ch-2")
	end := prior.ExpiresAt.Int64
	if got := e.user(prior.ID); got.ExpiresAt.Int64 != end+2*int64(term/time.Second) {
		t.Fatalf("two renewals: %d", got.ExpiresAt.Int64)
	}
	rv, err := e.s.Refund(ctx, first.ID)
	must(t, err)
	got := e.user(prior.ID)
	if rv.TariffRestored || got.TariffID.Int64 != e.sale.ID || got.Status != "active" || got.ExpiresAt.Int64 != end+int64(term/time.Second) {
		t.Fatalf("reverted %+v, user %+v", rv, got)
	}
	// Then the later one: back to where the subscription was.
	_, err = e.s.Refund(ctx, second.ID)
	must(t, err)
	if got := e.user(prior.ID); got.ExpiresAt.Int64 != end {
		t.Fatalf("both refunded: expires %d, want %d", got.ExpiresAt.Int64, end)
	}
}

// A subscription the payment created and a renewal built on: the renewal's days stay.
func TestRefundOfTheNewPaymentKeepsALaterRenewal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first := e.buy(0, "ch-new")
	uid := first.UserID.Int64
	end := e.user(uid).ExpiresAt.Int64
	e.now = e.now.Add(time.Hour)
	e.buy(uid, "ch-renew")
	_, err := e.s.Refund(ctx, first.ID)
	must(t, err)
	if got := e.user(uid); got.Status != "active" || got.ExpiresAt.Int64 != end {
		t.Fatalf("user %+v, want active until %d", got, end)
	}
}

// A tariff or limits the admin changed after the payment are not put back.
func TestRefundKeepsTheTariffTheAdminChanged(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	prior := e.subscriber(10 * 24 * time.Hour)
	p := e.buy(prior.ID, "ch-admin")
	_, err := e.st.DB.ExecContext(ctx, "UPDATE users SET device_limit = 99 WHERE id = $1", prior.ID)
	must(t, err)
	rv, err := e.s.Refund(ctx, p.ID)
	must(t, err)
	got := e.user(prior.ID)
	if rv.TariffRestored || got.DeviceLimit.Int64 != 99 || got.TariffID.Int64 != e.sale.ID || got.ExpiresAt != prior.ExpiresAt {
		t.Fatalf("reverted %+v, user %+v", rv, got)
	}
}

// Payments applied before the snapshot existed: a renewal loses the days of its term, a
// new subscription is turned off.
func TestRefundWithoutASnapshot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	prior := e.subscriber(10 * 24 * time.Hour)
	renewal := e.buy(prior.ID, "ch-old-r")
	created := e.buy(0, "ch-old-n")
	_, err := e.st.DB.ExecContext(ctx, "UPDATE payments SET revert = ''")
	must(t, err)

	rv, err := e.s.Refund(ctx, renewal.ID)
	must(t, err)
	got := e.user(prior.ID)
	if rv.Exact || rv.Action != "term" || rv.TariffRestored || got.ExpiresAt != prior.ExpiresAt || got.TariffID.Int64 != e.sale.ID {
		t.Fatalf("reverted %+v, user %+v", rv, got)
	}
	rv, err = e.s.Refund(ctx, created.ID)
	must(t, err)
	if rv.Action != "disabled" || e.user(created.UserID.Int64).Status != "disabled" {
		t.Fatalf("reverted %+v, user %+v", rv, e.user(created.UserID.Int64))
	}
}

func TestRefundPackageRemovesTheGrant(t *testing.T) {
	e, u, pk := packageEnv(t)
	ctx := context.Background()
	p, err := e.s.PackageInvoice(ctx, PackageRequest{TgID: 555, UserID: u.ID, PackageID: pk.ID, Provider: Stars})
	must(t, err)
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-pack", p.Currency, p.Amount))
	if gs, _ := e.st.Q.ListUserGrants(ctx, u.ID); len(gs) != 1 {
		t.Fatalf("grants: %+v", gs)
	}
	// Part of it used: what is left goes.
	_, err = e.st.DB.ExecContext(ctx, "UPDATE traffic_grants SET remaining = 1000")
	must(t, err)
	rv, err := e.s.Refund(ctx, p.ID)
	must(t, err)
	if gs, _ := e.st.Q.ListUserGrants(ctx, u.ID); len(gs) != 0 || rv.Action != "grant" {
		t.Fatalf("grants after the refund: %+v, reverted %+v", gs, rv)
	}
	if got := e.user(u.ID); got.Status != "active" || got.ExpiresAt != u.ExpiresAt {
		t.Fatalf("a package refund changed the subscription: %+v", got)
	}
	if notes := e.tg.refundNotes(); len(notes) != 1 || notes[0].p.Kind != KindPackage {
		t.Fatalf("buyer told: %+v", notes)
	}
}

func TestRefundGivesTheDiscountCodeBack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "SALE", Type: "percent", Value: 25, Currency: "XTR",
		MaxUses: sql.NullInt64{Int64: 1, Valid: true}, PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars, PromoCode: pc.Code})
	must(t, err)
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-promo", p.Currency, p.Amount))
	uses := func() int64 {
		c, err := e.st.Q.GetPromoCode(ctx, pc.ID)
		must(t, err)
		return c.UsedCount
	}
	if r, err := e.s.d.Promo.GetPaymentRedemption(ctx, p.ID); err != nil || r.Status != "applied" || uses() != 1 {
		t.Fatalf("before: %+v %v uses %d", r, err, uses())
	}
	rv, err := e.s.Refund(ctx, p.ID)
	must(t, err)
	if r, err := e.s.d.Promo.GetPaymentRedemption(ctx, p.ID); err != nil || r.Status != "released" || uses() != 0 || !rv.PromoReleased {
		t.Fatalf("after: %+v %v uses %d reverted %+v", r, err, uses(), rv)
	}
}

func TestRefundTelegramDidNotConfirmChangesNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	prior := e.subscriber(10 * 24 * time.Hour)
	p := e.buy(prior.ID, "ch-fail")
	after := e.user(prior.ID)
	e.tg.refundErr = errors.New("Telegram unavailable")
	if _, err := e.s.Refund(ctx, p.ID); err == nil {
		t.Fatal("a refund Telegram refused went through")
	}
	if got := e.payment(p.ID); got.Status != "applied" || e.user(prior.ID) != after || len(e.tg.refundNotes()) != 0 {
		t.Fatalf("payment %q, user %+v (was %+v)", got.Status, e.user(prior.ID), after)
	}
	// Telegram back: the same refund goes through.
	e.tg.refundErr = nil
	if _, err := e.s.Refund(ctx, p.ID); err != nil || e.payment(p.ID).Status != "refunded" {
		t.Fatalf("retry: %v %q", err, e.payment(p.ID).Status)
	}
}

// Telegram returned the Stars but taking the subscription back failed: the error says so,
// the payment stays applied, and pressing again finishes it.
func TestRefundTelegramAcceptedButNotApplied(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := e.buy(0, "ch-half")
	e.tg.onRefund = cancel
	if _, err := e.s.Refund(ctx, p.ID); !errors.Is(err, ErrRefundNotApplied) {
		t.Fatalf("refund: %v", err)
	}
	if e.payment(p.ID).Status != "applied" || e.user(p.UserID.Int64).Status != "active" {
		t.Fatalf("payment %q, user %q", e.payment(p.ID).Status, e.user(p.UserID.Int64).Status)
	}
	e.tg.onRefund = nil
	if _, err := e.s.Refund(context.Background(), p.ID); err != nil || e.user(p.UserID.Int64).Status != "disabled" {
		t.Fatalf("retry: %v", err)
	}
}

// Telegram says a payment was refunded (the buyer asked it, or the bot's own refund comes
// back as an update): the same is taken back, once, and no refund is asked of Telegram.
func TestStarsRefundedByTelegram(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.buy(0, "ch-tg")
	must(t, e.s.StarsRefunded(ctx, 556, "ch-tg")) // not the buyer's
	must(t, e.s.StarsRefunded(ctx, 555, "nope"))  // not a payment of ours
	if e.payment(p.ID).Status != "applied" {
		t.Fatal("a refund of someone else's payment was taken")
	}
	must(t, e.s.StarsRefunded(ctx, 555, "ch-tg"))
	must(t, e.s.StarsRefunded(ctx, 555, "ch-tg"))
	u := e.user(p.UserID.Int64)
	if got := e.payment(p.ID); got.Status != "refunded" || !got.RefundedAt.Valid || u.Status != "disabled" {
		t.Fatalf("payment %+v, user %q", got, u.Status)
	}
	if len(e.tg.refunds) != 0 || len(e.tg.refundNotes()) != 1 {
		t.Fatalf("refund calls %v, buyer told %d times", e.tg.refunds, len(e.tg.refundNotes()))
	}
	// The admin's refund of it afterwards is not offered.
	if _, err := e.s.Refund(ctx, p.ID); !errors.Is(err, ErrNotRefunable) {
		t.Fatalf("refund of a refunded payment: %v", err)
	}
}

// A payment refunded before it was applied is not applied later.
func TestStarsRefundedBeforeItWasApplied(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.invoice(555, 0, Stars)
	_, err := e.st.Q.MarkPaymentPaid(ctx, db.MarkPaymentPaidParams{ExternalID: sql.NullString{String: "ch-paid", Valid: true},
		PaidAt: sql.NullInt64{Int64: e.now.Unix(), Valid: true}, ID: p.ID})
	must(t, err)
	must(t, e.s.StarsRefunded(ctx, 555, "ch-paid"))
	e.s.Reconcile(ctx)
	if e.payment(p.ID).Status != "refunded" || e.users() != 0 || len(e.tg.refundNotes()) != 0 {
		t.Fatalf("payment %q, users %d", e.payment(p.ID).Status, e.users())
	}
	// The addons hear of the refund once, though nothing was applied.
	must(t, e.s.StarsRefunded(ctx, 555, "ch-paid"))
	if refunds := ofType(e.events(), events.PaymentRefunded); len(refunds) != 1 || refunds[0].TgID.Int64 != 555 {
		t.Fatalf("refund events of an unapplied payment: %v", typesOf(e.events()))
	}
}

// The admin's button and Telegram's update about the same refund together take back once:
// on a renewal the term returns exactly to where it was, not twice as far.
func TestRefundAndTelegramUpdateTogether(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	prior := e.subscriber(10 * 24 * time.Hour)
	p := e.buy(prior.ID, "ch-both")
	e.now = e.now.Add(time.Hour)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = e.s.Refund(ctx, p.ID) }()
	go func() { defer wg.Done(); _ = e.s.StarsRefunded(ctx, 555, "ch-both") }()
	wg.Wait()
	if got := e.user(prior.ID); e.payment(p.ID).Status != "refunded" || got.ExpiresAt != prior.ExpiresAt || got.TariffID != prior.TariffID || len(e.tg.refundNotes()) != 1 {
		t.Fatalf("payment %q, user %+v (was %+v), told %d times", e.payment(p.ID).Status, got, prior, len(e.tg.refundNotes()))
	}
}

// A subscription bought and then renewed, both refunded: each takes back its own term.
func TestRefundChainNewThenRenewal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first := e.buy(0, "ch-new")
	uid := first.UserID.Int64
	e.now = e.now.Add(time.Hour)
	second := e.buy(uid, "ch-renew")
	_, err := e.s.Refund(ctx, first.ID)
	must(t, err)
	_, err = e.s.Refund(ctx, second.ID)
	must(t, err)
	if got := e.user(uid); got.ExpiresAt.Int64 != e.now.Unix() {
		t.Fatalf("both refunded: expires %d, want now %d", got.ExpiresAt.Int64, e.now.Unix())
	}
}

// With a tariff restored the pools of the prior tariff come back with it.
func TestRefundRestoresThePoolsOfThePriorTariff(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ts, _ := e.st.Q.ListTariffs(ctx)
	pool, err := e.st.Q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: 1})
	must(t, err)
	must(t, e.st.Q.AddTariffPool(ctx, db.AddTariffPoolParams{TariffID: ts[0].ID, PoolID: pool.ID, TrafficLimit: 7 << 30}))
	prior := e.subscriber(10 * 24 * time.Hour)
	limit := func() sql.NullInt64 {
		up, err := e.st.Q.GetUserPool(ctx, db.GetUserPoolParams{UserID: prior.ID, PoolID: pool.ID})
		must(t, err)
		return up.TrafficLimit
	}
	want := sql.NullInt64{Int64: 7 << 30, Valid: true}
	if limit() != want {
		t.Fatalf("before: %+v", limit())
	}
	p := e.buy(prior.ID, "ch-pool")
	if limit().Valid {
		t.Fatalf("the sale tariff leaves the pool unlimited: %+v", limit())
	}
	rv, err := e.s.Refund(ctx, p.ID)
	must(t, err)
	if !rv.TariffRestored || limit() != want {
		t.Fatalf("reverted %+v, pool limit %+v", rv, limit())
	}
}

// A term counted in months is taken back in seconds when something came after it: pinned
// as an approximation (see rolledBack).
func TestRefundOfAMonthlyTermBehindALaterRenewal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.st.DB.ExecContext(ctx, "UPDATE tariffs SET billing_day = 15 WHERE id = $1", e.sale.ID)
	must(t, err)
	prior := e.subscriber(10 * 24 * time.Hour)
	first := e.buy(prior.ID, "ch-m1")
	end1 := e.user(prior.ID).ExpiresAt.Int64
	e.now = e.now.Add(time.Hour)
	e.buy(prior.ID, "ch-m2")
	end2 := e.user(prior.ID).ExpiresAt.Int64
	_, err = e.s.Refund(ctx, first.ID)
	must(t, err)
	if want := end2 - (end1 - prior.ExpiresAt.Int64); e.user(prior.ID).ExpiresAt.Int64 != want {
		t.Fatalf("expires %d, want %d", e.user(prior.ID).ExpiresAt.Int64, want)
	}
}

func TestRolledBack(t *testing.T) {
	const now, applied = 1000, 900
	n := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	p := func(v int64) *int64 { return &v }
	for _, c := range []struct {
		name string
		cur  sql.NullInt64
		ri   revertInfo
		days int64
		want sql.NullInt64
	}{
		{"nothing since: the old end", n(5000), revertInfo{Prior: &userState{ExpiresAt: p(3000)}, Set: &userState{ExpiresAt: p(5000)}}, 0, n(3000)},
		{"later payments: only this term", n(8000), revertInfo{Prior: &userState{ExpiresAt: p(3000)}, Set: &userState{ExpiresAt: p(5000)}}, 0, n(6000)},
		{"the old end was before the payment", n(5900), revertInfo{Prior: &userState{ExpiresAt: p(100)}, Set: &userState{ExpiresAt: p(5900)}}, 0, n(now)},
		{"unlimited before: unlimited again", n(5000), revertInfo{Prior: &userState{}, Set: &userState{ExpiresAt: p(5000)}}, 0, sql.NullInt64{}},
		{"made unlimited since: left alone", sql.NullInt64{}, revertInfo{Prior: &userState{ExpiresAt: p(3000)}, Set: &userState{ExpiresAt: p(5000)}}, 0, sql.NullInt64{}},
		{"already over: left alone", n(500), revertInfo{Prior: &userState{ExpiresAt: p(100)}, Set: &userState{ExpiresAt: p(5000)}}, 0, n(500)},
		{"unlimited before, a later change: only this term", n(8000), revertInfo{Prior: &userState{}, Set: &userState{ExpiresAt: p(5000)}}, 0, n(3900)},
		{"created, a later renewal: only this term", n(8000), revertInfo{Created: true, Set: &userState{ExpiresAt: p(5000)}}, 0, n(3900)},
		{"created, nothing since: ends now", n(5000), revertInfo{Created: true, Set: &userState{ExpiresAt: p(5000)}}, 0, n(now)},
		{"not recorded: the term's days", n(3 * day), revertInfo{}, 2, n(day)},
		{"not recorded, no days: left alone", n(3 * day), revertInfo{}, 0, n(3 * day)},
	} {
		if got := rolledBack(c.cur, c.ri, applied, c.days, now); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
