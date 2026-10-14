package billing

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// offerTrial turns the trial on, on the seeded trial tariff (3 days, 5 GB, one device),
// with selling off: a trial takes no payment.
func (e *env) offerTrial() db.Tariff {
	e.t.Helper()
	ctx := context.Background()
	ts, err := e.st.Q.ListTariffs(ctx)
	must(e.t, err)
	trial := ts[0]
	must(e.t, settings.Set(ctx, e.s.d.Settings, KeyConfig, Config{Stars: true, AllowNew: true, TrialTariffID: trial.ID}))
	return trial
}

func TestTrialOncePerAccount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.s.Trial(ctx, 901); !errors.Is(err, ErrTrialOff) || e.s.TrialOpen(ctx, 901) {
		t.Fatalf("a trial nobody offers: %v", err)
	}
	trial := e.offerTrial()
	if !e.s.TrialOpen(ctx, 901) {
		t.Fatal("the trial is not open to a new account")
	}
	u, err := e.s.Trial(ctx, 901)
	must(t, err)
	if u.TariffID.Int64 != trial.ID || u.TrafficLimit != trial.TrafficLimit || u.DeviceLimit != trial.DeviceLimit ||
		u.ExpiresAt.Int64 != e.now.Add(time.Duration(trial.DurationDays)*24*time.Hour).Unix() {
		t.Fatalf("trial user %+v on %+v", u, trial)
	}
	if u.Source != domain.UserFromTrial {
		t.Fatalf("a trial user came from %q", u.Source)
	}
	links, err := e.st.Q.ListTgLinksOf(ctx, 901)
	if err != nil || len(links) != 1 || links[0].ID != u.ID {
		t.Fatalf("the trial is not linked to the account: %+v %v", links, err)
	}
	if _, err := e.s.Trial(ctx, 901); !errors.Is(err, ErrTrialUsed) || e.s.TrialOpen(ctx, 901) {
		t.Fatalf("a second trial: %v", err)
	}
	// Deleting the trial subscription does not give the account another one.
	_, err = e.st.DB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", u.ID)
	must(t, err)
	if _, err := e.s.Trial(ctx, 901); !errors.Is(err, ErrTrialUsed) {
		t.Fatalf("a trial after deleting the first: %v", err)
	}
	if n, _ := e.st.Q.CountTrials(ctx); n != 1 {
		t.Fatalf("trials counted %d", n)
	}
}

// Customers do not get a trial: an account with a subscription or a paid payment.
func TestTrialIsForNewPeople(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.offerTrial()
	// An account that paid once (its subscription since deleted).
	_, err := e.st.Q.CreatePayment(ctx, db.CreatePaymentParams{Provider: Stars, Payload: "paid-before-trial-000000000000000", TgID: 902, Kind: "new",
		TariffID: sql.NullInt64{Int64: e.sale.ID, Valid: true}, TariffName: e.sale.Name, Amount: 150, Currency: "XTR", CreatedAt: e.now.Unix()})
	must(t, err)
	_, err = e.st.DB.ExecContext(ctx, "UPDATE payments SET status = 'applied' WHERE tg_id = 902")
	must(t, err)
	if _, err := e.s.Trial(ctx, 902); !errors.Is(err, ErrTrialUsed) || e.s.TrialOpen(ctx, 902) {
		t.Fatalf("a customer who paid: %v", err)
	}
	// An account with a subscription linked by the admin.
	u, err := e.s.d.Users.Create(ctx, domain.CreateInput{Name: "linked", TariffID: e.sale.ID})
	must(t, err)
	must(t, e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 903, CreatedAt: e.now.Unix()}))
	if _, err := e.s.Trial(ctx, 903); !errors.Is(err, ErrTrialUsed) {
		t.Fatalf("an account with a subscription: %v", err)
	}
	if n, _ := e.st.Q.CountTrials(ctx); n != 0 {
		t.Fatalf("refused trials counted: %d", n)
	}
}

// Two taps at once make one trial.
func TestTrialTapsAtOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.offerTrial()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = e.s.Trial(ctx, 904)
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrTrialUsed) {
			t.Fatalf("a tap failed: %v", err)
		}
	}
	links, _ := e.st.Q.ListTgLinksOf(ctx, 904)
	if ok != 1 || len(links) != 1 {
		t.Fatalf("%d trials given, %d subscriptions", ok, len(links))
	}
}

// An archived trial tariff turns the trial off.
func TestTrialOnAnArchivedTariff(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	trial := e.offerTrial()
	_, err := e.st.Q.ArchiveTariff(ctx, trial.ID)
	must(t, err)
	if _, err := e.s.Trial(ctx, 905); !errors.Is(err, ErrTrialOff) || e.s.TrialOpen(ctx, 905) {
		t.Fatalf("a trial on an archived tariff: %v", err)
	}
}

// A trial tariff that lost its term would give a subscription with no end: the trial is
// off until the tariff has a term again.
func TestTrialOnATariffWithoutTerm(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	trial := e.offerTrial()
	_, err := e.st.DB.ExecContext(ctx, "UPDATE tariffs SET duration_days = 0 WHERE id = $1", trial.ID)
	must(t, err)
	if _, err := e.s.Trial(ctx, 906); !errors.Is(err, ErrTrialOff) || e.s.TrialOpen(ctx, 906) {
		t.Fatalf("a trial on a tariff without a term: %v", err)
	}
}

type countChanges struct{ policies, slots int }

func (c *countChanges) PoliciesChanged() { c.policies++ }
func (c *countChanges) SlotsChanged()    { c.slots++ }

// The trial gets the tariff whole: reset strategy and pools too. With no free slot it
// refills the pool and retries (as a payment does), and the nodes are told about it.
func TestTrialIsTheTariffWhole(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	trial := e.offerTrial()
	pool, err := e.st.Q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "trial-pool", CreatedAt: e.now.Unix()})
	must(t, err)
	must(t, e.st.Q.AddTariffPool(ctx, db.AddTariffPoolParams{TariffID: trial.ID, PoolID: pool.ID, TrafficLimit: 1 << 20}))
	// No free slot: the first attempt hits domain.ErrNoSlots.
	_, err = e.st.DB.ExecContext(ctx, "UPDATE slots SET state = 'assigned' WHERE state = 'free'")
	must(t, err)
	clock := func() time.Time { return e.now }
	changes := &countChanges{}
	svc := New(Deps{Store: e.st, Settings: e.s.d.Settings, Users: domain.NewUsers(e.st, domain.NewPool(e.st, clock), changes, clock),
		Log: e.s.d.Log, Now: clock, MaxLinks: 5})
	u, err := svc.Trial(ctx, 906)
	must(t, err)
	if u.ResetStrategy != trial.ResetStrategy || u.SlotID.Int64 == 0 {
		t.Fatalf("trial user %+v on %+v", u, trial)
	}
	ups, err := e.st.Q.ListUserPools(ctx, u.ID)
	if err != nil || len(ups) != 1 || ups[0].PoolID != pool.ID || ups[0].TrafficLimit.Int64 != 1<<20 {
		t.Fatalf("the trial's pools: %+v %v", ups, err)
	}
	if changes.slots == 0 || changes.policies == 0 {
		t.Fatalf("the nodes were not told: %+v", changes)
	}
}
