package promo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

var (
	ErrNotFound           = errors.New("promo_not_found")
	ErrInactive           = errors.New("promo_inactive")
	ErrExpired            = errors.New("promo_expired")
	ErrLimit              = errors.New("promo_limit")
	ErrUserLimit          = errors.New("promo_user_limit")
	ErrTariff             = errors.New("promo_tariff")
	ErrMinimum            = errors.New("promo_minimum")
	ErrNewUser            = errors.New("promo_new_user")
	ErrFirstPurchase      = errors.New("promo_first_purchase")
	ErrCurrency           = errors.New("promo_currency")
	ErrAlreadyApplied     = errors.New("promo_already_applied")
	ErrSubscription       = errors.New("promo_subscription_required")
	ErrNoExpiry           = errors.New("promo_no_expiry")
	ErrNotDiscount        = errors.New("promo_not_discount")
	ErrUnavailable        = errors.New("promo_unavailable")
	ErrInvalidValue       = errors.New("promo_invalid_value")
	ErrReservationExpired = errors.New("promo_reservation_expired")
	ErrRefundUnsupported  = errors.New("promo_refund_unsupported")
	ErrNotBonus           = errors.New("promo_not_bonus")
)

const (
	maxPromoDays          int64 = 36500
	maxDiscountTTLSeconds int64 = 30 * 24 * 60 * 60
	defaultDiscountTTL          = 30 * time.Minute
)

type Service struct {
	Store   *store.Store
	Now     func() time.Time
	Changed func()
}

type Discount struct {
	PromoID      int64
	RedemptionID int64
	Code         string
	Amount       int64
	Original     int64
	Final        int64
	Currency     string
}

func New(st *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{Store: st, Now: now}
}

func Normalize(code string) string {
	return strings.ToUpper(strings.Join(strings.Fields(code), ""))
}

func check(p db.PromoCode, now time.Time) error {
	if p.Deleted != 0 || p.Enabled == 0 {
		return ErrInactive
	}
	n := now.Unix()
	if p.StartsAt.Valid && n < p.StartsAt.Int64 {
		return ErrInactive
	}
	if p.EndsAt.Valid && n >= p.EndsAt.Int64 {
		return ErrExpired
	}
	if p.MaxUses.Valid && p.UsedCount >= p.MaxUses.Int64 {
		return ErrLimit
	}
	return nil
}

func active(p db.PromoCode, now time.Time) error {
	return check(p, now)
}

func tariffs(p db.PromoCode) (map[int64]bool, error) {
	out := map[int64]bool{}
	if strings.TrimSpace(p.TariffIds) == "" || p.TariffIds == "[]" {
		return out, nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(p.TariffIds), &ids); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if id > 0 {
			out[id] = true
		}
	}
	return out, nil
}

func (s *Service) load(ctx context.Context, code string) (db.PromoCode, error) {
	p, err := s.Store.Q.GetPromoCodeByCode(ctx, Normalize(code))
	if errors.Is(err, sql.ErrNoRows) {
		return db.PromoCode{}, ErrNotFound
	}
	return p, err
}

func (s *Service) checkUser(ctx context.Context, q *db.Queries, p db.PromoCode, tgID, userID int64, now time.Time) error {
	if err := active(p, now); err != nil {
		return err
	}
	if p.NewUsersOnly != 0 {
		n, err := q.CountTgLinksOf(ctx, tgID)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrNewUser
		}
	}
	if p.FirstPurchaseOnly != 0 {
		n, err := q.CountUserPaidPayments(ctx, tgID)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrFirstPurchase
		}
	}
	n, err := q.CountPromoUser(ctx, db.CountPromoUserParams{PromoID: p.ID, TgID: tgID, UserID: sql.NullInt64{Int64: userID, Valid: userID != 0}})
	if err != nil {
		return err
	}
	if n >= p.PerUserLimit {
		return ErrUserLimit
	}
	return nil
}

// Check returns a code the account may use, without an order: a bonus is redeemed right
// after it, a discount is checked against the price at the checkout (Validate).
func (s *Service) Check(ctx context.Context, tgID, userID int64, code string) (db.PromoCode, error) {
	p, err := s.load(ctx, code)
	if err != nil {
		return p, err
	}
	return p, s.checkUser(ctx, s.Store.Q, p, tgID, userID, s.Now())
}

// Validate returns the current code and a human-readable machine status without consuming it.
// It is used by the Mini App before the actual action/payment.
func (s *Service) Validate(ctx context.Context, tgID, userID, tariffID, amount int64, currency, code string) (db.PromoCode, error) {
	p, err := s.load(ctx, code)
	if err != nil {
		return p, err
	}
	if err := s.checkUser(ctx, s.Store.Q, p, tgID, userID, s.Now()); err != nil {
		return p, err
	}
	if p.Type == "percent" || p.Type == "fixed" {
		if err := validateDiscount(p, tariffID, amount, currency); err != nil {
			return p, err
		}
	}
	return p, nil
}

func validateDiscount(p db.PromoCode, tariffID, amount int64, currency string) error {
	if (p.Type == "percent" && (p.Value < 1 || p.Value > 100)) || (p.Type == "fixed" && p.Value < 1) || p.DiscountTtl > maxDiscountTTLSeconds {
		return ErrInvalidValue
	}
	ids, err := tariffs(p)
	if err != nil {
		return err
	}
	if len(ids) > 0 && (tariffID <= 0 || !ids[tariffID]) {
		return ErrTariff
	}
	if amount <= 0 {
		return ErrMinimum
	}
	if p.MinOrder > 0 && amount < p.MinOrder {
		return ErrMinimum
	}
	if p.Currency != "" && p.Currency != currency {
		return ErrCurrency
	}
	d := discountAmount(p, amount)
	if d <= 0 || d >= amount {
		return ErrMinimum
	}
	return nil
}

func discountAmount(p db.PromoCode, original int64) int64 {
	if original <= 0 {
		return 0
	}
	var d int64
	if p.Type == "percent" {
		if p.Value > 0 && p.Value <= 100 {
			d = (original/100)*p.Value + (original%100)*p.Value/100
		}
		if d < 0 {
			d = 0
		}
	} else {
		d = p.Value
	}
	if p.MaxDiscount > 0 && d > p.MaxDiscount {
		d = p.MaxDiscount
	}
	if d > original {
		d = original
	}
	return d
}

// DiscountAmount is shared by the invoice and the Mini App preview so both report the
// same amount with integer-safe percentage arithmetic and maximum-discount handling.
func DiscountAmount(p db.PromoCode, original int64) int64 { return discountAmount(p, original) }

// ReserveDiscount atomically reserves one use and records the discount alongside the payment.
func (s *Service) ReserveDiscount(ctx context.Context, q *db.Queries, tgID, userID, tariffID, amount int64, currency, code string, paymentID int64) (Discount, error) {
	if strings.TrimSpace(code) == "" {
		return Discount{}, nil
	}
	p, err := q.GetPromoCodeByCode(ctx, Normalize(code))
	if errors.Is(err, sql.ErrNoRows) {
		return Discount{}, ErrNotFound
	}
	if err != nil {
		return Discount{}, err
	}
	if err := s.checkUser(ctx, q, p, tgID, userID, s.Now()); err != nil {
		return Discount{}, err
	}
	if p.Type != "percent" && p.Type != "fixed" {
		return Discount{}, ErrNotDiscount
	}
	if err := validateDiscount(p, tariffID, amount, currency); err != nil {
		return Discount{}, err
	}
	d := discountAmount(p, amount)
	n, err := q.IncrementPromoUse(ctx, p.ID)
	if err != nil {
		return Discount{}, err
	}
	if n != 1 {
		return Discount{}, ErrLimit
	}
	expires := sql.NullInt64{}
	ttl := defaultDiscountTTL
	if p.DiscountTtl > 0 {
		ttl = time.Duration(p.DiscountTtl) * time.Second
	}
	expires = sql.NullInt64{Int64: s.Now().Add(ttl).Unix(), Valid: true}
	r, err := q.CreatePromoRedemption(ctx, db.CreatePromoRedemptionParams{
		PromoID: p.ID, UserID: sql.NullInt64{Int64: userID, Valid: userID != 0}, TgID: tgID, PaymentID: sql.NullInt64{Int64: paymentID, Valid: paymentID != 0},
		Status: "reserved", RedeemedAt: s.Now().Unix(), ExpiresAt: expires, DiscountAmount: d, OriginalAmount: amount, FinalAmount: amount - d, Currency: currency, Note: p.Description,
	})
	if err != nil {
		_, _ = q.DecrementPromoUse(ctx, p.ID)
		return Discount{}, err
	}
	return Discount{PromoID: p.ID, RedemptionID: r.ID, Code: p.Code, Amount: d, Original: amount, Final: amount - d, Currency: currency}, nil
}

// RedeemBonus applies a days or traffic reward atomically and writes its history.
func (s *Service) RedeemBonus(ctx context.Context, tgID, userID int64, code string) (db.PromoRedemption, error) {
	if userID == 0 {
		return db.PromoRedemption{}, ErrSubscription
	}
	var out db.PromoRedemption
	err := s.Store.Tx(ctx, func(q *db.Queries) error {
		p, err := q.GetPromoCodeByCode(ctx, Normalize(code))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := s.checkUser(ctx, q, p, tgID, userID, s.Now()); err != nil {
			return err
		}
		if p.Type != "days" && p.Type != "traffic" {
			return ErrNotBonus
		}
		if p.Type == "days" && (p.Value < 1 || p.Value > maxPromoDays) {
			return ErrInvalidValue
		}
		u, err := q.GetUser(ctx, userID)
		if err != nil {
			return err
		}
		if strings.EqualFold(u.Status, "disabled") {
			return ErrUnavailable
		}
		ids, err := tariffs(p)
		if err != nil {
			return err
		}
		if len(ids) > 0 && (!u.TariffID.Valid || !ids[u.TariffID.Int64]) {
			return ErrTariff
		}
		now := s.Now()
		switch p.Type {
		case "days":
			if !u.ExpiresAt.Valid {
				return ErrNoExpiry
			}
			base := now.Unix()
			if u.ExpiresAt.Int64 > base {
				base = u.ExpiresAt.Int64
			}
			next := base + p.Value*int64(24*time.Hour/time.Second)
			_, err = q.UpdateUser(ctx, db.UpdateUserParams{
				Name: u.Name, Contact: u.Contact, Note: u.Note, Tags: u.Tags, Status: u.Status, TariffID: u.TariffID, TrafficLimit: u.TrafficLimit, DeviceLimit: u.DeviceLimit, SpeedLimit: u.SpeedLimit,
				ResetStrategy: u.ResetStrategy, PeriodDays: u.PeriodDays, PeriodStart: u.PeriodStart,
				ExpiresAt: sql.NullInt64{Int64: next, Valid: true}, Inbounds: u.Inbounds, BillingDay: u.BillingDay, UpdatedAt: now.Unix(), ID: u.ID,
			})
			if err != nil {
				return err
			}
		case "traffic":
			if p.Value < domain.MinGrantBytes || p.Value > domain.MaxGrantBytes {
				return ErrInvalidValue
			}
			// Bonus traffic uses the native grant mechanism and remains until consumed.
			g, err := domain.GrantTx(ctx, q, u, domain.GrantSpec{PoolID: p.PoolID.Int64, Bytes: p.Value, Lifetime: domain.LifetimeUsed, Source: domain.SourceAdmin, Note: "promo:" + p.Code}, now)
			if err != nil {
				return err
			}
			out.Bytes = g.Bytes
		}
		n, err := q.IncrementPromoUse(ctx, p.ID)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrLimit
		}
		days := int64(0)
		if p.Type == "days" {
			days = p.Value
		}
		bytes := p.Value
		if p.Type == "days" {
			bytes = 0
		}
		r, err := q.CreatePromoRedemption(ctx, db.CreatePromoRedemptionParams{PromoID: p.ID, UserID: sql.NullInt64{Int64: userID, Valid: true}, TgID: tgID, Status: "applied", RedeemedAt: now.Unix(), Days: days, Bytes: bytes, Note: p.Description})
		out = r
		return err
	})
	if err == nil && s.Changed != nil {
		s.Changed()
	}
	return out, err
}

func (s *Service) GetPaymentRedemption(ctx context.Context, paymentID int64) (db.PromoRedemption, error) {
	return s.Store.Q.GetPromoRedemptionByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
}

func (s *Service) ReleasePayment(ctx context.Context, paymentID int64) error {
	return s.Store.Tx(ctx, func(q *db.Queries) error {
		r, err := q.GetPromoRedemptionByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if r.Status != "reserved" {
			return nil
		}
		n, err := q.ReleasePromoRedemptionForClosedPayment(ctx, r.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		_, err = q.DecrementPromoUse(ctx, r.PromoID)
		return err
	})
}

// ReleaseApplied gives back the use of a code whose payment was applied and then refunded,
// on q's transaction (the refund's). It says whether the payment had a code to give back.
func (s *Service) ReleaseApplied(ctx context.Context, q *db.Queries, paymentID int64) (bool, error) {
	r, err := q.GetPromoRedemptionByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	n, err := q.ReleaseAppliedPromoRedemption(ctx, r.ID)
	if err != nil || n == 0 {
		return false, err
	}
	_, err = q.DecrementPromoUse(ctx, r.PromoID)
	return err == nil, err
}

func (s *Service) ReleaseExpired(ctx context.Context, before int64) error {
	rows, err := s.Store.Q.ListExpiredPromoPayments(ctx, before)
	if err != nil {
		return err
	}
	for _, id := range rows {
		if err := s.Store.Tx(ctx, func(q *db.Queries) error {
			r, err := q.GetPromoRedemptionByPayment(ctx, sql.NullInt64{Int64: id, Valid: id != 0})
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil || r.Status != "reserved" {
				return err
			}
			n, err := q.ReleaseExpiredPromoRedemption(ctx, db.ReleaseExpiredPromoRedemptionParams{ID: r.ID, Before: sql.NullInt64{Int64: before, Valid: true}})
			if err != nil || n == 0 {
				return err
			}
			_, err = q.DecrementPromoUse(ctx, r.PromoID)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ApplyPayment(ctx context.Context, q *db.Queries, paymentID, userID int64) error {
	payment, err := q.GetPayment(ctx, paymentID)
	if err != nil {
		return err
	}
	r, err := q.GetPromoRedemptionByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.Status != "reserved" && r.Status != "released" {
		return nil
	}
	paidAt := s.Now().Unix()
	if payment.PaidAt.Valid {
		paidAt = payment.PaidAt.Int64
	}
	if r.ExpiresAt.Valid && paidAt >= r.ExpiresAt.Int64 {
		return ErrReservationExpired
	}
	// A reservation released while its payment looked closed comes back when the payment
	// turns out paid in time. The use is counted again even if the code filled up since:
	// the buyer already paid the discounted price inside the window they were promised.
	if r.Status == "released" {
		if err := q.RestorePromoUse(ctx, r.PromoID); err != nil {
			return err
		}
	}
	n, err := q.MarkPromoApplied(ctx, db.MarkPromoAppliedParams{UserID: sql.NullInt64{Int64: userID, Valid: userID != 0}, ID: r.ID})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAlreadyApplied
	}
	return nil
}

func (s *Service) CodeForPayment(ctx context.Context, paymentID int64) (string, error) {
	r, err := s.Store.Q.GetPromoRedemptionByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
	if err != nil {
		return "", err
	}
	p, err := s.Store.Q.GetPromoCode(ctx, r.PromoID)
	if err != nil {
		return "", err
	}
	return p.Code, nil
}

func ParseInt(v string) (int64, error)   { return strconv.ParseInt(strings.TrimSpace(v), 10, 64) }
func Field(name string, err error) error { return fmt.Errorf("%s: %w", name, err) }
