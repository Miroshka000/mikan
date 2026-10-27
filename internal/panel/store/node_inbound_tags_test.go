package store

import (
	"context"
	"testing"
)

// Migration 0061 gives the nodes and inbounds a panel already has no labels ("[]", as a
// user without any), and going down takes the columns away again.
func TestNodeInboundTagsMigration(t *testing.T) {
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
	const migration = 61
	before := int64(0)
	for _, src := range p.ListSources() {
		if src.Version < migration {
			before = src.Version
		}
	}
	has := func(table string) bool {
		t.Helper()
		var ok bool
		if err := s.DB.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name='tags')", table).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if _, err := p.DownTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	if has("nodes") || has("inbounds") {
		t.Fatal("tags after the rollback")
	}
	if _, err := s.DB.ExecContext(ctx, "INSERT INTO nodes(id,name,address,created_at,updated_at) VALUES(7,'n','198.51.100.7:40000',1,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, "INSERT INTO inbounds(node_id,name,preset,port,config,created_at,updated_at) VALUES(7,'in','custom','443','type: vless',1,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, migration); err != nil {
		t.Fatal(err)
	}
	var node, inbound string
	if err := s.DB.QueryRowContext(ctx, "SELECT tags FROM nodes WHERE id = 7").Scan(&node); err != nil || node != "[]" {
		t.Fatalf("node tags: %q %v", node, err)
	}
	if err := s.DB.QueryRowContext(ctx, "SELECT tags FROM inbounds WHERE node_id = 7").Scan(&inbound); err != nil || inbound != "[]" {
		t.Fatalf("inbound tags: %q %v", inbound, err)
	}
	if _, err := p.DownTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	if has("nodes") || has("inbounds") {
		t.Fatal("tags after going down")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
}
