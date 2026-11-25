package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// The rules of the lifecycle over the wire: the session sets all of them, a full key only the
// carry-over (what deletes subscriptions is the admin's own call), a read key looks and no
// more, and without credentials nothing answers. A refused change leaves the rules as they
// were.
func TestLifecycleRulesOverHTTP(t *testing.T) {
	k := newKeyHarness(t)
	rules := func(carry int, removalOn bool, days int) map[string]any {
		return map[string]any{"carry": map[string]any{"on": true, "cap_percent": carry}, "removal": map[string]any{"on": removalOn, "days": days}}
	}
	stored := func() domain.Lifecycle {
		t.Helper()
		resp, out := k.do(http.MethodGet, k.api+"/lifecycle", nil, nil)
		var l domain.Lifecycle
		if resp.StatusCode != http.StatusOK || json.Unmarshal(out, &l) != nil {
			t.Fatalf("read the rules: %d %s", resp.StatusCode, out)
		}
		return l
	}
	if l := stored(); l != domain.DefaultLifecycle {
		t.Fatalf("before the admin sets any: %+v", l)
	}
	if resp, out := k.do(http.MethodPut, k.api+"/lifecycle", rules(50, true, 45), k.csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("the session sets the rules: %d %s", resp.StatusCode, out)
	}
	want := domain.Lifecycle{Carry: domain.CarryRules{On: true, Cap: 50}, Removal: domain.RemovalRules{On: true, Days: 45}}
	if l := stored(); l != want {
		t.Fatalf("after the session: %+v, want %+v", l, want)
	}

	// A key tunes the carry-over with the removal as it is, and nothing of the removal.
	if resp, out := k.asKey(k.full, http.MethodPut, "/lifecycle", rules(80, true, 45)); resp.StatusCode != http.StatusOK {
		t.Fatalf("a full key sets the carry-over: %d %s", resp.StatusCode, out)
	}
	want.Carry.Cap = 80
	for name, body := range map[string]map[string]any{"turned off": rules(80, false, 45), "made to reach further": rules(80, true, 14), "made to wait": rules(80, true, 90)} {
		resp, out := k.asKey(k.full, http.MethodPut, "/lifecycle", body)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(out), "session_only") || !strings.Contains(string(out), "removal") {
			t.Errorf("the removal %s by a full key: %d %s, want 403 session_only at removal", name, resp.StatusCode, out)
		}
	}
	if l := stored(); l != want {
		t.Fatalf("after the refusals: %+v, want %+v", l, want)
	}

	// A read key looks at the rules and at whom they reach, and changes nothing.
	for _, path := range []string{"/lifecycle", "/lifecycle/inactive"} {
		if resp, out := k.asKey(k.read, http.MethodGet, path, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("a read key reads %s: %d %s", path, resp.StatusCode, out)
		}
	}
	if resp, out := k.asKey(k.read, http.MethodPut, "/lifecycle", rules(10, true, 45)); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a read key sets the rules: %d %s", resp.StatusCode, out)
	}
	// Nobody at the door.
	for method, path := range map[string]string{http.MethodGet: "/lifecycle", http.MethodPut: "/lifecycle"} {
		if resp, out := k.asKey("", method, path, rules(10, false, 45)); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without credentials: %d %s", method, path, resp.StatusCode, out)
		}
	}

	// What is out of range is refused, whoever asks.
	for name, body := range map[string]map[string]any{"no cap": rules(0, false, 45), "a cap over the quota": rules(101, false, 45), "a week short of a week": rules(50, false, 6)} {
		if resp, out := k.do(http.MethodPut, k.api+"/lifecycle", body, k.csrf); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s, want 422", name, resp.StatusCode, out)
		}
	}
	if resp, out := k.do(http.MethodGet, k.api+"/lifecycle/inactive?days=3", nil, nil); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(out), "bad_removal_days") {
		t.Errorf("a dry run with 3 days: %d %s, want 422 bad_removal_days", resp.StatusCode, out)
	}
	if l := stored(); l != want {
		t.Fatalf("after the bad requests: %+v, want %+v", l, want)
	}
}

// The preview of a bulk action says what it would do and does none of it; the action then
// does what the preview said. Both are the writes' side of the API: a read key has neither.
func TestBulkPreviewOverHTTP(t *testing.T) {
	k := newKeyHarness(t)
	ctx := context.Background()
	tariffs, _ := k.st.Q.ListTariffs(ctx)
	var ids []int64
	for _, name := range []string{"a", "b", "c"} {
		u, err := k.p.users.Create(ctx, domain.CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	if _, err := k.p.users.Update(ctx, ids[2], domain.Patch{Disabled: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	type preview struct {
		Requested, Found, Missing, Changed, Unchanged int
		Sample                                        []struct {
			UserID int64 `json:"user_id"`
		}
		AtStake *struct {
			Active int `json:"active"`
		} `json:"at_stake"`
	}
	ask := func(path string, body map[string]any) preview {
		t.Helper()
		resp, out := k.asKey(k.full, http.MethodPost, path, body)
		var p preview
		if resp.StatusCode != http.StatusOK || json.Unmarshal(out, &p) != nil {
			t.Fatalf("%s %v: %d %s", path, body, resp.StatusCode, out)
		}
		return p
	}

	// Two of the three are on: switching off changes them, and the one already off stays; one
	// user asked for is gone.
	p := ask("/users/bulk/preview", map[string]any{"ids": append(ids, 9999), "action": "disable"})
	if p.Requested != 4 || p.Found != 3 || p.Missing != 1 || p.Changed != 2 || p.Unchanged != 1 || len(p.Sample) != 2 || p.AtStake != nil {
		t.Fatalf("the preview of switching off: %+v", p)
	}
	for _, id := range ids[:2] {
		if u, err := k.p.users.Get(ctx, id); err != nil || u.Status != "active" {
			t.Fatalf("the preview changed user %d: %+v %v", id, u, err)
		}
	}
	// A deletion says what goes with the users.
	if p := ask("/users/bulk/preview", map[string]any{"ids": ids, "action": "delete"}); p.Changed != 3 || p.AtStake == nil || p.AtStake.Active != 2 {
		t.Fatalf("the preview of a deletion: %+v", p)
	}
	if _, err := k.p.users.Get(ctx, ids[0]); err != nil {
		t.Fatalf("the preview of a deletion deleted: %v", err)
	}
	// The action does what the preview said.
	if resp, out := k.asKey(k.full, http.MethodPost, "/users/bulk", map[string]any{"ids": ids, "action": "disable"}); resp.StatusCode != http.StatusOK || !strings.Contains(string(out), `"affected":3`) {
		t.Fatalf("switching off: %d %s", resp.StatusCode, out)
	}
	if p := ask("/users/bulk/preview", map[string]any{"ids": ids, "action": "disable"}); p.Changed != 0 || p.Unchanged != 3 {
		t.Fatalf("the preview after it: %+v", p)
	}

	// An action the bulk does not know, no users and too many, and a folder that is not there.
	for name, body := range map[string]map[string]any{
		"an unknown action": {"ids": ids, "action": "explode"},
		"no users":          {"ids": []int64{}, "action": "disable"},
		"a missing folder":  {"ids": ids, "action": "move", "folder_id": 9999},
		"a longer term":     {"ids": ids, "action": "extend", "days": 4000},
	} {
		if resp, out := k.asKey(k.full, http.MethodPost, "/users/bulk/preview", body); resp.StatusCode < 400 || resp.StatusCode >= 500 {
			t.Errorf("%s: %d %s, want a refusal", name, resp.StatusCode, out)
		}
	}
	if resp, out := k.asKey(k.read, http.MethodPost, "/users/bulk/preview", map[string]any{"ids": ids, "action": "disable"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a read key previews: %d %s", resp.StatusCode, out)
	}
	if resp, out := k.asKey("", http.MethodPost, "/users/bulk/preview", map[string]any{"ids": ids, "action": "disable"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a preview without credentials: %d %s", resp.StatusCode, out)
	}
}

// A subscription's next tariff over the wire: set, shown in the user, changed and taken away;
// refused for a plan that is not there and for a subscription without an end; a read key
// sees it in the user and cannot set it.
func TestNextTariffOverHTTP(t *testing.T) {
	k := newKeyHarness(t)
	ctx := context.Background()
	tariffs, _ := k.st.Q.ListTariffs(ctx)
	u, err := k.p.users.Create(ctx, domain.CreateInput{Name: "next", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	endless, err := k.p.users.Create(ctx, domain.CreateInput{Name: "endless", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.st.DB.ExecContext(ctx, "UPDATE users SET expires_at = NULL WHERE id = $1", endless.ID); err != nil {
		t.Fatal(err)
	}
	path := func(id int64) string { return "/users/" + strconv.FormatInt(id, 10) + "/next-tariff" }
	type reply struct {
		Switched bool `json:"switched"`
		User     struct {
			NextTariff *struct {
				TariffID int64  `json:"tariff_id"`
				TermDays *int64 `json:"term_days"`
			} `json:"next_tariff"`
		} `json:"user"`
	}
	set := func(id int64, body map[string]any) (int, reply, string) {
		t.Helper()
		resp, out := k.asKey(k.full, http.MethodPut, path(id), body)
		var r reply
		_ = json.Unmarshal(out, &r)
		return resp.StatusCode, r, string(out)
	}
	if status, r, out := set(u.ID, map[string]any{"tariff_id": tariffs[2].ID, "term_days": 90}); status != http.StatusOK || r.Switched || r.User.NextTariff == nil ||
		r.User.NextTariff.TariffID != tariffs[2].ID || r.User.NextTariff.TermDays == nil || *r.User.NextTariff.TermDays != 90 {
		t.Fatalf("set: %d %s", status, out)
	}
	// Another one in its place, on the tariff's own first term.
	if status, r, out := set(u.ID, map[string]any{"tariff_id": tariffs[0].ID}); status != http.StatusOK || r.User.NextTariff == nil || r.User.NextTariff.TariffID != tariffs[0].ID || r.User.NextTariff.TermDays != nil {
		t.Fatalf("changed: %d %s", status, out)
	}
	for name, c := range map[string]struct {
		id   int64
		body map[string]any
		code string
	}{
		"a plan that is not there": {u.ID, map[string]any{"tariff_id": 9999}, "tariff_not_found"},
		"no end to follow":         {endless.ID, map[string]any{"tariff_id": tariffs[2].ID}, "no_term_end"},
		"a term too long":          {u.ID, map[string]any{"tariff_id": tariffs[2].ID, "term_days": 4000}, ""},
	} {
		status, _, out := set(c.id, c.body)
		if status != http.StatusUnprocessableEntity || !strings.Contains(out, c.code) {
			t.Errorf("%s: %d %s, want 422 %s", name, status, out, c.code)
		}
	}
	if status, _, out := set(9999, map[string]any{"tariff_id": tariffs[2].ID}); status != http.StatusNotFound {
		t.Errorf("a user that is not there: %d %s", status, out)
	}
	next := func() *db.UserNextTariff {
		t.Helper()
		n, err := k.st.Q.GetNextTariff(ctx, u.ID)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return &n
	}
	if n := next(); n == nil || n.TariffID != tariffs[0].ID {
		t.Fatalf("the refusals changed the next tariff: %+v", n)
	}

	// A read key sees it in the user, and neither sets it nor takes it away.
	resp, out := k.asKey(k.read, http.MethodGet, "/users/"+strconv.FormatInt(u.ID, 10), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(out), `"next_tariff":{"tariff_id":`+strconv.FormatInt(tariffs[0].ID, 10)) {
		t.Fatalf("a read key reads the user: %d %s", resp.StatusCode, out)
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		if resp, out := k.asKey(k.read, method, path(u.ID), map[string]any{"tariff_id": tariffs[2].ID}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("a read key: %s %d %s", method, resp.StatusCode, out)
		}
	}
	if resp, out := k.asKey("", http.MethodPut, path(u.ID), map[string]any{"tariff_id": tariffs[2].ID}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without credentials: %d %s", resp.StatusCode, out)
	}
	if resp, out := k.asKey(k.full, http.MethodDelete, path(u.ID), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("taking it away: %d %s", resp.StatusCode, out)
	}
	if n := next(); n != nil {
		t.Fatalf("taken away, and still there: %+v", n)
	}
}

func boolPtr(v bool) *bool { return &v }
