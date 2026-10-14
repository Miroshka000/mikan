package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"

	"github.com/jackc/pgx/v5"
)

// OpenTest is storetest.Open for this package's own tests, which cannot import storetest
// (it imports store): a fresh schema per fixture, dropped when the store closes.
func OpenTest(ctx context.Context, _ string) (*Store, error) {
	dsn := os.Getenv("MIKAN_TEST_DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("tests require MIKAN_TEST_DATABASE_URL pointing to a disposable PostgreSQL database")
	}
	var token [12]byte
	rand.Read(token[:])
	name := "mikan_test_" + hex.EncodeToString(token[:])
	exec := func(ctx context.Context, sql string) error {
		return withConn(ctx, dsn, func(c *pgx.Conn) error { _, err := c.Exec(ctx, sql); return err })
	}
	if err := exec(ctx, "CREATE SCHEMA "+quote(name)); err != nil {
		return nil, err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	query := u.Query()
	query.Set("search_path", name)
	u.RawQuery = query.Encode()
	return OpenMigrated(ctx, u.String(), func() error {
		return exec(context.Background(), "DROP SCHEMA IF EXISTS "+quote(name)+" CASCADE")
	})
}
