package store

import (
	"context"
	"testing"
)

// Migration 0060 makes the table of the admins' passkeys: a key belongs to one admin, leaves
// with them, and no two rows hold the same key. Going down drops it.
func TestAdminPasskeysMigration(t *testing.T) {
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
	if _, err := p.DownTo(ctx, 59); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, "SELECT 1 FROM admin_passkeys"); err == nil {
		t.Fatal("the table before its migration")
	}
	if _, err := p.UpTo(ctx, 60); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO admins (id, username, password_hash, created_at) VALUES (1, 'a', 'x', 1), (2, 'b', 'x', 1)`); err != nil {
		t.Fatal(err)
	}
	insert := func(admin int64, id string) error {
		_, err := s.DB.ExecContext(ctx, `INSERT INTO admin_passkeys (admin_id, credential_id, name, credential, created_at) VALUES ($1, $2, 'k', '{}', 1)`, admin, []byte(id))
		return err
	}
	if err := insert(1, "key-1"); err != nil {
		t.Fatal(err)
	}
	if err := insert(2, "key-2"); err != nil {
		t.Fatal(err)
	}
	if err := insert(2, "key-1"); !IsUnique(err) {
		t.Fatalf("a key of two admins: %v", err)
	}
	if err := insert(3, "key-3"); err == nil {
		t.Fatal("a key of nobody")
	}
	if _, err := s.DB.ExecContext(ctx, "DELETE FROM admins WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM admin_passkeys").Scan(&n); err != nil || n != 1 {
		t.Fatalf("after the admin left: %d keys (%v)", n, err)
	}
	if _, err := p.DownTo(ctx, 59); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, "SELECT 1 FROM admin_passkeys"); err == nil {
		t.Fatal("the table after going down")
	}
}
