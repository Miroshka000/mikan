package promo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := storetest.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}
func addUser(t *testing.T, st *store.Store, id int64, tg int64, expires int64) {
	t.Helper()
	ctx := context.Background()
	_, err := st.DB.ExecContext(ctx, `INSERT INTO users(id,name,period_start,sub_token,created_at,updated_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, "Test", 1, "token-"+string(rune(id)), 1, 1, expires)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.ExecContext(ctx, `INSERT INTO tg_links(user_id,tg_id,created_at) VALUES($1,$2,$3)`, id, tg, 1)
	if err != nil {
		t.Fatal(err)
	}
}
func addPromo(t *testing.T, st *store.Store, typ string, val int64, max *int64) *db.PromoCode {
	t.Helper()
	p, err := st.Q.CreatePromoCode(context.Background(), db.CreatePromoCodeParams{Code: Normalize("TEST" + typ), Type: typ, Value: val, Currency: "RUB", MaxUses: func() sql.NullInt64 {
		if max == nil {
			return sql.NullInt64{}
		}
		return sql.NullInt64{Int64: *max, Valid: true}
	}(), PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	return &p
}
func TestCheck(t *testing.T) {
	now := time.Unix(1000, 0)
	cases := []struct {
		name string
		p    db.PromoCode
		want error
	}{
		{name: "disabled", p: db.PromoCode{Enabled: 0}, want: ErrInactive},
		{name: "not started", p: db.PromoCode{Enabled: 1, StartsAt: sql.NullInt64{Int64: 1001, Valid: true}}, want: ErrInactive},
		{name: "expired", p: db.PromoCode{Enabled: 1, EndsAt: sql.NullInt64{Int64: 1000, Valid: true}}, want: ErrExpired},
		{name: "exhausted", p: db.PromoCode{Enabled: 1, UsedCount: 2, MaxUses: sql.NullInt64{Int64: 2, Valid: true}}, want: ErrLimit},
		{name: "active", p: db.PromoCode{Enabled: 1}, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := check(tc.p, now); !errors.Is(err, tc.want) {
				t.Fatalf("check() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize(" welcome 30 "); got != "WELCOME30" {
		t.Fatalf("got %q", got)
	}
}
func TestReserveDiscountAndLimit(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	ctx := context.Background()
	if err := domain.Seed(ctx, st, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	max := int64(1)
	p := addPromo(t, st, "percent", 25, &max)
	s := New(st, func() time.Time { return time.Unix(1000, 0) })
	var paymentID int64
	err := st.DB.QueryRowContext(ctx, `INSERT INTO payments(provider,payload,tg_id,kind,tariff_id,tariff_name,amount,currency,status,created_at) VALUES('stars','promo-test',10,'new',(SELECT min(id) FROM tariffs),'test',1000,'XTR','pending',1000) RETURNING id`).Scan(&paymentID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.ReserveDiscount(ctx, st.Q, 10, 0, 7, 1000, "RUB", p.Code, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Amount != 250 || d.Final != 750 {
		t.Fatalf("discount: %+v", d)
	}
	r, err := s.GetPaymentRedemption(ctx, paymentID)
	if err != nil || !r.ExpiresAt.Valid || r.ExpiresAt.Int64 != 1000+int64(defaultDiscountTTL/time.Second) {
		t.Fatalf("default reservation deadline=%+v err=%v", r.ExpiresAt, err)
	}
	if _, err := s.ReserveDiscount(ctx, st.Q, 11, 0, 7, 1000, "RUB", p.Code, 43); !errors.Is(err, ErrLimit) {
		t.Fatalf("expected limit, got %v", err)
	}
	if _, err := st.Q.SetPaymentStatus(ctx, db.SetPaymentStatusParams{NewStatus: "failed", ID: paymentID, OldStatus: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleasePayment(ctx, paymentID); err != nil {
		t.Fatal(err)
	}
	var secondPaymentID int64
	err = st.DB.QueryRowContext(ctx, `INSERT INTO payments(provider,payload,tg_id,kind,tariff_id,tariff_name,amount,currency,status,created_at) VALUES('stars','promo-test-2',11,'new',(SELECT min(id) FROM tariffs),'test',1000,'XTR','pending',1000) RETURNING id`).Scan(&secondPaymentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveDiscount(ctx, st.Q, 11, 0, 7, 1000, "RUB", p.Code, secondPaymentID); err != nil {
		t.Fatalf("released reservation should free use: %v", err)
	}
}

func TestPaidBeforeReservationExpiryCanApplyLater(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	ctx := context.Background()
	if err := domain.Seed(ctx, st, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	addUser(t, st, 1, 10, 2000)
	p := addPromo(t, st, "percent", 25, nil)
	if _, err := st.DB.ExecContext(ctx, `UPDATE promo_codes SET currency='XTR' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, `UPDATE promo_codes SET discount_ttl=30 WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	s := New(st, func() time.Time { return now })
	var paymentID int64
	if err := st.DB.QueryRowContext(ctx, `INSERT INTO payments(provider,payload,tg_id,kind,tariff_id,tariff_name,amount,currency,status,created_at) VALUES('stars','paid-in-time',10,'new',(SELECT min(id) FROM tariffs),'test',1000,'XTR','pending',1000) RETURNING id`).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveDiscount(ctx, st.Q, 10, 0, 1, 1000, "XTR", p.Code, paymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, `UPDATE payments SET status='paid', paid_at=1029 WHERE id=$1`, paymentID); err != nil {
		t.Fatal(err)
	}
	now = time.Unix(1060, 0)
	if err := s.ReleaseExpired(ctx, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPayment(ctx, st.Q, paymentID, 1); err != nil {
		t.Fatalf("ApplyPayment after timely capture: %v", err)
	}
	r, err := s.GetPaymentRedemption(ctx, paymentID)
	if err != nil || r.Status != "applied" {
		t.Fatalf("redemption status=%q err=%v, want applied", r.Status, err)
	}
}

func TestValidateDiscountUsesInvoiceRules(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	ctx := context.Background()
	if err := domain.Seed(ctx, st, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	p, err := st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "PREVIEW", Type: "percent", Value: 50, Currency: "RUB", MaxDiscount: 125, TariffIds: "[7]", Enabled: 1, PerUserLimit: 1, CreatedAt: 1000})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, func() time.Time { return time.Unix(1000, 0) })
	if _, err := s.Validate(ctx, 77, 0, 0, 0, "", p.Code); !errors.Is(err, ErrTariff) {
		t.Fatalf("discount accepted without an order: %v", err)
	}
	if _, err := s.Validate(ctx, 77, 0, 0, 1000, "RUB", p.Code); !errors.Is(err, ErrTariff) {
		t.Fatalf("missing tariff accepted: %v", err)
	}
	if _, err := s.Validate(ctx, 77, 0, 7, 1000, "RUB", p.Code); err != nil {
		t.Fatal(err)
	}
	if got := discountAmount(p, 1000); got != 125 {
		t.Fatalf("discountAmount=%d, want 125", got)
	}
	p.Value, p.MaxDiscount = 100, 0
	if err := validateDiscount(p, 7, math.MaxInt64, "RUB"); !errors.Is(err, ErrMinimum) {
		t.Fatalf("full-value discount accepted: %v", err)
	}
}

// The Mini App's one "Apply" takes a discount code before any order is picked: Check lets
// it through for the checkout, and still refuses what the account may not use.
func TestCheckTakesADiscountWithoutAnOrder(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	ctx := context.Background()
	p, err := st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "WELCOME15", Type: "percent", Value: 15, Currency: "RUB", MinOrder: 100, MaxDiscount: 500000,
		PerUserLimit: 1, FirstPurchaseOnly: 1, TariffIds: "[]", Enabled: 1, CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, func() time.Time { return time.Unix(1000, 0) })
	if _, err := s.Validate(ctx, 77, 0, 0, 0, "", "welcome15"); !errors.Is(err, ErrMinimum) {
		t.Fatalf("Validate without an order: %v, want ErrMinimum", err)
	}
	got, err := s.Check(ctx, 77, 0, " welcome15 ")
	if err != nil || got.ID != p.ID {
		t.Fatalf("Check = %+v, %v", got, err)
	}
	if _, err := s.Check(ctx, 77, 0, "NOPE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown code: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO payments(provider,payload,tg_id,kind,tariff_name,amount,currency,status,created_at) VALUES('stars','p1',77,'package','T',100,'XTR','applied',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(ctx, 77, 0, "WELCOME15"); !errors.Is(err, ErrFirstPurchase) {
		t.Fatalf("after a purchase: %v, want ErrFirstPurchase", err)
	}
}

func TestDeletedCodeCanBeRecreated(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	p := addPromo(t, st, "days", 3, nil)
	if n, err := st.Q.DeletePromoCode(context.Background(), p.ID); err != nil || n != 1 {
		t.Fatalf("delete: n=%d err=%v", n, err)
	}
	if _, err := st.Q.CreatePromoCode(context.Background(), db.CreatePromoCodeParams{Code: p.Code, Type: "days", Value: 3, PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: 2}); err != nil {
		t.Fatalf("recreate deleted code: %v", err)
	}
}

func TestDisabledPromoReleasesPoolReference(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	ctx := context.Background()
	pool, err := st.Q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "Promo pool", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "POOLCODE", Type: "traffic", Value: domain.MinGrantBytes,
		PoolID: sql.NullInt64{Int64: pool.ID, Valid: true}, PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := st.Q.CountEnabledPromoCodesForPool(ctx, sql.NullInt64{Int64: pool.ID, Valid: true}); err != nil || n != 1 {
		t.Fatalf("active references=%d err=%v", n, err)
	}
	if _, err := st.Q.SetPromoEnabled(ctx, db.SetPromoEnabledParams{Enabled: 0, ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.Q.ClearDisabledPromoPools(ctx, sql.NullInt64{Int64: pool.ID, Valid: true}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.Q.DeleteTrafficPool(ctx, pool.ID); err != nil || n != 1 {
		t.Fatalf("delete disabled-code pool: n=%d err=%v", n, err)
	}
}

func TestApplyPaymentRejectsExpiredReservation(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	ctx := context.Background()
	if err := domain.Seed(ctx, st, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	p := addPromo(t, st, "percent", 25, nil)
	if _, err := st.DB.ExecContext(ctx, `UPDATE promo_codes SET discount_ttl=$1 WHERE id=$2`, 30, p.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	s := New(st, func() time.Time { return now })
	_, err := st.DB.ExecContext(ctx, `INSERT INTO payments(provider,payload,tg_id,kind,tariff_id,tariff_name,amount,currency,status,created_at) VALUES('stars','expire-test',10,'new',(SELECT min(id) FROM tariffs),'test',1000,'XTR','pending',1000)`)
	if err != nil {
		t.Fatal(err)
	}
	var paymentID int64
	if err := st.DB.QueryRowContext(ctx, `SELECT id FROM payments WHERE payload='expire-test'`).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveDiscount(ctx, st.Q, 10, 0, 1, 1000, "RUB", p.Code, paymentID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := s.ApplyPayment(ctx, st.Q, paymentID, 1); !errors.Is(err, ErrReservationExpired) {
		t.Fatalf("ApplyPayment() = %v, want expired reservation", err)
	}
	r, err := s.GetPaymentRedemption(ctx, paymentID)
	if err != nil || r.Status != "reserved" {
		t.Fatalf("expired reservation unexpectedly changed: status=%q err=%v", r.Status, err)
	}
	if _, err := st.Q.SetPaymentStatus(ctx, db.SetPaymentStatusParams{NewStatus: "expired", ID: paymentID, OldStatus: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleasePayment(ctx, paymentID); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPayment(ctx, st.Q, paymentID, 1); !errors.Is(err, ErrReservationExpired) {
		t.Fatalf("ApplyPayment() for released reservation = %v, want expired reservation", err)
	}
}

func TestReleasePaymentKeepsPaidReservation(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	ctx := context.Background()
	if err := domain.Seed(ctx, st, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	p := addPromo(t, st, "percent", 25, nil)
	s := New(st, func() time.Time { return time.Unix(1000, 0) })
	var paymentID int64
	err := st.DB.QueryRowContext(ctx, `INSERT INTO payments(provider,payload,tg_id,kind,tariff_id,tariff_name,amount,currency,status,created_at) VALUES('stars','paid-reservation',10,'new',(SELECT min(id) FROM tariffs),'test',1000,'XTR','pending',1000) RETURNING id`).Scan(&paymentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveDiscount(ctx, st.Q, 10, 0, 7, 1000, "RUB", p.Code, paymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q.SetPaymentStatus(ctx, db.SetPaymentStatusParams{NewStatus: "paid", ID: paymentID, OldStatus: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleasePayment(ctx, paymentID); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetPaymentRedemption(ctx, paymentID)
	if err != nil || r.Status != "reserved" {
		t.Fatalf("paid payment reservation status = %q, err=%v; want to keep it reserved", r.Status, err)
	}
}

func TestReleaseExpiredReservationFreesPromoUse(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	ctx := context.Background()
	if err := domain.Seed(ctx, st, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	max := int64(1)
	p := addPromo(t, st, "percent", 25, &max)
	if _, err := st.DB.ExecContext(ctx, `UPDATE promo_codes SET discount_ttl=30 WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	var paymentID int64
	if err := st.DB.QueryRowContext(ctx, `INSERT INTO payments(provider,payload,tg_id,kind,tariff_id,tariff_name,amount,currency,status,created_at) VALUES('stars','ttl-release',10,'new',(SELECT min(id) FROM tariffs),'test',1000,'XTR','pending',1000) RETURNING id`).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	s := New(st, func() time.Time { return now })
	if _, err := s.ReserveDiscount(ctx, st.Q, 10, 0, 7, 1000, "RUB", p.Code, paymentID); err != nil {
		t.Fatal(err)
	}
	now = time.Unix(1031, 0)
	if err := s.ReleaseExpired(ctx, now.Unix()); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetPaymentRedemption(ctx, paymentID)
	if err != nil || r.Status != "released" {
		t.Fatalf("reservation status=%q err=%v; want released", r.Status, err)
	}
	code, err := st.Q.GetPromoCode(ctx, p.ID)
	if err != nil || code.UsedCount != 0 {
		t.Fatalf("used_count=%d err=%v; want zero", code.UsedCount, err)
	}
}

func TestPerUserLimitSurvivesSubscriptionRelink(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	ctx := context.Background()
	addUser(t, st, 1, 77, 2000)
	p := addPromo(t, st, "percent", 10, nil)
	_, err := st.Q.CreatePromoRedemption(ctx, db.CreatePromoRedemptionParams{PromoID: p.ID, UserID: sql.NullInt64{Int64: 1, Valid: true}, TgID: 77, Status: "applied", RedeemedAt: 1000})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, func() time.Time { return time.Unix(1000, 0) })
	if err := s.checkUser(ctx, st.Q, *p, 77, 2, time.Unix(1000, 0)); !errors.Is(err, ErrUserLimit) {
		t.Fatalf("relinked account bypassed per-user limit: %v", err)
	}
}

func TestReserveDiscountRejectsBonusCode(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	p := addPromo(t, st, "traffic", domainGiBForTest, nil)
	s := New(st, func() time.Time { return time.Unix(1000, 0) })
	_, err := s.ReserveDiscount(context.Background(), st.Q, 77, 0, 1, 1000, "RUB", p.Code, 0)
	if !errors.Is(err, ErrNotDiscount) {
		t.Fatalf("got %v, want ErrNotDiscount", err)
	}
}
func TestRedeemTrafficAndDays(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	ctx := context.Background()
	now := time.Unix(2000, 0)
	addUser(t, st, 1, 77, now.Add(24*time.Hour).Unix())
	s := New(st, func() time.Time { return now })
	p := addPromo(t, st, "traffic", 2*domainGiBForTest, nil)
	if _, err := s.RedeemBonus(ctx, 77, 1, p.Code); err != nil {
		t.Fatal(err)
	}
	gs, err := st.Q.ListUserGrants(ctx, 1)
	if err != nil || len(gs) != 1 || gs[0].Bytes != 2*domainGiBForTest {
		t.Fatalf("grant: %v %+v", err, gs)
	}
	pool, err := st.Q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	poolPromo, err := st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "POOLTRAFFIC", Type: "traffic", Value: domainGiBForTest, PoolID: sql.NullInt64{Int64: pool.ID, Valid: true}, PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemBonus(ctx, 77, 1, poolPromo.Code); err != nil {
		t.Fatal(err)
	}
	gs, err = st.Q.ListUserGrants(ctx, 1)
	if err != nil || len(gs) != 2 || !gs[0].PoolID.Valid || gs[0].PoolID.Int64 != pool.ID {
		t.Fatalf("pool grant: %v %+v", err, gs)
	}
	p2 := addPromo(t, st, "days", 30, nil)
	if _, err := s.RedeemBonus(ctx, 77, 1, p2.Code); err != nil {
		t.Fatal(err)
	}
	u, err := st.Q.GetUser(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if u.ExpiresAt.Int64 != now.Add(31*24*time.Hour).Unix() {
		t.Fatalf("expiry %d", u.ExpiresAt.Int64)
	}
}

func TestRedeemBonusRejectsDisabledUser(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	ctx := context.Background()
	if err := domain.Seed(ctx, st, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	addUser(t, st, 8, 88, 2000)
	if _, err := st.DB.ExecContext(ctx, `UPDATE users SET status='disabled' WHERE id=8`); err != nil {
		t.Fatal(err)
	}
	p := addPromo(t, st, "days", 30, nil)
	s := New(st, func() time.Time { return time.Unix(1000, 0) })
	if _, err := s.RedeemBonus(ctx, 88, 8, p.Code); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled subscription redeemed bonus: %v", err)
	}
}

const domainGiBForTest = int64(1) << 30

// Every sentinel of the package must be a refusal: subs answers a refused code with the generic
// "promo_unavailable", and an Err* left out of IsRefusal would be answered as the panel's fault.
// The list below is checked against the source, so a new Err* that is not added here fails.
func TestIsRefusalCoversEveryError(t *testing.T) {
	all := map[string]error{
		"ErrNotFound": ErrNotFound, "ErrInactive": ErrInactive, "ErrExpired": ErrExpired, "ErrLimit": ErrLimit,
		"ErrUserLimit": ErrUserLimit, "ErrTariff": ErrTariff, "ErrMinimum": ErrMinimum, "ErrNewUser": ErrNewUser,
		"ErrFirstPurchase": ErrFirstPurchase, "ErrCurrency": ErrCurrency, "ErrAlreadyApplied": ErrAlreadyApplied,
		"ErrSubscription": ErrSubscription, "ErrNoExpiry": ErrNoExpiry, "ErrNotDiscount": ErrNotDiscount,
		"ErrUnavailable": ErrUnavailable, "ErrInvalidValue": ErrInvalidValue, "ErrReservationExpired": ErrReservationExpired,
		"ErrRefundUnsupported": ErrRefundUnsupported, "ErrNotBonus": ErrNotBonus,
	}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	declared := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				for _, name := range spec.(*ast.ValueSpec).Names {
					if !strings.HasPrefix(name.Name, "Err") || !name.IsExported() {
						continue
					}
					declared++
					if _, ok := all[name.Name]; !ok {
						t.Errorf("%s is declared in the package but not listed in this test", name.Name)
					}
				}
			}
		}
	}
	if declared != len(all) {
		t.Errorf("the package declares %d Err* variables, the test lists %d", declared, len(all))
	}
	for name, err := range all {
		if !IsRefusal(err) {
			t.Errorf("%s is not a refusal", name)
		}
		if !IsRefusal(fmt.Errorf("redeem: %w", err)) {
			t.Errorf("%s is not a refusal once wrapped", name)
		}
		if !strings.HasPrefix(err.Error(), "promo_") {
			t.Errorf("%s says %q, want the promo_ code", name, err.Error())
		}
	}
	for _, err := range []error{nil, errors.New("promo_unavailable"), sql.ErrNoRows, fmt.Errorf("database is down: %w", errors.New("x"))} {
		if IsRefusal(err) {
			t.Errorf("%v is not one of the package's refusals", err)
		}
	}
}
