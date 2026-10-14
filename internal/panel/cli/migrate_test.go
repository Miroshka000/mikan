package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/config"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// After a restore on the new server, `admin migrate` repoints the panel at the new
// address: tokens, the subscription path and tariffs stay, the links move as a whole.
func TestMigrateCommand(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := storetest.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set := settings.New(st.Q)
	for k, v := range map[string]any{
		settings.KeyAdminPath: "adminOldPath12345678", settings.KeySubPath: "subOldPath12",
		settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355,
		settings.KeyDomain: "old.example.com",
	} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	before, err := st.Q.CreateUser(ctx, db.CreateUserParams{Name: "kept", SubToken: "0123456789abcdefghijklmn", Source: "admin", ResetStrategy: "none",
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, error) {
		var stdout, stderr bytes.Buffer
		err := migrateCmd(ctx, st, set, cfg, args, &stdout, &stderr)
		return stdout.String(), stderr.String(), err
	}

	if _, _, err := run("--public-host", "not a host!!"); err == nil {
		t.Fatal("bad host accepted")
	}
	if _, _, err := run(); err == nil {
		t.Fatal("missing host accepted")
	}
	stdout, _, err := run("--public-host", "198.51.100.20", "--no-check-nodes")
	if err != nil {
		t.Fatal(err)
	}
	if host, _ := set.String(ctx, settings.KeyPublicHost); host != "198.51.100.20" {
		t.Fatalf("host: %q", host)
	}
	// Domain and port come from the backup unless overridden.
	if d, _ := set.String(ctx, settings.KeyDomain); d != "old.example.com" {
		t.Fatalf("domain: %q", d)
	}
	if !strings.Contains(stdout, "https://old.example.com:21355/subOldPath12/<token>") {
		t.Fatalf("subscriptions base: %q", stdout)
	}
	after, err := st.Q.GetUserBySubToken(ctx, before.SubToken)
	if err != nil || after.ID != before.ID || after.Name != "kept" {
		t.Fatalf("user token did not survive: %v", err)
	}
	if p, _ := set.Paths(ctx); p.Sub != "subOldPath12" || p.Admin != "adminOldPath12345678" {
		t.Fatalf("paths changed: %+v", p)
	}

	// A new domain and port move the links; --no-domain drops the domain.
	if _, _, err := run("--public-host", "198.51.100.20", "--port", "22443", "--domain", "new.example.com", "--no-check-nodes"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = run("--public-host", "198.51.100.20", "--no-domain", "--no-check-nodes")
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := set.String(ctx, settings.KeyDomain); d != "" {
		t.Fatalf("domain not dropped: %q", d)
	}
	if !strings.Contains(stdout, "https://198.51.100.20:22443/subOldPath12/<token>") {
		t.Fatalf("subscriptions base after move: %q", stdout)
	}
}
