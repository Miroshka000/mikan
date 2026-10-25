package billing

import (
	"context"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// What a paid renewal carries over from the period before is the payment's, as its gifts
// are: the renewal gives it with the new period, a refund takes it back with the payment.
func TestRenewalCarriesOverWithThePayment(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first := e.buy(0, "ch-first")
	id := first.UserID.Int64
	must(t, settings.Set(ctx, settings.New(e.st.Q), domain.KeyLifecycle, domain.Lifecycle{Carry: domain.CarryRules{On: true, Cap: 100}, Removal: domain.DefaultLifecycle.Removal}))
	_, err := e.st.DB.ExecContext(ctx, "UPDATE users SET used_up = 0, used_down = $1 WHERE id = $2", 50*domain.GiB, id)
	must(t, err)

	e.now = e.now.Add(20 * 24 * time.Hour)
	renewal := e.buy(id, "ch-renew")
	gs, err := e.st.Q.ListUserGrants(ctx, id)
	must(t, err)
	if len(gs) != 1 || gs[0].Source != domain.SourceCarry || gs[0].PaymentID.Int64 != renewal.ID || gs[0].Bytes != 100*domain.GiB {
		t.Fatalf("carried by the renewal: %+v", gs)
	}
	if u := e.user(id); u.UsedUp+u.UsedDown != 0 {
		t.Fatalf("the renewal's period counted %d", u.UsedUp+u.UsedDown)
	}
	_, err = e.s.Refund(ctx, renewal.ID)
	must(t, err)
	if gs, _ = e.st.Q.ListUserGrants(ctx, id); len(gs) != 0 {
		t.Fatalf("the refund left %+v", gs)
	}
}

// A subscription bought on a tariff that starts at the first connection waits for it; a
// refund of that purchase turns it off with nothing left to wait with, so the housekeeping
// does not move its end on again.
func TestRefundEndsTheWait(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	must(t, domain.SetTariffLifecycle(ctx, e.st.Q, e.sale.ID, domain.TariffLifecycle{StartOnConnect: true}))
	p := e.buy(0, "ch-wait")
	id := p.UserID.Int64
	if waits, err := domain.Waiting(ctx, e.st.Q, id); err != nil || !waits {
		t.Fatalf("bought on a tariff that waits: %v %v", waits, err)
	}
	e.now = e.now.Add(time.Hour)
	_, err := e.s.Refund(ctx, p.ID)
	must(t, err)
	if waits, err := domain.Waiting(ctx, e.st.Q, id); err != nil || waits {
		t.Fatalf("refunded and still waiting: %v %v", waits, err)
	}
	e.now = e.now.Add(time.Hour)
	if _, err := e.s.d.Users.SlideWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	if u := e.user(id); u.Status != "disabled" || u.ExpiresAt.Int64 != e.now.Add(-time.Hour).Unix() {
		t.Fatalf("after the refund: %s, ends %d", u.Status, u.ExpiresAt.Int64)
	}
}
