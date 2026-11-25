package api

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"mikan/internal/panel/domain"
	"mikan/internal/sub/apps"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/serve"
	"mikan/internal/textutil"
)

// How a group's subscriptions look (#290): templates, headers, devices and the addresses its
// clients reach inbounds at, over the panel's settings for the group's clients alone. A null is
// the panel's setting, read live: a change of the setting reaches the group. The texts and links
// are held to the rules of the settings they stand for, and changed by the admin's session only,
// as those settings are: a leaked key must not send a group's subscribers anywhere.

// GroupTemplates are a group's template of each core; null: the core's default.
type GroupTemplates struct {
	Mihomo  *int64 `json:"mihomo" doc:"Шаблон Clash (mihomo); null — шаблон по умолчанию"`
	Singbox *int64 `json:"singbox" doc:"Шаблон sing-box; null — шаблон по умолчанию"`
	Xray    *int64 `json:"xray" doc:"Шаблон Xray; null — шаблон по умолчанию"`
}

// GroupAddressView is where a group's clients reach an inbound in place of its default address.
type GroupAddressView struct {
	InboundID int64  `json:"inbound_id" minimum:"1"`
	Address   string `json:"address" maxLength:"253" doc:"Адрес или домен; пусто — адрес ноды"`
	Port      int    `json:"port" minimum:"0" maximum:"65535" doc:"0 — порт подключения"`
	SNI       string `json:"sni" maxLength:"253" doc:"Пусто — обычное имя TLS"`
	Host      string `json:"host" maxLength:"253" doc:"HTTP Host у WebSocket и XHTTP; пусто — как у сервера"`
	Name      string `json:"name" maxLength:"48" doc:"Имя в подписке, с переменными заголовка; пусто — имя подключения"`
	CDN       bool   `json:"cdn" doc:"Впереди CDN со своим сертификатом: сертификат ноды не закрепляется"`
}

// GroupAppearanceView is how a group's subscriptions look.
type GroupAppearanceView struct {
	Templates   GroupTemplates `json:"templates"`
	Title       *string        `json:"title" doc:"Название подписки в приложениях, с переменными как в sub_title; null — как в настройках подписки"`
	Announce    *string        `json:"announce" doc:"Объявление; пустая строка — без объявления; null — как в настройках подписки"`
	AnnounceURL *string        `json:"announce_url" doc:"Куда ведёт нажатие на объявление; null — как в настройках подписки"`
	SupportURL  *string        `json:"support_url" doc:"Ссылка на поддержку; null — как в настройках подписки"`
	UpdateHours *int           `json:"update_hours" doc:"Как часто приложения обновляют подписку, в часах (Profile-Update-Interval); null — раз в час"`
	Headers     []apps.Header  `json:"headers" doc:"Свои заголовки каждого ответа подписки; заголовки правила ответа сильнее"`
	RequireHWID *bool          `json:"require_hwid" doc:"Не выдавать подписку приложениям без ID устройства; null — как в настройках"`
	// UnbindOwn says whether Unbind is the group's own; without, Unbind is the panel's rules.
	UnbindOwn bool               `json:"unbind_own" doc:"Свои правила отвязки устройств; false — правила для всех, они в unbind"`
	Unbind    domain.UnbindRules `json:"unbind"`
	Page      bool               `json:"page" doc:"Своя страница подписки: GET, PUT и DELETE /api/v1/groups/{id}/page"`
	Addresses []GroupAddressView `json:"addresses" doc:"Где клиенты группы подключаются к протоколу вместо его адреса по умолчанию"`
}

// groupTemplatesBody are a group's templates as a body sets them: a core left out is its default.
type groupTemplatesBody struct {
	Mihomo  *int64 `json:"mihomo,omitempty" doc:"Шаблон Clash (mihomo); не передан — шаблон по умолчанию"`
	Singbox *int64 `json:"singbox,omitempty" doc:"Шаблон sing-box; не передан — шаблон по умолчанию"`
	Xray    *int64 `json:"xray,omitempty" doc:"Шаблон Xray; не передан — шаблон по умолчанию"`
}

// groupAddressBody is a group's address as a body sets it: a field left out keeps what the
// default address gives.
type groupAddressBody struct {
	InboundID int64  `json:"inbound_id" minimum:"1"`
	Address   string `json:"address,omitempty" maxLength:"253" doc:"Адрес или домен; не передан — адрес ноды"`
	Port      int    `json:"port,omitempty" minimum:"0" maximum:"65535" doc:"Не передан — порт подключения"`
	SNI       string `json:"sni,omitempty" maxLength:"253"`
	Host      string `json:"host,omitempty" maxLength:"253"`
	Name      string `json:"name,omitempty" maxLength:"48"`
	CDN       bool   `json:"cdn,omitempty"`
}

// groupAppearanceBody is a group's appearance as a body sets it, whole: what is left out is the
// panel's. The page has its own endpoint.
type groupAppearanceBody struct {
	Templates   groupTemplatesBody  `json:"templates,omitempty"`
	Title       *string             `json:"title,omitempty" maxLength:"200"`
	Announce    *string             `json:"announce,omitempty" maxLength:"1000" doc:"До 10 строк; пустая строка — без объявления"`
	AnnounceURL *string             `json:"announce_url,omitempty" maxLength:"200" doc:"https://… или tg://…"`
	SupportURL  *string             `json:"support_url,omitempty" maxLength:"200" doc:"https://… или tg://…"`
	UpdateHours *int                `json:"update_hours,omitempty" minimum:"1" maximum:"168"`
	Headers     []apps.Header       `json:"headers,omitempty" maxItems:"10" doc:"Как дополнительные заголовки правила ответа: без заголовков кэша и длины"`
	RequireHWID *bool               `json:"require_hwid,omitempty"`
	Unbind      *domain.UnbindRules `json:"unbind,omitempty" doc:"Свои правила отвязки; не передано — правила для всех"`
	Addresses   []groupAddressBody  `json:"addresses,omitempty" maxItems:"200"`
}

// groupAppearanceView is a's view; panel are the panel's unbinding rules, shown when the group
// has none of its own.
func groupAppearanceView(a domain.GroupAppearance, addrs []domain.GroupAddress, panel domain.UnbindRules) GroupAppearanceView {
	v := GroupAppearanceView{Title: a.Title, Announce: a.Announce, AnnounceURL: a.AnnounceURL, SupportURL: a.SupportURL, UpdateHours: a.UpdateHours,
		Headers: a.Headers, RequireHWID: a.RequireHWID, Unbind: panel, Page: a.Page, Addresses: []GroupAddressView{}}
	id := func(core submodel.Core) *int64 {
		if t, ok := a.Templates[core]; ok {
			return &t
		}
		return nil
	}
	v.Templates = GroupTemplates{Mihomo: id(submodel.CoreMihomo), Singbox: id(submodel.CoreSingBox), Xray: id(submodel.CoreXray)}
	if v.Headers == nil {
		v.Headers = []apps.Header{}
	}
	if a.Unbind != nil {
		v.UnbindOwn, v.Unbind = true, *a.Unbind
	}
	for _, x := range addrs {
		v.Addresses = append(v.Addresses, GroupAddressView{InboundID: x.InboundID, Address: x.Address, Port: x.Port, SNI: x.SNI, Host: x.Host, Name: x.Name, CDN: x.CDN})
	}
	return v
}

// groupAppearance is the appearance b sets, checked by the rules of the settings it stands for.
// The refusals are huma's field errors under body.appearance.
func (h *handlers) groupAppearance(ctx context.Context, b *groupAppearanceBody) (domain.GroupAppearance, []domain.GroupAddress, error) {
	const at = "body.appearance."
	a := domain.GroupAppearance{Templates: map[submodel.Core]int64{}, UpdateHours: b.UpdateHours, Headers: b.Headers, RequireHWID: b.RequireHWID, Unbind: b.Unbind}
	for core, id := range map[submodel.Core]*int64{submodel.CoreMihomo: b.Templates.Mihomo, submodel.CoreSingBox: b.Templates.Singbox, submodel.CoreXray: b.Templates.Xray} {
		if id != nil {
			a.Templates[core] = *id
		}
	}
	trim := func(s *string) *string {
		if s == nil {
			return nil
		}
		v := strings.TrimSpace(*s)
		return &v
	}
	a.Title, a.AnnounceURL, a.SupportURL = trim(b.Title), trim(b.AnnounceURL), trim(b.SupportURL)
	if b.Announce != nil {
		v := strings.TrimRight(*b.Announce, " \t\r\n")
		a.Announce = &v
	}
	var details []error
	if a.Title != nil && strings.ContainsAny(*a.Title, "\r\n") {
		details = append(details, fieldDetail(at+"title", "one_line", nil))
	}
	if a.Announce != nil && strings.Count(*a.Announce, "\n") >= serve.AnnounceLines {
		details = append(details, fieldDetail(at+"announce", "announce_lines", nil))
	}
	for field, link := range map[string]*string{"announce_url": a.AnnounceURL, "support_url": a.SupportURL} {
		if link != nil && *link != "" && !textutil.ValidLink(*link, serve.AppLinks) {
			details = append(details, fieldDetail(at+field, "support_url_invalid", nil))
		}
	}
	for i := range a.Headers {
		a.Headers[i].Name = strings.TrimSpace(a.Headers[i].Name)
	}
	var ce *apps.CheckError
	if err := apps.CheckHeaders("headers", a.Headers); errors.As(err, &ce) {
		details = append(details, fieldDetail(at+listIndex.ReplaceAllString(ce.Path, "[$1]"), ce.Code, ce.Detail))
	}
	addrs := make([]domain.GroupAddress, 0, len(b.Addresses))
	for i, x := range b.Addresses {
		if name := strings.TrimSpace(x.Name); name != "" {
			if code := h.checkSubName(ctx, name); code != "" {
				details = append(details, fieldDetail(at+"addresses["+strconv.Itoa(i)+"].name", code, nil))
			}
		}
		addrs = append(addrs, domain.GroupAddress{InboundID: x.InboundID, Address: x.Address, Port: x.Port, SNI: x.SNI, Host: x.Host, Name: x.Name, CDN: x.CDN})
	}
	if len(details) > 0 {
		return a, nil, fieldErrs(details...)
	}
	return a, addrs, nil
}

// appearanceError names a refusal of the domain by the appearance's field, a place in a list
// as the schema names it (addresses[2].port).
func appearanceError(err error) error {
	var fe *domain.FieldError
	if errors.As(err, &fe) {
		return fieldErr("body.appearance."+listIndex.ReplaceAllString(fe.Field, "[$1]"), fe.Code)
	}
	return err
}
