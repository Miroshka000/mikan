package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// Migration 0056 makes the own Clash profile of the settings the default mihomo template
// and drops the setting; a blank one makes none. Going down puts the default back.
func TestSubTemplatesMigration(t *testing.T) {
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
	setting := func() (string, error) {
		var v string
		err := s.DB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = 'sub_template'").Scan(&v)
		return v, err
	}
	for _, c := range []struct {
		name, value, want string
	}{
		{"an own profile", `"mode: rule\nrules:\n  - MATCH,DIRECT\n"`, "mode: rule\nrules:\n  - MATCH,DIRECT\n"},
		{"a blank one", `"  \n"`, ""},
	} {
		if _, err := p.DownTo(ctx, 55); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('sub_template', $1)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, c.value); err != nil {
			t.Fatal(err)
		}
		if _, err := p.UpTo(ctx, 56); err != nil {
			t.Fatal(err)
		}
		var core, name, body string
		var def bool
		err := s.DB.QueryRowContext(ctx, "SELECT core, name, body, is_default FROM sub_templates").Scan(&core, &name, &body, &def)
		switch {
		case c.want == "" && !errors.Is(err, sql.ErrNoRows):
			t.Fatalf("%s: a template of nothing: %q (%v)", c.name, body, err)
		case c.want != "" && (err != nil || core != "mihomo" || name != "Clash" || body != c.want || !def):
			t.Fatalf("%s: %s %s %q %v (%v)", c.name, core, name, body, def, err)
		}
		if _, err := setting(); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("%s: the setting stays: %v", c.name, err)
		}
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO sub_templates (core, name, body, is_default, created_at, updated_at)
	 VALUES ('mihomo', 'a', 'rules: []', true, 1, 1), ('singbox', 'b', '{}', true, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO sub_templates (core, name, body, is_default, created_at, updated_at)
	 VALUES ('mihomo', 'c', 'rules: []', true, 1, 1)`); err == nil {
		t.Fatal("a second default of a core")
	}
	if _, err := p.DownTo(ctx, 55); err != nil {
		t.Fatal(err)
	}
	if v, err := setting(); err != nil || v != `"rules: []"` {
		t.Fatalf("down: %s %v", v, err)
	}
}
