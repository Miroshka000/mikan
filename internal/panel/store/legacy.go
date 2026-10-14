package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"mikan/internal/fsutil"
)

// What is left of the panel's SQLite past: 0.4 kept its data in data/panel/mikan.db, and
// 0.5.0.x moved them into PostgreSQL once (the import, with its proof in the table
// mikan_sqlite_import and a copy in the marker file below). This release no longer
// imports: a server still on SQLite goes through 0.5.0.4 first (the release index sends
// it there, "from" in .github/upgrade-from), and this one refuses to start on its data.

// MigrationMarker is the file the import left next to the SQLite database.
const MigrationMarker = "postgres-migration.json"

// ErrSQLiteData: the server's data are still in SQLite; this release cannot move them.
var ErrSQLiteData = errors.New("this server's data are still in SQLite (mikan 0.4): update through mikan 0.5.0.4 first, it moves them to PostgreSQL")

// importRecord is the proof an import (or an explicit PostgreSQL restore) left behind.
type importRecord struct {
	Format     int    `json:"format"`
	ImportedAt int64  `json:"imported_at"`
	SourceKind string `json:"source_kind,omitempty"`
}

// checkLegacy refuses data still in SQLite. A server that moved has the SQLite file and
// the marker next to the PostgreSQL record of the move; the marker is written again from
// the record when a crash lost it. A marker without a record means the PostgreSQL data
// were lost after the move: the backup has to be restored, not the old SQLite trusted.
func checkLegacy(ctx context.Context, conn *sql.DB, dataDir string) error {
	_, legacyErr := os.Stat(filepath.Join(dataDir, "mikan.db"))
	_, markerErr := os.Stat(filepath.Join(dataDir, MigrationMarker))
	for _, err := range []error{legacyErr, markerErr} {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("legacy database: %w", err)
		}
	}
	if legacyErr != nil && markerErr != nil {
		return nil // never on SQLite
	}
	var report string
	err := conn.QueryRowContext(ctx, "SELECT report FROM mikan_sqlite_import WHERE id = 1").Scan(&report)
	switch {
	case err == nil:
		return writeMigrationMarker(dataDir, []byte(report))
	case markerErr == nil:
		return errors.New("the move from SQLite is recorded on this server but not in PostgreSQL: restore the PostgreSQL backup before starting the panel")
	default:
		return ErrSQLiteData
	}
}

func writeMigrationMarker(dir string, report []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, MigrationMarker), report, 0o600)
}

// ConfirmPostgresRestore marks the explicitly restored database as authoritative.
// A fresh-install dump has no SQLite import row, even on a server that once migrated;
// its stale original SQLite must neither block startup nor overwrite the restored data.
func ConfirmPostgresRestore(ctx context.Context, pg *sql.DB, dir string) error {
	var report string
	err := pg.QueryRowContext(ctx, "SELECT report FROM mikan_sqlite_import WHERE id=1").Scan(&report)
	if errors.Is(err, sql.ErrNoRows) {
		encoded, err := json.Marshal(importRecord{Format: 1, ImportedAt: time.Now().Unix(), SourceKind: "postgresql-restore"})
		if err != nil {
			return err
		}
		report = string(encoded)
		if _, err := pg.ExecContext(ctx, "INSERT INTO mikan_sqlite_import(id,report) VALUES(1,$1)", report); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return writeMigrationMarker(dir, []byte(report))
}
