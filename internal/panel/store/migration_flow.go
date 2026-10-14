package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
)

// MigrateResult is what Migrate did: the schema versions before and after it.
type MigrateResult struct {
	Before, After int64
}

// Migrate applies the PostgreSQL migrations; it refuses to run beside a running panel, and
// on data still in SQLite (checkLegacy).
func Migrate(ctx context.Context, dsn, dataDir string) (MigrateResult, error) {
	var res MigrateResult
	conn, err := connectPostgres(ctx, dsn, maintenanceSession)
	if err != nil {
		return res, err
	}
	defer conn.Close()
	guard, err := conn.Conn(ctx)
	if err != nil {
		return res, err
	}
	defer guard.Close()
	unlock, err := lockOffline(ctx, guard)
	if err != nil {
		return res, err
	}
	defer unlock()
	if err := recoverRestore(ctx, conn); err != nil {
		return res, err
	}
	if err := checkLegacy(ctx, conn, dataDir); err != nil {
		return res, err
	}
	if res.Before, err = schemaVersion(ctx, conn); err != nil {
		return res, err
	}
	if err := migrateUp(ctx, conn, postgresFS); err != nil {
		return res, err
	}
	res.After, err = schemaVersion(ctx, conn)
	return res, err
}

// openMaintenance opens maintenance connections, after bringing back what an interrupted
// restore set aside.
func openMaintenance(ctx context.Context, dsn string) (*sql.DB, error) {
	conn, err := connectPostgres(ctx, dsn, maintenanceSession)
	if err != nil {
		return nil, err
	}
	if err := recoverRestore(ctx, conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// migrateUp applies every pending PostgreSQL migration.
func migrateUp(ctx context.Context, conn *sql.DB, fsys fs.FS) error {
	p, err := postgresProvider(ctx, conn, fsys)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("PostgreSQL migrations: %w", err)
	}
	return nil
}

// schemaVersion is the applied PostgreSQL migration version.
func schemaVersion(ctx context.Context, conn *sql.DB) (int64, error) {
	p, err := postgresProvider(ctx, conn, postgresFS)
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}
