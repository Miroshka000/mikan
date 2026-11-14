package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/support"
)

// A bulk action by filter is held to what the list is held to: an operator who may not see
// the subscribers does not search them by it (the count would tell), while a filter without
// a search is theirs as the list is; an operator who only views previews and does nothing,
// by filter as by ids.
func TestBulkByFilterScopes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h, st, users := lifecycleEnv(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	for _, name := range []string{"anna", "boris"} {
		if _, err := users.Create(ctx, domain.CreateInput{Name: name, TariffID: tariffs[1].ID}); err != nil {
			t.Fatal(err)
		}
	}
	blind := context.WithValue(ctx, keySupport, supportCtx{grant: db.SupportGrant{Level: support.LevelChange}})
	seeing := context.WithValue(ctx, keySupport, supportCtx{grant: db.SupportGrant{Level: support.LevelChange, ShowPeople: true}})
	in := func(f BulkFilter) *bulkInput {
		b := &bulkInput{}
		b.Body.Filter, b.Body.Action = &f, domain.BulkDisable
		return b
	}

	if _, err := h.bulkPreview(blind, in(BulkFilter{Query: "anna"})); err == nil {
		t.Fatal("an operator without the subscribers searched them by a preview")
	} else if status, _, code := refusalOf(err); status != http.StatusForbidden || code != "support_denied" {
		t.Fatalf("the preview's search: %d %s", status, code)
	}
	act := in(BulkFilter{Query: "anna"})
	act.Body.Expect = bulkDigest([]int64{1})
	if _, err := h.bulkUsers(blind, act); err == nil {
		t.Fatal("an operator without the subscribers acted on a search")
	} else if status, _, code := refusalOf(err); status != http.StatusForbidden || code != "support_denied" {
		t.Fatalf("the action's search: %d %s", status, code)
	}
	// Blanks are no search, as in the list.
	if out, err := h.bulkPreview(blind, in(BulkFilter{Query: "   "})); err != nil || out.Body.Found != 2 {
		t.Fatalf("an operator's preview without a search: %+v %v", out, err)
	}
	if out, err := h.bulkPreview(seeing, in(BulkFilter{Query: "anna"})); err != nil || out.Body.Found != 1 {
		t.Fatalf("an operator who sees the subscribers: %+v %v", out, err)
	}
	if u, _ := st.Q.ListUsers(ctx); len(u) != 2 || u[0].Status != "active" || u[1].Status != "active" {
		t.Fatalf("a refused action changed users: %+v", u)
	}

	// The operations stay what the gate already holds them to: writes, which a view grant
	// does not reach.
	view := db.SupportGrant{Level: support.LevelView, ShowPeople: true}
	for _, id := range []string{"bulk-users", "bulk-users-preview"} {
		if status, code := supportGate(&huma.Operation{OperationID: id, Tags: []string{"users"}}, true, view); status != http.StatusForbidden || code != "support_read_only" {
			t.Errorf("%s at view: %d %s", id, status, code)
		}
	}
}

// A filter that takes more users than one action may reach is refused, in the preview as in
// the action, before anything is done; at the limit it goes through.
func TestBulkByFilterLimit(t *testing.T) {
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
	limit := maxBulkFilterUsers
	t.Cleanup(func() { maxBulkFilterUsers = limit })
	maxBulkFilterUsers = 2
	in := &bulkInput{}
	in.Body.Filter, in.Body.Action, in.Body.Expect = &BulkFilter{}, domain.BulkDisable, bulkDigest(ids)
	if _, err := h.bulkPreview(ctx, in); err == nil {
		t.Fatal("a preview over the limit")
	} else if status, location, code := refusalOf(err); status != http.StatusUnprocessableEntity || location != "body.filter" || code != "too_many_users" {
		t.Fatalf("a preview over the limit: %d %s %s", status, location, code)
	}
	if _, err := h.bulkUsers(ctx, in); err == nil {
		t.Fatal("an action over the limit")
	} else if _, _, code := refusalOf(err); code != "too_many_users" {
		t.Fatalf("an action over the limit: %s", code)
	}
	if u, _ := st.Q.ListUsers(ctx); u[0].Status != "active" || u[1].Status != "active" || u[2].Status != "active" {
		t.Fatalf("a refused action changed users: %+v", u)
	}
	maxBulkFilterUsers = 3
	if out, err := h.bulkUsers(ctx, in); err != nil || out.Body.Affected != 3 {
		t.Fatalf("an action at the limit: %+v %v", out, err)
	}
}

// The digest is the documented one: the SHA-256 (hex) of the ids in ascending order, joined
// by commas, whatever order they come in.
func TestBulkDigest(t *testing.T) {
	sum := sha256.Sum256([]byte("1,2,3"))
	if got, want := bulkDigest([]int64{3, 1, 2}), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("bulkDigest(3, 1, 2) = %s, want %s", got, want)
	}
}
