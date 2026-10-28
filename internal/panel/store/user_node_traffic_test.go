package store

import (
	"context"
	"testing"
)

// Migration 0062 adds the users' traffic by node, empty: what was carried before is not
// there to split. A row names a user and a node that exist; going down takes the table away.
func TestUserNodeTrafficMigration(t *testing.T) {
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
	const migration = 62
	before := int64(0)
	for _, src := range p.ListSources() {
		if src.Version < migration {
			before = src.Version
		}
	}
	has := func() bool {
		t.Helper()
		var ok bool
		if err := s.DB.QueryRowContext(ctx, "SELECT to_regclass('user_node_traffic') IS NOT NULL").Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if _, err := p.DownTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Fatal("user_node_traffic after the rollback")
	}
	if _, err := p.UpTo(ctx, migration); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM user_node_traffic").Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after the migration: %d %v", n, err)
	}
	// A pair needs its user and its node.
	if _, err := s.DB.ExecContext(ctx, "INSERT INTO user_node_traffic(user_id,node_id,up,down,last_seen) VALUES(424242,424242,1,2,3)"); err == nil {
		t.Fatal("a pair of nobody on no node went in")
	}
	if _, err := p.DownTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Fatal("user_node_traffic after going down")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
}
