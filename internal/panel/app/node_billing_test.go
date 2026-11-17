package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// A node's billing is saved whole and comes back with the next payment and the plan's traffic
// so far; a date that is no day of the calendar is refused under its field.
func TestNodeBillingRoundTrip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1/nodes/1"
	type billing struct {
		Provider     string `json:"provider"`
		PayDate      string `json:"pay_date"`
		PayMonths    int64  `json:"pay_months"`
		RemindDays   int64  `json:"remind_days"`
		TrafficLimit int64  `json:"traffic_limit"`
		ResetDay     int64  `json:"reset_day"`
		TrafficAlert int64  `json:"traffic_alert"`
	}
	var node struct {
		Billing     billing `json:"billing"`
		NextPayment string  `json:"next_payment"`
		VPSTraffic  *struct {
			Used   int64  `json:"used"`
			Since  string `json:"since"`
			Resets string `json:"resets"`
		} `json:"vps_traffic"`
	}
	// A new node: monthly, reminded 3 days before, the period from the 1st, told at 80%.
	resp, body := h.do(http.MethodGet, "/"+adminPath+"/api/v1/nodes", nil, nil)
	var nodes []json.RawMessage
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &nodes) != nil || len(nodes) == 0 || json.Unmarshal(nodes[0], &node) != nil {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	if want := (billing{PayMonths: 1, RemindDays: 3, ResetDay: 1, TrafficAlert: 80}); node.Billing != want || node.NextPayment != "" || node.VPSTraffic != nil {
		t.Fatalf("defaults: %+v", node)
	}

	today := h.now.UTC()
	if err := h.st.Q.AddNodeTraffic(ctx, db.AddNodeTrafficParams{NodeID: 1, Hour: today.Unix() / 3600, Day: domain.Day(today), Up: 300, Down: 700}); err != nil {
		t.Fatal(err)
	}
	// Paid a year ago on today's date, every 12 months: the next payment is today (on 29
	// February a year ago was 1 March, and the next one is tomorrow).
	sent := billing{Provider: " Hetzner ", PayDate: today.AddDate(-1, 0, 0).Format(time.DateOnly), PayMonths: 12, RemindDays: 7, TrafficLimit: 20 << 40, ResetDay: int64(today.Day()), TrafficAlert: 90}
	resp, body = h.do(http.MethodPatch, api, map[string]any{"billing": sent}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &node) != nil {
		t.Fatalf("patch: %d %s", resp.StatusCode, body)
	}
	sent.Provider = "Hetzner"
	next := today.Format(time.DateOnly)
	if today.Month() == time.February && today.Day() == 29 {
		next = today.AddDate(0, 0, 1).Format(time.DateOnly)
	}
	if node.Billing != sent || node.NextPayment != next {
		t.Fatalf("billing %+v, next %q", node.Billing, node.NextPayment)
	}
	if v := node.VPSTraffic; v == nil || v.Used != 1000 || v.Since != today.Format(time.DateOnly) || v.Resets <= v.Since {
		t.Fatalf("traffic: %+v", v)
	}
	// Other fields of the node keep the billing.
	resp, body = h.do(http.MethodPatch, api, map[string]any{"public_name": "NL"}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &node) != nil || node.Billing != sent {
		t.Fatalf("other patch: %d %s", resp.StatusCode, body)
	}
	// 31 February is refused; so is a reset day the API does not take.
	for _, bad := range []map[string]any{
		{"provider": "", "pay_date": "2026-02-31", "pay_months": 1, "remind_days": 3, "traffic_limit": 0, "reset_day": 1, "traffic_alert": 80},
		{"provider": "", "pay_date": "", "pay_months": 1, "remind_days": 3, "traffic_limit": 0, "reset_day": 32, "traffic_alert": 80},
	} {
		resp, body = h.do(http.MethodPatch, api, map[string]any{"billing": bad}, csrf)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%v: %d %s", bad, resp.StatusCode, body)
		}
	}
	// Cleared: no payment, no limit.
	resp, body = h.do(http.MethodPatch, api, map[string]any{"billing": billing{PayMonths: 1, RemindDays: 3, ResetDay: 1, TrafficAlert: 80}}, csrf)
	node.VPSTraffic, node.NextPayment = nil, ""
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &node) != nil || node.NextPayment != "" || node.VPSTraffic != nil || node.Billing.PayDate != "" {
		t.Fatalf("cleared: %d %s", resp.StatusCode, body)
	}
}
