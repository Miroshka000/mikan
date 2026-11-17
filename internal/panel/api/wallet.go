package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The accounts' balances and their top-ups by hand: the admin sees each request with its
// screenshot and approves or rejects it; an account's balance may be corrected by hand.

type TopupView struct {
	ID         int64      `json:"id"`
	TgID       int64      `json:"tg_id"`
	TgUsername string     `json:"tg_username,omitempty"`
	Amount     int64      `json:"amount" doc:"В копейках (сотых) валюты"`
	Currency   string     `json:"currency"`
	Status     string     `json:"status" enum:"pending,approved,rejected"`
	Photo      bool       `json:"photo" doc:"Есть скриншот: GET /api/v1/topups/{id}/photo"`
	Reason     string     `json:"reason,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	DecidedAt  *time.Time `json:"decided_at,omitempty"`
	Balance    int64      `json:"balance" doc:"Баланс аккаунта сейчас"`
}

type BalanceOpView struct {
	ID        int64     `json:"id"`
	Amount    int64     `json:"amount" doc:"+ пришло, − ушло"`
	Kind      string    `json:"kind" enum:"topup,spend,refund,adjust"`
	PaymentID *int64    `json:"payment_id,omitempty"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ImportedPaymentView is a transaction of the account in a sales bot it came from.
type ImportedPaymentView struct {
	Source      string    `json:"source" doc:"Откуда: bedolaga"`
	Kind        string    `json:"kind" doc:"Тип в боте: deposit, subscription_payment, refund, withdrawal, referral_reward и другие"`
	Amount      int64     `json:"amount" doc:"В копейках (сотых) валюты, как в боте"`
	Currency    string    `json:"currency"`
	Method      string    `json:"method,omitempty" doc:"Способ оплаты в боте"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type BalanceView struct {
	TgID     int64                 `json:"tg_id"`
	Balance  int64                 `json:"balance"`
	Currency string                `json:"currency"`
	Ops      []BalanceOpView       `json:"ops"`
	Imported []ImportedPaymentView `json:"imported" doc:"История из бота продаж, откуда перенесён аккаунт: последние 50; это не платежи mikan"`
}

type listTopupsInput struct {
	Status string `query:"status" enum:"pending,approved,rejected,"`
	Before int64  `query:"before" minimum:"0" doc:"Следующая страница: заявки старше этой (id последней показанной)"`
	Limit  int32  `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

type topupsOutput struct {
	Body struct {
		Items   []TopupView `json:"items"`
		Pending int64       `json:"pending"`
	}
}

type topupOutput struct{ Body TopupView }

type decideTopupInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Approve bool   `json:"approve"`
		Reason  string `json:"reason,omitempty" maxLength:"200" doc:"Почему отклонено: увидит клиент"`
	}
}

type topupIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type walletInput struct {
	TgID int64 `path:"tg_id" minimum:"1"`
}

type adjustBalanceInput struct {
	TgID int64 `path:"tg_id" minimum:"1"`
	Body struct {
		Delta int64  `json:"delta" minimum:"-1000000000" maximum:"1000000000" doc:"На сколько изменить, в копейках (сотых) валюты; минус — списать"`
		Note  string `json:"note" minLength:"1" maxLength:"200" doc:"За что: видит клиент в истории баланса"`
	}
}

type walletOutput struct{ Body BalanceView }

func (h *handlers) registerWallet() {
	tags := []string{"payments"}
	op := sessionOps("payments")
	huma.Register(h.api, huma.Operation{OperationID: "list-topups", Method: http.MethodGet, Path: "/api/v1/topups", Summary: "Заявки на пополнение баланса", Tags: tags}, h.listTopups)
	huma.Register(h.api, op("decide-topup", http.MethodPost, "/api/v1/topups/{id}/decide", "Одобрить или отклонить пополнение", 0), h.decideTopup)
	huma.Register(h.api, huma.Operation{OperationID: "get-balance", Method: http.MethodGet, Path: "/api/v1/balances/{tg_id}", Summary: "Баланс Telegram-аккаунта и история", Tags: tags}, h.getBalance)
	huma.Register(h.api, op("adjust-balance", http.MethodPost, "/api/v1/balances/{tg_id}/adjust", "Изменить баланс вручную", 0), h.adjustBalance)
	// The screenshot is a picture, fetched from Telegram with the bot's own token.
	huma.Register(h.api, huma.Operation{OperationID: "get-topup-photo", Method: http.MethodGet, Path: "/api/v1/topups/{id}/photo", Summary: "Скриншот перевода",
		Tags: tags, Responses: map[string]*huma.Response{"200": {Description: "Картинка", Content: map[string]*huma.MediaType{"image/*": {}}}}}, h.topupPhoto)
}

func (h *handlers) viewTopup(ctx context.Context, t db.Topup) TopupView {
	v := TopupView{ID: t.ID, TgID: t.TgID, Amount: t.Amount, Currency: t.Currency, Status: t.Status, Photo: t.Photo != "", Reason: t.Reason,
		CreatedAt: store.Time(t.CreatedAt), DecidedAt: store.TimePtr(t.DecidedAt)}
	if c, err := h.d.Store.Q.GetTgChat(ctx, t.TgID); err == nil {
		v.TgUsername = c.Username
	}
	v.Balance, _ = h.d.Billing.BalanceOf(ctx, t.TgID)
	return v
}

func (h *handlers) listTopups(ctx context.Context, in *listTopupsInput) (*topupsOutput, error) {
	rows, err := h.d.Store.Q.ListTopups(ctx, db.ListTopupsParams{Status: in.Status, Before: in.Before, Lim: in.Limit})
	if err != nil {
		return nil, err
	}
	out := &topupsOutput{}
	out.Body.Items = make([]TopupView, 0, len(rows))
	for _, t := range rows {
		out.Body.Items = append(out.Body.Items, h.viewTopup(ctx, t))
	}
	if out.Body.Pending, err = h.d.Store.Q.CountPendingTopups(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *handlers) decideTopup(ctx context.Context, in *decideTopupInput) (*topupOutput, error) {
	admin := sessionOf(ctx).AdminID
	reason := strings.TrimSpace(in.Body.Reason)
	t, balance, err := h.d.Billing.DecideTopup(ctx, in.ID, in.Body.Approve, admin, reason)
	switch {
	case errors.Is(err, billing.ErrTopupGone):
		return nil, huma.Error409Conflict("topup_decided")
	case errors.Is(err, billing.ErrTopupCurrency):
		return nil, huma.Error409Conflict("topup_currency")
	case err != nil:
		return nil, err
	}
	h.audit(ctx, admin, "balance.topup", "tg", strconv.FormatInt(t.TgID, 10), map[string]any{"topup": t.ID, "status": t.Status, "amount": t.Amount,
		"currency": t.Currency, "balance": balance, "reason": reason})
	return &topupOutput{Body: h.viewTopup(ctx, t)}, nil
}

func (h *handlers) balanceView(ctx context.Context, tgID int64) (BalanceView, error) {
	v := BalanceView{TgID: tgID, Currency: h.d.Billing.Config(ctx).Balance.Currency, Ops: []BalanceOpView{}, Imported: []ImportedPaymentView{}}
	var err error
	if v.Balance, err = h.d.Billing.BalanceOf(ctx, tgID); err != nil {
		return v, err
	}
	ops, err := h.d.Billing.BalanceHistory(ctx, tgID, 50)
	if err != nil {
		return v, err
	}
	for _, op := range ops {
		v.Ops = append(v.Ops, BalanceOpView{ID: op.ID, Amount: op.Amount, Kind: op.Kind, PaymentID: store.Int64Ptr(op.PaymentID), Note: op.Note, CreatedAt: store.Time(op.CreatedAt)})
	}
	old, err := h.d.Store.Q.ListImportedPayments(ctx, db.ListImportedPaymentsParams{TgID: tgID, Limit: 50})
	if err != nil {
		return v, err
	}
	for _, p := range old {
		v.Imported = append(v.Imported, ImportedPaymentView{Source: p.Source, Kind: p.Kind, Amount: p.Amount, Currency: p.Currency, Method: p.Method,
			Description: p.Description, CreatedAt: store.Time(p.CreatedAt)})
	}
	return v, nil
}

func (h *handlers) getBalance(ctx context.Context, in *walletInput) (*walletOutput, error) {
	v, err := h.balanceView(ctx, in.TgID)
	if err != nil {
		return nil, err
	}
	return &walletOutput{Body: v}, nil
}

func (h *handlers) adjustBalance(ctx context.Context, in *adjustBalanceInput) (*walletOutput, error) {
	admin := sessionOf(ctx).AdminID
	note := strings.TrimSpace(in.Body.Note)
	if note == "" {
		return nil, fieldErr("body.note", "required")
	}
	balance, err := h.d.Billing.AdjustBalance(ctx, in.TgID, in.Body.Delta, admin, note)
	switch {
	case errors.Is(err, billing.ErrBalanceLow):
		return nil, fieldErr("body.delta", "balance_low")
	case err != nil:
		return nil, err
	}
	h.audit(ctx, admin, "balance.adjust", "tg", strconv.FormatInt(in.TgID, 10), map[string]any{"delta": in.Body.Delta, "balance": balance, "note": note})
	v, err := h.balanceView(ctx, in.TgID)
	if err != nil {
		return nil, err
	}
	return &walletOutput{Body: v}, nil
}

// topupPhoto is a top-up's screenshot as Telegram keeps it.
func (h *handlers) topupPhoto(ctx context.Context, in *topupIDInput) (*subImageBytes, error) {
	// A transfer's screenshot names the payer and their card: no mask reads a picture.
	if hidesPeople(ctx) {
		return nil, huma.Error403Forbidden("support_denied")
	}
	t, err := h.d.Store.Q.GetTopup(ctx, in.ID)
	if err != nil || t.Photo == "" {
		return nil, huma.Error404NotFound("not_found")
	}
	if h.d.Gateway == nil || h.d.Gateway.Client() == nil {
		return nil, huma.Error409Conflict("bot_off")
	}
	raw, err := h.d.Gateway.Client().File(ctx, t.Photo)
	if err != nil {
		h.d.Log.Warn("topup photo", "topup", in.ID, "err", err)
		return nil, huma.Error502BadGateway("telegram_unavailable")
	}
	ct := http.DetectContentType(raw)
	if !strings.HasPrefix(ct, "image/") {
		return nil, huma.Error502BadGateway("not_a_picture")
	}
	return &subImageBytes{ContentType: ct, CacheControl: "private, max-age=3600", Nosniff: "nosniff", Body: raw}, nil
}
