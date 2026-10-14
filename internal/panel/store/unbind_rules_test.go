package store

import (
	"context"
	"testing"
)

// Migration 0019 moves the last unbind of every subscriber into the list the admin's
// limit counts, and back: a panel rolled back keeps the moment, and pauses after an unbind
// do not turn into bans for good.
func TestUnbindMigrationKeepsTheLastUnbind(t *testing.T) {
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
	const migration = 19
	before := int64(0)
	for _, src := range p.ListSources() {
		if src.Version < migration {
			before = src.Version
		}
	}
	if _, err := p.DownTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := s.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users(id,name,sub_token,period_start,created_at,updated_at,unbound_at) VALUES
	 (51,'unbound','t51',1,1,1,1800000000), (52,'never','t52',1,1,1,0)`)
	exec("INSERT INTO device_bans(user_id,hwid,label,banned_at) VALUES(51,'tv-0123456789ab','TV',5)")
	if _, err := p.UpTo(ctx, migration); err != nil {
		t.Fatal(err)
	}
	var n, at int64
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*), COALESCE(max(at),0) FROM device_unbinds WHERE user_id = 51").Scan(&n, &at); err != nil || n != 1 || at != 1800000000 {
		t.Fatalf("the last unbind: %d at %d (%v)", n, at, err)
	}
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM device_unbinds WHERE user_id = 52").Scan(&n); err != nil || n != 0 {
		t.Fatalf("a subscriber who never unbound has %d (%v)", n, err)
	}
	var forGood bool
	if err := s.DB.QueryRowContext(ctx, "SELECT until IS NULL FROM device_bans WHERE user_id = 51").Scan(&forGood); err != nil || !forGood {
		t.Fatalf("the admin's ban stays for good: %v %v", forGood, err)
	}

	// Back: the moment returns to the user, a pause goes, the admin's ban stays.
	exec("INSERT INTO device_unbinds(user_id,at) VALUES(51,1800000500)")
	exec("INSERT INTO device_bans(user_id,hwid,label,banned_at,until) VALUES(51,'phone-0123456789','Phone',6,1800090000)")
	if _, err := p.DownTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, "SELECT unbound_at FROM users WHERE id = 51").Scan(&at); err != nil || at != 1800000500 {
		t.Fatalf("unbound_at after the rollback: %d (%v)", at, err)
	}
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM device_bans WHERE user_id = 51").Scan(&n); err != nil || n != 1 {
		t.Fatalf("bans after the rollback: %d (%v), want only the admin's", n, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
}
