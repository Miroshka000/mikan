package app

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// A subscription that cannot connect gets one notice in place of its servers once the
// admin turns it on: the built-in words by what happened, or the admin's own with the
// title's variables; {tariff} names the tariff.
func TestInactiveServer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355, settings.KeyDeviceBinding: false} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	fetch := func() string {
		t.Helper()
		_, body := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken+"?format=uri", nil, map[string]string{"User-Agent": "Happ/3.4.1"})
		links, _ := base64.StdEncoding.DecodeString(string(body))
		return string(links)
	}
	notice := func(links string) string {
		i := strings.LastIndex(links, "#")
		if i < 0 {
			return ""
		}
		s, _ := url.PathUnescape(strings.TrimSpace(links[i+1:]))
		return s
	}
	if _, err := h.st.DB.ExecContext(ctx, "UPDATE users SET expires_at = $1 WHERE id = $2", h.now.Add(-time.Hour).Unix(), u.ID); err != nil {
		t.Fatal(err)
	}
	if links := fetch(); strings.Contains(links, "127.0.0.1:1") || !strings.Contains(links, "vless://") {
		t.Fatalf("off: the servers as before: %.200s", links)
	}
	if err := settings.Set(ctx, set, settings.KeyInactiveServer, true); err != nil {
		t.Fatal(err)
	}
	links := fetch()
	if strings.Count(links, "://") != 1 || !strings.Contains(links, "127.0.0.1:1") || !strings.Contains(notice(links), "Подписка закончилась") {
		t.Fatalf("expired: %q", links)
	}
	if err := settings.Set(ctx, set, settings.KeyInactiveText, "⛔ {tariff} ended {date}, renew: @bot"); err != nil {
		t.Fatal(err)
	}
	if got := notice(fetch()); !strings.HasPrefix(got, "⛔ "+tariffs[1].Name+" ended ") || !strings.HasSuffix(got, "@bot") {
		t.Fatalf("own text: %q", got)
	}
	// A subscription that can connect is not touched.
	if _, err := h.st.DB.ExecContext(ctx, "UPDATE users SET expires_at = NULL WHERE id = $1", u.ID); err != nil {
		t.Fatal(err)
	}
	if links := fetch(); strings.Contains(links, "127.0.0.1:1") {
		t.Fatalf("active: %.200s", links)
	}
}
