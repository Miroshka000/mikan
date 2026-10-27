package api

import (
	"context"
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
	if _, err := h.bulkUsers(blind, in(BulkFilter{Query: "anna"})); err == nil {
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
