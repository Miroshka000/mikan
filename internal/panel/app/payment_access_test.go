package app

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

func TestPaymentAccessProfile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyDeviceBinding: false, settings.KeyInactiveServer: true} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := domain.LoadPaymentAccess(ctx, set)
	if err != nil {
		t.Fatal(err)
	}
	inbounds, err := h.st.Q.ListInbounds(ctx)
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
	clock := func() time.Time { return h.now }
	tariffs, err := h.st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	users := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock)
	u, err := users.Create(ctx, domain.CreateInput{Name: "payment", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	group, err := h.st.Q.CreateGroup(ctx, db.CreateGroupParams{Name: "payment", Color: "gray", CreatedAt: h.now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetUserGroup(ctx, db.SetUserGroupParams{UserID: u.ID, GroupID: group.ID}); err != nil {
			return err
		}
		return domain.SetGroupAddresses(ctx, q, group.ID, []domain.GroupAddress{{
			InboundID: cfg.InboundID, Address: "renewal.example.com", Port: 9443, Name: "regular-group-proxy",
		}})
	}); err != nil {
		t.Fatal(err)
	}
	setExpiry := func(when time.Time) {
		t.Helper()
		u.ExpiresAt.Valid, u.ExpiresAt.Int64 = true, when.Unix()
		if _, err := h.st.DB.ExecContext(ctx, "UPDATE users SET expires_at = $1 WHERE id = $2", u.ExpiresAt.Int64, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	fetch := func() string {
		t.Helper()
		rr, body := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken+"?format=uri", nil, map[string]string{"User-Agent": "Happ/3.4.1"})
		links, err := base64.StdEncoding.DecodeString(string(body))
		if rr.StatusCode != http.StatusOK || err != nil {
			t.Fatalf("profile response %d: %s", rr.StatusCode, body)
		}
		return string(links)
	}
	setExpiry(h.now.Add(-time.Hour))
	base, err := h.st.Q.GetSlot(ctx, u.SlotID.Int64)
	if err != nil {
		t.Fatal(err)
	}
	key := domain.PaymentSlot(u, base.Secret)
	if got := fetch(); !strings.Contains(got, "vless://"+key.UUID+"@renewal.example.com:9443?") || strings.Contains(got, "#regular-group-proxy") {
		t.Fatal("expired subscription did not get the payment profile at its group's address")
	}
	// The panel's routing mode stays, ru_direct by default: Russian checkout and bank pages
	// go past the tunnel, whose node lets only the listed hosts through.
	if rr, body := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "mihomo/1.19.31"}); rr.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), key.UUID) || !strings.Contains(string(body), "GEOSITE,category-ru,DIRECT") {
		t.Fatalf("the renewal profile lost the panel's routing mode: %d %s", rr.StatusCode, body)
	}
	if err := h.st.Q.AddPaymentAccessTraffic(ctx, db.AddPaymentAccessTrafficParams{UserIds: []int64{u.ID}, ExpiredAts: []int64{u.ExpiresAt.Int64}, Bytes: []int64{cfg.Remaining(0)}}); err != nil {
		t.Fatal(err)
	}
	if got := fetch(); !strings.Contains(got, "127.0.0.1:1") || strings.Contains(got, key.UUID) {
		t.Fatal("exhausted payment quota still returned a key")
	}
	if _, err := users.Extend(ctx, u.ID, 30); err != nil {
		t.Fatal(err)
	}
	if got := fetch(); !strings.Contains(got, "vless://") || strings.Contains(got, key.UUID) || strings.Contains(got, "127.0.0.1:1") {
		t.Fatal("renewed subscription did not return to its regular profile")
	}
	setExpiry(h.now.Add(-14 * 24 * time.Hour))
	if got := fetch(); !strings.Contains(got, "127.0.0.1:1") {
		t.Fatal("subscription outside the payment window still returned a key")
	}
}
