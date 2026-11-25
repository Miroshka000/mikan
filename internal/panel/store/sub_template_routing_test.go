package store

import (
	"context"
	"testing"
)

// Migration 0083 gives a template a routing of its own: a simple Clash template with no text.
// The database holds to that, and going down takes the simple templates away with the column,
// since the releases before would read them as Clash text.
func TestSubTemplateRoutingMigration(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	insert := func(core, body, routing string) error {
		var r any
		if routing != "" {
			r = routing
		}
		_, err := s.DB.ExecContext(ctx, `INSERT INTO sub_templates (core, name, body, routing, is_default, created_at, updated_at)
		 VALUES ($1, 'x', $2, $3, false, 1, 1)`, core, body, r)
		return err
	}
	if err := insert("mihomo", "", `{"mode": "all", "routes": {}, "rules": ""}`); err != nil {
		t.Fatalf("a simple template: %v", err)
	}
	if err := insert("mihomo", "rules: []", ""); err != nil {
		t.Fatalf("a template of text: %v", err)
	}
	for _, c := range []struct{ name, core, body, routing string }{
		{"with a text", "mihomo", "rules: []", `{"mode": "all"}`},
		{"of sing-box", "singbox", "", `{"mode": "all"}`},
		{"not an object", "mihomo", "", `["all"]`},
	} {
		if err := insert(c.core, c.body, c.routing); err == nil {
			t.Errorf("%s: taken", c.name)
		}
	}
	p, err := postgresProvider(ctx, s.DB, postgresFS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 82); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM sub_templates").Scan(&n); err != nil || n != 1 {
		t.Fatalf("down: %d templates (%v), want the one of text", n, err)
	}
	if _, err := p.UpTo(ctx, 83); err != nil {
		t.Fatal(err)
	}
}
