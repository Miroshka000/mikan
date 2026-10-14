package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

type PaymentSettingsView struct {
	Enabled            bool   `json:"enabled" doc:"Продажа подписок: выключено — бот и Mini App ничего не продают, новые счета не создаются, уже открытые засчитываются"`
	Stars              bool   `json:"stars" doc:"Telegram Stars: нужен только запущенный бот"`
	AllowNew           bool   `json:"allow_new" doc:"Новые люди могут купить подписку в боте; иначе — только продление"`
	RenewResetsTraffic bool   `json:"renew_resets_traffic" doc:"Оплаченное продление обнуляет трафик и начинает новый период; иначе только добавляет срок"`
	TrialTariffID      *int64 `json:"trial_tariff_id" doc:"Тариф пробного периода: один раз на Telegram-аккаунт без подписки и оплат, кнопка в приветствии бота; null — пробного периода нет. Работает и при выключенной продаже"`
	TrialStars         int64  `json:"trial_stars" doc:"Цена пробного периода в Stars; 0 вместе с trial_rub — бесплатно"`
	TrialRub           int64  `json:"trial_rub" doc:"Цена пробного периода в копейках; с ценой пробный период покупают один раз в боте и Mini App, нужна включённая продажа"`
	Trials             int64  `json:"trials" doc:"Сколько пробных подписок выдано"`
	FreeTariffID       *int64 `json:"free_tariff_id" doc:"Тариф бесплатного тарифа: кнопка в боте и Mini App, снова взять можно, когда прошлая бесплатная подписка аккаунта закончилась; null: бесплатного тарифа нет. Работает и при выключенной продаже"`
	FreeCondition      string `json:"free_condition" enum:"none,channel" doc:"Условие бесплатного тарифа: none: без условий, channel: подписка на канал бота (Telegram → Меню и тексты)"`
	FreePlans          int64  `json:"free_plans" doc:"Сколько бесплатных подписок выдано"`
	Available          struct {
		Stars  bool     `json:"stars"`
		Addons []string `json:"addons" doc:"Адаптеры маркетплейса, которые принимают оплату прямо сейчас"`
	} `json:"available" doc:"Что принимает оплату прямо сейчас: включено, настроено, для Stars — бот запущен"`
	OnSale int `json:"on_sale" doc:"Сколько тарифов бот может продать прямо сейчас: «В продаже» и с ценой для способа, который принимает оплату"`
}

type paymentSettingsOutput struct{ Body PaymentSettingsView }

type patchPaymentSettingsInput struct {
	Body struct {
		Enabled            *bool  `json:"enabled,omitempty"`
		Stars              *bool  `json:"stars,omitempty"`
		AllowNew           *bool  `json:"allow_new,omitempty"`
		RenewResetsTraffic *bool  `json:"renew_resets_traffic,omitempty"`
		TrialTariffID      *int64 `json:"trial_tariff_id,omitempty" minimum:"0" doc:"Тариф пробного периода; 0 — выключить"`
		// The bounds are domain.MaxPriceStars and domain.MaxPriceRub, which a tag cannot
		// name; the handler checks the prices against them.
		TrialStars *int64 `json:"trial_stars,omitempty" minimum:"0" maximum:"10000" doc:"Цена пробного периода в Stars; 0 — не за Stars. Без пробного периода (trial_tariff_id 0) обе цены сбрасываются"`
		TrialRub   *int64 `json:"trial_rub,omitempty" minimum:"0" maximum:"100000000" doc:"Цена пробного периода в копейках, от 100 (1 ₽); 0 — не за рубли. Обе 0 — пробный период бесплатный"`
		// The free plan: its tariff needs a term, as the trial's does, and the channel
		// condition a channel set up in the bot.
		FreeTariffID  *int64  `json:"free_tariff_id,omitempty" minimum:"0" doc:"Тариф бесплатного тарифа, нужен срок; 0: выключить"`
		FreeCondition *string `json:"free_condition,omitempty" enum:"none,channel" doc:"none: без условий; channel: только подписчикам канала бота, канал должен быть указан на странице Telegram"`
	}
}

type PaymentView struct {
	ID         int64      `json:"id"`
	Provider   string     `json:"provider" doc:"stars или addon:<id> — адаптер маркетплейса"`
	Kind       string     `json:"kind" enum:"new,renew,package,trial" doc:"package — пакет трафика: tariff_name — название пакета; trial — платный пробный период"`
	Status     string     `json:"status" enum:"pending,paid,applied,expired,failed,refunded"`
	TgID       int64      `json:"tg_id"`
	TgUsername string     `json:"tg_username,omitempty"`
	UserID     *int64     `json:"user_id,omitempty"`
	UserName   string     `json:"user_name,omitempty"`
	TariffName string     `json:"tariff_name"`
	TermDays   *int64     `json:"term_days,omitempty" doc:"Купленный срок в днях (0 — бессрочно); нет у пакетов и у платежей до сроков в тарифах"`
	Amount     int64      `json:"amount" doc:"Stars или копейки"`
	Currency   string     `json:"currency" enum:"XTR,RUB"`
	ExternalID string     `json:"external_id,omitempty" doc:"Номер платежа у провайдера"`
	Error      string     `json:"error,omitempty" doc:"Почему оплаченный платёж ещё не применён"`
	CreatedAt  time.Time  `json:"created_at"`
	PaidAt     *time.Time `json:"paid_at,omitempty"`
	AppliedAt  *time.Time `json:"applied_at,omitempty"`
	RefundedAt *time.Time `json:"refunded_at,omitempty"`
}

type PaymentTotal struct {
	Currency string `json:"currency" enum:"XTR,RUB"`
	Count    int64  `json:"count"`
	Total    int64  `json:"total"`
}

type listPaymentsInput struct {
	Status   string `query:"status" enum:"pending,paid,applied,expired,failed,refunded,"`
	Provider string `query:"provider" pattern:"^(stars|addon:[a-z0-9][a-z0-9-]{0,31})?$"`
	UserID   int64  `query:"user_id" minimum:"0"`
	Before   int64  `query:"before" minimum:"0" doc:"id последнего платежа предыдущей страницы"`
	Limit    int64  `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

type paymentsOutput struct {
	Body struct {
		Items  []PaymentView  `json:"items"`
		Totals []PaymentTotal `json:"totals" doc:"Применённые платежи за 30 дней"`
	}
}

type paymentOutput struct{ Body PaymentView }

func (h *handlers) registerPayments() {
	tags := []string{"payments"}
	op := sessionOps("payments")
	huma.Register(h.api, op("get-payment-settings", http.MethodGet, "/api/v1/payments/settings", "Настройки оплаты", 0), h.getPaymentSettings)
	huma.Register(h.api, op("update-payment-settings", http.MethodPatch, "/api/v1/payments/settings", "Изменить настройки оплаты", 0), h.updatePaymentSettings)
	huma.Register(h.api, huma.Operation{OperationID: "list-payments", Method: http.MethodGet, Path: "/api/v1/payments", Summary: "История платежей", Tags: tags}, h.listPayments)
	huma.Register(h.api, op("refund-payment", http.MethodPost, "/api/v1/payments/{id}/refund", "Вернуть Stars покупателю", 0), h.refundPayment)
	byHand := op("mark-payment-refunded", http.MethodPost, "/api/v1/payments/{id}/refunded-by-hand", "Отметить, что деньги возвращены вручную", 0)
	byHand.Description = "Оплата через способ оплаты из маркетплейса, оплаченная, но не применённая (например, пробный период, который уже нельзя выдать): админ вернул деньги в кабинете платёжной системы, платёж становится возвращённым, покупателю приходит сообщение. Stars возвращаются через refund."
	huma.Register(h.api, byHand, h.refundedByHand)
}

func (h *handlers) paymentSettings(ctx context.Context) (PaymentSettingsView, error) {
	c, err := h.d.Billing.LoadConfig(ctx)
	if err != nil {
		return PaymentSettingsView{}, err
	}
	v := PaymentSettingsView{Enabled: c.Enabled, Stars: c.Stars, AllowNew: c.AllowNew, RenewResetsTraffic: c.RenewResetsTraffic, TrialStars: c.TrialStars, TrialRub: c.TrialRub}
	if t, ok := h.d.Billing.TrialTariff(ctx); ok {
		v.TrialTariffID = &t.ID
	}
	if v.Trials, err = h.d.Store.Q.CountTrials(ctx); err != nil {
		return v, err
	}
	if t, ok := h.d.Billing.FreeTariff(ctx); ok {
		v.FreeTariffID = &t.ID
	}
	v.FreeCondition = freeConditionView(c.FreeCondition)
	if v.FreePlans, err = h.d.Store.Q.CountFreePlans(ctx); err != nil {
		return v, err
	}
	av := h.d.Billing.Available(ctx)
	v.Available.Stars, v.Available.Addons = av.Stars, av.Addons
	if v.Available.Addons == nil {
		v.Available.Addons = []string{}
	}
	offers, _, err := h.d.Billing.Offers(ctx)
	if err != nil {
		return v, err
	}
	v.OnSale = len(offers)
	return v, nil
}

func (h *handlers) getPaymentSettings(ctx context.Context, _ *struct{}) (*paymentSettingsOutput, error) {
	v, err := h.paymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &paymentSettingsOutput{Body: v}, nil
}

func (h *handlers) updatePaymentSettings(ctx context.Context, in *patchPaymentSettingsInput) (*paymentSettingsOutput, error) {
	b := in.Body
	if b.TrialTariffID != nil {
		if id := *b.TrialTariffID; id != 0 {
			t, err := h.d.Store.Q.GetTariff(ctx, id)
			if err != nil || t.Archived != 0 {
				return nil, fieldErr("body.trial_tariff_id", "tariff_not_found")
			}
			// A tariff without a term would make the free trial a subscription with no end.
			if t.DurationDays <= 0 {
				return nil, fieldErr("body.trial_tariff_id", "trial_no_term")
			}
		}
	}
	if b.FreeTariffID != nil {
		if id := *b.FreeTariffID; id != 0 {
			t, err := h.d.Store.Q.GetTariff(ctx, id)
			if err != nil || t.Archived != 0 {
				return nil, fieldErr("body.free_tariff_id", "tariff_not_found")
			}
			// Without a term a free subscription never ends, and the account never gets
			// another: the plan would be a trial with no end.
			if t.DurationDays <= 0 {
				return nil, fieldErr("body.free_tariff_id", "free_no_term")
			}
		}
	}
	if b.FreeCondition != nil && *b.FreeCondition == "channel" && (h.d.Gateway == nil || h.d.Gateway.ChannelConfig(ctx).Chat == "") {
		return nil, fieldErr("body.free_condition", "free_no_channel")
	}
	// The trial costs what a tariff may cost (domain.CheckPrices); 0 is no price at all,
	// and both prices at 0 make it free.
	if b.TrialStars != nil {
		if n := *b.TrialStars; n != 0 && !domain.PriceStarsOK(n) {
			return nil, fieldErr("body.trial_stars", "bad_price")
		}
	}
	if b.TrialRub != nil {
		if n := *b.TrialRub; n != 0 && !domain.PriceRubOK(n) {
			// A price under a ruble is the mistake worth its own words.
			code := "bad_price"
			if n < domain.MinPriceRub {
				code = "trial_rub_min"
			}
			return nil, fieldErr("body.trial_rub", code)
		}
	}
	// Read and written in one transaction: a change made meanwhile is not overwritten
	// with the fields this request did not name.
	var c billing.Config
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		var err error
		if c, err = billing.LoadConfig(ctx, set); err != nil {
			return err
		}
		for dst, v := range map[*bool]*bool{&c.Enabled: b.Enabled, &c.Stars: b.Stars, &c.AllowNew: b.AllowNew, &c.RenewResetsTraffic: b.RenewResetsTraffic} {
			if v != nil {
				*dst = *v
			}
		}
		if b.TrialTariffID != nil {
			c.TrialTariffID = *b.TrialTariffID
		}
		for dst, v := range map[*int64]*int64{&c.TrialStars: b.TrialStars, &c.TrialRub: b.TrialRub} {
			if v != nil {
				*dst = *v
			}
		}
		// No trial, no price, as in the form: a price kept for a trial turned off would
		// come back with the next one.
		if c.TrialTariffID == 0 {
			c.TrialStars, c.TrialRub = 0, 0
		}
		if b.FreeTariffID != nil {
			c.FreeTariffID = *b.FreeTariffID
		}
		if b.FreeCondition != nil {
			c.FreeCondition = billing.FreeNoCondition
			if *b.FreeCondition == "channel" {
				c.FreeCondition = billing.FreeChannel
			}
		}
		return settings.Set(ctx, set, billing.KeyConfig, c)
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "payments.settings", "", "", map[string]any{"enabled": c.Enabled, "stars": c.Stars,
		"allow_new": c.AllowNew, "renew_resets_traffic": c.RenewResetsTraffic, "trial_tariff_id": c.TrialTariffID,
		"trial_stars": c.TrialStars, "trial_rub": c.TrialRub, "free_tariff_id": c.FreeTariffID, "free_condition": freeConditionView(c.FreeCondition)})
	v, err := h.paymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &paymentSettingsOutput{Body: v}, nil
}

// refundedByHand closes a payment the admin refunded in the provider's dashboard: paid
// through an adapter and never applied (billing.RefundedByHand).
func (h *handlers) refundedByHand(ctx context.Context, in *userIDInput) (*paymentOutput, error) {
	err := h.d.Billing.RefundedByHand(ctx, in.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, huma.Error404NotFound("not_found")
	case errors.Is(err, billing.ErrNotRefunable):
		return nil, huma.Error409Conflict("not_refundable")
	case err != nil:
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "payment.refunded_by_hand", "payment", "", map[string]any{"id": in.ID})
	p, err := h.d.Store.Q.GetPayment(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &paymentOutput{Body: h.viewPayment(ctx, p)}, nil
}

// freeConditionView is the free plan's condition as the API names it: "none" for none.
func freeConditionView(c string) string {
	if c == billing.FreeChannel {
		return "channel"
	}
	return "none"
}

// paymentView is a payment without the names; the list's query brings those along.
func paymentView(p db.Payment) PaymentView {
	v := PaymentView{ID: p.ID, Provider: p.Provider, Kind: p.Kind, Status: p.Status, TgID: p.TgID, TariffName: p.TariffName, TermDays: store.Int64Ptr(p.TermDays), Amount: p.Amount,
		Currency: p.Currency, ExternalID: p.ExternalID.String, Error: p.Error, CreatedAt: time.Unix(p.CreatedAt, 0).UTC(),
		PaidAt: store.TimePtr(p.PaidAt), AppliedAt: store.TimePtr(p.AppliedAt), RefundedAt: store.TimePtr(p.RefundedAt)}
	if p.UserID.Valid {
		id := p.UserID.Int64
		v.UserID = &id
	}
	return v
}

// viewPayment is one payment with the user's and the buyer's names.
func (h *handlers) viewPayment(ctx context.Context, p db.Payment) PaymentView {
	v := paymentView(p)
	if p.UserID.Valid {
		if u, err := h.d.Store.Q.GetUser(ctx, p.UserID.Int64); err == nil {
			v.UserName = u.Name
		}
	}
	if c, err := h.d.Store.Q.GetTgChat(ctx, p.TgID); err == nil {
		v.TgUsername = c.Username
	}
	return v
}

func (h *handlers) listPayments(ctx context.Context, in *listPaymentsInput) (*paymentsOutput, error) {
	before := in.Before
	if before == 0 {
		before = 1 << 62
	}
	rows, err := h.d.Store.Q.ListPayments(ctx, db.ListPaymentsParams{BeforeID: before, Status: in.Status, Provider: in.Provider, UserID: in.UserID, Lim: in.Limit})
	if err != nil {
		return nil, err
	}
	out := &paymentsOutput{}
	out.Body.Items = make([]PaymentView, 0, len(rows))
	for _, r := range rows {
		v := paymentView(r.Payment)
		v.UserName, v.TgUsername = r.UserName, r.TgUsername
		out.Body.Items = append(out.Body.Items, v)
	}
	totals, err := h.d.Store.Q.PaymentTotals(ctx, sql.NullInt64{Int64: h.d.Now().Add(-30 * 24 * time.Hour).Unix(), Valid: true})
	if err != nil {
		return nil, err
	}
	out.Body.Totals = make([]PaymentTotal, 0, len(totals))
	for _, t := range totals {
		out.Body.Totals = append(out.Body.Totals, PaymentTotal{Currency: t.Currency, Count: t.N, Total: t.Total})
	}
	return out, nil
}

func (h *handlers) refundPayment(ctx context.Context, in *userIDInput) (*paymentOutput, error) {
	reverted, err := h.d.Billing.Refund(ctx, in.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, huma.Error404NotFound("not_found")
	case errors.Is(err, billing.ErrNotRefunable):
		return nil, huma.Error409Conflict("not_refundable")
	case errors.Is(err, billing.ErrRefundNotApplied):
		// The Stars are back with the buyer; the subscription is not yet taken back.
		h.d.Log.Warn("refund returned by Telegram but not applied", "payment", in.ID, "err", err)
		return nil, huma.Error502BadGateway("refund_not_applied")
	case err != nil:
		h.d.Log.Warn("refund", "payment", in.ID, "err", err)
		return nil, huma.Error502BadGateway("refund_failed")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "payment.refund", "payment", "", map[string]any{"id": in.ID, "reverted": reverted})
	p, err := h.d.Store.Q.GetPayment(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &paymentOutput{Body: h.viewPayment(ctx, p)}, nil
}
