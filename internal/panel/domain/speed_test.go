package domain

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// A tariff's speed cap goes to its users like the device limit; the admin may set or lift
// a user's own, and the edits that do not touch it leave it as it was.
func TestSpeedLimitFollowsTariffAndPatches(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	std := tariffs[1]
	if _, err := st.Q.UpdateTariff(ctx, db.UpdateTariffParams{Name: std.Name, TrafficLimit: std.TrafficLimit, DurationDays: std.DurationDays, DeviceLimit: std.DeviceLimit,
		ResetStrategy: std.ResetStrategy, PriceLabel: std.PriceLabel, Sort: std.Sort, ID: std.ID, SpeedLimit: sql.NullInt64{Int64: 50, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: std.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !u.SpeedLimit.Valid || u.SpeedLimit.Int64 != 50 {
		t.Fatalf("a new user takes the tariff's cap: %+v", u.SpeedLimit)
	}

	note := "renamed elsewhere"
	if u, err = users.Update(ctx, u.ID, Patch{Note: &note}); err != nil || u.SpeedLimit.Int64 != 50 {
		t.Fatalf("an edit of something else must keep the cap: %+v %v", u.SpeedLimit, err)
	}
	own := int64(200)
	if u, err = users.Update(ctx, u.ID, Patch{SpeedLimit: &own}); err != nil || u.SpeedLimit.Int64 != 200 {
		t.Fatalf("the admin's own cap: %+v %v", u.SpeedLimit, err)
	}
	if u, err = users.Update(ctx, u.ID, Patch{ClearSpeedLimit: true}); err != nil || u.SpeedLimit.Valid {
		t.Fatalf("lifted: %+v %v", u.SpeedLimit, err)
	}
	for _, bad := range []int64{0, -5, MaxSpeedLimit + 1} {
		if _, err := users.Update(ctx, u.ID, Patch{SpeedLimit: &bad}); !errors.Is(err, ErrBadSpeedLimit) {
			t.Fatalf("%d Mbit/s must be refused: %v", bad, err)
		}
	}
	// Switching to the tariff brings its cap back.
	if u, err = users.Update(ctx, u.ID, Patch{TariffID: &std.ID}); err != nil || u.SpeedLimit.Int64 != 50 {
		t.Fatalf("the tariff's cap comes back with the tariff: %+v %v", u.SpeedLimit, err)
	}
}
