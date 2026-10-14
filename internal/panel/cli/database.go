package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mikan/internal/panel/config"
	"mikan/internal/panel/store"
)

func databaseCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("database needs migrate, backup FILE or restore FILE")
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	dsn, err := store.DatabaseURL()
	if err != nil {
		return err
	}
	switch args[0] {
	case "migrate":
		if len(args) != 1 {
			return errors.New("usage: mikan database migrate")
		}
		res, err := store.Migrate(ctx, dsn, cfg.DataDir)
		if err != nil {
			return err
		}
		// The installer reads this line: an unchanged schema lets a failed update go back.
		fmt.Printf("PostgreSQL schema version: %d -> %d\n", res.Before, res.After)
		return nil
	case "backup":
		if len(args) != 2 {
			return errors.New("usage: mikan database backup FILE")
		}
		if err := databaseBackup(ctx, dsn, args[1]); err != nil {
			return err
		}
		fmt.Println("Database copied to", args[1])
		return nil
	case "restore":
		if len(args) != 2 {
			return errors.New("usage: mikan database restore FILE (panel must be stopped)")
		}
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		magic := make([]byte, 16)
		_, readErr := io.ReadFull(f, magic)
		f.Close()
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%s is too short to be a database backup", args[1])
		} else if readErr != nil {
			return readErr
		}
		if string(magic) == "SQLite format 3\x00" {
			return errors.New("this is a database of mikan 0.4 (SQLite): mikan 0.5.0.4 restores it and moves it to PostgreSQL; restore it there, then update")
		} else if string(magic[:5]) == "PGDMP" {
			if err := checkDump(ctx, args[1]); err != nil {
				return err
			}
			err = store.RestorePostgres(ctx, dsn, cfg.DataDir, func(ctx context.Context) error {
				// An empty --dbname makes pg_restore connect (to the database PGDATABASE names,
				// like every other parameter here) instead of printing an SQL script.
				return postgresTool(ctx, dsn, nil, "pg_restore", "--dbname=", "--clean", "--if-exists", "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl", args[1])
			})
		} else {
			return errors.New("unsupported database backup format")
		}
		// The restored data are in place; only the old copy is left to clean up.
		var leftover *store.LeftoverSchemaError
		if errors.As(err, &leftover) {
			fmt.Fprintln(os.Stderr, "Warning:", leftover)
		} else if err != nil {
			return err
		}
		fmt.Println("Database restored; start the panel after restoring its matching files")
		return nil
	default:
		return fmt.Errorf("unknown database command %q", args[0])
	}
}

// databaseBackup dumps the panel's schema; it runs beside a running panel, pg_dump reads
// one consistent snapshot.
func databaseBackup(ctx context.Context, dsn, path string) error {
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	schema, err := store.CurrentSchema(qctx, dsn)
	cancel()
	if err != nil {
		return fmt.Errorf("backup schema: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	err = postgresTool(ctx, dsn, f, "pg_dump", "--format=custom", "--no-owner", "--no-acl", "--schema="+schema)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return fmt.Errorf("PostgreSQL backup failed: %w", err)
	}
	return nil
}

// toolOutputLimit is how much of a client's diagnostics an error carries: the tail, where
// the reason is.
const toolOutputLimit = 2000

// toolOutput is the tail of a client's diagnostics with the password masked: libpq
// takes it from PGPASSWORD and does not print it, the mask is for a server that echoes.
func toolOutput(dsn, out string) string {
	out = strings.TrimSpace(out)
	if cfg, err := store.ParseDatabaseURL(dsn); err == nil && cfg.Password != "" {
		out = strings.ReplaceAll(out, cfg.Password, "***")
	}
	if len(out) > toolOutputLimit {
		cut := len(out) - toolOutputLimit
		for cut < len(out) && !utf8.RuneStart(out[cut]) {
			cut++
		}
		out = "…" + out[cut:]
	}
	if out == "" {
		return ""
	}
	return ": " + out
}

func postgresEnv(dsn string) ([]string, error) {
	// PGDATABASE does not expand a URI into user/password/host. Give libpq each
	// field explicitly, especially with UID 65532 which has no OS login in the image.
	cfg, err := store.ParseDatabaseURL(dsn)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errors.New("database backup/restore requires a postgres:// or postgresql:// MIKAN_DATABASE_URL")
	}
	// Only what a PostgreSQL client needs from the panel's environment: MIKAN_* holds the
	// panel's secrets, and no tool gets them.
	var env []string
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		switch {
		case name == "PATH", name == "HOME", name == "TZ", name == "TMPDIR", name == "LANG", strings.HasPrefix(name, "LC_"):
			env = append(env, value)
		}
	}
	env = append(env, "PGHOST="+cfg.Host, "PGPORT="+strconv.Itoa(int(cfg.Port)), "PGUSER="+cfg.User, "PGPASSWORD="+cfg.Password, "PGDATABASE="+cfg.Database, "PGCONNECT_TIMEOUT=10")
	query := u.Query()
	for key, variable := range map[string]string{"sslmode": "PGSSLMODE", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY", "sslrootcert": "PGSSLROOTCERT", "sslcrl": "PGSSLCRL", "sslcrldir": "PGSSLCRLDIR", "sslpassword": "PGSSLPASSWORD", "sslcertmode": "PGSSLCERTMODE", "gssencmode": "PGGSSENCMODE", "channel_binding": "PGCHANNELBINDING", "target_session_attrs": "PGTARGETSESSIONATTRS", "options": "PGOPTIONS"} {
		if value := query.Get(key); value != "" {
			env = append(env, variable+"="+value)
		}
	}
	return env, nil
}

// postgresTool runs a PostgreSQL client with the credentials in its environment, never in
// its arguments; stdout, when not nil, takes what the client writes there.
func postgresTool(ctx context.Context, dsn string, stdout io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var err error
	cmd.Env, err = postgresEnv(dsn)
	if err != nil {
		return err
	}
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w%s", name, err, toolOutput(dsn, stderr.String()))
	}
	return nil
}
