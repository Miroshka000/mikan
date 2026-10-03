package domain

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// An admin's extension and a payment that lands at the same time both count: the expiry is
// read and written on one transaction, not worked out from a copy read before the payment.
func TestExtendKeepsConcurrentPayments(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	clock := func() time.Time { return now }
	users := NewUsers(st, NewPool(st, clock), quietChanges{}, clock) // the counting fake is not for goroutines
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	std := tariffs[1] // 30 days
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: std.ID})
	if err != nil {
		t.Fatal(err)
	}
	start := u.ExpiresAt.Int64

	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := users.Extend(ctx, u.ID, 1)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			errs <- st.Tx(ctx, func(q *db.Queries) error {
				_, _, err := users.Purchase(ctx, q, u.ID, std.ID, sql.NullInt64{}, "", false)
				return err
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.Q.GetUser(ctx, u.ID)
	if want := start + n*(1+30)*86400; got.ExpiresAt.Int64 != want {
		t.Fatalf("expiry %d, want %d: %d days lost", got.ExpiresAt.Int64, want, (want-got.ExpiresAt.Int64)/86400)
	}
}

// quietChanges ignores the notices: the tests that run goroutines must not share the
// counting fake.
type quietChanges struct{}

func (quietChanges) PoliciesChanged() {}
func (quietChanges) SlotsChanged()    {}

// An extension turns a disabled user on; a term that already ended counts from now.
func TestExtendFromNowAfterTheTerm(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, _ := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID})
	off := true
	if _, err := users.Update(ctx, u.ID, Patch{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(100 * 24 * time.Hour)
	got, err := users.Extend(ctx, u.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(10 * 24 * time.Hour).Unix(); got.ExpiresAt.Int64 != want || got.Status != "active" {
		t.Fatalf("expiry %d status %s, want %d active", got.ExpiresAt.Int64, got.Status, want)
	}
	if _, err := users.Extend(ctx, 9999, 1); err == nil {
		t.Fatal("an unknown user was extended")
	}
}
