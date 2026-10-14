package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// A pool is not deleted while it holds what users paid for: the cascade would take their
// grants (and the catalog's packages) with it, and a paid invoice of a package would lose
// the package. Each thing that blocks it is named, nothing is deleted, and once they are
// gone the pool goes.
func TestDeletePoolKeepsPaidTraffic(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1"
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	const gb = int64(1) << 30

	var pool struct {
		ID int64 `json:"id"`
	}
	if resp, body := h.do(http.MethodPost, api+"/pools", map[string]any{"name": "WL"}, csrf); resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &pool) != nil {
		t.Fatalf("create pool: %d %s", resp.StatusCode, body)
	}
	var pk struct {
		ID int64 `json:"id"`
	}
	resp, body := h.do(http.MethodPost, api+"/packages", map[string]any{"name": "+50 GB WL", "bytes": 50 * gb, "pool_id": pool.ID, "lifetime": "used", "price_stars": 75}, csrf)
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &pk) != nil {
		t.Fatalf("create package: %d %s", resp.StatusCode, body)
	}
	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	resp, body = h.do(http.MethodPost, api+"/users/"+id(u.ID)+"/grants", map[string]any{"pool_id": pool.ID, "bytes": 10 * gb, "lifetime": "used"}, csrf)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("grant: %d %s", resp.StatusCode, body)
	}
	pay, err := h.st.Q.CreatePackagePayment(ctx, db.CreatePackagePaymentParams{Provider: "stars", Payload: "p1", TgID: 7, UserID: sql.NullInt64{Int64: u.ID, Valid: true},
		PackageID: sql.NullInt64{Int64: pk.ID, Valid: true}, TariffName: "+50 GB WL", Amount: 75, Currency: "XTR", CreatedAt: h.now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	remove := func() (*http.Response, []byte) { return h.do(http.MethodDelete, api+"/pools/"+id(pool.ID), nil, csrf) }
	refused := func(step string, want ...string) {
		t.Helper()
		resp, body := remove()
		if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "pool_in_use") {
			t.Fatalf("%s: %d %s, want 409 pool_in_use", step, resp.StatusCode, body)
		}
		for _, w := range []string{"pool_has_grants", "pool_has_packages", "pool_has_payments"} {
			if has := strings.Contains(string(body), w); has != slices.Contains(want, w) {
				t.Fatalf("%s: %s present=%v in %s", step, w, has, body)
			}
		}
		if _, err := h.st.Q.GetTrafficPool(ctx, pool.ID); err != nil {
			t.Fatalf("%s: the pool is gone: %v", step, err)
		}
		if gs, _ := h.st.Q.ListUserGrants(ctx, u.ID); len(gs) != 1 || gs[0].Remaining != 10*gb {
			t.Fatalf("%s: the user's grant changed: %+v", step, gs)
		}
	}

	refused("all three", "pool_has_grants", "pool_has_packages", "pool_has_payments")
	// The catalog is cleaned up; the grant and the open invoice still hold the pool.
	if resp, _ := h.do(http.MethodDelete, api+"/packages/"+id(pk.ID), nil, csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("archive package: %d", resp.StatusCode)
	}
	refused("package archived", "pool_has_grants", "pool_has_payments")
	// A paid invoice that is not applied yet is still money the customer is owed.
	setStatus := func(status string) {
		t.Helper()
		if _, err := h.st.DB.ExecContext(ctx, "UPDATE payments SET status = $1 WHERE id = $2", status, pay.ID); err != nil {
			t.Fatal(err)
		}
	}
	setStatus("paid")
	refused("invoice paid", "pool_has_grants", "pool_has_payments")
	setStatus("applied")
	refused("invoice applied", "pool_has_grants")
	// An expired grant is worth nothing and does not hold the pool; a used-up one neither.
	if _, err := h.st.DB.ExecContext(ctx, "UPDATE traffic_grants SET remaining = 0 WHERE user_id = $1", u.ID); err != nil {
		t.Fatal(err)
	}
	if resp, body := remove(); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("nothing left in the pool: %d %s", resp.StatusCode, body)
	}
	if resp, _ := remove(); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleting it again: %d", resp.StatusCode)
	}
}
