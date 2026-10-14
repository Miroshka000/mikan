package store

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"mikan/internal/panel/store/db"
)

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var exists bool
	if err := s.DB.QueryRowContext(context.Background(), "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func schemaExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var exists bool
	if err := s.DB.QueryRowContext(context.Background(), "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname=$1)", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// A restore cut short by a crash never reported success: the next start brings the
// previous data back, but never while a restore still runs.
func TestInterruptedRestoreComesBack(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dsn := fixtureDSN(t, s)
	if err := s.Q.SetSetting(ctx, db.SetSettingParams{Key: "before", Value: "keep"}); err != nil {
		t.Fatal(err)
	}
	c, err := s.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	schema, aside, lockID, err := restoreNames(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := inTx(ctx, c, "ALTER SCHEMA "+quote(schema)+" RENAME TO "+quote(aside), "CREATE SCHEMA "+quote(schema), "CREATE TABLE "+quote(schema)+".partial (id BIGINT)"); err != nil {
		t.Fatal(err)
	}

	if _, err := c.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		t.Fatal(err)
	}
	if _, err := openMaintenance(ctx, dsn); err == nil || !strings.Contains(err.Error(), "restore is running") {
		t.Fatal("recovered beside a running restore:", err)
	}
	if _, err := c.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lockID); err != nil {
		t.Fatal(err)
	}

	pg, err := openMaintenance(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	pg.Close()
	if v, err := s.Q.GetSetting(ctx, "before"); err != nil || v != "keep" {
		t.Fatal("previous data did not come back", v, err)
	}
	if tableExists(t, s, "partial") || schemaExists(t, s, aside) {
		t.Fatal("the unfinished restore was not discarded")
	}
}

// A restore that loaded stays even when its previous schema cannot be dropped: the next
// start must not take the leftover for an interrupted restore and bring the old data back.
func TestFinishedRestoreSurvivesAFailedDrop(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dsn := fixtureDSN(t, s)
	if err := s.Q.SetSetting(ctx, db.SetSettingParams{Key: "previous", Value: "old"}); err != nil {
		t.Fatal(err)
	}
	// A reader of the previous data holds its table: DROP SCHEMA waits for it and gives up.
	reader, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ExecContext(ctx, "LOCK TABLE settings IN ACCESS SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	query := u.Query()
	query.Set("lock_timeout", "300ms")
	u.RawQuery = query.Encode()
	// The backup: this release's schema with a setting the previous data have not.
	err = RestorePostgres(ctx, u.String(), dir, func(ctx context.Context) error {
		pg, err := connectPostgres(ctx, u.String(), maintenanceSession)
		if err != nil {
			return err
		}
		defer pg.Close()
		if err := migrateUp(ctx, pg, postgresFS); err != nil {
			return err
		}
		_, err = pg.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('restored','yes')")
		return err
	})
	reader.Rollback()
	var leftover *LeftoverSchemaError
	if !errors.As(err, &leftover) {
		t.Fatal("a failed drop is not reported as a leftover:", err)
	}

	pg, err := openMaintenance(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	pg.Close()
	if v, err := s.Q.GetSetting(ctx, "restored"); err != nil || v != "yes" {
		t.Fatal("the next start brought the previous data back", v, err)
	}
	if !schemaExists(t, s, leftover.Schema) {
		t.Fatal("the leftover schema the error names is not there")
	}
	if _, err := s.DB.ExecContext(ctx, "DROP SCHEMA "+quote(leftover.Schema)+" CASCADE"); err != nil {
		t.Fatal(err)
	}
}
