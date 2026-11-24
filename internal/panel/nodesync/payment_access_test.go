package nodesync

import (
	"context"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

func TestPaymentAccessAccounting(t *testing.T) {
	s, node, st, users, now := setup(t)
	s.m.storeInterval = 0
	ctx := context.Background()
	set := settings.New(st.Q)
	cfg, err := domain.LoadPaymentAccess(ctx, set)
	if err != nil {
		t.Fatal(err)
	}
	inbounds, err := st.Q.ListInbounds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range inbounds {
		cfg.InboundID = in.ID
		if cfg.Matches(in) {
			break
		}
		cfg.InboundID = 0
	}
	if cfg.InboundID == 0 {
		t.Fatal("no VLESS inbound")
	}
	if err := settings.Set(ctx, set, domain.KeyPaymentAccess, cfg); err != nil {
		t.Fatal(err)
	}
	tariffs, err := st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := users.Create(ctx, domain.CreateInput{Name: "payment", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	u.ExpiresAt.Valid, u.ExpiresAt.Int64 = true, now.Add(-time.Hour).Unix()
	if _, err := st.DB.ExecContext(ctx, "UPDATE users SET expires_at = $1 WHERE id = $2", u.ExpiresAt.Int64, u.ID); err != nil {
		t.Fatal(err)
	}
	base, err := st.Q.GetSlot(ctx, u.SlotID.Int64)
	if err != nil {
		t.Fatal(err)
	}
	key := domain.PaymentSlot(u, base.Secret)
	desired, _, err := s.desiredPin(ctx)
	if err != nil || desired.PaymentAccess == nil || len(desired.PaymentAccess.Slots) != 1 || desired.PaymentAccess.Slots[0] != key {
		t.Fatalf("missing payment key: %v", err)
	}
	for _, slot := range desired.Slots {
		if slot == key {
			t.Fatal("regular slots would expose payment credentials to older nodes")
		}
	}
	node.batch = nodeapi.Counters{Epoch: "payment", Seq: 1, Slots: map[string]nodeapi.Traffic{key.Name: {Up: 100, Down: 900}}}
	s.pullCounters(ctx)
	s.pullCounters(ctx)
	used, err := st.Q.PaymentAccessTraffic(ctx, db.PaymentAccessTrafficParams{UserID: u.ID, ExpiredAt: u.ExpiresAt.Int64})
	if err != nil || used != 1000 {
		t.Fatalf("payment bytes: %d, %v", used, err)
	}
	user, err := st.Q.GetUser(ctx, u.ID)
	if err != nil || user.UsedUp != 0 || user.UsedDown != 0 {
		t.Fatalf("regular quota was charged: %d/%d, %v", user.UsedUp, user.UsedDown, err)
	}
	_, policies, _, err := s.policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range policies {
		if p.Slot == key.Name {
			if !p.Allowed || p.QuotaRemaining != cfg.Remaining(1000) || p.BaseSeq != 1 {
				t.Fatalf("payment policy was not re-based: %+v", p)
			}
			return
		}
	}
	t.Fatal("missing payment policy")
}
