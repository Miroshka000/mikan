package api

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/torrent"
)

// TelegramLink is the Telegram account that manages a subscription in the bot.
type TelegramLink struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type UserView struct {
	Telegram      *TelegramLink `json:"telegram,omitempty" doc:"Только в карточке пользователя"`
	Legacy        *LegacyLinks  `json:"legacy,omitempty" doc:"Старые ссылки подписки из панели, откуда импортирован пользователь. Только в карточке и не для ключа только на чтение: это тоже ссылка"`
	ID            int64         `json:"id"`
	Name          string        `json:"name"`
	Contact       string        `json:"contact"`
	Note          string        `json:"note"`
	Tags          []string      `json:"tags"`
	State         string        `json:"state" enum:"active,expiring,limited,expired,disabled"`
	TariffID      *int64        `json:"tariff_id"`
	TrafficLimit  *int64        `json:"traffic_limit" doc:"Байты за период; null — без лимита"`
	TrafficExtra  int64         `json:"traffic_extra" doc:"Байты, оставшиеся в пакетах трафика основного лимита: тратятся после лимита тарифа"`
	UsedUp        int64         `json:"used_up"`
	UsedDown      int64         `json:"used_down"`
	TotalUp       int64         `json:"total_up"`
	TotalDown     int64         `json:"total_down"`
	DeviceLimit   *int64        `json:"device_limit"`
	DeviceMode    string        `json:"device_mode" enum:"bound,online" doc:"Как считается лимит устройств: bound: привязанные устройства, online: любое число устройств, считаются только одновременно онлайн"`
	SpeedLimit    *int64        `json:"speed_limit" doc:"Мбит/с в каждую сторону; null — без ограничения"`
	ResetStrategy string        `json:"reset_strategy" enum:"none,month_start,period" doc:"month_start — раз в месяц: в день оплаты, без него 1-го числа"`
	ResetsAt      *time.Time    `json:"resets_at"`
	ExpiresAt     *time.Time    `json:"expires_at"`
	BillingDay    *int64        `json:"billing_day" doc:"День месяца, в который заканчивается срок (1–31); null — продление днями"`
	Inbounds      []int64       `json:"inbounds" doc:"Разрешённые подключения; пусто — все"`
	SubURL        string        `json:"sub_url"`
	Online        bool          `json:"online"`
	OnlineIPs     []string      `json:"online_ips"`
	BoundDevices  int64         `json:"bound_devices" doc:"Привязанные устройства: с привязкой это занятые места"`
	OnlineAt      *time.Time    `json:"online_at"`
	TorrentBan    *time.Time    `json:"torrent_ban" doc:"До какого времени действует бан блокировщика торрентов; null — бана нет"`
	Source        string        `json:"source" enum:"admin,bot,trial,free,import" doc:"Откуда пользователь: admin — создан в панели или по ключу API, bot — куплен в боте, trial — пробный период бота, free — бесплатный тариф из бота или Mini App, import — перенесён из другой панели. Заполняется при создании, не меняется"`
	FolderID      *int64        `json:"folder_id" doc:"Папка пользователя; null — вне папок"`
	Hidden        bool          `json:"hidden" doc:"Скрыт из списка пользователей. Только прячет строку: подписка, оплата и ноды работают как у всех, счётчики обзора не меняются"`
	// StartsOnConnect: the term waits for the first connection (domain/starts.go).
	StartsOnConnect bool `json:"starts_on_connect" doc:"Срок ждёт первого подключения: до него стоит на месте, expires_at уходит вперёд вместе с часами (обновляется раз в 10 минут), подписка показывает весь срок"`
	// NextTariff is the tariff the subscription moves to once its term ends.
	NextTariff *NextTariffView `json:"next_tariff" doc:"Следующий тариф: включится сам, когда кончится срок; null — нет"`
	CreatedAt  time.Time       `json:"created_at"`
}

// userEnv is what every user of one response shares, read once for the whole response: a
// list of 500 users asked for the subscription address (six settings) and the online map
// (every slot of every node) 500 times each.
type userEnv struct {
	subBase string // "": no address yet, or the caller may not see links
	online  map[string]nodeapi.Online
	bans    map[int64]int64 // user → when the torrent ban ends; nil while the blocker is off
	// The lifecycle of the answer's users (lifecycleOf): whose term waits for its first
	// connection, and who has a next tariff.
	waiting map[int64]bool
	next    map[int64]domain.NextTariff
}

// lifecycleOf reads the lifecycle of the users of ids into e: two small tables, one read
// each for the whole answer.
func (h *handlers) lifecycleOf(ctx context.Context, e *userEnv, ids []int64) error {
	var err error
	if e.waiting, err = domain.WaitingOf(ctx, h.d.Store.Q, ids); err != nil {
		return err
	}
	e.next, err = domain.NextTariffsOf(ctx, h.d.Store.Q, ids)
	return err
}

func (h *handlers) userEnv(ctx context.Context) userEnv {
	var e userEnv
	// A subscription link is the user's credential: a read-only key does not get it, nor does
	// a support operator the admin did not let see the subscribers.
	if h.d.SubBase != nil && !hidesLinks(ctx) {
		e.subBase = h.d.SubBase(ctx)
	}
	e.online = h.online()
	// The list still shows without them: a ban is a note on a user, not the user.
	if h.d.Settings == nil {
		return e
	}
	if c, err := torrent.Load(ctx, h.d.Settings); err == nil && c.Enabled {
		if rows, err := h.d.Store.Q.ActiveTorrentBans(ctx, h.d.Now().Unix()); err == nil {
			e.bans = make(map[int64]int64, len(rows))
			for _, r := range rows {
				e.bans[r.UserID] = r.BannedUntil
			}
		}
	}
	return e
}

// online is who is online by slot name: nobody when the panel runs without nodes.
func (h *handlers) online() map[string]nodeapi.Online {
	if h.d.Online == nil {
		return nil
	}
	return h.d.Online()
}

func (h *handlers) viewUser(u db.User, slots []string, bound int64, grants domain.GrantsLeft, env userEnv) UserView {
	now := h.d.Now()
	v := UserView{
		ID: u.ID, Name: u.Name, Contact: u.Contact, Note: u.Note, Tags: domain.DecodeTags(u.Tags),
		State: domain.State(u, grants.Main(u.ID), now), TariffID: store.Int64Ptr(u.TariffID),
		TrafficLimit: store.Int64Ptr(u.TrafficLimit), TrafficExtra: grants.Main(u.ID), UsedUp: u.UsedUp, UsedDown: u.UsedDown,
		TotalUp: u.TotalUp, TotalDown: u.TotalDown, DeviceLimit: store.Int64Ptr(u.DeviceLimit), DeviceMode: u.DeviceMode,
		SpeedLimit: store.Int64Ptr(u.SpeedLimit), ResetStrategy: u.ResetStrategy, ExpiresAt: store.TimePtr(u.ExpiresAt),
		BillingDay: store.Int64Ptr(u.BillingDay),
		Inbounds:   domain.DecodeInbounds(u.Inbounds), OnlineAt: store.TimePtr(u.OnlineAt),
		CreatedAt: store.Time(u.CreatedAt), OnlineIPs: []string{}, BoundDevices: bound,
		Source: u.Source, FolderID: store.Int64Ptr(u.FolderID), Hidden: u.Hidden != 0,
	}
	if v.Inbounds == nil {
		v.Inbounds = []int64{}
	}
	if t, ok := domain.NextReset(u, now); ok {
		v.ResetsAt = &t
	}
	if env.subBase != "" {
		v.SubURL = env.subBase + "/" + u.SubToken
	}
	if until, ok := env.bans[u.ID]; ok {
		v.TorrentBan = store.TimePtr(sql.NullInt64{Int64: until, Valid: true})
	}
	v.OnlineIPs = env.liveIPs(slots)
	v.Online = len(v.OnlineIPs) > 0
	v.StartsOnConnect = env.waiting[u.ID]
	n, ok := env.next[u.ID]
	v.NextTariff = nextView(n, ok)
	return v
}

// liveIPs merges the online devices of a user's slots: the own one and bound devices'.
func (e userEnv) liveIPs(slots []string) []string {
	out := []string{}
	for _, s := range slots {
		for _, ip := range e.online[s].IPs {
			if !slices.Contains(out, ip) {
				out = append(out, ip)
			}
		}
	}
	slices.Sort(out)
	return out
}

// userSlots maps the users of ids to the names of all their slots: for a list's page.
func (h *handlers) userSlots(ctx context.Context, ids []int64) (map[int64][]string, error) {
	rows, err := h.d.Store.Q.UserSlotsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	m := map[int64][]string{}
	for _, r := range rows {
		m[r.UserID] = append(m[r.UserID], r.SlotName)
	}
	return m, nil
}

type listUsersInput struct {
	State  string `query:"state" enum:"all,active,expiring,limited,expired,disabled,attention" default:"all" doc:"attention — исчерпан лимит, истекают и истекли, в этом порядке: одним запросом для обзора"`
	Query  string `query:"q" maxLength:"100" doc:"Поиск по имени, контакту, заметке и меткам. Оператору поддержки без показа подписчиков закрыт: 403 support_denied"`
	Folder string `query:"folder" maxLength:"20" default:"all" doc:"all — любая; none — вне папок; число — id папки"`
	Source string `query:"source" enum:"all,admin,bot,trial,free,import" default:"all" doc:"Откуда пользователь, см. source у пользователя"`
	Hidden string `query:"hidden" enum:"hide,show,only" default:"show" doc:"show — вместе со скрытыми (по умолчанию: скрытие касается только списка в панели); hide — без скрытых; only — только скрытые"`
	Limit  int    `query:"limit" minimum:"1" maximum:"500" default:"100"`
	Offset int    `query:"offset" minimum:"0" default:"0"`
}

type UserCounts struct {
	All      int `json:"all"`
	Active   int `json:"active"`
	Expiring int `json:"expiring"`
	Limited  int `json:"limited"`
	Expired  int `json:"expired"`
	Disabled int `json:"disabled"`
}

type listUsersOutput struct {
	Body struct {
		Items  []UserView `json:"items"`
		Total  int        `json:"total" doc:"Сколько подходит под фильтр"`
		Counts UserCounts `json:"counts" doc:"Пользователи по состояниям среди подходящих под папку, источник и скрытых; состояние и поиск не учитываются"`
		// Each facet is counted with the other filters applied and its own left out, so a
		// chip says how many users it would show.
		FolderCounts map[string]int `json:"folder_counts" doc:"Пользователи по папкам (ключ — id папки, none — вне папок) среди подходящих под источник и скрытых"`
		SourceCounts map[string]int `json:"source_counts" doc:"Пользователи по источникам (admin, bot, trial, import) среди подходящих под папку и скрытых"`
		UsersTotal   int            `json:"users_total" doc:"Все пользователи панели, как бы ни был задан фильтр"`
		HiddenTotal  int            `json:"hidden_total" doc:"Сколько из них скрыто"`
	}
}

type userOutput struct{ Body UserView }

type userIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type createUserInput struct {
	Body struct {
		Name     string   `json:"name" minLength:"1" maxLength:"100"`
		Contact  string   `json:"contact,omitempty" maxLength:"100"`
		Note     string   `json:"note,omitempty" maxLength:"2000"`
		Tags     []string `json:"tags,omitempty" maxItems:"20"`
		TariffID int64    `json:"tariff_id" minimum:"1"`
		// StartsOnConnect over the tariff's own word; nil: the tariff's.
		StartsOnConnect *bool `json:"starts_on_connect,omitempty" doc:"Срок начнётся с первого подключения; не задано — как у тарифа"`
	}
}

type patchUserInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Name             *string    `json:"name,omitempty" minLength:"1" maxLength:"100"`
		Contact          *string    `json:"contact,omitempty" maxLength:"100"`
		Note             *string    `json:"note,omitempty" maxLength:"2000"`
		Tags             *[]string  `json:"tags,omitempty" maxItems:"20"`
		Disabled         *bool      `json:"disabled,omitempty"`
		TrafficLimit     *int64     `json:"traffic_limit,omitempty" minimum:"0"`
		TrafficUnlimited bool       `json:"traffic_unlimited,omitempty"`
		DeviceLimit      *int64     `json:"device_limit,omitempty" minimum:"1" maximum:"100"`
		DevicesUnlimited bool       `json:"devices_unlimited,omitempty"`
		DeviceMode       *string    `json:"device_mode,omitempty" enum:"bound,online" doc:"Как считается лимит устройств: bound: привязанные устройства, online: любое число, считаются только одновременно онлайн"`
		SpeedLimit       *int64     `json:"speed_limit,omitempty" minimum:"1" maximum:"100000" doc:"Скорость, Мбит/с в каждую сторону"`
		SpeedUnlimited   bool       `json:"speed_unlimited,omitempty" doc:"Снять ограничение скорости"`
		ExpiresAt        *time.Time `json:"expires_at,omitempty"`
		NeverExpires     bool       `json:"never_expires,omitempty"`
		BillingDay       *int64     `json:"billing_day,omitempty" minimum:"0" maximum:"31" doc:"День оплаты 1–31; 0 — убрать"`
		Inbounds         *[]int64   `json:"inbounds,omitempty"`
		TariffID         *int64     `json:"tariff_id,omitempty" minimum:"1" doc:"Применить тариф: лимиты из тарифа, срок — от сегодня"`
		Hidden           *bool      `json:"hidden,omitempty" doc:"Скрыть пользователя из списка или вернуть в него; доступ он не теряет"`
		FolderID         *int64     `json:"folder_id,omitempty" minimum:"0" doc:"Положить в папку; 0 — убрать из папки"`
		StartsOnConnect  *bool      `json:"starts_on_connect,omitempty" doc:"true — срок ждёт первого подключения (только у не подключавшегося, со сроком впереди: иначе 422 already_connected, no_term_end, term_ended); false — запустить ждущий срок сейчас. Дата окончания, заданная вручную (expires_at, never_expires), тоже запускает его и остаётся как задана"`
	}
}

type extendInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Days   int64 `json:"days,omitempty" minimum:"0" maximum:"3650"`
		Months int   `json:"months,omitempty" minimum:"0" maximum:"36" doc:"Месяцами: до дня оплаты или того же числа"`
	}
}

type bulkInput struct {
	Body struct {
		IDs    []int64     `json:"ids,omitempty" maxItems:"1000" doc:"Пользователи по id; задаётся либо ids, либо filter"`
		Filter *BulkFilter `json:"filter,omitempty" doc:"Все, кто подходит под фильтр списка пользователей (те же параметры и тот же смысл, что у GET /users), сколько бы их ни было; задаётся либо ids, либо filter"`
		Expect *int        `json:"expect,omitempty" minimum:"0" doc:"С filter: сколько пользователей подходило в предпросмотре (found). Если сейчас подходит другое число — 409 filter_changed, и ничего не сделано"`
		Action string      `json:"action" enum:"extend,reset,disable,enable,delete,hide,unhide,move" doc:"hide и unhide прячут пользователей из списка и возвращают, move кладёт в папку: ни то ни другое не меняет доступ"`
		Days   int64       `json:"days,omitempty" minimum:"0" maximum:"3650" doc:"Для extend; не задано — на один период: до следующего дня оплаты или на 30 дней"`
		Folder int64       `json:"folder_id,omitempty" minimum:"0" doc:"Для move: папка; 0 или не задано — убрать из папок"`
	}
}

// BulkFilter is the users list's filter (listUsersInput without the page) in a bulk body.
type BulkFilter struct {
	State  string `json:"state,omitempty" enum:"all,active,expiring,limited,expired,disabled,attention" default:"all"`
	Query  string `json:"q,omitempty" maxLength:"100" doc:"Оператору поддержки без показа подписчиков закрыт: 403 support_denied"`
	Folder string `json:"folder,omitempty" maxLength:"20" default:"all" doc:"all — любая; none — вне папок; число — id папки"`
	Source string `json:"source,omitempty" enum:"all,admin,bot,trial,free,import" default:"all"`
	Hidden string `json:"hidden,omitempty" enum:"hide,show,only" default:"show" doc:"Как у списка: по умолчанию вместе со скрытыми"`
}

type bulkOutput struct {
	Body struct {
		Affected int `json:"affected"`
	}
}

// BulkSample is one of the users a bulk action would change, as it is and what changes.
type BulkSample struct {
	UserID       int64      `json:"user_id"`
	UserName     string     `json:"user_name"`
	State        string     `json:"state" enum:"active,expiring,limited,expired,disabled"`
	ExpiresAt    *time.Time `json:"expires_at"`
	ExpiresAfter *time.Time `json:"expires_after,omitempty" doc:"extend: новый конец срока"`
	Used         int64      `json:"used" doc:"Израсходовано за период: reset начинает период заново"`
	FolderID     *int64     `json:"folder_id"`
}

// BulkAtStake is what the users a deletion reaches hold, as the removal of the inactive
// counts it: what a deletion takes with them.
type BulkAtStake struct {
	Active      int `json:"active" doc:"Ещё работают: срок и трафик не кончились, не отключены"`
	WithBalance int `json:"with_balance" doc:"С деньгами на балансе привязанного аккаунта"`
	Paying      int `json:"paying" doc:"С платежом, который ещё не применён, или пополнением на проверке"`
	Granted     int `json:"granted" doc:"С действующими пакетами трафика"`
}

type bulkPreviewOutput struct {
	Body struct {
		Requested int          `json:"requested" doc:"Сколько разных пользователей в запросе"`
		Found     int          `json:"found" doc:"Сколько из них есть: к ним действие и применится (affected)"`
		Missing   int          `json:"missing" doc:"Сколько уже удалены"`
		Changed   int          `json:"changed" doc:"Скольких действие изменит"`
		Unchanged int          `json:"unchanged" doc:"Сколько уже такие: например, уже отключены"`
		Sample    []BulkSample `json:"sample" doc:"Первые 10 изменяемых, по id"`
		AtStake   *BulkAtStake `json:"at_stake,omitempty" doc:"Только для delete: что удалится вместе с пользователями"`
	}
}

type trafficInput struct {
	ID    int64  `path:"id" minimum:"1"`
	Range string `query:"range" enum:"24h,7d,30d" default:"7d"`
}

type TrafficPoint struct {
	T    time.Time `json:"t"`
	Up   int64     `json:"up"`
	Down int64     `json:"down"`
}

type trafficOutput struct {
	Body struct {
		Points []TrafficPoint `json:"points"`
	}
}

type DeviceView struct {
	IP        string    `json:"ip"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Online    bool      `json:"online"`
}

type devicesOutput struct{ Body []DeviceView }

func (h *handlers) registerUsers() {
	tags := []string{"users"}
	huma.Register(h.api, huma.Operation{OperationID: "list-users", Method: http.MethodGet, Path: "/api/v1/users", Summary: "Список пользователей", Tags: tags}, h.listUsers)
	huma.Register(h.api, huma.Operation{OperationID: "create-user", Method: http.MethodPost, Path: "/api/v1/users", Summary: "Создать пользователя по тарифу", Tags: tags, DefaultStatus: http.StatusCreated}, h.createUser)
	huma.Register(h.api, huma.Operation{OperationID: "get-user", Method: http.MethodGet, Path: "/api/v1/users/{id}", Summary: "Пользователь", Tags: tags}, h.getUser)
	huma.Register(h.api, huma.Operation{OperationID: "update-user", Method: http.MethodPatch, Path: "/api/v1/users/{id}", Summary: "Изменить пользователя", Tags: tags}, h.updateUser)
	huma.Register(h.api, huma.Operation{OperationID: "delete-user", Method: http.MethodDelete, Path: "/api/v1/users/{id}", Summary: "Удалить пользователя", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteUser)
	huma.Register(h.api, huma.Operation{OperationID: "extend-user", Method: http.MethodPost, Path: "/api/v1/users/{id}/extend", Summary: "Продлить", Tags: tags}, h.extendUser)
	huma.Register(h.api, huma.Operation{OperationID: "reset-user-traffic", Method: http.MethodPost, Path: "/api/v1/users/{id}/reset-traffic", Summary: "Сбросить трафик", Tags: tags}, h.resetUserTraffic)
	huma.Register(h.api, huma.Operation{OperationID: "reissue-user", Method: http.MethodPost, Path: "/api/v1/users/{id}/reissue", Summary: "Перевыпустить ссылку", Tags: tags}, h.reissueUser)
	huma.Register(h.api, huma.Operation{OperationID: "bulk-users", Method: http.MethodPost, Path: "/api/v1/users/bulk", Summary: "Массовое действие", Tags: tags}, h.bulkUsers)
	huma.Register(h.api, huma.Operation{OperationID: "bulk-users-preview", Method: http.MethodPost, Path: "/api/v1/users/bulk/preview",
		Summary: "Что сделает массовое действие: без изменений, для подтверждения", Tags: tags}, h.bulkPreview)
	huma.Register(h.api, huma.Operation{OperationID: "user-traffic", Method: http.MethodGet, Path: "/api/v1/users/{id}/traffic", Summary: "График трафика пользователя", Tags: tags}, h.userTraffic)
	huma.Register(h.api, huma.Operation{OperationID: "user-devices", Method: http.MethodGet, Path: "/api/v1/users/{id}/devices", Summary: "Адреса, с которых заходил пользователь", Tags: tags}, h.userDevices)
	huma.Register(h.api, huma.Operation{OperationID: "user-bound-devices", Method: http.MethodGet, Path: "/api/v1/users/{id}/bound-devices", Summary: "Устройства, привязанные к подписке", Tags: tags}, h.boundDevices)
	huma.Register(h.api, huma.Operation{OperationID: "unbind-device", Method: http.MethodDelete, Path: "/api/v1/users/{id}/bound-devices/{device}", Summary: "Отвязать устройство: его ключи сгорают", Tags: tags, DefaultStatus: http.StatusNoContent}, h.unbindDevice)
	huma.Register(h.api, huma.Operation{OperationID: "rename-device", Method: http.MethodPatch, Path: "/api/v1/users/{id}/bound-devices/{device}", Summary: "Переименовать привязанное устройство", Tags: tags}, h.renameDevice)
	huma.Register(h.api, huma.Operation{OperationID: "ban-device", Method: http.MethodPost, Path: "/api/v1/users/{id}/bound-devices/{device}/ban", Summary: "Заблокировать устройство: отвязать и не давать привязаться снова по его ID", Tags: tags, DefaultStatus: http.StatusCreated}, h.banDevice)
	huma.Register(h.api, huma.Operation{OperationID: "device-bans", Method: http.MethodGet, Path: "/api/v1/users/{id}/device-bans", Summary: "Заблокированные устройства пользователя", Tags: tags}, h.deviceBans)
	huma.Register(h.api, huma.Operation{OperationID: "unban-device", Method: http.MethodDelete, Path: "/api/v1/users/{id}/device-bans/{ban}", Summary: "Разблокировать устройство: оно снова сможет привязаться", Tags: tags, DefaultStatus: http.StatusNoContent}, h.unbanDevice)
	h.registerLifecycle()
}

func mapDomainErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return huma.Error404NotFound("not_found")
	case errors.Is(err, domain.ErrNoSlots):
		return huma.Error503ServiceUnavailable("no_free_slots")
	case errors.Is(err, domain.ErrBadBillingDay):
		return fieldErr("body.billing_day", "bad_billing_day")
	case errors.Is(err, domain.ErrBadSpeedLimit):
		return fieldErr("body.speed_limit", "bad_speed_limit")
	case errors.Is(err, domain.ErrBanShared):
		return refusal("device_no_hwid")
	}
	if fe, ok := fieldFromDomain(err); ok {
		return fe
	}
	return err
}

// fieldFromDomain is the API's 422 of a field the domain refused (domain.FieldError), at
// body.<field>; ok false for any other error.
func fieldFromDomain(err error) (error, bool) {
	var fe *domain.FieldError
	if errors.As(err, &fe) {
		return fieldErr("body."+fe.Field, fe.Code), true
	}
	return nil, false
}

// folderFilter is the list's folder parameter: any folder, none, or one folder.
type folderFilter struct {
	any bool
	id  sql.NullInt64 // invalid: no folder
}

// parseFolderFilter reads s; a bad one is refused at location.
func parseFolderFilter(s, location string) (folderFilter, error) {
	switch s {
	case "", "all":
		return folderFilter{any: true}, nil
	case "none":
		return folderFilter{}, nil
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id < 1 {
		return folderFilter{}, fieldErrValue(location, "bad_folder", s)
	}
	return folderFilter{id: sql.NullInt64{Int64: id, Valid: true}}, nil
}

func (f folderFilter) matches(u db.User) bool { return f.any || f.id == u.FolderID }

// folderKey is the key of a user's folder in folder_counts.
func folderKey(u db.User) string {
	if !u.FolderID.Valid {
		return "none"
	}
	return strconv.FormatInt(u.FolderID.Int64, 10)
}

// hiddenMatches: "hide" lists the users that are not hidden, "show" all, "only" the hidden.
func hiddenMatches(mode string, u db.User) bool {
	switch mode {
	case "show":
		return true
	case "only":
		return u.Hidden != 0
	}
	return u.Hidden == 0
}

// stateAttention lists what the overview asks the admin to look at: users out of traffic,
// then those about to expire, then the expired; one pass over the users instead of three.
const stateAttention = "attention"

func attentionRank(state string) int {
	switch state {
	case domain.StateLimited:
		return 0
	case domain.StateExpiring:
		return 1
	case domain.StateExpired:
		return 2
	}
	return -1
}

// userFilter is the users list's filter, and what a bulk action by filter takes its users
// by: the same code on both sides, so "the users that match" are the same users.
//
// The filters stay in Go: the search lowercases as Go does (PostgreSQL's lower() follows the
// database's locale, and a C locale leaves Cyrillic as it is), and the states are
// domain.State.
type userFilter struct {
	state, source, hidden string
	query                 string // lowercased and trimmed; empty: no search
	folder                folderFilter
}

// newUserFilter reads a filter whose fields are at at ("query." for the list, "body.filter."
// for a bulk body); what is not given is the list's default. A search by name, contact, note
// or tag tells with its total and the ids it finds whether the text is in a subscriber,
// however the answer is masked: an operator who may not see the subscribers does not search
// them.
func newUserFilter(ctx context.Context, at, state, query, folder, source, hidden string) (userFilter, error) {
	f := userFilter{state: cmp.Or(state, "all"), source: cmp.Or(source, "all"), hidden: cmp.Or(hidden, "show"), query: strings.ToLower(strings.TrimSpace(query))}
	if f.query != "" && hidesPeople(ctx) {
		return userFilter{}, huma.Error403Forbidden("support_denied")
	}
	var err error
	f.folder, err = parseFolderFilter(folder, at+"folder")
	return f, err
}

// facets are the parts of f the list counts users by, each on its own.
func (f userFilter) facets(u db.User) (okHidden, okFolder, okSource bool) {
	return hiddenMatches(f.hidden, u), f.folder.matches(u), f.source == "all" || f.source == u.Source
}

// wants is the rest of f, for a user whose facets match and whose state is st: the state
// and the search.
func (f userFilter) wants(u db.User, st string) bool {
	if f.state == stateAttention {
		if attentionRank(st) < 0 {
			return false
		}
	} else if f.state != "all" && !(st == f.state || (f.state == domain.StateActive && st == domain.StateExpiring)) {
		return false
	}
	return f.query == "" || strings.Contains(strings.ToLower(u.Name+" "+u.Contact+" "+u.Note+" "+u.Tags), f.query)
}

// matchingIDs are the ids of the users f matches now: the users the list shows for it,
// whatever their number.
func (h *handlers) matchingIDs(ctx context.Context, f userFilter) ([]int64, error) {
	users, err := h.d.Store.Q.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	now := h.d.Now()
	grants, err := domain.LoadGrantsLeft(ctx, h.d.Store.Q, now)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, u := range users {
		if okHidden, okFolder, okSource := f.facets(u); okHidden && okFolder && okSource && f.wants(u, domain.State(u, grants.Main(u.ID), now)) {
			ids = append(ids, u.ID)
		}
	}
	return ids, nil
}

func (h *handlers) listUsers(ctx context.Context, in *listUsersInput) (*listUsersOutput, error) {
	// Only the page's users get their slots read.
	f, err := newUserFilter(ctx, "query.", in.State, in.Query, in.Folder, in.Source, in.Hidden)
	if err != nil {
		return nil, err
	}
	users, err := h.d.Store.Q.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	now := h.d.Now()
	grants, err := domain.LoadGrantsLeft(ctx, h.d.Store.Q, now)
	if err != nil {
		return nil, err
	}
	out := &listUsersOutput{}
	b := &out.Body
	b.FolderCounts = map[string]int{"none": 0}
	b.SourceCounts = map[string]int{}
	for _, o := range domain.UserOrigins {
		b.SourceCounts[o] = 0
	}
	folders, err := h.d.Store.Q.ListFolders(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range folders {
		b.FolderCounts[strconv.FormatInt(f.ID, 10)] = 0
	}
	var matched []db.User
	for _, u := range users {
		b.UsersTotal++
		if u.Hidden != 0 {
			b.HiddenTotal++
		}
		// Each part of the filter on its own; a count leaves out only its own part.
		okHidden, okFolder, okSource := f.facets(u)
		if okHidden && okSource {
			b.FolderCounts[folderKey(u)]++
		}
		if okHidden && okFolder {
			b.SourceCounts[u.Source]++
		}
		if !okHidden || !okFolder || !okSource {
			continue
		}
		st := domain.State(u, grants.Main(u.ID), now)
		c := &b.Counts
		c.All++
		switch st {
		case domain.StateActive:
			c.Active++
		case domain.StateExpiring:
			c.Expiring++
			c.Active++
		case domain.StateLimited:
			c.Limited++
		case domain.StateExpired:
			c.Expired++
		case domain.StateDisabled:
			c.Disabled++
		}
		if !f.wants(u, st) {
			continue
		}
		matched = append(matched, u)
	}
	if f.state == stateAttention {
		slices.SortStableFunc(matched, func(a, b db.User) int {
			return attentionRank(domain.State(a, grants.Main(a.ID), now)) - attentionRank(domain.State(b, grants.Main(b.ID), now))
		})
	}
	out.Body.Total = len(matched)
	end := min(in.Offset+in.Limit, len(matched))
	out.Body.Items = []UserView{}
	if in.Offset < len(matched) {
		page := matched[in.Offset:end]
		ids := make([]int64, len(page))
		for i, u := range page {
			ids[i] = u.ID
		}
		names, err := h.userSlots(ctx, ids)
		if err != nil {
			return nil, err
		}
		counts, err := h.d.Store.Q.CountBoundDevicesOf(ctx, ids)
		if err != nil {
			return nil, err
		}
		bound := make(map[int64]int64, len(counts))
		for _, c := range counts {
			bound[c.UserID] = c.N
		}
		env := h.userEnv(ctx)
		if err := h.lifecycleOf(ctx, &env, ids); err != nil {
			return nil, err
		}
		for _, u := range page {
			out.Body.Items = append(out.Body.Items, h.viewUser(u, names[u.ID], bound[u.ID], grants, env))
		}
	}
	return out, nil
}

func (h *handlers) userResult(ctx context.Context, u db.User, err error) (*userOutput, error) {
	if err != nil {
		return nil, mapDomainErr(err)
	}
	slots, err := h.d.Store.Q.ListUserSlots(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	bound, err := h.d.Store.Q.CountBoundDevices(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	grants, err := domain.UserGrantsLeft(ctx, h.d.Store.Q, u.ID, h.d.Now())
	if err != nil {
		return nil, err
	}
	env := h.userEnv(ctx)
	if err := h.lifecycleOf(ctx, &env, []int64{u.ID}); err != nil {
		return nil, err
	}
	return &userOutput{Body: h.viewUser(u, slots, bound, grants, env)}, nil
}

func (h *handlers) createUser(ctx context.Context, in *createUserInput) (*userOutput, error) {
	u, err := h.d.Users.Create(ctx, domain.CreateInput{Name: in.Body.Name, Contact: in.Body.Contact, Note: in.Body.Note, Tags: in.Body.Tags, TariffID: in.Body.TariffID,
		Source: domain.UserFromAdmin, StartsOnConnect: in.Body.StartsOnConnect})
	if errors.Is(err, domain.ErrNotFound) {
		return nil, fieldErr("body.tariff_id", "tariff_not_found")
	}
	if err == nil {
		h.audit(ctx, sessionOf(ctx).AdminID, "user.create", "user", strconv.FormatInt(u.ID, 10), map[string]any{"tariff_id": in.Body.TariffID})
	}
	return h.userResult(ctx, u, err)
}

func (h *handlers) getUser(ctx context.Context, in *userIDInput) (*userOutput, error) {
	u, err := h.d.Users.Get(ctx, in.ID)
	out, err := h.userResult(ctx, u, err)
	if err == nil {
		if l, lerr := h.d.Store.Q.TgLinkOfUser(ctx, u.ID); lerr == nil {
			out.Body.Telegram = &TelegramLink{ID: l.TgID, Username: l.Username, Name: l.FirstName}
		}
		// An old link is a credential like the own one: not for a key that may only read.
		if !hidesLinks(ctx) {
			out.Body.Legacy = h.legacyLinks(ctx, u.ID)
		}
	}
	return out, err
}

func (h *handlers) updateUser(ctx context.Context, in *patchUserInput) (*userOutput, error) {
	b := in.Body
	if b.ExpiresAt != nil && (b.ExpiresAt.Year() < 0 || b.ExpiresAt.Year() > 9999) {
		return nil, fieldErr("body.expires_at", "bad_expiry")
	}
	p := domain.Patch{
		Name: b.Name, Contact: b.Contact, Note: b.Note, Tags: b.Tags, Disabled: b.Disabled,
		TrafficLimit: b.TrafficLimit, ClearTrafficLimit: b.TrafficUnlimited,
		DeviceLimit: b.DeviceLimit, ClearDeviceLimit: b.DevicesUnlimited, DeviceMode: b.DeviceMode,
		SpeedLimit: b.SpeedLimit, ClearSpeedLimit: b.SpeedUnlimited,
		ExpiresAt: b.ExpiresAt, ClearExpiry: b.NeverExpires, Inbounds: b.Inbounds, TariffID: b.TariffID, Hidden: b.Hidden,
		StartsOnConnect: b.StartsOnConnect,
	}
	if b.FolderID != nil {
		if *b.FolderID == 0 {
			p.ClearFolder = true
		} else {
			p.FolderID = b.FolderID
		}
	}
	if b.BillingDay != nil {
		if *b.BillingDay == 0 {
			p.ClearBillingDay = true
		} else {
			p.BillingDay = b.BillingDay
		}
	}
	// A rename is written down with the old name: the journal is where the admin finds who
	// a user was called before.
	var before string
	if b.Name != nil {
		if old, err := h.d.Users.Get(ctx, in.ID); err == nil {
			before = old.Name
		}
	}
	u, err := h.d.Users.Update(ctx, in.ID, p)
	if err == nil {
		var details any
		if b.Name != nil && u.Name != before {
			details = map[string]any{"name": u.Name, "name_was": before}
		}
		h.audit(ctx, sessionOf(ctx).AdminID, "user.update", "user", strconv.FormatInt(in.ID, 10), details)
	}
	return h.userResult(ctx, u, err)
}

func (h *handlers) deleteUser(ctx context.Context, in *userIDInput) (*struct{}, error) {
	if err := h.d.Users.Delete(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user.delete", "user", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

func (h *handlers) extendUser(ctx context.Context, in *extendInput) (*userOutput, error) {
	b := in.Body
	if (b.Days > 0) == (b.Months > 0) {
		return nil, fieldErr("body", "extend_amount")
	}
	var u db.User
	var err error
	if b.Months > 0 {
		u, err = h.d.Users.ExtendMonths(ctx, in.ID, b.Months)
	} else {
		u, err = h.d.Users.Extend(ctx, in.ID, b.Days)
	}
	if err == nil {
		h.audit(ctx, sessionOf(ctx).AdminID, "user.extend", "user", strconv.FormatInt(in.ID, 10), map[string]any{"days": b.Days, "months": b.Months})
	}
	return h.userResult(ctx, u, err)
}

// BoundDeviceView is a device bound to a subscription, as the admin sees it.
type BoundDeviceView struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name" doc:"Своё имя устройства от админа или подписчика; пусто — имя от приложения (модель, система)"`
	HWID        string    `json:"hwid" doc:"ID устройства от приложения; пусто — общее место приложений без ID"`
	OS          string    `json:"os"`
	OSVersion   string    `json:"os_version"`
	Model       string    `json:"model"`
	App         string    `json:"app"`
	LastIP      string    `json:"last_ip"`
	Online      bool      `json:"online"`
	CreatedAt   time.Time `json:"created_at"`
	LastSeen    time.Time `json:"last_seen"`
	TrafficUp   int64     `json:"traffic_up" doc:"Байты от устройства с тех пор, как оно привязано (до 0.5.0.5 не считались)"`
	TrafficDown int64     `json:"traffic_down" doc:"Байты к устройству с тех пор, как оно привязано"`
}

type boundDevicesOutput struct{ Body []BoundDeviceView }

type unbindInput struct {
	ID     int64 `path:"id" minimum:"1"`
	Device int64 `path:"device" minimum:"1"`
}

func (h *handlers) boundDevices(ctx context.Context, in *userIDInput) (*boundDevicesOutput, error) {
	if _, err := h.d.Users.Get(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	devs, err := h.d.Store.Q.ListBoundDevices(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	rows, err := h.d.Store.Q.ListBoundDeviceSlots(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	name := make(map[int64]string, len(rows))
	for _, r := range rows {
		name[r.DeviceID] = r.SlotName
	}
	used, err := h.d.Store.Q.ListBoundDeviceTraffic(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	traffic := make(map[int64]db.ListBoundDeviceTrafficRow, len(used))
	for _, t := range used {
		traffic[t.DeviceID] = t
	}
	env := userEnv{online: h.online()}
	out := &boundDevicesOutput{Body: []BoundDeviceView{}}
	for _, d := range devs {
		out.Body = append(out.Body, BoundDeviceView{ID: d.ID, Name: d.Name, HWID: d.Hwid, OS: d.Os, OSVersion: d.OsVersion, Model: d.Model, App: d.App, LastIP: d.LastIp,
			Online: len(env.liveIPs([]string{name[d.ID]})) > 0, CreatedAt: store.Time(d.CreatedAt), LastSeen: store.Time(d.LastSeen),
			TrafficUp: traffic[d.ID].Up, TrafficDown: traffic[d.ID].Down})
	}
	return out, nil
}

func (h *handlers) unbindDevice(ctx context.Context, in *unbindInput) (*struct{}, error) {
	if err := h.d.Devices.Unbind(ctx, in.ID, in.Device, false); err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user.unbind_device", "user", strconv.FormatInt(in.ID, 10), map[string]any{"device": in.Device})
	return nil, nil
}

type renameDeviceInput struct {
	ID     int64 `path:"id" minimum:"1"`
	Device int64 `path:"device" minimum:"1"`
	Body   struct {
		Name string `json:"name" maxLength:"40" doc:"Своё имя устройства, до 40 символов одной строкой; пустое — вернуть имя от приложения"`
	}
}

type renameDeviceOutput struct {
	Body struct {
		Name string `json:"name" doc:"Имя, как оно сохранено: без пробелов по краям"`
	}
}

func (h *handlers) renameDevice(ctx context.Context, in *renameDeviceInput) (*renameDeviceOutput, error) {
	name, err := h.d.Devices.Rename(ctx, in.ID, in.Device, in.Body.Name)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user.rename_device", "user", strconv.FormatInt(in.ID, 10), map[string]any{"device": in.Device, "name": name})
	out := &renameDeviceOutput{}
	out.Body.Name = name
	return out, nil
}

// DeviceBanView is a device banned from a subscription.
type DeviceBanView struct {
	ID       int64     `json:"id"`
	HWID     string    `json:"hwid" doc:"ID устройства, которому закрыта привязка"`
	Label    string    `json:"label" doc:"Как устройство называлось, когда его заблокировали; пусто — приложение ничего о себе не сообщило"`
	Admin    string    `json:"admin" doc:"Кто заблокировал; пусто — ключ API или удалённый админ"`
	BannedAt time.Time `json:"banned_at"`
	// Until is set for a device the subscriber unbound: it may bind again from then.
	Until *time.Time `json:"until,omitempty" doc:"Подписчик сам отвязал устройство: оно сможет привязаться снова с этого момента; пусто — заблокировано админом навсегда"`
	// Forever: the subscriber unbound it, and it comes back only when the admin lets it.
	Forever bool `json:"forever,omitempty" doc:"Подписчик отвязал устройство, а вернуть его может только админ (until не показывается)"`
}

type deviceBanOutput struct{ Body DeviceBanView }

type deviceBansOutput struct{ Body []DeviceBanView }

type unbanInput struct {
	ID  int64 `path:"id" minimum:"1"`
	Ban int64 `path:"ban" minimum:"1"`
}

func (h *handlers) banDevice(ctx context.Context, in *unbindInput) (*deviceBanOutput, error) {
	admin := sessionOf(ctx).AdminID
	b, err := h.d.Devices.Ban(ctx, in.ID, in.Device, admin)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, admin, "user.ban_device", "user", strconv.FormatInt(in.ID, 10), map[string]any{"device": in.Device, "ban": b.ID, "label": b.Label})
	v := DeviceBanView{ID: b.ID, HWID: b.Hwid, Label: b.Label, BannedAt: store.Time(b.BannedAt)}
	if b.AdminID.Valid {
		if a, err := h.d.Store.Q.GetAdmin(ctx, b.AdminID.Int64); err == nil {
			v.Admin = a.Username
		}
	}
	return &deviceBanOutput{Body: v}, nil
}

func (h *handlers) deviceBans(ctx context.Context, in *userIDInput) (*deviceBansOutput, error) {
	if _, err := h.d.Users.Get(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	rows, err := h.d.Store.Q.ListDeviceBans(ctx, db.ListDeviceBansParams{UserID: in.ID, Until: sql.NullInt64{Int64: h.d.Now().Unix(), Valid: true}})
	if err != nil {
		return nil, err
	}
	out := &deviceBansOutput{Body: make([]DeviceBanView, 0, len(rows))}
	for _, r := range rows {
		v := DeviceBanView{ID: r.ID, HWID: r.Hwid, Label: r.Label, Admin: r.AdminName, BannedAt: store.Time(r.BannedAt)}
		if r.Until.Valid {
			t := store.Time(r.Until.Int64)
			if domain.Forever(t) {
				v.Forever = true
			} else {
				v.Until = &t
			}
		}
		out.Body = append(out.Body, v)
	}
	return out, nil
}

func (h *handlers) unbanDevice(ctx context.Context, in *unbanInput) (*struct{}, error) {
	b, err := h.d.Devices.Unban(ctx, in.ID, in.Ban)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user.unban_device", "user", strconv.FormatInt(in.ID, 10), map[string]any{"ban": b.ID, "label": b.Label})
	return nil, nil
}

func (h *handlers) resetUserTraffic(ctx context.Context, in *userIDInput) (*userOutput, error) {
	u, err := h.d.Users.ResetTraffic(ctx, in.ID)
	if err == nil {
		h.audit(ctx, sessionOf(ctx).AdminID, "user.reset_traffic", "user", strconv.FormatInt(in.ID, 10), nil)
	}
	return h.userResult(ctx, u, err)
}

func (h *handlers) reissueUser(ctx context.Context, in *userIDInput) (*userOutput, error) {
	u, err := h.d.Users.Reissue(ctx, in.ID)
	if err == nil {
		h.audit(ctx, sessionOf(ctx).AdminID, "user.reissue", "user", strconv.FormatInt(in.ID, 10), nil)
	}
	return h.userResult(ctx, u, err)
}

// bulkOpt is what the action of in needs besides the users.
func bulkOpt(in *bulkInput) domain.BulkOpt {
	opt := domain.BulkOpt{Days: in.Body.Days}
	if in.Body.Action == domain.BulkMove && in.Body.Folder > 0 {
		opt.Folder = &in.Body.Folder
	}
	return opt
}

// bulkPreview is what bulkUsers would do with the same body, done by nothing: the admin
// confirms what they see. A folder named is checked as the action checks it.
// bulkTargets are the users the body names: its ids, or the users its filter matches now
// (the list's own filter, so they are the users the list shows for it). One of the two.
func (h *handlers) bulkTargets(ctx context.Context, in *bulkInput) ([]int64, error) {
	b := &in.Body
	switch {
	case b.Filter != nil && len(b.IDs) > 0:
		return nil, fieldErr("body.filter", "ids_or_filter")
	case b.Filter == nil && len(b.IDs) == 0:
		return nil, fieldErr("body.ids", "ids_or_filter")
	case b.Filter == nil:
		return b.IDs, nil
	}
	f := b.Filter
	uf, err := newUserFilter(ctx, "body.filter.", f.State, f.Query, f.Folder, f.Source, f.Hidden)
	if err != nil {
		return nil, err
	}
	return h.matchingIDs(ctx, uf)
}

func (h *handlers) bulkPreview(ctx context.Context, in *bulkInput) (*bulkPreviewOutput, error) {
	ids, err := h.bulkTargets(ctx, in)
	if err != nil {
		return nil, err
	}
	opt := bulkOpt(in)
	if opt.Folder != nil {
		if _, err := h.d.Store.Q.GetFolder(ctx, *opt.Folder); errors.Is(err, sql.ErrNoRows) {
			return nil, fieldErr("body.folder_id", "folder_not_found")
		} else if err != nil {
			return nil, err
		}
	}
	plan, err := h.d.Users.BulkPlan(ctx, ids, in.Body.Action, opt)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	out := &bulkPreviewOutput{}
	b := &out.Body
	b.Requested, b.Found, b.Missing, b.Changed, b.Unchanged = plan.Asked, plan.Found, plan.Asked-plan.Found, plan.Changed, plan.Found-plan.Changed
	b.Sample = make([]BulkSample, 0, len(plan.Sample))
	for _, c := range plan.Sample {
		s := BulkSample{UserID: c.User.ID, UserName: c.User.Name, State: c.State, ExpiresAt: store.TimePtr(c.User.ExpiresAt),
			Used: c.User.UsedUp + c.User.UsedDown, FolderID: store.Int64Ptr(c.User.FolderID)}
		if in.Body.Action == domain.BulkExtend {
			s.ExpiresAfter = store.TimePtr(c.Expires)
		}
		b.Sample = append(b.Sample, s)
	}
	if plan.AtStake != nil {
		b.AtStake = &BulkAtStake{Active: plan.Active, WithBalance: int(plan.AtStake.WithBalance), Paying: int(plan.AtStake.Paying), Granted: int(plan.AtStake.Granted)}
	}
	return out, nil
}

func (h *handlers) bulkUsers(ctx context.Context, in *bulkInput) (*bulkOutput, error) {
	ids, err := h.bulkTargets(ctx, in)
	if err != nil {
		return nil, err
	}
	// By filter, the admin confirmed a number: another one now (users came or went since the
	// preview) is asked again rather than done to users nobody counted.
	if in.Body.Filter != nil && in.Body.Expect != nil && *in.Body.Expect != len(ids) {
		return nil, huma.Error409Conflict("filter_changed")
	}
	// One transaction for the list: it is applied whole or not at all.
	opt := bulkOpt(in)
	details := map[string]any{"requested": len(ids)}
	if f := in.Body.Filter; f != nil {
		details["filter"] = f
	}
	if opt.Folder != nil {
		details["folder"] = *opt.Folder
	}
	n, err := h.d.Users.Bulk(ctx, ids, in.Body.Action, opt)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	out := &bulkOutput{}
	out.Body.Affected = n
	details["count"] = n
	h.audit(ctx, sessionOf(ctx).AdminID, "user.bulk_"+in.Body.Action, "user", "", details)
	return out, nil
}

func (h *handlers) userTraffic(ctx context.Context, in *trafficInput) (*trafficOutput, error) {
	if _, err := h.d.Users.Get(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	out := &trafficOutput{}
	out.Body.Points = []TrafficPoint{}
	daily, since := trafficSince(h.d.Now(), in.Range)
	if daily {
		rows, err := h.d.Store.Q.UserTrafficDaily(ctx, db.UserTrafficDailyParams{UserID: in.ID, Day: since})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out.Body.Points = append(out.Body.Points, TrafficPoint{T: store.Time(r.Day * 86400), Up: r.Up, Down: r.Down})
		}
		return out, nil
	}
	rows, err := h.d.Store.Q.UserTrafficHourly(ctx, db.UserTrafficHourlyParams{UserID: in.ID, Hour: since})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.Body.Points = append(out.Body.Points, TrafficPoint{T: store.Time(r.Hour * 3600), Up: r.Up, Down: r.Down})
	}
	return out, nil
}

func (h *handlers) userDevices(ctx context.Context, in *userIDInput) (*devicesOutput, error) {
	if _, err := h.d.Users.Get(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	rows, err := h.d.Store.Q.ListUserDevices(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	slots, err := h.d.Store.Q.ListUserSlots(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	live := userEnv{online: h.online()}.liveIPs(slots)
	out := &devicesOutput{Body: []DeviceView{}}
	for _, d := range rows {
		out.Body = append(out.Body, DeviceView{IP: d.Ip, FirstSeen: store.Time(d.FirstSeen), LastSeen: store.Time(d.LastSeen), Online: slices.Contains(live, d.Ip)})
	}
	return out, nil
}

// requireUser is nil when the user exists and a 404 not_found when it does not: the check
// the per-user endpoints (pools, grants, torrent bans) make before they touch anything.
func requireUser(ctx context.Context, q *db.Queries, id int64) error {
	if _, err := q.GetUser(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return huma.Error404NotFound("not_found")
	} else if err != nil {
		return err
	}
	return nil
}
