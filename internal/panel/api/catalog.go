package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

type TariffView struct {
	ID            int64       `json:"id"`
	Name          string      `json:"name"`
	TrafficLimit  *int64      `json:"traffic_limit" doc:"Байты; null — без лимита"`
	DurationDays  int64       `json:"duration_days" doc:"0 — бессрочно"`
	DeviceLimit   *int64      `json:"device_limit"`
	DeviceMode    string      `json:"device_mode" enum:"bound,online" doc:"Как считается лимит устройств: bound: привязанные устройства (места), online: любое число устройств, считаются только одновременно онлайн"`
	SpeedLimit    *int64      `json:"speed_limit" doc:"Мбит/с в каждую сторону; null — без ограничения"`
	ResetStrategy string      `json:"reset_strategy" enum:"none,month_start,period"`
	BillingDay    *int64      `json:"billing_day" doc:"День месяца, в который заканчивается срок; null — срок в днях"`
	PriceLabel    string      `json:"price_label"`
	Description   string      `json:"description" doc:"Текст для покупателей: чем тариф отличается, когда лимиты этого не показывают; до 300 символов, до 5 строк, пустой — только строка из лимитов"`
	PriceStars    *int64      `json:"price_stars" doc:"Цена в Telegram Stars; null — не продаётся за Stars"`
	PriceRub      *int64      `json:"price_rub" doc:"Цена в копейках (ЮKassa, CryptoBot); null — не продаётся за рубли"`
	OnSale        bool        `json:"on_sale" doc:"Продаётся в боте и Mini App"`
	Terms         []TermView  `json:"terms" doc:"Сроки, на которые продаётся тариф, по порядку; первый — тот же, что duration_days, price_stars и price_rub"`
	Pools         []PoolLimit `json:"pools" doc:"Лимиты пулов трафика и закрытые пулы; пул не в списке — без лимита. Лимиты действуют на новых пользователей и при продлении или смене тарифа; закрытие и открытие пула действуют сразу на всех пользователей тарифа: при открытии на них ставится лимит тарифа, ручной лимит пользователя затирается"`
	Sort          int64       `json:"sort"`
}

// TermView is one term a tariff is sold for.
type TermView struct {
	Days       int64  `json:"days" doc:"0 — бессрочно; с днём оплаты — месяцы по 30 дней"`
	PriceStars *int64 `json:"price_stars" doc:"Цена в Telegram Stars; null — не за Stars"`
	PriceRub   *int64 `json:"price_rub" doc:"Цена в копейках; null — не за рубли"`
}

func viewTariff(t db.Tariff, terms []db.TariffTerm) TariffView {
	v := TariffView{ID: t.ID, Name: t.Name, TrafficLimit: store.Int64Ptr(t.TrafficLimit),
		DurationDays: t.DurationDays, DeviceLimit: store.Int64Ptr(t.DeviceLimit), DeviceMode: t.DeviceMode,
		SpeedLimit: store.Int64Ptr(t.SpeedLimit), ResetStrategy: t.ResetStrategy, BillingDay: store.Int64Ptr(t.BillingDay), PriceLabel: t.PriceLabel, Description: t.Description, Sort: t.Sort,
		PriceStars: store.Int64Ptr(t.PriceStars), PriceRub: store.Int64Ptr(t.PriceRub), OnSale: t.OnSale != 0}
	for _, term := range domain.TariffTerms(t, terms) {
		v.Terms = append(v.Terms, TermView{Days: term.Days, PriceStars: store.Int64Ptr(term.PriceStars), PriceRub: store.Int64Ptr(term.PriceRub)})
	}
	return v
}

type termBody struct {
	Days       int64  `json:"days" minimum:"0" maximum:"3650" doc:"0 — бессрочно; с днём оплаты — месяцы по 30 дней"`
	PriceStars *int64 `json:"price_stars,omitempty" minimum:"1" maximum:"10000" doc:"Цена в Telegram Stars"`
	PriceRub   *int64 `json:"price_rub,omitempty" minimum:"100" maximum:"100000000" doc:"Цена в копейках: 19900 — 199 ₽"`
}

func (b termBody) term() domain.Term {
	return domain.Term{Days: b.Days, PriceStars: domain.NullInt(b.PriceStars), PriceRub: domain.NullInt(b.PriceRub)}
}

// A tariff is sold for at most 12 terms: the maxItems tag of terms is the limit.
type tariffBody struct {
	Name          string      `json:"name" minLength:"1" maxLength:"60"`
	TrafficLimit  *int64      `json:"traffic_limit,omitempty" minimum:"1"`
	DurationDays  int64       `json:"duration_days" minimum:"0" maximum:"3650"`
	DeviceLimit   *int64      `json:"device_limit,omitempty" minimum:"1" maximum:"100"`
	DeviceMode    string      `json:"device_mode,omitempty" enum:"bound,online" doc:"bound: лимит это число привязанных устройств; online: привязывать можно любое число, считаются устройства, одновременно подключённые к серверам. Не передан: у нового тарифа bound, у существующего без изменений"`
	SpeedLimit    *int64      `json:"speed_limit,omitempty" minimum:"1" maximum:"100000" doc:"Скорость, Мбит/с в каждую сторону; не передана — без ограничения"`
	ResetStrategy string      `json:"reset_strategy" enum:"none,month_start,period" default:"none"`
	BillingDay    *int64      `json:"billing_day,omitempty" minimum:"1" maximum:"31" doc:"Срок до этого числа месяца: месяц = от дня оплаты до дня оплаты"`
	PriceLabel    string      `json:"price_label,omitempty" maxLength:"40"`
	Description   string      `json:"description,omitempty" doc:"Текст для покупателей, виден в боте и Mini App рядом со строкой из лимитов: чем тариф отличается, когда лимиты этого не показывают («с белыми списками»). Простой текст до 300 символов и до 5 строк; не передан или пустой — только строка из лимитов"`
	PriceStars    *int64      `json:"price_stars,omitempty" minimum:"1" maximum:"10000" doc:"Цена в Telegram Stars"`
	PriceRub      *int64      `json:"price_rub,omitempty" minimum:"100" maximum:"100000000" doc:"Цена в копейках: 19900 — 199 ₽"`
	OnSale        bool        `json:"on_sale,omitempty" doc:"Продавать в боте и Mini App; нужна хотя бы одна цена"`
	Terms         []termBody  `json:"terms,omitempty" maxItems:"12" doc:"Все сроки по порядку, когда тариф продаётся на несколько; тогда duration_days, price_stars и price_rub берутся из первого. Не передан — первый срок из duration_days, price_stars и price_rub, остальные без изменений"`
	Pools         []PoolLimit `json:"pools,omitempty" maxItems:"100" doc:"Лимиты пулов трафика; не передан — без изменений"`
	Sort          int64       `json:"sort,omitempty"`
}

type tariffInput struct{ Body tariffBody }
type tariffUpdateInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body tariffBody
}
type tariffOutput struct{ Body TariffView }
type tariffsOutput struct{ Body []TariffView }

func (h *handlers) registerCatalog() {
	huma.Register(h.api, huma.Operation{OperationID: "list-tariffs", Method: http.MethodGet, Path: "/api/v1/tariffs", Summary: "Тарифы", Tags: []string{"tariffs"}}, h.listTariffs)
	huma.Register(h.api, huma.Operation{OperationID: "create-tariff", Method: http.MethodPost, Path: "/api/v1/tariffs", Summary: "Создать тариф", Tags: []string{"tariffs"}, DefaultStatus: http.StatusCreated}, h.createTariff)
	huma.Register(h.api, huma.Operation{OperationID: "update-tariff", Method: http.MethodPut, Path: "/api/v1/tariffs/{id}", Summary: "Изменить тариф", Tags: []string{"tariffs"}}, h.updateTariff)
	huma.Register(h.api, huma.Operation{OperationID: "archive-tariff", Method: http.MethodDelete, Path: "/api/v1/tariffs/{id}", Summary: "Убрать тариф в архив", Tags: []string{"tariffs"}, DefaultStatus: http.StatusNoContent}, h.archiveTariff)
	huma.Register(h.api, huma.Operation{OperationID: "apply-tariff-pools", Method: http.MethodPost, Path: "/api/v1/tariffs/{id}/apply-pools", Summary: "Применить лимиты пулов тарифа к уже выданным подпискам: свои лимиты пулов у клиентов заменятся, израсходованное останется", Tags: []string{"tariffs"}}, h.applyTariffPools)
}

func (h *handlers) listTariffs(ctx context.Context, _ *struct{}) (*tariffsOutput, error) {
	rows, err := h.d.Store.Q.ListTariffs(ctx)
	if err != nil {
		return nil, err
	}
	pools, err := h.d.Store.Q.ListAllTariffPools(ctx)
	if err != nil {
		return nil, err
	}
	terms, err := h.d.Store.Q.ListAllTariffTerms(ctx)
	if err != nil {
		return nil, err
	}
	out := &tariffsOutput{Body: make([]TariffView, 0, len(rows))}
	for _, t := range rows {
		v := viewTariff(t, terms)
		v.Pools = tariffPoolsOf(pools, t.ID)
		out.Body = append(out.Body, v)
	}
	return out, nil
}

// createTariff and updateTariff write the tariff and its pool limits in one transaction: a
// refused pool list leaves the tariff, and the limits it had, as they were.
func (h *handlers) createTariff(ctx context.Context, in *tariffInput) (*tariffOutput, error) {
	b := in.Body
	terms, err := b.terms(nil)
	if err != nil {
		return nil, err
	}
	description, err := cleanDescription(b.Description)
	if err != nil {
		return nil, err
	}
	first := terms[0]
	var t db.Tariff
	// The catalog holds no invariant across rows (a pool deleted meanwhile fails on its
	// foreign key): READ COMMITTED.
	err = h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		var err error
		t, err = q.CreateTariff(ctx, db.CreateTariffParams{Name: strings.TrimSpace(b.Name), TrafficLimit: domain.NullInt(b.TrafficLimit),
			DurationDays: first.Days, DeviceLimit: domain.NullInt(b.DeviceLimit), ResetStrategy: b.ResetStrategy, PriceLabel: b.PriceLabel,
			Sort: b.Sort, CreatedAt: h.d.Now().Unix(), BillingDay: domain.NullInt(b.BillingDay), PriceStars: first.PriceStars, PriceRub: first.PriceRub, OnSale: domain.Flag(b.OnSale),
			SpeedLimit: domain.NullInt(b.SpeedLimit), Description: description})
		if err != nil {
			return err
		}
		if err := setTariffMode(ctx, q, &t, b.DeviceMode); err != nil {
			return err
		}
		if err := domain.SetTariffTerms(ctx, q, t.ID, terms); err != nil || b.Pools == nil {
			return err
		}
		return setTariffPools(ctx, q, t.ID, b.Pools)
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "tariff.create", "tariff", strconv.FormatInt(t.ID, 10), nil)
	return h.tariffOut(ctx, t)
}

func (h *handlers) updateTariff(ctx context.Context, in *tariffUpdateInput) (*tariffOutput, error) {
	b := in.Body
	description, err := cleanDescription(b.Description)
	if err != nil {
		return nil, err
	}
	var t db.Tariff
	err = h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		// Without terms in the body the tariff keeps the ones after the first.
		var kept []domain.Term
		if b.Terms == nil {
			rows, err := q.ListTariffTerms(ctx, in.ID)
			if err != nil {
				return err
			}
			for _, r := range rows {
				kept = append(kept, domain.Term{Days: r.Days, PriceStars: r.PriceStars, PriceRub: r.PriceRub})
			}
		}
		terms, err := b.terms(kept)
		if err != nil {
			return err
		}
		first := terms[0]
		t, err = q.UpdateTariff(ctx, db.UpdateTariffParams{Name: strings.TrimSpace(b.Name), TrafficLimit: domain.NullInt(b.TrafficLimit),
			DurationDays: first.Days, DeviceLimit: domain.NullInt(b.DeviceLimit), ResetStrategy: b.ResetStrategy, PriceLabel: b.PriceLabel,
			Sort: b.Sort, BillingDay: domain.NullInt(b.BillingDay), PriceStars: first.PriceStars, PriceRub: first.PriceRub, OnSale: domain.Flag(b.OnSale), ID: in.ID,
			SpeedLimit: domain.NullInt(b.SpeedLimit), Description: description})
		if err != nil {
			return err
		}
		if err := setTariffMode(ctx, q, &t, b.DeviceMode); err != nil {
			return err
		}
		if err := domain.SetTariffTerms(ctx, q, t.ID, terms); err != nil || b.Pools == nil {
			return err
		}
		return setTariffPools(ctx, q, t.ID, b.Pools)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	if in.Body.Pools != nil {
		h.d.Changes.PoliciesChanged() // a pool closed or opened for the tariff's users
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "tariff.update", "tariff", strconv.FormatInt(t.ID, 10), nil)
	return h.tariffOut(ctx, t)
}

// setTariffMode writes the mode the body names on the tariff t (just written) and in t; an
// empty one leaves the tariff's own, which a new tariff has as "bound".
func setTariffMode(ctx context.Context, q *db.Queries, t *db.Tariff, mode string) error {
	if mode == "" || mode == t.DeviceMode {
		return nil
	}
	if !domain.ValidDeviceMode(mode) {
		return huma.Error422UnprocessableEntity("bad_device_mode", &huma.ErrorDetail{Location: "body.device_mode", Message: "bad_device_mode"})
	}
	if err := q.SetTariffDeviceMode(ctx, db.SetTariffDeviceModeParams{DeviceMode: mode, ID: t.ID}); err != nil {
		return err
	}
	t.DeviceMode = mode
	return nil
}

func (h *handlers) archiveTariff(ctx context.Context, in *userIDInput) (*struct{}, error) {
	n, err := h.d.Store.Q.ArchiveTariff(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "tariff.archive", "tariff", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

type applyPoolsOutput struct {
	Body struct {
		Applied int `json:"applied" doc:"У скольких клиентов тарифа обновлены лимиты пулов"`
	}
}

func (h *handlers) applyTariffPools(ctx context.Context, in *userIDInput) (*applyPoolsOutput, error) {
	n, err := h.d.Users.ApplyTariffPoolsToAll(ctx, in.ID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "tariff.apply_pools", "tariff", strconv.FormatInt(in.ID, 10), map[string]any{"users": n})
	out := &applyPoolsOutput{}
	out.Body.Applied = n
	return out, nil
}

// cleanDescription is the tariff's description as stored; a text that is too long or has
// too many lines is refused under the field's name, like the other checks of the body.
func cleanDescription(s string) (string, error) {
	out, err := domain.CleanDescription(s)
	var fe *domain.FieldError
	if errors.As(err, &fe) {
		return "", fieldErr("body."+fe.Field, fe.Code)
	}
	return out, err
}

// terms are the tariff's terms after this body: the body's own, or the first from its
// duration and prices followed by kept (the tariff's stored terms) after their first.
// Days tell the terms apart (months, with a billing day: lengths that round to the same
// number of months are one term); with several, each needs a price, and a tariff on sale
// needs one at all.
func (b tariffBody) terms(kept []domain.Term) ([]domain.Term, error) {
	var terms []domain.Term
	loc := "body.terms[%d]"
	if b.Terms != nil {
		if len(b.Terms) == 0 {
			return nil, fieldErr("body.terms", "terms_empty")
		}
		for _, t := range b.Terms {
			terms = append(terms, t.term())
		}
	} else {
		terms = []domain.Term{{Days: b.DurationDays, PriceStars: domain.NullInt(b.PriceStars), PriceRub: domain.NullInt(b.PriceRub)}}
		if len(kept) > 1 {
			terms = append(terms, kept[1:]...)
		}
		loc = "body.duration_days"
	}
	at := func(i int, field string) string {
		if b.Terms == nil {
			return loc
		}
		return fmt.Sprintf(loc, i) + "." + field
	}
	seen := map[int64]bool{}
	for i, t := range terms {
		key := t.Days
		if b.BillingDay != nil && t.Days > 0 {
			key = int64(domain.TermMonths(t.Days))
		}
		if seen[key] {
			return nil, fieldErr(at(i, "days"), "term_days_repeat")
		}
		seen[key] = true
		if len(terms) > 1 && !t.PriceStars.Valid && !t.PriceRub.Valid {
			return nil, fieldErr(at(i, "price_rub"), "term_no_price")
		}
	}
	if b.OnSale && !terms[0].PriceStars.Valid && !terms[0].PriceRub.Valid {
		return nil, fieldErr("body.on_sale", "on_sale_no_price")
	}
	return terms, nil
}

func (h *handlers) tariffOut(ctx context.Context, t db.Tariff) (*tariffOutput, error) {
	pools, err := h.d.Store.Q.ListTariffPools(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	terms, err := h.d.Store.Q.ListTariffTerms(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	v := viewTariff(t, terms)
	v.Pools = tariffPoolsOf(pools, t.ID)
	return &tariffOutput{Body: v}, nil
}
