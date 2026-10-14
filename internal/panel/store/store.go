package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"math/rand/v2"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"mikan/internal/panel/store/db"
)

//go:embed postgres/*.sql
var postgresMigrations embed.FS

// postgresFS holds the PostgreSQL migrations at its root, as goose reads them.
var postgresFS = func() fs.FS {
	f, err := fs.Sub(postgresMigrations, "postgres")
	if err != nil {
		panic(err) // a fixed embedded directory
	}
	return f
}()

type Store struct {
	DB      *sql.DB
	Q       *db.Queries
	cleanup func() error

	conflicts atomic.Uint64
}

// Conflicts counts the serialization conflicts and deadlocks Tx and TxRC have retried
// since the store opened.
func (s *Store) Conflicts() uint64 { return s.conflicts.Load() }

// DatabaseURL is MIKAN_DATABASE_URL, the panel's PostgreSQL.
func DatabaseURL() (string, error) {
	dsn := os.Getenv("MIKAN_DATABASE_URL")
	if dsn == "" {
		return "", errNoDatabaseURL
	}
	return dsn, nil
}

var errNoDatabaseURL = errors.New("MIKAN_DATABASE_URL is required; run the current mikan installer to set up PostgreSQL")

// ParseDatabaseURL parses a MIKAN_DATABASE_URL without echoing it: it holds the password.
func ParseDatabaseURL(dsn string) (*pgx.ConnConfig, error) {
	if dsn == "" {
		return nil, errNoDatabaseURL
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid MIKAN_DATABASE_URL")
	}
	return cfg, nil
}

func Open(ctx context.Context, dataDir string) (*Store, error) {
	dsn, err := DatabaseURL()
	if err != nil {
		return nil, err
	}
	return OpenPostgres(ctx, dataDir, dsn)
}

// OpenPostgres refuses data still in SQLite (checkLegacy): such a server goes through
// 0.5.0.4 first, which moves them.
func OpenPostgres(ctx context.Context, dataDir, dsn string) (*Store, error) {
	if err := prepareSchema(ctx, dataDir, dsn); err != nil {
		return nil, err
	}
	return openRuntime(ctx, dsn, nil)
}

// OpenMigrated brings dsn's schema to this binary's version and opens it, without the
// SQLite check of OpenPostgres: for test fixtures (storetest), each in a schema
// of its own. cleanup runs after the store closes.
func OpenMigrated(ctx context.Context, dsn string, cleanup func() error) (*Store, error) {
	conn, err := connectPostgres(ctx, dsn, maintenanceSession)
	if err != nil {
		return nil, err
	}
	err = migrateUp(ctx, conn, postgresFS)
	if err == nil {
		err = ensureLocalNode(ctx, conn)
	}
	conn.Close()
	if err != nil {
		return nil, err
	}
	return openRuntime(ctx, dsn, cleanup)
}

// prepareSchema checks and migrates on maintenance connections: a migration may run
// longer than a runtime statement may.
func prepareSchema(ctx context.Context, dataDir, dsn string) error {
	conn, err := openMaintenance(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := checkLegacy(ctx, conn, dataDir); err != nil {
		return err
	}
	if err := migrateUp(ctx, conn, postgresFS); err != nil {
		return err
	}
	return ensureLocalNode(ctx, conn)
}

func openRuntime(ctx context.Context, dsn string, cleanup func() error) (*Store, error) {
	conn, err := connectPostgres(ctx, dsn, runtimeSession)
	if err != nil {
		return nil, err
	}
	return &Store{DB: conn, Q: db.New(conn), cleanup: cleanup}, nil
}

func ensureLocalNode(ctx context.Context, conn *sql.DB) error {
	_, err := conn.ExecContext(ctx, "INSERT INTO nodes(id,created_at,updated_at) VALUES(1,$1,$1) ON CONFLICT(id) DO NOTHING", time.Now().Unix())
	return err
}

// session is what a pool's connections are for.
type session int

const (
	// runtimeSession bounds every statement: a stuck query fails instead of holding one
	// of the pool's few connections for good.
	runtimeSession session = iota
	// maintenanceSession runs import, restore and migrations, whose statements may
	// legitimately run long; waiting on a lock and idling in a transaction stay bounded.
	maintenanceSession
)

// poolSize bounds the connections of one pool; all of them may idle, so a burst after a
// quiet minute does not pay for new connections.
const poolSize = 16

func connectPostgres(ctx context.Context, dsn string, kind session) (*sql.DB, error) {
	cfg, err := ParseDatabaseURL(dsn)
	if err != nil {
		return nil, err
	}
	cfg.ConnectTimeout = 10 * time.Second
	// What the URL sets wins: an operator may tune these for a big installation.
	for name, value := range map[string]string{
		"application_name":                    "mikan",
		"statement_timeout":                   "30s",
		"lock_timeout":                        "10s",
		"idle_in_transaction_session_timeout": "60s",
	} {
		if _, set := cfg.RuntimeParams[name]; !set {
			cfg.RuntimeParams[name] = value
		}
	}
	if kind == maintenanceSession {
		cfg.RuntimeParams["statement_timeout"] = "0"
	}
	conn := stdlib.OpenDB(*cfg)
	conn.SetMaxOpenConns(poolSize)
	conn.SetMaxIdleConns(poolSize)
	conn.SetConnMaxIdleTime(5 * time.Minute)
	conn.SetConnMaxLifetime(30 * time.Minute)
	if err := waitReady(ctx, conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	return conn, nil
}

// startupWait is how long opening waits for a PostgreSQL that is not up yet: the panel
// and its database start together, after a reboot or an update.
const startupWait = 60 * time.Second

func waitReady(ctx context.Context, conn *sql.DB) error {
	deadline := time.Now().Add(startupWait)
	delay := 250 * time.Millisecond
	for {
		err := conn.PingContext(ctx)
		if err == nil || !notReady(err) || time.Now().Add(delay).After(deadline) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), err)
		case <-timer.C:
		}
		delay = min(2*delay, 5*time.Second)
	}
}

// notReady tells a PostgreSQL that does not accept connections yet: nothing listens on
// its port or socket, or it is starting up or recovering (57P03 cannot_connect_now).
// A wrong password or a missing database never gets better by waiting.
func notReady(err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code == "57P03"
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}

// Ping checks that dsn's PostgreSQL answers a query, on one connection of its own: for
// the healthcheck, which must stay cheap.
func Ping(ctx context.Context, dsn string) error {
	return withConn(ctx, dsn, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SELECT 1")
		return err
	})
}

// CurrentSchema is the schema dsn's tables live in.
func CurrentSchema(ctx context.Context, dsn string) (string, error) {
	var schema string
	err := withConn(ctx, dsn, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, "SELECT current_schema()").Scan(&schema)
	})
	return schema, err
}

func withConn(ctx context.Context, dsn string, fn func(*pgx.Conn) error) error {
	cfg, err := ParseDatabaseURL(dsn)
	if err != nil {
		return err
	}
	cfg.RuntimeParams["application_name"] = "mikan"
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer c.Close(context.WithoutCancel(ctx))
	return fn(c)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// schemaLock derives a session advisory lock id that belongs to this database and schema
// alone, so that installations sharing a server never wait on each other.
func schemaLock(ctx context.Context, q queryRower, purpose string) (schema string, id int64, err error) {
	var database string
	if err = q.QueryRowContext(ctx, "SELECT current_database(),current_schema()").Scan(&database, &schema); err != nil {
		return "", 0, err
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "mikan-%s:%s:%s", purpose, database, schema)
	return schema, int64(h.Sum64()), nil
}

func postgresProvider(ctx context.Context, conn *sql.DB, fsys fs.FS) (*goose.Provider, error) {
	_, lockID, err := schemaLock(ctx, conn, "goose")
	if err != nil {
		return nil, err
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(lockID))
	if err != nil {
		return nil, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, conn, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("PostgreSQL migrations: %w", err)
	}
	// GetDBVersion initializes Goose's table under the session lock. GetVersions
	// initializes without it and races when two fresh processes start together.
	current, err := p.GetDBVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("PostgreSQL migration versions: %w", err)
	}
	sources := p.ListSources()
	target := sources[len(sources)-1].Version
	if current > target {
		return nil, fmt.Errorf("PostgreSQL schema %d is newer than this binary supports (%d); downgrade refused", current, target)
	}
	return p, nil
}

func (s *Store) Close() error {
	err := s.DB.Close()
	if s.cleanup != nil {
		err = errors.Join(err, s.cleanup())
	}
	return err
}

// txAttempts bounds the retries of a serialization conflict; with the backoff below the
// last attempt starts within about three seconds at most.
const txAttempts = 16

// Tx preserves read/check/write invariants (quotas, payments, ports, slot numbers) with
// serializable transactions. Callbacks only change database state: a serialization
// conflict retries the whole callback, never just its final statement.
func (s *Store) Tx(ctx context.Context, fn func(q *db.Queries) error) error {
	return s.retry(ctx, sql.LevelSerializable, fn)
}

// TxRC runs fn in one READ COMMITTED transaction: for writes that hold no read/check/write
// invariant across rows (settings, catalog edits, upserts that add in place). It never
// fails on a serialization conflict; a deadlock still retries the whole callback, so the
// same rule holds: callbacks only change database state.
func (s *Store) TxRC(ctx context.Context, fn func(q *db.Queries) error) error {
	return s.retry(ctx, sql.LevelReadCommitted, fn)
}

func (s *Store) retry(ctx context.Context, level sql.IsolationLevel, fn func(q *db.Queries) error) error {
	for attempt := 0; ; attempt++ {
		err := s.txOnce(ctx, level, fn)
		var pe *pgconn.PgError
		if err == nil || attempt == txAttempts-1 || !errors.As(err, &pe) || (pe.Code != "40001" && pe.Code != "40P01") {
			return err
		}
		// Exponential backoff with full jitter: writers that collided together must not
		// all come back together, or the same conflict repeats until the attempts run out.
		s.conflicts.Add(1)
		window := min(5*time.Millisecond<<attempt, 250*time.Millisecond)
		timer := time.NewTimer(rand.N(window) + time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) txOnce(ctx context.Context, level sql.IsolationLevel, fn func(q *db.Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: level})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(s.Q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func IsUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// IsForeignKey: the row points at one that is not there (any more).
func IsForeignKey(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23503"
}
