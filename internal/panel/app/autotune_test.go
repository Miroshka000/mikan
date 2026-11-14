package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// A client's subscription fetch is recorded (the block detector trusts only up-to-date
// profiles) and asks for hourly updates; the per-inbound switches change nothing clients
// get, so they leave updated_at alone; the global switches live in settings.
func TestAutoSwitchesAndSubscriptionFetch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	clock := func() time.Time { return h.now }
	tariffs, err := h.st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}

	resp, _ := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "mihomo/1.19.31"})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Profile-Update-Interval") != "1" {
		t.Fatalf("subscription: %d, interval %q", resp.StatusCode, resp.Header.Get("Profile-Update-Interval"))
	}
	fetches, err := h.st.Q.ListSubFetchesSince(ctx, 0)
	if err != nil || len(fetches) != 1 || fetches[0].UserID != u.ID || fetches[0].Ip != "127.0.0.1" || fetches[0].FetchedAt != h.now.Unix() {
		t.Fatalf("the fetch is recorded per device: %+v (%v)", fetches, err)
	}

	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	inbounds, err := h.st.Q.ListInbounds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	x := inbounds[0]
	h.advance(time.Hour)
	resp, body := h.do(http.MethodPatch, "/"+adminPath+"/api/v1/inbounds/"+strconv.FormatInt(x.ID, 10), map[string]any{"auto_port": false}, csrf)
	var view struct {
		AutoPort bool           `json:"auto_port"`
		AutoSNI  bool           `json:"auto_sni"`
		Auto     map[string]any `json:"auto"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &view) != nil || view.AutoPort || !view.AutoSNI || view.Auto == nil {
		t.Fatalf("patch: %d %s", resp.StatusCode, body)
	}
	after, err := h.st.Q.GetInbound(ctx, x.ID)
	if err != nil || after.AutoPort != 0 || after.AutoSni != 1 || after.UpdatedAt != x.UpdatedAt {
		t.Fatalf("stored: auto %d/%d, updated_at %d (was %d)", after.AutoPort, after.AutoSni, after.UpdatedAt, x.UpdatedAt)
	}

	resp, body = h.do(http.MethodPatch, "/"+adminPath+"/api/v1/settings", map[string]any{"auto_sni": false}, csrf)
	var s struct {
		AutoPort bool `json:"auto_port"`
		AutoSNI  bool `json:"auto_sni"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &s) != nil || !s.AutoPort || s.AutoSNI {
		t.Fatalf("settings: %d %s", resp.StatusCode, body)
	}
}
