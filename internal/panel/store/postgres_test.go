package store

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"mikan/internal/panel/store/db"
)

// A slot belongs to one buyer even when two purchases reach PostgreSQL together.
func TestPostgresConcurrentSlots(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, n := range []string{"a", "b"} {
		if err := s.Q.InsertSlot(ctx, db.InsertSlotParams{Name: n, Uuid: n, Secret: n, CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	ids := make(chan int64, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			var id int64
			err := s.Tx(ctx, func(q *db.Queries) error {
				slot, err := q.TakeFreeSlot(ctx)
				if err == nil {
					id = slot.ID
				}
				return err
			})
			if err == nil {
				ids <- id
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	a, b := <-ids, <-ids
	if a == b {
		t.Fatalf("two transactions received slot %d", a)
	}
}

func TestConcurrentFreshPostgresMigrations(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var schema string
	if err := s.DB.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, "DROP SCHEMA \""+schema+"\" CASCADE; CREATE SCHEMA \""+schema+"\""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- migrateUp(ctx, s.DB, postgresFS) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("concurrent initialization:", err)
		}
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM goose_db_version WHERE version_id=1 AND is_applied").Scan(&n); err != nil || n != 1 {
		t.Fatalf("baseline applied %d times: %v", n, err)
	}
}

func fixtureDSN(t *testing.T, s *Store) string {
	t.Helper()
	var schema string
	if err := s.DB.QueryRowContext(context.Background(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("MIKAN_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String()
}

func TestRuntimeRefusesIncompleteOrLostMigration(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	dsn := fixtureDSN(t, s)
	if err := os.WriteFile(filepath.Join(dir, "mikan.db"), []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	if live, err := OpenPostgres(ctx, dir, dsn); !errors.Is(err, ErrSQLiteData) {
		if live != nil {
			live.Close()
		}
		t.Fatal("started on data still in SQLite:", err)
	}
	if _, err := Migrate(ctx, dsn, dir); !errors.Is(err, ErrSQLiteData) {
		t.Fatal("migrated data still in SQLite:", err)
	}
	if err := os.Remove(filepath.Join(dir, "mikan.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, MigrationMarker), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if live, err := OpenPostgres(ctx, dir, dsn); err == nil {
		live.Close()
		t.Fatal("started after PostgreSQL data loss")
	}
	if err := ConfirmPostgresRestore(ctx, s.DB, dir); err != nil {
		t.Fatal(err)
	}
	live, err := OpenPostgres(ctx, dir, dsn)
	if err != nil {
		t.Fatal("explicit PG restore cannot start:", err)
	}
	live.Close()
}
