package app

import (
	"net/http"
	"strings"
	"testing"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// Settings that cannot be read are an error, never the defaults: a change built on the
// defaults and saved would switch selling off and forget the providers.
func TestUnreadablePaymentSettingsAreNotOverwritten(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	const broken = `{"enabled":true,"stars":tru`
	if err := h.st.Q.SetSetting(ctx, db.SetSettingParams{Key: billing.KeyConfig, Value: broken}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1/payments/settings"
	if resp, _ := h.do(http.MethodGet, api, nil, nil); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("read: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodPatch, api, map[string]any{"allow_new": false}, map[string]string{"X-CSRF-Token": h.csrf}); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("write: %d", resp.StatusCode)
	}
	if raw, err := h.st.Q.GetSetting(ctx, billing.KeyConfig); err != nil || raw != broken {
		t.Fatalf("overwritten: %q %v", raw, err)
	}
}

// A Telegram PATCH is checked whole before anything is saved: a bad token leaves the new
// route unsaved.
func TestTelegramPatchIsAllOrNothing(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1/telegram"
	resp, body := h.do(http.MethodPatch, api, map[string]any{"route": map[string]any{"mode": "proxy", "proxy": "http://127.0.0.1:1"}, "token": "not-a-token"},
		map[string]string{"X-CSRF-Token": h.csrf})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "tg_token_format") {
		t.Fatalf("bad token: %d %s", resp.StatusCode, body)
	}
	if raw, _ := h.st.Q.GetSetting(t.Context(), tgate.KeyRoute); raw != "" {
		t.Fatalf("route saved despite the refusal: %s", raw)
	}
}
