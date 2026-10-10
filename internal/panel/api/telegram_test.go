package api

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/tgbot"
)

// The sub path is the one thing about the panel a scanner cannot guess, and the bot's
// Mini App address carries that path. A read key is for monitoring, so the address stays
// out of its answer; a full key and the session still see it.
func TestReadKeyDoesNotGetTheSubPath(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := settings.New(st.Q)
	if err := settings.Set(ctx, set, tgbot.KeyConfig, tgbot.Default("en")); err != nil {
		t.Fatal(err)
	}
	const sub = "s3cr3t-sub-path"
	bot := tgbot.New(tgbot.Deps{
		Store:    st,
		Settings: set,
		Now:      func() time.Time { return time.Unix(1_800_000_000, 0) },
		SubBase:  func(context.Context) string { return "https://203.0.113.10/" + sub },
		MiniApp:  func() bool { return true },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h := &handlers{d: Deps{Store: st, Settings: set, Telegram: bot}}

	for name, c := range map[string]struct {
		ctx  context.Context
		want bool
	}{
		"read key": {context.WithValue(ctx, keyAPIKey, db.ApiKey{Scope: "read"}), false},
		"full key": {context.WithValue(ctx, keyAPIKey, db.ApiKey{Scope: "full"}), true},
		"session":  {ctx, true},
	} {
		v, err := h.telegramView(c.ctx)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := strings.Contains(v.MiniAppURL, sub); got != c.want {
			t.Errorf("%s: mini_app_url %q, the sub path in it: %v", name, v.MiniAppURL, c.want)
		}
	}
}
