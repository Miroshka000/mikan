package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

type PromoCodeView struct {
	ID                int64      `json:"id"`
	Code              string     `json:"code"`
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	Type              string     `json:"type" enum:"days,traffic,percent,fixed"`
	Value             int64      `json:"value"`
	Currency          string     `json:"currency"`
	StartsAt          *time.Time `json:"starts_at,omitempty"`
	EndsAt            *time.Time `json:"ends_at,omitempty"`
	MaxUses           *int64     `json:"max_uses,omitempty"`
	UsedCount         int64      `json:"used_count"`
	PerUserLimit      int64      `json:"per_user_limit"`
	DiscountTtl       int64      `json:"discount_ttl"`
	MinOrder          int64      `json:"min_order"`
	MaxDiscount       int64      `json:"max_discount"`
	TariffIDs         []int64    `json:"tariff_ids"`
	PoolID            *int64     `json:"pool_id,omitempty"`
	FirstPurchaseOnly bool       `json:"first_purchase_only"`
	NewUsersOnly      bool       `json:"new_users_only"`
	Enabled           bool       `json:"enabled"`
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"created_at"`
	CreatedBy         *int64     `json:"created_by,omitempty"`
}
type PromoBody struct {
	Code              string  `json:"code" minLength:"1" maxLength:"64"`
	Name              string  `json:"name" maxLength:"120"`
	Description       string  `json:"description" maxLength:"500"`
	Type              string  `json:"type" enum:"days,traffic,percent,fixed"`
	Value             int64   `json:"value" minimum:"1" doc:"days: дни; traffic: байты (минимум 1 GiB); percent: проценты 1-100; fixed: сумма в минимальных единицах оплаты"`
	Currency          string  `json:"currency" doc:"Для fixed: RUB или XTR; RUB — копейки, XTR — Stars. Для percent: пусто — любая валюта, тогда min_order и max_discount равны 0. Для остальных типов не используется."`
	StartsAt          *int64  `json:"starts_at,omitempty" doc:"Unix time в секундах"`
	EndsAt            *int64  `json:"ends_at,omitempty" doc:"Unix time в секундах; значение не может быть в прошлом"`
	MaxUses           *int64  `json:"max_uses,omitempty" minimum:"1"`
	PerUserLimit      int64   `json:"per_user_limit" minimum:"1"`
	DiscountTtl       int64   `json:"discount_ttl" minimum:"0" doc:"Срок действия резерва скидки в секундах; 0 = 30 минут"`
	MinOrder          int64   `json:"min_order" minimum:"0" doc:"Минимальная сумма заказа в минимальных единицах оплаты: RUB — копейки, XTR — Stars"`
	MaxDiscount       int64   `json:"max_discount" minimum:"0" doc:"Максимальная скидка в минимальных единицах оплаты: RUB — копейки, XTR — Stars"`
	TariffIDs         []int64 `json:"tariff_ids"`
	PoolID            *int64  `json:"pool_id,omitempty" minimum:"1" doc:"Пул для бонусного трафика; без значения — основной трафик"`
	FirstPurchaseOnly bool    `json:"first_purchase_only"`
	NewUsersOnly      bool    `json:"new_users_only"`
	Enabled           bool    `json:"enabled"`
}
type promoListInput struct {
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}
type promoListOutput struct {
	Body struct {
		Items []PromoCodeView `json:"items"`
		Total int64           `json:"total"`
	}
}
type promoOneOutput struct{ Body PromoCodeView }
type promoRedemptionsInput struct {
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}
type PromoRedemptionView struct {
	ID             int64      `json:"id"`
	PromoID        int64      `json:"promo_id"`
	UserID         *int64     `json:"user_id,omitempty"`
	TgID           int64      `json:"tg_id"`
	PaymentID      *int64     `json:"payment_id,omitempty"`
	Status         string     `json:"status"`
	RedeemedAt     time.Time  `json:"redeemed_at"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Days           int64      `json:"days"`
	Bytes          int64      `json:"bytes"`
	DiscountAmount int64      `json:"discount_amount"`
	OriginalAmount int64      `json:"original_amount"`
	FinalAmount    int64      `json:"final_amount"`
	Currency       string     `json:"currency"`
	Code           string     `json:"code"`
}
type promoRedemptionsOutput struct {
	Body struct {
		Items []PromoRedemptionView `json:"items"`
		Total int64                 `json:"total"`
	}
}
type PromoStatsView struct {
	PromoCodes            int64 `json:"promo_codes"`
	Active                int64 `json:"active"`
	SuccessfulActivations int64 `json:"successful_activations"`
	BonusDays             int64 `json:"bonus_days"`
	BonusBytes            int64 `json:"bonus_bytes"`
	DiscountOrders        int64 `json:"discount_orders"`
	DiscountAmount        int64 `json:"discount_amount"`
}
type promoStatsOutput struct{ Body PromoStatsView }

func (h *handlers) registerPromocodes() {
	tags := []string{"promocodes"}
	huma.Register(h.api, huma.Operation{OperationID: "list-promocodes", Method: http.MethodGet, Path: "/api/v1/promocodes", Summary: "Промокоды", Tags: tags}, h.listPromocodes)
	huma.Register(h.api, huma.Operation{OperationID: "create-promocode", Method: http.MethodPost, Path: "/api/v1/promocodes", Summary: "Создать промокод", Tags: tags}, h.createPromocode)
	huma.Register(h.api, huma.Operation{OperationID: "update-promocode", Method: http.MethodPut, Path: "/api/v1/promocodes/{id}", Summary: "Изменить промокод", Tags: tags}, h.updatePromocode)
	huma.Register(h.api, huma.Operation{OperationID: "enable-promocode", Method: http.MethodPost, Path: "/api/v1/promocodes/{id}/enabled", Summary: "Включить или выключить промокод", Tags: tags}, h.enablePromocode)
	huma.Register(h.api, huma.Operation{OperationID: "delete-promocode", Method: http.MethodDelete, Path: "/api/v1/promocodes/{id}", Summary: "Удалить промокод", Tags: tags}, h.deletePromocode)
	huma.Register(h.api, huma.Operation{OperationID: "list-promocode-redemptions", Method: http.MethodGet, Path: "/api/v1/promocodes/redemptions", Summary: "История промокодов", Tags: tags}, h.listPromocodeRedemptions)
	huma.Register(h.api, huma.Operation{OperationID: "promocode-stats", Method: http.MethodGet, Path: "/api/v1/promocodes/stats", Summary: "Статистика промокодов", Tags: tags}, h.promocodeStats)
}
func ts(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}
func promoView(p db.PromoCode, now time.Time) PromoCodeView {
	var ids []int64
	_ = json.Unmarshal([]byte(p.TariffIds), &ids)
	st := "active"
	if p.Deleted != 0 {
		st = "deleted"
	} else if p.Enabled == 0 {
		st = "disabled"
	} else if p.StartsAt.Valid && now.Unix() < p.StartsAt.Int64 {
		st = "inactive"
	} else if p.EndsAt.Valid && now.Unix() >= p.EndsAt.Int64 {
		st = "expired"
	} else if p.MaxUses.Valid && p.UsedCount >= p.MaxUses.Int64 {
		st = "exhausted"
	}
	return PromoCodeView{ID: p.ID, Code: p.Code, Name: p.Name, Description: p.Description, Type: p.Type, Value: p.Value, Currency: p.Currency, StartsAt: ts(p.StartsAt), EndsAt: ts(p.EndsAt), PoolID: func() *int64 {
		if p.PoolID.Valid {
			v := p.PoolID.Int64
			return &v
		}
		return nil
	}(), MaxUses: func() *int64 {
		if p.MaxUses.Valid {
			x := p.MaxUses.Int64
			return &x
		}
		return nil
	}(), UsedCount: p.UsedCount, PerUserLimit: p.PerUserLimit, DiscountTtl: p.DiscountTtl, MinOrder: p.MinOrder, MaxDiscount: p.MaxDiscount, TariffIDs: ids, FirstPurchaseOnly: p.FirstPurchaseOnly != 0, NewUsersOnly: p.NewUsersOnly != 0, Enabled: p.Enabled != 0, Status: st, CreatedAt: time.Unix(p.CreatedAt, 0), CreatedBy: func() *int64 {
		if p.CreatedBy.Valid {
			x := p.CreatedBy.Int64
			return &x
		}
		return nil
	}()}
}
func validatePromoBody(in PromoBody, now time.Time, usedCount int64) error {
	if promo.Normalize(in.Code) == "" {
		return errors.New("bad_code")
	}
	switch in.Type {
	case "days":
		if in.Value > 36500 {
			return errors.New("bad_promo_days")
		}
	case "traffic":
		if in.Value < domain.MinGrantBytes || in.Value > domain.MaxGrantBytes {
			return errors.New("bad_traffic")
		}
	case "percent":
		if in.Value < 1 || in.Value > 100 {
			return errors.New("bad_percent")
		}
		if in.Currency != "" && in.Currency != "RUB" && in.Currency != "XTR" {
			return errors.New("bad_currency")
		}
		// Sums are in the currency's smallest unit: kopecks and Stars do not compare.
		if in.Currency == "" && (in.MinOrder > 0 || in.MaxDiscount > 0) {
			return errors.New("promo_sum_needs_currency")
		}
	case "fixed":
		if in.Currency != "RUB" && in.Currency != "XTR" {
			return errors.New("bad_currency")
		}
	default:
		return errors.New("bad_type")
	}
	if in.StartsAt != nil && in.EndsAt != nil && *in.StartsAt >= *in.EndsAt {
		return errors.New("bad_dates")
	}
	if in.EndsAt != nil && *in.EndsAt <= now.Unix() {
		return errors.New("ends_at_past")
	}
	if in.MaxUses != nil && *in.MaxUses < usedCount {
		return errors.New("max_uses_below_used")
	}
	if in.PerUserLimit < 1 || in.DiscountTtl < 0 || in.DiscountTtl > 30*24*60*60 || in.MinOrder < 0 || in.MaxDiscount < 0 || in.Value < 1 {
		return errors.New("bad_value")
	}
	if in.PoolID != nil && (in.Type != "traffic" || *in.PoolID < 1) {
		return errors.New("bad_pool")
	}
	if in.NewUsersOnly && (in.Type == "days" || in.Type == "traffic") {
		return errors.New("new_users_only_not_supported_for_bonus")
	}
	for _, id := range in.TariffIDs {
		if id <= 0 {
			return errors.New("bad_tariff_ids")
		}
	}
	return nil
}

func (h *handlers) promoInput(in PromoBody, createdBy sql.NullInt64, usedCount int64) (db.CreatePromoCodeParams, error) {
	if err := validatePromoBody(in, h.d.Now(), usedCount); err != nil {
		return db.CreatePromoCodeParams{}, err
	}
	code := promo.Normalize(in.Code)
	if in.PerUserLimit < 1 {
		in.PerUserLimit = 1
	}
	if in.Type != "fixed" && in.Type != "percent" {
		in.Currency = ""
	}
	ids, _ := json.Marshal(in.TariffIDs)
	if len(in.TariffIDs) == 0 {
		ids = []byte("[]")
	}
	return db.CreatePromoCodeParams{Code: code, Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description), Type: in.Type, Value: in.Value, Currency: in.Currency, StartsAt: domain.NullInt(in.StartsAt), EndsAt: domain.NullInt(in.EndsAt), MaxUses: domain.NullInt(in.MaxUses), PerUserLimit: in.PerUserLimit, DiscountTtl: in.DiscountTtl, MinOrder: in.MinOrder, MaxDiscount: in.MaxDiscount, TariffIds: string(ids), PoolID: domain.NullInt(in.PoolID), FirstPurchaseOnly: domain.Flag(in.FirstPurchaseOnly), NewUsersOnly: domain.Flag(in.NewUsersOnly), Enabled: domain.Flag(in.Enabled), CreatedAt: h.d.Now().Unix(), CreatedBy: createdBy}, nil
}

func (h *handlers) validatePromoReferences(ctx context.Context, in PromoBody) error {
	if in.PoolID != nil {
		if in.Type != "traffic" {
			return errors.New("bad_pool")
		}
		if _, err := h.d.Store.Q.GetTrafficPool(ctx, *in.PoolID); err != nil {
			return errors.New("pool_not_found")
		}
	}
	for _, id := range in.TariffIDs {
		if _, err := h.d.Store.Q.GetTariff(ctx, id); err != nil {
			return errors.New("bad_tariff_ids")
		}
	}
	return nil
}
func (h *handlers) listPromocodes(ctx context.Context, in *promoListInput) (*promoListOutput, error) {
	xs, e := h.d.Store.Q.ListPromoCodes(ctx, db.ListPromoCodesParams{Lim: in.Limit, RowOffset: in.Offset})
	if e != nil {
		return nil, e
	}
	n, e := h.d.Store.Q.CountPromoCodes(ctx)
	if e != nil {
		return nil, e
	}
	o := &promoListOutput{}
	o.Body.Total = n
	// An empty list is [], not null: the page reads its length.
	o.Body.Items = make([]PromoCodeView, 0, len(xs))
	for _, p := range xs {
		o.Body.Items = append(o.Body.Items, promoView(p, h.d.Now()))
	}
	return o, nil
}
func (h *handlers) createPromocode(ctx context.Context, in *struct{ Body PromoBody }) (*promoOneOutput, error) {
	if e := h.validatePromoReferences(ctx, in.Body); e != nil {
		return nil, huma.Error400BadRequest(e.Error())
	}
	p, e := h.promoInput(in.Body, sql.NullInt64{Int64: sessionOf(ctx).AdminID, Valid: true}, 0)
	if e != nil {
		return nil, huma.Error400BadRequest(e.Error())
	}
	x, e := h.d.Store.Q.CreatePromoCode(ctx, p)
	if store.IsUnique(e) {
		return nil, huma.Error409Conflict("duplicate_code")
	}
	if e != nil {
		return nil, e
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "promocode.create", "promocode", strconv64(x.ID), map[string]any{"code": x.Code})
	return &promoOneOutput{Body: promoView(x, h.d.Now())}, nil
}
func strconv64(v int64) string { return fmt.Sprintf("%d", v) }
func (h *handlers) updatePromocode(ctx context.Context, in *struct {
	ID   int64 `path:"id"`
	Body PromoBody
}) (*promoOneOutput, error) {
	if e := h.validatePromoReferences(ctx, in.Body); e != nil {
		return nil, huma.Error400BadRequest(e.Error())
	}
	old, e := h.d.Store.Q.GetPromoCode(ctx, in.ID)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if e != nil {
		return nil, e
	}
	p, e := h.promoInput(in.Body, old.CreatedBy, old.UsedCount)
	if e != nil {
		return nil, huma.Error400BadRequest(e.Error())
	}
	u, e := h.d.Store.Q.UpdatePromoCode(ctx, db.UpdatePromoCodeParams{Code: p.Code, Name: p.Name, Description: p.Description, Type: p.Type, Value: p.Value, Currency: p.Currency, StartsAt: p.StartsAt, EndsAt: p.EndsAt, MaxUses: p.MaxUses, PerUserLimit: p.PerUserLimit, DiscountTtl: p.DiscountTtl, MinOrder: p.MinOrder, MaxDiscount: p.MaxDiscount, TariffIds: p.TariffIds, PoolID: p.PoolID, FirstPurchaseOnly: p.FirstPurchaseOnly, NewUsersOnly: p.NewUsersOnly, Enabled: p.Enabled, ID: in.ID})
	if store.IsUnique(e) {
		return nil, huma.Error409Conflict("duplicate_code")
	}
	if errors.Is(e, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if e != nil {
		return nil, e
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "promocode.update", "promocode", strconv64(in.ID), nil)
	return &promoOneOutput{Body: promoView(u, h.d.Now())}, nil
}
func (h *handlers) enablePromocode(ctx context.Context, in *struct {
	ID   int64 `path:"id"`
	Body struct {
		Enabled bool `json:"enabled"`
	}
}) (*promoOneOutput, error) {
	n, e := h.d.Store.Q.SetPromoEnabled(ctx, db.SetPromoEnabledParams{ID: in.ID, Enabled: domain.Flag(in.Body.Enabled)})
	if e != nil {
		return nil, e
	}
	if n != 1 {
		return nil, huma.Error404NotFound("not_found")
	}
	p, e := h.d.Store.Q.GetPromoCode(ctx, in.ID)
	if e != nil {
		return nil, e
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "promocode.enabled", "promocode", strconv64(in.ID), map[string]any{"enabled": in.Body.Enabled})
	return &promoOneOutput{Body: promoView(p, h.d.Now())}, nil
}
func (h *handlers) deletePromocode(ctx context.Context, in *struct {
	ID int64 `path:"id"`
}) (*struct{}, error) {
	n, e := h.d.Store.Q.DeletePromoCode(ctx, in.ID)
	if e != nil {
		return nil, e
	}
	if n != 1 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "promocode.delete", "promocode", strconv64(in.ID), nil)
	return &struct{}{}, nil
}
func (h *handlers) listPromocodeRedemptions(ctx context.Context, in *promoRedemptionsInput) (*promoRedemptionsOutput, error) {
	xs, e := h.d.Store.Q.ListPromoRedemptionsWithCode(ctx, db.ListPromoRedemptionsWithCodeParams{Lim: in.Limit, RowOffset: in.Offset})
	if e != nil {
		return nil, e
	}
	n, e := h.d.Store.Q.CountPromoRedemptions(ctx)
	if e != nil {
		return nil, e
	}
	o := &promoRedemptionsOutput{}
	o.Body.Total = n
	o.Body.Items = make([]PromoRedemptionView, 0, len(xs))
	for _, row := range xs {
		r := row
		o.Body.Items = append(o.Body.Items, PromoRedemptionView{ID: r.ID, PromoID: r.PromoID, UserID: func() *int64 {
			if r.UserID.Valid {
				x := r.UserID.Int64
				return &x
			}
			return nil
		}(), TgID: r.TgID, PaymentID: func() *int64 {
			if r.PaymentID.Valid {
				x := r.PaymentID.Int64
				return &x
			}
			return nil
		}(), Status: r.Status, RedeemedAt: time.Unix(r.RedeemedAt, 0), ExpiresAt: ts(r.ExpiresAt), Days: r.Days, Bytes: r.Bytes, DiscountAmount: r.DiscountAmount, OriginalAmount: r.OriginalAmount, FinalAmount: r.FinalAmount, Currency: r.Currency, Code: row.Code})
	}
	return o, nil
}
func (h *handlers) promocodeStats(ctx context.Context, _ *struct{}) (*promoStatsOutput, error) {
	st, e := h.d.Store.Q.PromoStats(ctx)
	if e != nil {
		return nil, e
	}
	n, e := h.d.Store.Q.CountPromoCodes(ctx)
	if e != nil {
		return nil, e
	}
	now := sql.NullInt64{Int64: h.d.Now().Unix(), Valid: true}
	active, e := h.d.Store.Q.CountActivePromoCodes(ctx, now)
	if e != nil {
		return nil, e
	}
	return &promoStatsOutput{Body: PromoStatsView{PromoCodes: n, Active: active, SuccessfulActivations: st.SuccessfulActivations, BonusDays: st.BonusDays, BonusBytes: st.BonusBytes, DiscountAmount: st.DiscountAmount, DiscountOrders: st.DiscountOrders}}, nil
}
