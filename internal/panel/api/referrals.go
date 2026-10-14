package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/referral"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// ReferralView is the referral programme as the admin panel shows it: the setup, what it
// has earned so far, and how many accounts hold a balance the panel would have to pay out.
type ReferralView struct {
	Config referral.Program    `json:"config"`
	Stats  referral.AdminStats `json:"stats"`
}

type ReferralRewardView struct {
	ID           int64      `json:"id"`
	ReferrerTgID int64      `json:"referrer_tg_id"`
	ReferrerName string     `json:"referrer_name,omitempty"`
	ReferralID   int64      `json:"referral_id"`
	PaymentID    *int64     `json:"payment_id,omitempty"`
	RuleID       string     `json:"rule_id"`
	Kind         string     `json:"kind" enum:"percent,days"`
	Amount       int64      `json:"amount" doc:"Копейки на баланс; 0 у правила днями"`
	Days         int64      `json:"days"`
	EveryN       int64      `json:"every_n"`
	CreatedAt    time.Time  `json:"created_at"`
	RevertedAt   *time.Time `json:"reverted_at,omitempty" doc:"Возврат платежа забрал начисление"`
}

type ReferralLinkView struct {
	ReferralID   int64      `json:"referral_id"`
	ReferrerTgID int64      `json:"referrer_tg_id"`
	ReferrerCode string     `json:"referrer_code"`
	RefereeTgID  int64      `json:"referee_tg_id"`
	RefereeName  string     `json:"referee_name,omitempty"`
	Code         string     `json:"code"`
	CreatedAt    time.Time  `json:"created_at"`
	FirstPaidAt  *time.Time `json:"first_paid_at,omitempty" doc:"Не пусто — реферал уже оплатил подписку"`
}

type referralsOutput struct{ Body ReferralView }

type patchReferralsInput struct {
	Body referral.Program
}

type referralRewardsInput struct {
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}

type referralRewardsOutput struct {
	Body struct {
		Items []ReferralRewardView `json:"items"`
		Total int64                `json:"total"`
	}
}

type referralLinksInput struct {
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}

type referralLinksOutput struct {
	Body struct {
		Items []ReferralLinkView `json:"items"`
		Total int64              `json:"total"`
	}
}

func (h *handlers) registerReferrals() {
	tags := []string{"referrals"}
	huma.Register(h.api, huma.Operation{OperationID: "get-referrals", Method: http.MethodGet, Path: "/api/v1/referrals", Summary: "Реферальная программа", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.getReferrals)
	huma.Register(h.api, huma.Operation{OperationID: "update-referrals", Method: http.MethodPatch, Path: "/api/v1/referrals", Summary: "Изменить реферальную программу", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.updateReferrals)
	huma.Register(h.api, huma.Operation{OperationID: "list-referral-rewards", Method: http.MethodGet, Path: "/api/v1/referrals/rewards", Summary: "Начисления по реферальной программе", Tags: tags}, h.listReferralRewards)
	huma.Register(h.api, huma.Operation{OperationID: "list-referral-links", Method: http.MethodGet, Path: "/api/v1/referrals/links", Summary: "Приглашения и приглашённые", Tags: tags}, h.listReferralLinks)
}

func (h *handlers) getReferrals(ctx context.Context, _ *struct{}) (*referralsOutput, error) {
	if h.d.Referral == nil {
		return nil, huma.Error503ServiceUnavailable("referral_unavailable")
	}
	cfg, stats, err := h.d.Referral.View(ctx)
	if err != nil {
		return nil, err
	}
	return &referralsOutput{Body: ReferralView{Config: cfg, Stats: stats}}, nil
}

func (h *handlers) updateReferrals(ctx context.Context, in *patchReferralsInput) (*referralsOutput, error) {
	if h.d.Referral == nil {
		return nil, huma.Error503ServiceUnavailable("referral_unavailable")
	}
	cfg := in.Body
	if err := cfg.Validate(); err != nil {
		return nil, huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.rules", Message: err.Error()})
	}
	if err := h.checkReferralTariffs(ctx, cfg); err != nil {
		return nil, err
	}
	if err := settings.Set(ctx, h.d.Settings, referral.KeyConfig, cfg); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "referral.update", "referral", "", map[string]any{"enabled": cfg.Enabled, "rules": len(cfg.Rules), "spend": cfg.Spend})
	cfg, stats, err := h.d.Referral.View(ctx)
	if err != nil {
		return nil, err
	}
	return &referralsOutput{Body: ReferralView{Config: cfg, Stats: stats}}, nil
}

// checkReferralTariffs refuses a rule naming a tariff that is not there: the rule would
// never pay and the admin would wait for a bonus that cannot come.
func (h *handlers) checkReferralTariffs(ctx context.Context, cfg referral.Program) error {
	for _, r := range cfg.Rules {
		for _, id := range r.TariffIDs {
			if _, err := h.d.Store.Q.GetTariff(ctx, id); err != nil {
				if !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				return huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.rules", Message: "tariff_not_found"})
			}
		}
	}
	return nil
}

func (h *handlers) listReferralRewards(ctx context.Context, in *referralRewardsInput) (*referralRewardsOutput, error) {
	if h.d.Referral == nil {
		return nil, huma.Error503ServiceUnavailable("referral_unavailable")
	}
	rows, err := h.d.Store.Q.ListReferralRewards(ctx, db.ListReferralRewardsParams{Lim: in.Limit, RowOffset: in.Offset})
	if err != nil {
		return nil, err
	}
	totals, err := h.d.Store.Q.CountReferralRewards(ctx)
	if err != nil {
		return nil, err
	}
	out := &referralRewardsOutput{}
	out.Body.Total = totals.N
	out.Body.Items = make([]ReferralRewardView, 0, len(rows))
	for _, r := range rows {
		v := ReferralRewardView{ID: r.ID, ReferrerTgID: r.ReferrerTgID, ReferralID: r.ReferralID, RuleID: r.RuleID, Kind: r.Kind,
			Amount: r.Amount, Days: r.Days, EveryN: r.EveryN, CreatedAt: time.Unix(r.CreatedAt, 0).UTC(), RevertedAt: ts(r.RevertedAt)}
		if r.ReferrerUsername != "" {
			v.ReferrerName = "@" + r.ReferrerUsername
		} else {
			v.ReferrerName = r.ReferrerName
		}
		if r.PaymentID.Valid {
			id := r.PaymentID.Int64
			v.PaymentID = &id
		}
		out.Body.Items = append(out.Body.Items, v)
	}
	return out, nil
}

func (h *handlers) listReferralLinks(ctx context.Context, in *referralLinksInput) (*referralLinksOutput, error) {
	if h.d.Referral == nil {
		return nil, huma.Error503ServiceUnavailable("referral_unavailable")
	}
	rows, err := h.d.Store.Q.ListRecentReferrals(ctx, db.ListRecentReferralsParams{Lim: in.Limit, RowOffset: in.Offset})
	if err != nil {
		return nil, err
	}
	totals, err := h.d.Store.Q.CountReferrals(ctx)
	if err != nil {
		return nil, err
	}
	out := &referralLinksOutput{}
	out.Body.Total = totals.Total
	out.Body.Items = make([]ReferralLinkView, 0, len(rows))
	for _, r := range rows {
		out.Body.Items = append(out.Body.Items, ReferralLinkView{ReferralID: r.ID, ReferrerTgID: r.ReferrerTgID, ReferrerCode: r.ReferrerCode,
			RefereeTgID: r.RefereeTgID, RefereeName: r.RefereeUsername, Code: r.Code,
			CreatedAt: time.Unix(r.CreatedAt, 0).UTC(), FirstPaidAt: ts(r.FirstPaidAt)})
	}
	return out, nil
}
