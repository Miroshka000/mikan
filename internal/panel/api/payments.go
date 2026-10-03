package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

type PaymentSettingsView struct {
	Enabled            bool   `json:"enabled" doc:"Продажа подписок: выключено — бот и Mini App ничего не продают, новые счета не создаются, уже открытые засчитываются"`
	Stars              bool   `json:"stars" doc:"Telegram Stars: нужен только запущенный бот"`
	AllowNew           bool   `json:"allow_new" doc:"Новые люди могут купить подписку в боте; иначе — только продление"`
	RenewResetsTraffic bool   `json:"renew_resets_traffic" doc:"Оплаченное продление обнуляет трафик и начинает новый период; иначе только добавляет срок"`
	TrialTariffID      *int64 `json:"trial_tariff_id" doc:"Тариф пробного периода: один раз на Telegram-аккаунт без подписки и оплат, кнопка в приветствии бота; null — пробного периода нет. Работает и при выключенной продаже"`
	Trials             int64  `json:"trials" doc:"Сколько пробных подписок выдано"`
	Available          struct {
		Stars  bool     `json:"stars"`
		Addons []string `json:"addons" doc:"Адаптеры маркетплейса, которые принимают оплату прямо сейчас"`
	} `json:"available" doc:"Что принимает оплату прямо сейчас: включено, настроено, для Stars — бот запущен"`
	OnSale int `json:"on_sale" doc:"Сколько тарифов бот может продать прямо сейчас: «В продаже» и с ценой для способа, который принимает оплату"`
	// Moving: the built-in YooKassa and CryptoBot moved to the marketplace in 0.4.4; their
	// adapters are installed after the update.
	Moving []string `json:"moving" doc:"Встроенные ЮKassa и CryptoBot переехали в маркетплейс: адаптеры, которые сервер ещё ставит"`
}

type paymentSettingsOutput struct{ Body PaymentSettingsView }

type patchPaymentSettingsInput struct {
	Body struct {
		Enabled            *bool  `json:"enabled,omitempty"`
		Stars              *bool  `json:"stars,omitempty"`
		AllowNew           *bool  `json:"allow_new,omitempty"`
		RenewResetsTraffic *bool  `json:"renew_resets_traffic,omitempty"`
		TrialTariffID      *int64 `json:"trial_tariff_id,omitempty" minimum:"0" doc:"Тариф пробного периода; 0 — выключить"`
	}
}

type PaymentView struct {
	ID         int64      `json:"id"`
	Provider   string     `json:"provider" doc:"stars или addon:<id> — адаптер маркетплейса"`
	Kind       string     `json:"kind" enum:"new,renew,package" doc:"package — пакет трафика: tariff_name — название пакета"`
	Status     string     `json:"status" enum:"pending,paid,applied,expired,failed,refunded"`
	TgID       int64      `json:"tg_id"`
	TgUsername string     `json:"tg_username,omitempty"`
	UserID     *int64     `json:"user_id,omitempty"`
	UserName   string     `json:"user_name,omitempty"`
	TariffName string     `json:"tariff_name"`
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
	huma.Register(h.api, huma.Operation{OperationID: "get-payment-settings", Method: http.MethodGet, Path: "/api/v1/payments/settings", Summary: "Настройки оплаты", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.getPaymentSettings)
	huma.Register(h.api, huma.Operation{OperationID: "update-payment-settings", Method: http.MethodPatch, Path: "/api/v1/payments/settings", Summary: "Изменить настройки оплаты", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.updatePaymentSettings)
	huma.Register(h.api, huma.Operation{OperationID: "list-payments", Method: http.MethodGet, Path: "/api/v1/payments", Summary: "История платежей", Tags: tags}, h.listPayments)
	huma.Register(h.api, huma.Operation{OperationID: "refund-payment", Method: http.MethodPost, Path: "/api/v1/payments/{id}/refund", Summary: "Вернуть Stars покупателю", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.refundPayment)
}

func (h *handlers) paymentSettings(ctx context.Context) (PaymentSettingsView, error) {
	c, err := h.d.Billing.LoadConfig(ctx)
	if err != nil {
		return PaymentSettingsView{}, err
	}
	v := PaymentSettingsView{Enabled: c.Enabled, Stars: c.Stars, AllowNew: c.AllowNew, RenewResetsTraffic: c.RenewResetsTraffic}
	if t, ok := h.d.Billing.TrialTariff(ctx); ok {
		v.TrialTariffID = &t.ID
	}
	if v.Trials, err = h.d.Store.Q.CountTrials(ctx); err != nil {
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
	if v.Moving, err = h.d.Billing.Moving(ctx); err != nil {
		return v, err
	}
	if v.Moving == nil {
		v.Moving = []string{}
	}
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
	c, err := h.d.Billing.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	for dst, v := range map[*bool]*bool{&c.Enabled: b.Enabled, &c.Stars: b.Stars, &c.AllowNew: b.AllowNew, &c.RenewResetsTraffic: b.RenewResetsTraffic} {
		if v != nil {
			*dst = *v
		}
	}
	if b.TrialTariffID != nil {
		if id := *b.TrialTariffID; id != 0 {
			if t, err := h.d.Store.Q.GetTariff(ctx, id); err != nil || t.Archived != 0 {
				return nil, huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.trial_tariff_id", Message: "tariff_not_found"})
			}
		}
		c.TrialTariffID = *b.TrialTariffID
	}
	if err := settings.Set(ctx, h.d.Settings, billing.KeyConfig, c); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "payments.settings", "", "", map[string]any{"enabled": c.Enabled, "stars": c.Stars,
		"allow_new": c.AllowNew, "renew_resets_traffic": c.RenewResetsTraffic, "trial_tariff_id": c.TrialTariffID})
	v, err := h.paymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &paymentSettingsOutput{Body: v}, nil
}

func unixPtr(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.Unix(n.Int64, 0).UTC()
	return &t
}

// paymentView is a payment without the names; the list's query brings those along.
func paymentView(p db.Payment) PaymentView {
	v := PaymentView{ID: p.ID, Provider: p.Provider, Kind: p.Kind, Status: p.Status, TgID: p.TgID, TariffName: p.TariffName, Amount: p.Amount,
		Currency: p.Currency, ExternalID: p.ExternalID.String, Error: p.Error, CreatedAt: time.Unix(p.CreatedAt, 0).UTC(),
		PaidAt: unixPtr(p.PaidAt), AppliedAt: unixPtr(p.AppliedAt), RefundedAt: unixPtr(p.RefundedAt)}
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
	err := h.d.Billing.Refund(ctx, in.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, huma.Error404NotFound("not_found")
	case errors.Is(err, billing.ErrNotRefunable):
		return nil, huma.Error409Conflict("not_refundable")
	case err != nil:
		h.d.Log.Warn("refund", "payment", in.ID, "err", err)
		return nil, huma.Error502BadGateway("refund_failed")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "payment.refund", "payment", "", map[string]any{"id": in.ID})
	p, err := h.d.Store.Q.GetPayment(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &paymentOutput{Body: h.viewPayment(ctx, p)}, nil
}
