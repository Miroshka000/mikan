package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// selfDump makes a database backup the way the host's own backups do: by running this
// binary's `mikan database backup FILE` (pg_dump, custom format, the panel's schema). The
// command reads MIKAN_DATABASE_URL from the panel's environment.
func selfDump(ctx context.Context, path string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "database", "backup", path)
	// Stopped, the command is asked to end (it stops its pg_dump on SIGTERM) and only then
	// killed: a plain kill would leave pg_dump running on after it.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, tail(strings.TrimSpace(string(out)), 500))
	}
	return nil
}

// tail is the end of s, at most n bytes: where a tool says what went wrong.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
