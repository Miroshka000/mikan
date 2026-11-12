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

// The seed of the front pages is made once and stays: a page that changed at every start
// would be no page of a site.
func TestFrontSeed(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := New(st.Q)
	first, err := set.FrontSeed(ctx)
	if err != nil || len(first) < 20 {
		t.Fatalf("the seed: %q %v", first, err)
	}
	if again, err := New(st.Q).FrontSeed(ctx); err != nil || again != first {
		t.Fatalf("read again: %q %v, want %q", again, err, first)
	}
	if stored, err := set.String(ctx, KeyFrontSeed); err != nil || stored != first {
		t.Fatalf("stored: %q %v", stored, err)
	}
}
