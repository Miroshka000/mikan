package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The 0.5 installer may run its database commands against this release: migrate has
// nothing to do, backup writes the SQLite copy, anything else is refused.
func TestDatabaseCommandsOfTheNextInstaller(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("MIKAN_DATA_DIR", dir)
	if err := databaseCmd(ctx, []string{"migrate"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.dump")
	if err := databaseCmd(ctx, []string{"backup", path}); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 16)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Read(head); err != nil || string(head) != "SQLite format 3\x00" {
		t.Fatalf("not a SQLite copy: %q %v", head, err)
	}
	if err := databaseCmd(ctx, []string{"restore", path}); err == nil {
		t.Fatal("restore accepted on a SQLite release")
	}
}
