package store

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// database migrate and restore never run under a live panel; a backup is not refused.
func TestMigrateAndRestoreRefuseALivePanel(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dsn := fixtureDSN(t, s)
	dir := t.TempDir()
	release, err := LockPanel(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	// A second panel process only shares the guard.
	other, err := LockPanel(ctx, dsn)
	if err != nil {
		t.Fatal("a shared holder refused another:", err)
	}
	other()
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "the panel is running") {
			t.Fatalf("%s beside a live panel: %v", what, err)
		}
	}
	_, err = Migrate(ctx, dsn, dir)
	refused("migrate", err)
	refused("PostgreSQL restore", RestorePostgres(ctx, dsn, dir, func(context.Context) error {
		t.Fatal("pg_restore ran beside a live panel")
		return nil
	}))
	if _, err := os.Stat(filepath.Join(dir, MigrationMarker)); !os.IsNotExist(err) {
		t.Fatal("a refused command left a marker", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, dsn, dir); err != nil {
		t.Fatal("migrate after the panel stopped:", err)
	}
}

// The panel does not start in the middle of a migrate or restore.
func TestPanelWaitsForNoMigrate(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	unlock, err := lockOffline(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := LockPanel(ctx, fixtureDSN(t, s)); err == nil {
		release()
		t.Fatal("the panel started during a migrate")
	}
	unlock()
	release, err := LockPanel(ctx, fixtureDSN(t, s))
	if err != nil {
		t.Fatal(err)
	}
	release()
}

// Runtime statements are bounded; import, restore and migrations are not, beyond waiting
// on a lock and idling in a transaction.
func TestSessionSettings(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	maintenance, err := connectPostgres(ctx, fixtureDSN(t, s), maintenanceSession)
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.Close()
	for _, c := range []struct {
		conn                  *sql.DB
		statement, lock, idle string
	}{
		{s.DB, "30s", "10s", "1min"},
		{maintenance, "0", "10s", "1min"},
	} {
		var app, statement, lock, idle string
		if err := c.conn.QueryRowContext(ctx, "SELECT current_setting('application_name'), current_setting('statement_timeout'), current_setting('lock_timeout'), current_setting('idle_in_transaction_session_timeout')").Scan(&app, &statement, &lock, &idle); err != nil {
			t.Fatal(err)
		}
		if app != "mikan" || statement != c.statement || lock != c.lock || idle != c.idle {
			t.Errorf("session: %s %s %s %s, want mikan %s %s %s", app, statement, lock, idle, c.statement, c.lock, c.idle)
		}
	}
}

// Only a PostgreSQL that is still coming up is waited for: a wrong password or a missing
// database fails at once.
func TestNotReady(t *testing.T) {
	dial := func(errno syscall.Errno) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
	}
	for _, c := range []struct {
		err  error
		want bool
	}{
		{dial(syscall.ECONNREFUSED), true},
		{fmt.Errorf("failed to connect: %w", dial(syscall.ECONNREFUSED)), true},
		{&net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.ENOENT)}, true},
		{&pgconn.PgError{Code: "57P03", Message: "the database system is starting up"}, true},
		{fmt.Errorf("server error: %w", &pgconn.PgError{Code: "57P03"}), true},
		{&pgconn.PgError{Code: "28P01", Message: "password authentication failed"}, false},
		{&pgconn.PgError{Code: "3D000", Message: "database does not exist"}, false},
		{dial(syscall.EHOSTUNREACH), false},
		{context.DeadlineExceeded, false},
	} {
		if got := notReady(c.err); got != c.want {
			t.Errorf("notReady(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
