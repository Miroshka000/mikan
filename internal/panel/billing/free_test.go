package billing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// offerFree turns the free plan on, on the seeded trial tariff (3 days), with selling off:
// the free plan takes no payment.
func (e *env) offerFree(condition string) db.Tariff {
	e.t.Helper()
	ctx := context.Background()
	ts, err := e.st.Q.ListTariffs(ctx)
	must(e.t, err)
	free := ts[0]
	must(e.t, settings.Set(ctx, e.s.d.Settings, KeyConfig, Config{FreeTariffID: free.ID, FreeCondition: condition}))
	return free
}

// The free plan is taken again only once the previous free subscription has ended; it is
// counted, and its users come from the free plan.
func TestFreePlanAgainAfterTheEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.s.FreePlan(ctx, 911); !errors.Is(err, ErrFreeOff) {
		t.Fatalf("a free plan nobody offers: %v", err)
	}
	if _, ok := e.s.FreeOffer(ctx, 911); ok {
		t.Fatal("a free plan nobody offers is on offer")
	}
	free := e.offerFree(FreeNoCondition)
	if _, ok := e.s.FreeOffer(ctx, 911); !ok {
		t.Fatal("the free plan is not offered to a new account")
	}
	u, err := e.s.FreePlan(ctx, 911)
	must(t, err)
	if u.TariffID.Int64 != free.ID || u.Source != domain.UserFromFree || u.ExpiresAt.Int64 != e.now.Add(time.Duration(free.DurationDays)*24*time.Hour).Unix() {
		t.Fatalf("free user %+v on %+v", u, free)
	}
	if links, err := e.st.Q.ListTgLinksOf(ctx, 911); err != nil || len(links) != 1 || links[0].ID != u.ID {
		t.Fatalf("the free subscription is not linked: %+v %v", links, err)
	}
	if _, err := e.s.FreePlan(ctx, 911); !errors.Is(err, ErrFreeActive) {
		t.Fatalf("a second free plan while the first runs: %v", err)
	}
	if _, ok := e.s.FreeOffer(ctx, 911); ok {
		t.Fatal("the free plan is offered while the first runs")
	}
	// The term is over: another one.
	e.now = e.now.Add(time.Duration(free.DurationDays)*24*time.Hour + time.Second)
	if _, ok := e.s.FreeOffer(ctx, 911); !ok {
		t.Fatal("the free plan is not offered after the first ended")
	}
	again, err := e.s.FreePlan(ctx, 911)
	must(t, err)
	if again.ID == u.ID {
		t.Fatal("the same subscription again")
	}
	// A deleted free subscription has ended too.
	_, err = e.st.DB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", again.ID)
	must(t, err)
	_, err = e.s.FreePlan(ctx, 911)
	must(t, err)
	if n, _ := e.st.Q.CountFreePlans(ctx); n != 3 {
		t.Fatalf("free plans counted %d", n)
	}
	// The trial is another thing: the account still may not have it, being a customer now,
	// and the free plan does not count as the trial.
	if taken, _ := e.st.Q.HasTrial(ctx, 911); taken {
		t.Fatal("the free plan took the trial")
	}
}

// Turning the free plan off hides it and stops it; the subscriptions it gave stay.
func TestFreePlanOff(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.offerFree(FreeNoCondition)
	u, err := e.s.FreePlan(ctx, 912)
	must(t, err)
	must(t, settings.Set(ctx, e.s.d.Settings, KeyConfig, Config{}))
	if _, err := e.s.FreePlan(ctx, 913); !errors.Is(err, ErrFreeOff) {
		t.Fatalf("a free plan turned off: %v", err)
	}
	if _, err := e.st.Q.GetUser(ctx, u.ID); err != nil {
		t.Fatalf("the free subscription is gone with the plan: %v", err)
	}
}

// The channel condition: a member takes it, a stranger is told to join, no channel set up
// turns the plan off, and a channel the bot cannot ask about lets the account through.
func TestFreePlanChannel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.offerFree(FreeChannel)
	if _, ok := e.s.FreeOffer(ctx, 914); ok {
		t.Fatal("a free plan for a channel nobody set up is on offer")
	}
	if _, err := e.s.FreePlan(ctx, 914); !errors.Is(err, ErrFreeOff) {
		t.Fatalf("a free plan for a channel nobody set up: %v", err)
	}
	e.tg.mu.Lock()
	e.tg.channel, e.tg.members = "https://t.me/mikan_news", map[int64]bool{915: true}
	e.tg.mu.Unlock()
	if o, ok := e.s.FreeOffer(ctx, 914); !ok || o.Channel != "https://t.me/mikan_news" {
		t.Fatalf("the offer does not name the channel: %+v %v", o, ok)
	}
	if _, err := e.s.FreePlan(ctx, 914); !errors.Is(err, ErrFreeChannel) {
		t.Fatalf("a stranger to the channel: %v", err)
	}
	if links, _ := e.st.Q.ListTgLinksOf(ctx, 914); len(links) != 0 {
		t.Fatalf("a stranger got a subscription: %+v", links)
	}
	if _, err := e.s.FreePlan(ctx, 915); err != nil {
		t.Fatalf("a member: %v", err)
	}
	e.tg.mu.Lock()
	e.tg.memberErr = errors.New("member list is inaccessible")
	e.tg.mu.Unlock()
	if _, err := e.s.FreePlan(ctx, 914); err != nil {
		t.Fatalf("a channel the bot cannot ask about locked the account out: %v", err)
	}
}

// Two taps at once make one free subscription.
func TestFreePlanTapsAtOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.offerFree(FreeNoCondition)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() { _, errs[i] = e.s.FreePlan(ctx, 916) })
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrFreeActive) {
			t.Fatalf("a tap failed: %v", err)
		}
	}
	links, _ := e.st.Q.ListTgLinksOf(ctx, 916)
	if ok != 1 || len(links) != 1 {
		t.Fatalf("%d free plans given, %d subscriptions", ok, len(links))
	}
}

// An account that holds as many subscriptions as it may gets no free one.
func TestFreePlanLinkLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.offerFree(FreeNoCondition)
	for range e.s.d.MaxLinks {
		u, err := e.s.d.Users.Create(ctx, domain.CreateInput{Name: "held", TariffID: e.sale.ID})
		must(t, err)
		must(t, e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 917, CreatedAt: e.now.Unix()}))
	}
	if _, ok := e.s.FreeOffer(ctx, 917); ok {
		t.Fatal("the free plan is offered past the limit")
	}
	if _, err := e.s.FreePlan(ctx, 917); !errors.Is(err, ErrTooManySubs) {
		t.Fatalf("a free plan past the limit: %v", err)
	}
}
