package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// A reset ends every session, API key and passkey of the admin: it is what the owner runs on
// the server after a hijacked session, and a key made from that session would outlive it.
func TestResetPasswordRevokesSessionsAndKeys(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	hash, _ := auth.HashPassword("old-password-long")
	a, err := st.Q.CreateAdmin(ctx, db.CreateAdminParams{Username: "admin", PasswordHash: hash, CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.NewSessions(st.Q, func() time.Time { return now }, nil).Create(ctx, a.ID, "127.0.0.1", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q.CreateAPIKey(ctx, db.CreateAPIKeyParams{AdminID: a.ID, Name: "ci", Prefix: "mk_abcdefg", Hash: "h", Scope: "full", CreatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"key-1", "key-2"} {
		if _, err := st.Q.CreateAdminPasskey(ctx, db.CreateAdminPasskeyParams{AdminID: a.ID, CredentialID: []byte(id), Name: id, Credential: "{}", CreatedAt: now.Unix()}); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := resetPassword(ctx, st, []string{"--password-stdin"}, strings.NewReader("brand-new-password\n"), &out); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.Q.CountAPIKeys(ctx); n != 0 {
		t.Fatalf("%d API keys survived the reset", n)
	}
	if rows, _ := st.Q.ListAdminSessions(ctx, a.ID); len(rows) != 0 {
		t.Fatalf("%d sessions survived the reset", len(rows))
	}
	if rows, _ := st.Q.ListAdminPasskeys(ctx, a.ID); len(rows) != 0 {
		t.Fatalf("%d passkeys survived the reset", len(rows))
	}
	if !strings.Contains(out.String(), "1 API keys and 2 passkeys are revoked") || strings.Contains(out.String(), "brand-new-password") {
		t.Fatalf("output: %q", out.String())
	}
	got, _ := st.Q.GetAdmin(ctx, a.ID)
	if ok, _ := auth.VerifyPassword("brand-new-password", got.PasswordHash); !ok {
		t.Fatal("the new password does not work")
	}
}
