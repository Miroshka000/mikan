package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/metacubex/age"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgbackup"
)

// newDatabase makes a database of its own on the test server, in the public schema like
// an installation's, and drops it at the end.
func newDatabase(t *testing.T, label string) string {
	t.Helper()
	ctx := context.Background()
	base := os.Getenv("MIKAN_TEST_DATABASE_URL")
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(context.Background()) })
	name := fmt.Sprintf("mikan_%s_%d", label, time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Skip("CREATE DATABASE is not permitted here:", err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// The whole way of a Telegram backup: made on one server by the panel (pg_dump through
// `mikan database backup`), packed and encrypted, then on a freshly installed server
// decrypted, unpacked and restored by `mikan database restore` (pg_restore and the
// migrations), and the new server's panel starts on the old data.
func TestTelegramBackupRestoresOnANewServer(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump is not installed")
	}
	ctx := context.Background()
	const pass = "a long enough passphrase for the test"

	// The old server, with data of its own.
	oldDSN, oldDir := newDatabase(t, "old"), t.TempDir()
	oldStore, err := store.OpenPostgres(ctx, oldDir, oldDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer oldStore.Close()
	for k, v := range map[string]string{"brand": `"Old VPN"`, "domain": `"old.example.com"`, "public_host": `"203.0.113.10"`} {
		if err := oldStore.Q.SetSetting(ctx, db.SetSettingParams{Key: k, Value: v}); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MIKAN_DATABASE_URL", oldDSN)
	t.Setenv("MIKAN_DATA_DIR", oldDir)
	work := t.TempDir()
	path, _, err := tgbackup.Make(ctx, func(ctx context.Context, p string) error { return databaseCmd(ctx, []string{"backup", p}) }, work, pass, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// age -d -o mikan.tar.gz …, then what mikan restore unpacks.
	file, _ := os.ReadFile(path)
	id, _ := age.NewScryptIdentity(pass)
	plain, err := age.Decrypt(bytes.NewReader(file), id)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	dump := filepath.Join(t.TempDir(), "restore.dump")
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == "data/panel/backup.dump" {
			b, _ := io.ReadAll(tr)
			if err := os.WriteFile(dump, b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	// A freshly installed server: its own database with the current schema and defaults.
	newDSN, newDir := newDatabase(t, "new"), t.TempDir()
	fresh, err := store.OpenPostgres(ctx, newDir, newDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Q.SetSetting(ctx, db.SetSettingParams{Key: "brand", Value: `"New install"`}); err != nil {
		t.Fatal(err)
	}
	fresh.Close()
	t.Setenv("MIKAN_DATABASE_URL", newDSN)
	t.Setenv("MIKAN_DATA_DIR", newDir)
	if err := databaseCmd(ctx, []string{"restore", dump}); err != nil {
		t.Fatal(err)
	}

	// The panel starts on the new server with the old server's data, migrated.
	live, err := store.OpenPostgres(ctx, newDir, newDSN)
	if err != nil {
		t.Fatal("the restored database does not start:", err)
	}
	defer live.Close()
	for k, want := range map[string]string{"brand": `"Old VPN"`, "domain": `"old.example.com"`, "public_host": `"203.0.113.10"`} {
		if v, err := live.Q.GetSetting(ctx, k); err != nil || v != want {
			t.Errorf("%s on the new server: %s %v, want %s", k, v, err, want)
		}
	}
	var applied, latest int64
	if err := live.DB.QueryRowContext(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	// The binary's latest migration: the highest number, not the count (numbers can skip).
	entries, _ := os.ReadDir(filepath.Join("..", "store", "postgres"))
	for _, e := range entries {
		num, _, _ := strings.Cut(e.Name(), "_")
		if v, err := strconv.ParseInt(num, 10, 64); err == nil {
			latest = max(latest, v)
		}
	}
	if applied != latest {
		t.Errorf("schema version %d after the restore, the binary's latest migration is %d", applied, latest)
	}
}
