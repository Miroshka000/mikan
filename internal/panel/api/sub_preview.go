package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/subsrc"
	"mikan/internal/sub/apps"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/serve"
)

// The preview of a client's subscription (#290): what the app of the headers the admin writes
// would get of it, made by the very code that answers a fetch, with nothing changed: the keys
// are placeholders, no device takes a place, the client is placed in no balance group and
// nothing is logged (subsrc.Handler.PreviewUser). The card shows it as "what this client
// gets" and "how this app sees it".

// maxPreviewBody bounds the profile a preview carries back: a Clash profile with every list
// is a few hundred kilobytes, a broken template must not make it megabytes.
const maxPreviewBody = 2 << 20

type subPreviewInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		UserAgent string        `json:"user_agent" maxLength:"1024" doc:"User-Agent приложения; пусто — запрос без него"`
		Headers   []apps.Header `json:"headers,omitempty" maxItems:"30" doc:"Остальные заголовки запроса: X-Hwid, X-Device-Os, Accept, Host, свои"`
		Format    string        `json:"format,omitempty" enum:"clash,singbox,uri,xray,html" doc:"Формат, который называет ссылка (?format=); не задан — как решат заголовки"`
	}
}

// SubPreviewHeader is a header of the answer; a value an app reads as base64 is told as text too.
type SubPreviewHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Text  string `json:"text,omitempty" doc:"Значение вида base64:… как текст"`
}

// SubPreviewProxy is a server of the answer as the app gets it.
type SubPreviewProxy struct {
	Name   string `json:"name"`
	Type   string `json:"type" doc:"Протокол: vless, hysteria2, tuic…"`
	Server string `json:"server"`
	Port   int    `json:"port"`
	Ports  string `json:"ports,omitempty" doc:"Диапазон портов Hysteria2"`
	NodeID int64  `json:"node_id"`
}

// SubPreviewLeft is a server of the panel, or one of its addresses, the answer leaves out.
type SubPreviewLeft struct {
	InboundID   int64  `json:"inbound_id"`
	InboundName string `json:"inbound_name" doc:"Имя протокола в подписке"`
	NodeID      int64  `json:"node_id"`
	HostID      int64  `json:"host_id,omitempty" doc:"Адрес для клиентов; не задан — протокол целиком"`
	Address     string `json:"address,omitempty" doc:"Адрес; пусто у адреса — адрес ноды"`
	HostName    string `json:"host_name,omitempty" doc:"Своё имя адреса в подписке"`
	Reason      string `json:"reason" enum:"inbound_off,node_off,not_own,not_in_group,not_in_tariff,pool_shut,hidden,balance,host_state,host_trial,host_group,app,broken" doc:"inbound_off — протокол выключен, node_off — нода выключена, not_own — нет в своём списке протоколов карточки, not_in_group — нет в серверах группы, not_in_tariff — нет в серверах тарифа, pool_shut — группа серверов закрыта или её трафик кончился, hidden — на ноде клиенту не выдаётся ни один адрес, balance — балансировка держит клиента на другой ноде, host_state, host_trial, host_group — адрес выдаётся другим статусам, пробным или группам, app — приложение не умеет этот протокол, broken — протокол не собрался"`
	Error       string `json:"error,omitempty" doc:"Для broken: почему"`
}

// SubPreviewTemplate is the own template the profile is made of.
type SubPreviewTemplate struct {
	ID   int64  `json:"id"`
	Core string `json:"core" enum:"mihomo,singbox,xray"`
	Kind string `json:"kind" enum:"text,simple" doc:"simple — простой шаблон: встроенный профиль из его маршрутизации"`
	Name string `json:"name"`
}

// SubPreview is what the app of a request gets of a client's subscription.
type SubPreview struct {
	Status   int    `json:"status" doc:"Код ответа; 0 — соединение закрыто без ответа"`
	Outcome  string `json:"outcome" enum:"served,stub,blocked,dropped,error" doc:"error — панель не смогла ответить, причина в её журнале"`
	AppID    string `json:"app_id" doc:"Известное приложение по User-Agent; пусто — неизвестное"`
	AppName  string `json:"app_name"`
	Rule     string `json:"rule" doc:"Что решило: имя правила ответа, builtin:ssr, builtin:old, apps (список приложений); пусто — ничего"`
	Position int    `json:"position" doc:"Место сработавшего правила в списке, с 1; 0 — не правило из списка"`
	Respond  string `json:"respond" enum:"serve,stub,403,404,html,drop"`
	Format   string `json:"format" enum:",clash,singbox,uri,xray,html" doc:"Формат ответа: clash, singbox, xray, uri, html (страница подписки); пусто — правило ответило без профиля"`
	// Template is nil for the built-in profile.
	Template       *SubPreviewTemplate `json:"template" doc:"Свой шаблон, из которого собран профиль; null — встроенный профиль"`
	TemplateFailed bool                `json:"template_failed" doc:"Шаблон не собрался для этого клиента, ушёл встроенный профиль"`
	Stub           string              `json:"stub" enum:",unsupported,device_limit,no_hwid,device_banned,device_unbound,inactive,no_proxies" doc:"Почему вместо серверов ушла заглушка: unsupported, device_limit, no_hwid, device_banned, device_unbound, inactive, no_proxies; пусто — заглушки нет"`
	Device         string              `json:"device" enum:",known,new,shared,inactive" doc:"Что получит устройство: known — привязано и останется, new — займёт новое место, shared — общее место приложений без ID, inactive — подписка не работает, места не займёт; пусто — до ключей не дошло"`
	GroupID        *int64              `json:"group_id" doc:"Группа, в которой клиент сейчас"`
	ByGroup        bool                `json:"by_group" doc:"Серверы клиента — свои серверы его группы"`
	Own            bool                `json:"own" doc:"Серверы клиента — свой список протоколов карточки"`
	Headers        []SubPreviewHeader  `json:"headers" doc:"Заголовки ответа, по имени"`
	Proxies        []SubPreviewProxy   `json:"proxies" doc:"Серверы в ответе, по порядку"`
	Left           []SubPreviewLeft    `json:"left" doc:"Что из серверов панели в ответ не попало, и почему"`
	Notes          []submodel.Note     `json:"notes" doc:"Что ядро формата (sing-box, Xray) делает с профилем по-своему или опускает"`
	Body           string              `json:"body" doc:"Тело ответа: профиль с ключами-заглушками, ссылки — раскрытыми из base64; токен ссылки скрыт. Пусто для страницы подписки"`
	BodyCut        bool                `json:"body_cut,omitempty" doc:"Тело длиннее 2 МиБ и обрезано"`
}

type subPreviewOutput struct{ Body SubPreview }

func (h *handlers) registerSubPreview() {
	huma.Register(h.api, huma.Operation{OperationID: "user-sub-preview", Method: http.MethodPost, Path: "/api/v1/users/{id}/sub-preview",
		Summary:     "Что получит клиент: ответ на запрос его подписки",
		Description: "Ничего не меняет: ключи — заглушки, устройство места не занимает, балансировка клиента никуда не ставит, журнал запросов не пишется.",
		Tags:        []string{"users"}, Metadata: supportDiagnostic}, h.userSubPreview)
}

func (h *handlers) userSubPreview(ctx context.Context, in *subPreviewInput) (*subPreviewOutput, error) {
	// The answer is the subscriber's own: their name, traffic and term are in it.
	if hidesPeople(ctx) {
		return nil, huma.Error403Forbidden("support_denied")
	}
	if h.d.SubPreview == nil {
		return nil, huma.Error404NotFound("not_found")
	}
	u, err := h.d.Store.Q.GetUser(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	req := http.Header{}
	if ua := strings.TrimSpace(in.Body.UserAgent); ua != "" {
		req.Set("User-Agent", ua)
	}
	for i, hd := range in.Body.Headers {
		if !apps.ValidHeaderName(hd.Name) {
			return nil, fieldErrValue("body.headers["+strconv.Itoa(i)+"].name", "resp_header", hd.Name)
		}
		req.Add(hd.Name, hd.Value)
	}
	p, err := h.d.SubPreview(ctx, u, subsrc.PreviewInput{Headers: req, Format: in.Body.Format})
	if err != nil {
		return nil, err
	}
	v, err := h.subPreviewView(ctx, p)
	if err != nil {
		return nil, err
	}
	return &subPreviewOutput{Body: v}, nil
}

// subPreviewView is p as the API tells it.
func (h *handlers) subPreviewView(ctx context.Context, p subsrc.UserPreview) (SubPreview, error) {
	d := p.Decision
	v := SubPreview{Status: p.Status, Outcome: string(p.Outcome), AppID: d.App.ID, AppName: d.App.Name, Rule: d.Rule, Position: d.Position, Respond: d.Responds(),
		Format: p.Format, TemplateFailed: p.TemplateFailed, Stub: p.Stub, Device: p.Device, ByGroup: p.ByGroup, Own: p.Own,
		Headers: []SubPreviewHeader{}, Proxies: []SubPreviewProxy{}, Left: []SubPreviewLeft{}, Notes: p.Notes}
	if v.Outcome == "" {
		v.Outcome = "error"
	}
	if p.Group != 0 {
		v.GroupID = &p.Group
	}
	if v.Notes == nil {
		v.Notes = []submodel.Note{}
	}
	if p.Template.ID != 0 {
		t := &SubPreviewTemplate{ID: p.Template.ID, Core: string(p.Template.Core), Kind: templateKind(p.Template.Simple)}
		heads, err := h.d.Store.Q.ListSubTemplateHeads(ctx)
		if err != nil {
			return v, err
		}
		for _, x := range heads {
			if x.ID == t.ID {
				t.Name = x.Name
			}
		}
		v.Template = t
	}
	names := make([]string, 0, len(p.Header))
	for name := range p.Header {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		for _, value := range p.Header[name] {
			hv := SubPreviewHeader{Name: name, Value: value}
			if enc, ok := strings.CutPrefix(value, "base64:"); ok {
				if raw, err := base64.StdEncoding.DecodeString(enc); err == nil {
					hv.Text = string(raw)
				}
			}
			v.Headers = append(v.Headers, hv)
		}
	}
	served := p.Served
	if served == nil {
		served = p.Proxies
	}
	for _, x := range served {
		v.Proxies = append(v.Proxies, SubPreviewProxy{Name: x.Name, Type: x.Type, Server: x.Server, Port: x.Port, Ports: x.Ports, NodeID: x.Node})
	}
	for _, l := range p.Left {
		v.Left = append(v.Left, SubPreviewLeft{InboundID: l.Inbound.ID, InboundName: domain.ProxyName(l.Inbound), NodeID: l.Inbound.NodeID,
			HostID: l.Host, Address: l.Address, HostName: l.HostName, Reason: l.Reason})
	}
	// What the app cannot use, and what could not be rendered, of what the subscription gave.
	kept := map[int64]bool{}
	for _, in := range p.Kept {
		kept[in.ID] = true
	}
	if p.Kept != nil {
		for _, in := range p.Inbounds {
			if !kept[in.ID] {
				v.Left = append(v.Left, SubPreviewLeft{InboundID: in.ID, InboundName: in.Name, NodeID: in.Node, Reason: "app"})
			}
		}
	}
	for _, s := range p.Skipped {
		v.Left = append(v.Left, SubPreviewLeft{InboundID: s.Inbound.ID, InboundName: s.Inbound.Name, NodeID: s.Inbound.Node, Reason: "broken", Error: s.Err})
	}
	v.Body, v.BodyCut = previewBody(p.Body, p.Format)
	return v, nil
}

// previewBody is the answer's body as text: the share links of the uri format undone from
// base64, cut at maxPreviewBody.
func previewBody(body []byte, format string) (string, bool) {
	if format == serve.FormatURI {
		if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body))); err == nil {
			body = raw
		}
	}
	if len(body) > maxPreviewBody {
		return string(body[:maxPreviewBody]), true
	}
	return string(body), false
}
