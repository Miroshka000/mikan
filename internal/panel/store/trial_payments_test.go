package store

import (
	"context"
	"testing"
)

// Migration 0021 lets a payment be of kind trial. Rolled back, the trial payments stay
// (they are money taken) and the old check holds for new rows only; up again, it is whole.
func TestTrialPaymentsMigrationKeepsThePayments(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := postgresProvider(ctx, s.DB, postgresFS)
	if err != nil {
		t.Fatal(err)
	}
	const migration = 21
	before := int64(0)
	for _, src := range p.ListSources() {
		if src.Version < migration {
			before = src.Version
		}
	}
	exec := func(q string) error {
		_, err := s.DB.ExecContext(ctx, q)
		return err
	}
	if err := exec("INSERT INTO tariffs(id,name,duration_days,created_at) VALUES(5,'t',3,1)"); err != nil {
		t.Fatal(err)
	}
	trialPayment := func(payload string) error {
		return exec(`INSERT INTO payments(provider,payload,tg_id,kind,tariff_id,tariff_name,amount,currency,status,created_at)
		 VALUES('stars','` + payload + `',7,'trial',5,'t',1,'XTR','applied',1)`)
	}
	if err := trialPayment("paid-trial"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM payments WHERE kind = 'trial'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("trial payments after the rollback: %d (%v)", n, err)
	}
	if err := trialPayment("new-trial"); err == nil {
		t.Fatal("a new trial payment passed the old check")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := trialPayment("new-trial"); err != nil {
		t.Fatal(err)
	}
}
