package settings

import (
	"context"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// A cached Settings keeps a value for its ttl: what another process writes (the CLI, here
// straight into the table) waits for it, what this process writes is seen at once.
func TestCachedSettings(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	plain, cached := New(st.Q), Cached(st.Q, time.Hour)
	if err := Set(ctx, plain, KeyDefaultLang, "ru"); err != nil {
		t.Fatal(err)
	}
	writtenAt.Store(time.Now().Add(-writeGrace).UnixNano()) // as if written a while ago

	lang := func() string {
		t.Helper()
		v, err := cached.Lang(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if lang() != "ru" {
		t.Fatal("first read")
	}
	if err := st.Q.SetSetting(ctx, db.SetSettingParams{Key: KeyDefaultLang, Value: `"en"`}); err != nil {
		t.Fatal(err)
	}
	if lang() != "ru" {
		t.Fatal("a value written by another process must wait for the ttl")
	}
	if err := Set(ctx, plain, KeyDefaultLang, "en"); err != nil {
		t.Fatal(err)
	}
	if lang() != "en" {
		t.Fatal("a value written by this process must be seen at once")
	}
	// Read within writeGrace of the write: not kept, the next read asks again.
	if err := st.Q.SetSetting(ctx, db.SetSettingParams{Key: KeyDefaultLang, Value: `"ru"`}); err != nil {
		t.Fatal(err)
	}
	if lang() != "ru" {
		t.Fatal("a read right after a write must not be kept: its transaction may not have committed")
	}
	if v, _, _ := Get[string](ctx, plain, KeyDefaultLang); v != "ru" {
		t.Fatal("a plain Settings reads the table every time")
	}
}

// Subscription links point at the subscription domain when one is set, else at the
// panel's address; the subscription port replaces the panel's when set.
func TestSubEndpoint(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := New(st.Q)
	if err := Set(ctx, set, KeyPublicHost, "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	if err := Set(ctx, set, KeyPanelPort, 2053); err != nil {
		t.Fatal(err)
	}
	if ep, err := set.SubEndpoint(ctx); err != nil || (ep != Endpoint{Host: "203.0.113.7", Port: 2053}) {
		t.Fatalf("the public address: %+v, %v", ep, err)
	}
	if err := Set(ctx, set, KeyDomain, "vpn.example.com"); err != nil {
		t.Fatal(err)
	}
	if ep, err := set.SubEndpoint(ctx); err != nil || (ep != Endpoint{Host: "vpn.example.com", Port: 2053}) {
		t.Fatalf("the domain wins over the address: %+v, %v", ep, err)
	}
	if err := Set(ctx, set, KeySubPort, 8443); err != nil {
		t.Fatal(err)
	}
	if ep, err := set.SubEndpoint(ctx); err != nil || (ep != Endpoint{Host: "vpn.example.com", Port: 8443}) {
		t.Fatalf("the subscription port: %+v, %v", ep, err)
	}
	if err := Set(ctx, set, KeySubDomain, "sub.example.com"); err != nil {
		t.Fatal(err)
	}
	if ep, err := set.SubEndpoint(ctx); err != nil || (ep != Endpoint{Host: "sub.example.com", Port: 8443}) {
		t.Fatalf("the subscription domain: %+v, %v", ep, err)
	}
	// Without it the links keep pointing at the panel, whatever the subscription port.
	if err := Set(ctx, set, KeySubDomain, ""); err != nil {
		t.Fatal(err)
	}
	if ep, err := set.SubEndpoint(ctx); err != nil || (ep != Endpoint{Host: "vpn.example.com", Port: 8443}) {
		t.Fatalf("cleared: %+v, %v", ep, err)
	}
}
