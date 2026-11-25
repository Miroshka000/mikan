package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/support"
)

// lifecycleEnv is the handlers over a seeded store, its clock at *now.
func lifecycleEnv(t *testing.T, now *time.Time) (*handlers, *store.Store, *domain.Users) {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := domain.Seed(ctx, st, *now); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return *now }
	users := domain.NewUsers(st, domain.NewPool(st, clock), noChanges{}, clock)
	h := &handlers{d: Deps{Store: st, Settings: settings.New(st.Q), Users: users, Now: clock, Changes: noChanges{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	return h, st, users
}

// refusalOf is the status of err with its first detail's location and code; with no detail,
// its own code.
func refusalOf(err error) (status int, location, code string) {
	var se huma.StatusError
	if !errors.As(err, &se) {
		return 0, "", ""
	}
	var em *huma.ErrorModel
	if errors.As(err, &em) && len(em.Errors) > 0 {
		return se.GetStatus(), em.Errors[0].Location, em.Errors[0].Message
	}
	return se.GetStatus(), "", se.Error()
}

// The carry-over is anyone's with the right to change the panel; whether subscribers are
// removed by a standing rule only the admin's own session decides; rules out of bounds are
// refused before anything is written.
func TestLifecycleRules(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h, _, _ := lifecycleEnv(t, &now)
	ctx := context.Background()
	key := context.WithValue(ctx, keyAPIKey, db.ApiKey{Scope: "full", Prefix: "mk_test"})
	operator := context.WithValue(ctx, keySupport, supportCtx{grant: db.SupportGrant{Level: support.LevelChange}})

	carry := domain.DefaultLifecycle
	carry.Carry = domain.CarryRules{On: true, Cap: 50}
	if out, err := h.updateLifecycle(key, &lifecycleInput{Body: carry}); err != nil || out.Body != carry {
		t.Fatalf("the carry-over by a key: %+v %v", out, err)
	}
	removal := carry
	removal.Removal = domain.RemovalRules{On: true, Days: 30}
	for who, c := range map[string]struct {
		ctx  context.Context
		code string
	}{"a key": {key, "session_only"}, "an operator": {operator, "support_extended"}} {
		if _, err := h.updateLifecycle(c.ctx, &lifecycleInput{Body: removal}); err == nil {
			t.Errorf("the removal by %s went through", who)
		} else if status, _, code := refusalOf(err); status != http.StatusForbidden || code != c.code {
			t.Errorf("the removal by %s: %d %s, want 403 %s", who, status, code, c.code)
		}
	}
	if got, err := h.getLifecycle(ctx, nil); err != nil || got.Body != carry {
		t.Fatalf("after the refusals: %+v %v", got, err)
	}
	if out, err := h.updateLifecycle(ctx, &lifecycleInput{Body: removal}); err != nil || out.Body != removal {
		t.Fatalf("the removal by the session: %+v %v", out, err)
	}
	// What a key may still do with the removal on: the carry-over, the removal as it is.
	keep := removal
	keep.Carry.Cap = 80
	if _, err := h.updateLifecycle(key, &lifecycleInput{Body: keep}); err != nil {
		t.Fatalf("the carry-over by a key, the removal kept: %v", err)
	}

	for _, c := range []struct {
		l             domain.Lifecycle
		location, msg string
	}{
		{domain.Lifecycle{Carry: domain.CarryRules{On: true, Cap: 0}, Removal: removal.Removal}, "body.carry.cap_percent", "bad_carry_cap"},
		{domain.Lifecycle{Carry: carry.Carry, Removal: domain.RemovalRules{On: true, Days: 3}}, "body.removal.days", "bad_removal_days"},
	} {
		_, err := h.updateLifecycle(ctx, &lifecycleInput{Body: c.l})
		if status, location, code := refusalOf(err); status != http.StatusUnprocessableEntity || location != c.location || code != c.msg {
			t.Errorf("%+v: %d %s %s, want 422 %s at %s", c.l, status, location, code, c.msg, c.location)
		}
	}
}

// The dry run lists whom the rule reaches, with the saved days or others the admin tries,
// due and kept apart, and removes nobody.
func TestInactiveDryRun(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h, st, users := lifecycleEnv(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	var ids []int64
	for _, name := range []string{"due", "kept"} {
		u, err := users.Create(ctx, domain.CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB.ExecContext(ctx, "UPDATE users SET expires_at = $1 WHERE id = $2", now.Add(-40*24*time.Hour).Unix(), u.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	kept, _ := users.Get(ctx, ids[1])
	if _, err := domain.GrantTx(ctx, st.Q, kept, domain.GrantSpec{Bytes: domain.GiB, Lifetime: domain.LifetimeUsed, Source: domain.SourceAdmin}, now); err != nil {
		t.Fatal(err)
	}
	out, err := h.listInactive(ctx, &inactiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	b := out.Body
	if b.On || b.Days != domain.DefaultLifecycle.Removal.Days || b.Due != 1 || b.Kept != 1 || len(b.Items) != 2 {
		t.Fatalf("the dry run: %+v", b)
	}
	for _, it := range b.Items {
		if want := map[int64]string{ids[0]: "", ids[1]: domain.KeepGrant}[it.UserID]; it.Keep != want || it.UserName == "" || it.OnlineAt != nil {
			t.Errorf("%+v: kept for %q, want %q", it, it.Keep, want)
		}
	}
	if out, err = h.listInactive(ctx, &inactiveInput{Days: 60}); err != nil || out.Body.Days != 60 || out.Body.Due+out.Body.Kept != 0 {
		t.Fatalf("with 60 days: %+v %v", out.Body, err)
	}
	if _, err := h.listInactive(ctx, &inactiveInput{Days: 3}); err == nil {
		t.Fatal("a dry run of 3 days")
	} else if status, location, code := refusalOf(err); status != http.StatusUnprocessableEntity || location != "query.days" || code != "bad_removal_days" {
		t.Fatalf("a dry run of 3 days: %d %s %s", status, location, code)
	}
	for _, id := range ids {
		if _, err := users.Get(ctx, id); err != nil {
			t.Fatalf("the dry run removed %d", id)
		}
	}
}

// The preview of a bulk action counts what it would do and shows the first users it
// changes, with the new end of an extension; a deletion says what the users hold; a folder
// that is not there is refused, as the action refuses it. Nothing changes.
func TestBulkPreview(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h, st, users := lifecycleEnv(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	var ids []int64
	for _, name := range []string{"a", "b", "c"} {
		u, err := users.Create(ctx, domain.CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	off := true
	if _, err := users.Update(ctx, ids[1], domain.Patch{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	preview := func(action string, days, folder int64) bulkPreviewOutput {
		t.Helper()
		in := &bulkInput{}
		in.Body.IDs, in.Body.Action, in.Body.Days, in.Body.Folder = append(append([]int64{}, ids...), 9999), action, days, folder
		out, err := h.bulkPreview(ctx, in)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		return *out
	}
	p := preview(domain.BulkDisable, 0, 0).Body
	if p.Requested != 4 || p.Found != 3 || p.Missing != 1 || p.Changed != 2 || p.Unchanged != 1 || len(p.Sample) != 2 || p.AtStake != nil {
		t.Fatalf("disable: %+v", p)
	}
	if s := p.Sample[0]; s.UserID != ids[0] || s.UserName != "a" || s.State != domain.StateActive || s.ExpiresAfter != nil {
		t.Fatalf("disable's first: %+v", s)
	}
	p = preview(domain.BulkExtend, 10, 0).Body
	if p.Changed != 3 || len(p.Sample) != 3 || !p.Sample[1].ExpiresAfter.Equal(p.Sample[1].ExpiresAt.Add(10*24*time.Hour)) {
		t.Fatalf("extend: %+v", p)
	}
	p = preview(domain.BulkDelete, 0, 0).Body
	if p.AtStake == nil || *p.AtStake != (BulkAtStake{Active: 2}) {
		t.Fatalf("delete: %+v", p.AtStake)
	}
	in := &bulkInput{}
	in.Body.IDs, in.Body.Action, in.Body.Folder = ids, domain.BulkMove, 9999
	if _, err := h.bulkPreview(ctx, in); err == nil {
		t.Fatal("a move into a folder that is not there")
	} else if status, location, code := refusalOf(err); status != http.StatusUnprocessableEntity || location != "body.folder_id" || code != "folder_not_found" {
		t.Fatalf("a move into a folder that is not there: %d %s %s", status, location, code)
	}
	for _, id := range ids {
		if _, err := users.Get(ctx, id); err != nil {
			t.Fatalf("the preview deleted %d", id)
		}
	}
	if u, _ := users.Get(ctx, ids[0]); u.Status != "active" {
		t.Fatal("the preview switched a user off")
	}
}

// The user's card shows the next tariff and the wait for the first connection, and edits
// them: what is refused is refused at its field.
func TestUserLifecycleThroughTheAPI(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h, st, _ := lifecycleEnv(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	yes, no := true, false
	in := &createUserInput{}
	in.Body.Name, in.Body.TariffID, in.Body.StartsOnConnect = "a", tariffs[1].ID, &yes
	created, err := h.createUser(ctx, in)
	if err != nil || !created.Body.StartsOnConnect || created.Body.NextTariff != nil {
		t.Fatalf("created: %+v %v", created, err)
	}
	id := created.Body.ID
	patch := &patchUserInput{ID: id}
	patch.Body.StartsOnConnect = &no
	if out, err := h.updateUser(ctx, patch); err != nil || out.Body.StartsOnConnect {
		t.Fatalf("started: %+v %v", out, err)
	}

	next := func(user, tariff int64, days *int64) (*nextTariffOutput, error) {
		in := &nextTariffInput{ID: user}
		in.Body.TariffID, in.Body.TermDays = tariff, days
		return h.setNextTariff(ctx, in)
	}
	if _, err := next(9999, tariffs[2].ID, nil); err == nil {
		t.Fatal("an unknown user got a next tariff")
	} else if status, _, _ := refusalOf(err); status != http.StatusNotFound {
		t.Fatalf("an unknown user: %v", err)
	}
	if _, err := next(id, 9999, nil); err == nil {
		t.Fatal("an unknown tariff was set")
	} else if status, location, code := refusalOf(err); status != http.StatusUnprocessableEntity || location != "body.tariff_id" || code != "tariff_not_found" {
		t.Fatalf("an unknown tariff: %d %s %s", status, location, code)
	}
	week := int64(7)
	out, err := next(id, tariffs[2].ID, &week)
	if err != nil || out.Body.Switched {
		t.Fatalf("set: %+v %v", out, err)
	}
	if n := out.Body.User.NextTariff; n == nil || n.TariffID != tariffs[2].ID || n.TermDays == nil || *n.TermDays != week {
		t.Fatalf("the next tariff in the answer: %+v", n)
	}
	if got, err := h.getUser(ctx, &userIDInput{ID: id}); err != nil || got.Body.NextTariff == nil || got.Body.NextTariff.TariffID != tariffs[2].ID {
		t.Fatalf("the card: %+v %v", got, err)
	}
	if _, err := h.clearNextTariff(ctx, &userIDInput{ID: id}); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.getUser(ctx, &userIDInput{ID: id}); got.Body.NextTariff != nil {
		t.Fatalf("cleared: %+v", got.Body.NextTariff)
	}
	if _, err := h.clearNextTariff(ctx, &userIDInput{ID: 9999}); err == nil {
		t.Fatal("cleared an unknown user's")
	}

	// A subscription that connected may not wait any more.
	if err := st.TxRC(ctx, func(q *db.Queries) error {
		return domain.CountTraffic(ctx, q, domain.TrafficBatch{Main: map[int64]domain.Bytes{id: {Down: 1}}}, now)
	}); err != nil {
		t.Fatal(err)
	}
	patch.Body.StartsOnConnect = &yes
	if _, err := h.updateUser(ctx, patch); err == nil {
		t.Fatal("a subscription that connected waits again")
	} else if status, location, code := refusalOf(err); status != http.StatusUnprocessableEntity || location != "body.starts_on_connect" || code != "already_connected" {
		t.Fatalf("waits after connecting: %d %s %s", status, location, code)
	}
}

// A tariff's own part of the lifecycle is written with it and kept by a body that leaves it
// out.
func TestTariffLifecycleInTheCatalog(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h, _, _ := lifecycleEnv(t, &now)
	ctx := context.Background()
	yes, no := true, false
	body := tariffBody{Name: "waits", DurationDays: 30, ResetStrategy: "none", CarryOver: carryOn, StartsOnConnect: &yes}
	created, err := h.createTariff(ctx, &tariffInput{Body: body})
	if err != nil || created.Body.CarryOver != carryOn || !created.Body.StartsOnConnect {
		t.Fatalf("created: %+v %v", created, err)
	}
	id := created.Body.ID
	body.CarryOver, body.StartsOnConnect = "", nil
	if out, err := h.updateTariff(ctx, &tariffUpdateInput{ID: id, Body: body}); err != nil || out.Body.CarryOver != carryOn || !out.Body.StartsOnConnect {
		t.Fatalf("left out: %+v %v", out, err)
	}
	body.CarryOver, body.StartsOnConnect = carryInherit, &no
	if _, err := h.updateTariff(ctx, &tariffUpdateInput{ID: id, Body: body}); err != nil {
		t.Fatal(err)
	}
	list, err := h.listTariffs(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range list.Body {
		if v.CarryOver != carryInherit || v.StartsOnConnect {
			t.Errorf("tariff %s: carry %s, waits %v", v.Name, v.CarryOver, v.StartsOnConnect)
		}
	}
}
