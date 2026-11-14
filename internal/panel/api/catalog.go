package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/model"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

type TariffView struct {
	ID            int64         `json:"id"`
	Name          string        `json:"name"`
	TrafficLimit  *int64        `json:"traffic_limit" doc:"Байты; null — без лимита"`
	DurationDays  int64         `json:"duration_days" doc:"0 — бессрочно"`
	DeviceLimit   *int64        `json:"device_limit"`
	DeviceMode    string        `json:"device_mode" enum:"bound,online" doc:"Как считается лимит устройств: bound: привязанные устройства (места), online: любое число устройств, считаются только одновременно онлайн"`
	SpeedLimit    *int64        `json:"speed_limit" doc:"Мбит/с в каждую сторону; null — без ограничения"`
	ResetStrategy string        `json:"reset_strategy" enum:"none,month_start,period"`
	BillingDay    *int64        `json:"billing_day" doc:"День месяца, в который заканчивается срок; null — срок в днях"`
	PriceLabel    string        `json:"price_label"`
	Description   string        `json:"description" doc:"Текст для покупателей: чем тариф отличается, когда лимиты этого не показывают; до 300 символов, до 5 строк, пустой — только строка из лимитов"`
	PriceStars    *int64        `json:"price_stars" doc:"Цена в Telegram Stars; null — не продаётся за Stars"`
	PriceRub      *int64        `json:"price_rub" doc:"Цена в копейках (ЮKassa, CryptoBot); null — не продаётся за рубли"`
	OnSale        bool          `json:"on_sale" doc:"Продаётся в боте и Mini App"`
	Terms         []TermView    `json:"terms" doc:"Сроки, на которые продаётся тариф, по порядку; первый — тот же, что duration_days, price_stars и price_rub"`
	ExtraDevices  ExtraDevices  `json:"extra_devices" doc:"Докупка устройств в боте и Mini App: цена одного и сколько можно докупить"`
	Servers       ServersView   `json:"servers" doc:"Какие серверы входят в тариф: все или выбранные. Действует сразу на всех клиентов тарифа; клиент со своим списком протоколов в карточке остаётся при нём"`
	Pools         []PoolSetting `json:"pools" doc:"Как тариф держит каждую группу серверов; группа без настройки тарифа — в основном трафике. Режим и лимит действуют на новых клиентов, при продлении, смене тарифа и по «Применить к выданным подпискам»; закрытие и открытие группы действует сразу на всех клиентов тарифа"`
	Gifts         []int64       `json:"gifts" doc:"Пакеты трафика в подарок при каждой оплате тарифа: покупке и продлении"`
	Subscribers   int64         `json:"subscribers" doc:"Сколько подписок выдано на этом тарифе"`
	Sort          int64         `json:"sort"`
	// The tariff's own part of the lifecycle (domain.TariffLifecycle).
	CarryOver       string `json:"carry_over" enum:"inherit,on,off" doc:"Перенос остатка трафика в новый период при продлении и переходе на этот тариф: inherit — как в правилах подписок, on — переносить, off — нет"`
	StartsOnConnect bool   `json:"starts_on_connect" doc:"Срок новой подписки на этом тарифе начинается с первого подключения, а не с создания"`
}

// The tariff's word on the carry-over (TariffView.CarryOver, tariffBody.CarryOver).
const (
	carryInherit = "inherit"
	carryOn      = "on"
	carryOff     = "off"
)

// carryWord is how the API says a tariff's own carry-over.
func carryWord(v sql.NullBool) string {
	switch {
	case !v.Valid:
		return carryInherit
	case v.Bool:
		return carryOn
	}
	return carryOff
}

// carryRule is a tariff's own carry-over as the API says it.
func carryRule(word string) sql.NullBool {
	switch word {
	case carryOn:
		return sql.NullBool{Bool: true, Valid: true}
	case carryOff:
		return sql.NullBool{Valid: true}
	}
	return sql.NullBool{}
}

// ServersView is what a tariff includes: every server, or only the chosen nodes and
// protocols.
type ServersView struct {
	All      bool    `json:"all" doc:"Все серверы; false — только выбранные ноды и протоколы"`
	Nodes    []int64 `json:"nodes" doc:"Ноды целиком, с протоколами, которые на них появятся потом"`
	Inbounds []int64 `json:"inbounds" doc:"Отдельные протоколы"`
}

// serversBody is the tariff's servers as the body sets them.
type serversBody struct {
	All      bool    `json:"all" doc:"Все серверы; false — только выбранные, хотя бы одна нода или протокол"`
	Nodes    []int64 `json:"nodes,omitempty" maxItems:"500" doc:"Ноды целиком, с протоколами, которые на них появятся потом"`
	Inbounds []int64 `json:"inbounds,omitempty" maxItems:"2000" doc:"Отдельные протоколы"`
}

func viewServers(s domain.Servers) ServersView {
	v := ServersView{All: s.All, Nodes: []int64{}, Inbounds: []int64{}}
	for id := range s.Nodes {
		v.Nodes = append(v.Nodes, id)
	}
	for id := range s.Inbounds {
		v.Inbounds = append(v.Inbounds, id)
	}
	slices.Sort(v.Nodes)
	slices.Sort(v.Inbounds)
	return v
}

// ExtraDevices is one more device on top of the tariff: its price and how many a
// subscription may buy (0: none).
type ExtraDevices struct {
	PriceStars *int64 `json:"price_stars" minimum:"1" maximum:"10000" doc:"Цена одного устройства в Stars; null — не за Stars"`
	PriceRub   *int64 `json:"price_rub" minimum:"100" maximum:"100000000" doc:"Цена одного устройства в копейках; null — не за рубли"`
	Max        int64  `json:"max" minimum:"0" maximum:"50" doc:"Сколько устройств подписка может докупить; 0 — докупки нет"`
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
		PriceStars: store.Int64Ptr(t.PriceStars), PriceRub: store.Int64Ptr(t.PriceRub), OnSale: t.OnSale != 0,
		ExtraDevices: ExtraDevices{PriceStars: store.Int64Ptr(t.DevicePriceStars), PriceRub: store.Int64Ptr(t.DevicePriceRub), Max: t.DeviceExtraMax}}
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
	Name          string        `json:"name" minLength:"1" maxLength:"60"`
	TrafficLimit  *int64        `json:"traffic_limit,omitempty" minimum:"1"`
	DurationDays  int64         `json:"duration_days" minimum:"0" maximum:"3650"`
	DeviceLimit   *int64        `json:"device_limit,omitempty" minimum:"1" maximum:"100"`
	DeviceMode    string        `json:"device_mode,omitempty" enum:"bound,online" doc:"bound: лимит это число привязанных устройств; online: привязывать можно любое число, считаются устройства, одновременно подключённые к серверам. Не передан: у нового тарифа bound, у существующего без изменений"`
	SpeedLimit    *int64        `json:"speed_limit,omitempty" minimum:"1" maximum:"100000" doc:"Скорость, Мбит/с в каждую сторону; не передана — без ограничения"`
	ResetStrategy string        `json:"reset_strategy" enum:"none,month_start,period" default:"none"`
	BillingDay    *int64        `json:"billing_day,omitempty" minimum:"1" maximum:"31" doc:"Срок до этого числа месяца: месяц = от дня оплаты до дня оплаты"`
	PriceLabel    string        `json:"price_label,omitempty" maxLength:"40"`
	Description   string        `json:"description,omitempty" doc:"Текст для покупателей, виден в боте и Mini App рядом со строкой из лимитов: чем тариф отличается, когда лимиты этого не показывают («с белыми списками»). Простой текст до 300 символов и до 5 строк; не передан или пустой — только строка из лимитов"`
	PriceStars    *int64        `json:"price_stars,omitempty" minimum:"1" maximum:"10000" doc:"Цена в Telegram Stars"`
	PriceRub      *int64        `json:"price_rub,omitempty" minimum:"100" maximum:"100000000" doc:"Цена в копейках: 19900 — 199 ₽"`
	OnSale        bool          `json:"on_sale,omitempty" doc:"Продавать в боте и Mini App; нужна хотя бы одна цена"`
	Terms         []termBody    `json:"terms,omitempty" maxItems:"12" doc:"Все сроки по порядку, когда тариф продаётся на несколько; тогда duration_days, price_stars и price_rub берутся из первого. Не передан — первый срок из duration_days, price_stars и price_rub, остальные без изменений"`
	Servers       *serversBody  `json:"servers,omitempty" doc:"Какие серверы входят в тариф: все или выбранные ноды и протоколы. Не передан — без изменений (у нового тарифа — все)"`
	Pools         []PoolSetting `json:"pools,omitempty" maxItems:"100" doc:"Как тариф держит группы серверов; не названная группа — в основном трафике. Не передан — без изменений"`
	Gifts         []int64       `json:"gifts,omitempty" maxItems:"20" doc:"Пакеты трафика в подарок при каждой оплате тарифа; каждый должен добавлять к квоте, которую тариф даёт: к основному трафику с лимитом или к группе со своим лимитом. Не передан — без изменений"`
	ExtraDevices  *ExtraDevices `json:"extra_devices,omitempty" doc:"Докупка устройств; не передана — без изменений (у нового тарифа — нет). Нужен лимит устройств"`
	Sort          int64         `json:"sort,omitempty"`
	// CarryOver and StartsOnConnect: the tariff's own part of the lifecycle; left out, as it was.
	CarryOver       string `json:"carry_over,omitempty" enum:"inherit,on,off" doc:"Перенос остатка трафика: inherit — как в правилах подписок, on, off. Не передан — без изменений (у нового тарифа — inherit)"`
	StartsOnConnect *bool  `json:"starts_on_connect,omitempty" doc:"Срок новой подписки начинается с первого подключения. Не передан — без изменений (у нового тарифа — нет)"`
}

// setTariffLifecycle writes the tariff's own part of the lifecycle from the body on q's
// transaction; what the body leaves out stays as it was.
func setTariffLifecycle(ctx context.Context, q *db.Queries, id int64, b tariffBody) error {
	if b.CarryOver == "" && b.StartsOnConnect == nil {
		return nil
	}
	tl, err := domain.LoadTariffLifecycle(ctx, q, id)
	if err != nil {
		return err
	}
	if b.CarryOver != "" {
		tl.Carry = carryRule(b.CarryOver)
	}
	if b.StartsOnConnect != nil {
		tl.StartOnConnect = *b.StartsOnConnect
	}
	return domain.SetTariffLifecycle(ctx, q, id, tl)
}

// setExtraDevices writes the tariff's price of one more device; nil keeps it as it is.
func setExtraDevices(ctx context.Context, q *db.Queries, t *db.Tariff, e *ExtraDevices) error {
	if e == nil {
		return nil
	}
	if e.Max > 0 && !t.DeviceLimit.Valid {
		return fieldErr("body.extra_devices.max", "extra_devices_unlimited")
	}
	if e.Max > 0 && e.PriceStars == nil && e.PriceRub == nil {
		return fieldErr("body.extra_devices.price_rub", "extra_devices_price")
	}
	t.DevicePriceStars, t.DevicePriceRub, t.DeviceExtraMax = domain.NullInt(e.PriceStars), domain.NullInt(e.PriceRub), e.Max
	return q.SetTariffExtraDevices(ctx, db.SetTariffExtraDevicesParams{DevicePriceStars: t.DevicePriceStars, DevicePriceRub: t.DevicePriceRub,
		DeviceExtraMax: e.Max, ID: t.ID})
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
	huma.Register(h.api, huma.Operation{OperationID: "apply-tariff", Method: http.MethodPost, Path: "/api/v1/tariffs/{id}/apply", Summary: "Применить тариф к уже выданным подпискам: выбранные части тарифа заменят свои значения клиентов, срок и израсходованное останутся", Tags: []string{"tariffs"}}, h.applyTariff)
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
	c, err := h.catalogExtras(ctx)
	if err != nil {
		return nil, err
	}
	out := &tariffsOutput{Body: make([]TariffView, 0, len(rows))}
	for _, t := range rows {
		v := viewTariff(t, terms)
		c.fill(&v, pools)
		out.Body = append(out.Body, v)
	}
	return out, nil
}

// createTariff and updateTariff write the tariff, its server groups, its servers and its gifts
// in one transaction: a refused part leaves the tariff, and what it had, as it was.
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
		if err := setExtraDevices(ctx, q, &t, b.ExtraDevices); err != nil {
			return err
		}
		if err := domain.SetTariffTerms(ctx, q, t.ID, terms); err != nil {
			return err
		}
		if err := setTariffLifecycle(ctx, q, t.ID, b); err != nil {
			return err
		}
		return setTariffContents(ctx, q, t, b)
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
		if err := setExtraDevices(ctx, q, &t, b.ExtraDevices); err != nil {
			return err
		}
		if err := domain.SetTariffTerms(ctx, q, t.ID, terms); err != nil {
			return err
		}
		if err := setTariffLifecycle(ctx, q, t.ID, b); err != nil {
			return err
		}
		return setTariffContents(ctx, q, t, b)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	if in.Body.Pools != nil || in.Body.Servers != nil {
		h.d.Changes.PoliciesChanged() // a group or a server closed or opened for the tariff's users
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
		return fieldErr("body.device_mode", "bad_device_mode")
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

type applyTariffInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Traffic bool `json:"traffic,omitempty" doc:"Трафик за период и когда он обнуляется"`
		Devices bool `json:"devices,omitempty" doc:"Лимит устройств (докупленные остаются сверху) и как он считается"`
		Speed   bool `json:"speed,omitempty" doc:"Скорость"`
		Groups  bool `json:"groups,omitempty" doc:"Группы серверов: режимы и лимиты"`
	}
}

type applyTariffOutput struct {
	Body struct {
		Applied int `json:"applied" doc:"Скольким подпискам тарифа применены его части"`
	}
}

func (h *handlers) applyTariff(ctx context.Context, in *applyTariffInput) (*applyTariffOutput, error) {
	parts := domain.TariffParts{Traffic: in.Body.Traffic, Devices: in.Body.Devices, Speed: in.Body.Speed, Groups: in.Body.Groups}
	if !parts.Any() {
		return nil, fieldErr("body", "apply_nothing")
	}
	n, err := h.d.Users.ApplyTariffToAll(ctx, in.ID, parts)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "tariff.apply", "tariff", strconv.FormatInt(in.ID, 10),
		map[string]any{"users": n, "traffic": parts.Traffic, "devices": parts.Devices, "speed": parts.Speed, "groups": parts.Groups})
	out := &applyTariffOutput{}
	out.Body.Applied = n
	return out, nil
}

// setTariffContents writes the tariff's groups and gifts from the body on q's transaction.
// The gifts are checked against the groups as they are now: a body that leaves the gifts
// out still has the tariff's own checked, since a change of a group or of the main traffic
// may leave a gift adding to no quota.
func setTariffContents(ctx context.Context, q *db.Queries, t db.Tariff, b tariffBody) error {
	if s := b.Servers; s != nil {
		if err := domain.SetTariffServers(ctx, q, t.ID, domain.ServersInput{All: s.All, Nodes: s.Nodes, Inbounds: s.Inbounds}); err != nil {
			return mapDomainErr(err)
		}
	}
	if b.Pools != nil {
		if err := setTariffPools(ctx, q, t.ID, b.Pools); err != nil {
			return err
		}
	}
	var err error
	if b.Gifts != nil {
		err = domain.SetTariffGifts(ctx, q, t, b.Gifts)
	} else {
		err = domain.RecheckTariffGifts(ctx, q, t)
	}
	if err != nil {
		return mapDomainErr(err)
	}
	return nil
}

// catalogExtras is what the tariffs' views take besides their rows: the servers, the
// groups, the gifts, how many subscriptions each one was given to and its own part of the
// lifecycle.
type catalogExtras struct {
	servers   map[int64]domain.Servers
	groups    []db.TrafficPool
	gifts     map[int64][]int64
	users     map[int64]int64
	lifecycle map[int64]domain.TariffLifecycle
}

func (h *handlers) catalogExtras(ctx context.Context) (catalogExtras, error) {
	q := h.d.Store.Q
	var c catalogExtras
	var err error
	if c.servers, err = domain.AllTariffServers(ctx, q); err != nil {
		return c, err
	}
	if c.groups, err = q.ListTrafficPools(ctx); err != nil {
		return c, err
	}
	if c.gifts, err = domain.TariffGifts(ctx, q); err != nil {
		return c, err
	}
	counts, err := q.CountUsersByTariff(ctx)
	if err != nil {
		return c, err
	}
	c.users = make(map[int64]int64, len(counts))
	for _, r := range counts {
		c.users[r.TariffID] = r.Users
	}
	c.lifecycle, err = domain.TariffLifecycles(ctx, q)
	return c, err
}

// fill puts the tariff's servers, groups (from rows, the tariffs' settings of groups),
// gifts and subscriptions in v.
func (c catalogExtras) fill(v *TariffView, rows []db.TariffPool) {
	v.Servers = viewServers(domain.ServersOf(c.servers, sql.NullInt64{Int64: v.ID, Valid: true}))
	v.Pools = tariffPoolsOf(rows, c.groups, v.ID)
	v.Gifts = c.gifts[v.ID]
	if v.Gifts == nil {
		v.Gifts = []int64{}
	}
	v.Subscribers = c.users[v.ID]
	tl := c.lifecycle[v.ID]
	v.CarryOver, v.StartsOnConnect = carryWord(tl.Carry), tl.StartOnConnect
}

// cleanDescription is the tariff's description as stored; a text that is too long or has
// too many lines is refused under the field's name, like the other checks of the body.
func cleanDescription(s string) (string, error) {
	out, err := domain.CleanDescription(s)
	if fe, ok := fieldFromDomain(err); ok {
		return "", fe
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
			key = int64(model.TermMonths(t.Days))
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
	c, err := h.catalogExtras(ctx)
	if err != nil {
		return nil, err
	}
	v := viewTariff(t, terms)
	c.fill(&v, pools)
	return &tariffOutput{Body: v}, nil
}
