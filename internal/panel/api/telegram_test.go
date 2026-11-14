package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/botpanel"
	"mikan/internal/panel/infraalerts"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/tgate"
	"mikan/internal/panel/tgbot"
)

// fakeDiscordCheck answers a webhook check with name or err.
type fakeDiscordCheck struct {
	name  string
	err   error
	asked []string
}

func (f *fakeDiscordCheck) Check(_ context.Context, hook string) (string, error) {
	f.asked = append(f.asked, hook)
	return f.name, f.err
}

// A Discord webhook is a Discord address that Discord knows: anything else is refused before
// it is kept, and the address never comes back, to a key or to the session, nor goes to the
// journal; the view says it is set and its name.
func TestDiscordWebhookIsCheckedAndNotShown(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := settings.New(st.Q)
	if err := settings.Set(ctx, set, tgate.KeyConfig, tgbot.Default("en")); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Unix(1_800_000_000, 0) }
	bot := botpanel.New(botpanel.Deps{
		Backend: &botpanel.Local{Store: st, Settings: set, Now: now,
			SubBase: func(context.Context) string { return "https://203.0.113.10/sub" }, MiniApp: func() bool { return true }},
		Now: now,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	check := &fakeDiscordCheck{name: "alerts"}
	h := &handlers{d: Deps{Store: st, Settings: set, Telegram: bot, Discord: check}}
	const hook = "https://discord.com/api/webhooks/123/s3cr3t-token"
	patch := func(s string) (*infraalerts.Discord, map[string]any, error) {
		details := map[string]any{}
		d, err := h.nextDiscord(ctx, &infraalerts.AlertsConfigPatch{DiscordWebhook: &s}, details)
		return d, details, err
	}
	code := func(err error) string {
		var se huma.StatusError
		if !errors.As(err, &se) {
			return ""
		}
		if m, ok := se.(*huma.ErrorModel); ok && len(m.Errors) > 0 {
			return m.Errors[0].Message
		}
		return se.Error()
	}

	if _, _, err := patch("https://example.com/api/webhooks/1/x"); code(err) != "discord_webhook_format" || len(check.asked) != 0 {
		t.Fatalf("another host: %v, asked %q", err, check.asked)
	}
	check.err = &infraalerts.DiscordError{Code: 404, Message: "Unknown Webhook"}
	if _, _, err := patch(hook); code(err) != "discord_webhook_invalid" {
		t.Fatalf("a webhook Discord does not know: %v", err)
	}
	check.err = errors.New("dial tcp: timeout")
	if _, _, err := patch(hook); code(err) != "discord_unreachable" {
		t.Fatalf("Discord down: %v", err)
	}
	check.err = nil
	d, details, err := patch(" " + hook + " ")
	if err != nil || d == nil || d.URL != hook || d.Name != "alerts" || details["discord"] != "set" {
		t.Fatalf("set: %+v %v %v", d, details, err)
	}
	if raw, _ := json.Marshal(details); strings.Contains(string(raw), "s3cr3t") {
		t.Fatalf("the journal gets the address: %s", raw)
	}
	if err := settings.Set(ctx, set, infraalerts.KeyDiscord, *d); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]context.Context{
		"read key": context.WithValue(ctx, keyAPIKey, db.ApiKey{Scope: "read"}),
		"full key": context.WithValue(ctx, keyAPIKey, db.ApiKey{Scope: "full"}),
		"session":  ctx,
	} {
		v, err := h.telegramView(c)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw, _ := json.Marshal(v)
		if !v.DiscordSet || v.DiscordName != "alerts" || strings.Contains(string(raw), "s3cr3t") {
			t.Errorf("%s: discord_set %v, name %q, the address in the view: %v", name, v.DiscordSet, v.DiscordName, strings.Contains(string(raw), "s3cr3t"))
		}
	}
	if d, details, err := patch(""); err != nil || d == nil || d.URL != "" || details["discord"] != "removed" {
		t.Fatalf("remove: %+v %v %v", d, details, err)
	}
	if d, err := h.nextDiscord(ctx, &infraalerts.AlertsConfigPatch{}, map[string]any{}); err != nil || d != nil {
		t.Fatalf("a patch without the webhook changes it: %+v %v", d, err)
	}
}

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
	if err := settings.Set(ctx, set, tgate.KeyConfig, tgbot.Default("en")); err != nil {
		t.Fatal(err)
	}
	const sub = "s3cr3t-sub-path"
	now := func() time.Time { return time.Unix(1_800_000_000, 0) }
	bot := botpanel.New(botpanel.Deps{
		Backend: &botpanel.Local{
			Store:    st,
			Settings: set,
			Now:      now,
			SubBase:  func(context.Context) string { return "https://203.0.113.10/" + sub },
			MiniApp:  func() bool { return true },
		},
		Now: now,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
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
