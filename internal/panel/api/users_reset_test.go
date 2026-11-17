package api

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// A client's own reset through the API: PATCH sets it and the next reset follows, a reset
// the panel does not know is refused at its field.
func TestUserResetThroughTheAPI(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h, st, _ := lifecycleEnv(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	in := &createUserInput{}
	in.Body.Name, in.Body.TariffID = "a", tariffs[2].ID
	created, err := h.createUser(ctx, in)
	if err != nil || created.Body.ResetStrategy != "none" || created.Body.ResetsAt != nil {
		t.Fatalf("created on the tariff without limits: %+v %v", created, err)
	}
	patch := &patchUserInput{ID: created.Body.ID}
	month := "month_start"
	patch.Body.ResetStrategy = &month
	out, err := h.updateUser(ctx, patch)
	if err != nil || out.Body.ResetStrategy != month || out.Body.ResetsAt == nil {
		t.Fatalf("the client's own reset: %+v %v", out, err)
	}
	bad := "weekly"
	patch.Body.ResetStrategy = &bad
	if _, err := h.updateUser(ctx, patch); err == nil {
		t.Fatal("an unknown reset was taken")
	} else if status, location, code := refusalOf(err); status != http.StatusUnprocessableEntity || location != "body.reset_strategy" || code != "bad_reset_strategy" {
		t.Fatalf("an unknown reset: %d %s %s", status, location, code)
	}
}
