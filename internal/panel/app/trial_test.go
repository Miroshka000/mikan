package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"mikan/internal/panel/domain"
)

// The trial's tariff in the payment settings: an unknown or archived tariff is refused,
// one picked comes back, 0 turns the trial off.
func TestTrialSetting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	path := "/" + adminPath + "/api/v1/payments/settings"
	ts, _ := h.st.Q.ListTariffs(ctx)
	var v struct {
		TrialTariffID *int64 `json:"trial_tariff_id"`
		Trials        int64  `json:"trials"`
	}
	read := func(body []byte) {
		t.Helper()
		v.TrialTariffID = nil
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := h.do(http.MethodGet, path, nil, nil)
	read(body)
	if resp.StatusCode != http.StatusOK || v.TrialTariffID != nil || v.Trials != 0 {
		t.Fatalf("off by default: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, path, map[string]any{"trial_tariff_id": 99999}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "tariff_not_found") {
		t.Fatalf("an unknown tariff: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPatch, path, map[string]any{"trial_tariff_id": ts[0].ID}, csrf)
	read(body)
	if resp.StatusCode != http.StatusOK || v.TrialTariffID == nil || *v.TrialTariffID != ts[0].ID {
		t.Fatalf("set: %d %s", resp.StatusCode, body)
	}
	// Another setting changed alone leaves the trial as it is.
	resp, body = h.do(http.MethodPatch, path, map[string]any{"allow_new": false}, csrf)
	read(body)
	if resp.StatusCode != http.StatusOK || v.TrialTariffID == nil {
		t.Fatalf("the trial after another change: %d %s", resp.StatusCode, body)
	}
	if _, err := h.st.Q.ArchiveTariff(ctx, ts[1].ID); err != nil {
		t.Fatal(err)
	}
	if resp, body := h.do(http.MethodPatch, path, map[string]any{"trial_tariff_id": ts[1].ID}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an archived tariff: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPatch, path, map[string]any{"trial_tariff_id": 0}, csrf)
	read(body)
	if resp.StatusCode != http.StatusOK || v.TrialTariffID != nil {
		t.Fatalf("off: %d %s", resp.StatusCode, body)
	}
	// An API key cannot change it: the payment settings are for a session only.
	if resp, _ := h.do(http.MethodPatch, path, map[string]any{"trial_tariff_id": ts[0].ID}, map[string]string{"Authorization": "Bearer mk_not_a_session"}); resp.StatusCode == http.StatusOK {
		t.Fatal("changed without a session")
	}
}
