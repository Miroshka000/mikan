package app

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// The support link goes to the subscription page and into a header of every subscription,
// so it is held to the same rule as the other links: https with a host, or tg://, without
// spaces or control characters.
func TestSupportURLIsAValidLink(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1/settings"
	for _, c := range []struct {
		url  string
		ok   bool
		want string
	}{
		{"https://t.me/support", true, "https://t.me/support"},
		{"tg://resolve?domain=support", true, "tg://resolve?domain=support"},
		{"  https://t.me/support  ", true, "https://t.me/support"},
		{"", true, ""},
		{"https://t.me/support me", false, ""},
		{"https://t.me/\tsupport", false, ""},
		{"https://", false, ""},
		{"http://t.me/support", false, ""},
		{"javascript:alert(1)", false, ""},
	} {
		resp, body := h.do(http.MethodPatch, api, map[string]any{"support_url": c.url}, csrf)
		if c.ok && resp.StatusCode != http.StatusOK || !c.ok && (resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "support_url_invalid")) {
			t.Errorf("%q: %d %s", c.url, resp.StatusCode, body)
			continue
		}
		if c.ok {
			if got, _ := settings.New(h.st.Q).String(ctx, settings.KeySupportURL); got != c.want {
				t.Errorf("%q saved as %q, want %q", c.url, got, c.want)
			}
		}
	}
}
