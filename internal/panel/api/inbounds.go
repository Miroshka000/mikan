package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	"mikan/internal/sub/apps"
)

type InboundView struct {
	ID          int64    `json:"id"`
	NodeID      int64    `json:"node_id"`
	Name        string   `json:"name"`
	Preset      string   `json:"preset"`
	Title       string   `json:"title"`
	Type        string   `json:"type" doc:"Тип листенера mihomo"`
	Network     string   `json:"network"`
	Port        string   `json:"port"`
	Listen      string   `json:"listen" doc:"Адрес, на котором нода слушает: пусто — все адреса, 127.0.0.1 — только сам сервер (за nginx или HAProxy)"`
	Enabled     bool     `json:"enabled"`
	DisplayName string   `json:"display_name" doc:"Своё имя в подписке; пусто — имя по умолчанию"`
	SubName     string   `json:"sub_name" doc:"Имя, которое увидит клиент"`
	Config      string   `json:"config" doc:"Шаблон листенера (YAML)"`
	Dest        string   `json:"dest,omitempty" doc:"Сайт для маскировки REALITY"`
	ServerNames []string `json:"server_names,omitempty"`
	// Fingerprint is the inbound's own uTLS profile, "" for the panel's default; absent when
	// its clients do not dial through uTLS (QUIC protocols, shared keys).
	Fingerprint *string        `json:"fingerprint,omitempty" doc:"Отпечаток TLS (uTLS) у клиентов; пусто — общий из настроек"`
	Obfs        *string        `json:"obfs,omitempty" doc:"Hysteria2: salamander, gecko или пусто (без обфускации); у других типов поля нет"`
	Status      string         `json:"status" enum:"ok,error,unknown"`
	Error       string         `json:"error,omitempty"`
	UpdatedAt   time.Time      `json:"updated_at"`
	Apps        []string       `json:"apps" doc:"Приложения, которым подключение попадает в подписку: mihomo, xray, singbox, stash, other"`
	Shared      bool           `json:"shared,omitempty" doc:"Один ключ на всех: учёт, лимиты и отключение по пользователям не работают"`
	NodeCert    bool           `json:"node_cert,omitempty" doc:"Работает на сертификате ноды (Hysteria2, TUIC, AnyTLS, TrustTunnel, VLESS TLS): с самоподписанным в sing-box приложениях не появится"`
	AutoPort    bool           `json:"auto_port" doc:"Панель сама переносит подключение на другой порт, если его блокируют (и включено в настройках)"`
	AutoSNI     bool           `json:"auto_sni" doc:"Панель сама меняет сайт маскировки REALITY, если он перестал подходить (и включено в настройках)"`
	Auto        AutoView       `json:"auto"`
	Outbound    string         `json:"outbound" enum:"direct,warp,node" doc:"Выход в интернет: напрямую с сервера, через WARP ноды или через другую ноду (каскад)"`
	ExitNodeID  *int64         `json:"exit_node_id,omitempty" doc:"Нода, через которую выходит трафик, если outbound=node"`
	PoolID      *int64         `json:"pool_id,omitempty" doc:"Группа серверов подключения: какой трафик оно тратит; нет — основной трафик"`
	InAuto      bool           `json:"in_auto" doc:"Подключение входит в «Авто» подписки; выключено — только в списке и группе страны (выключенная нода убирает все свои)"`
	Client      ClientEndpoint `json:"client" doc:"Куда подключаются клиенты, если не к ноде напрямую (mikan.client в шаблоне)"`
	ClientSNI   bool           `json:"client_sni" doc:"Можно ли задать клиентам свой SNI: у REALITY имя задаёт сайт маскировки"`
	ClientHost  bool           `json:"client_host" doc:"Можно ли задать клиентам свой HTTP Host: VLESS и Trojan по WebSocket и XHTTP"`
	CDNPossible bool           `json:"cdn_possible" doc:"Можно ли поставить подключение за CDN: VLESS или Trojan на сертификате ноды по XHTTP, WebSocket или gRPC"`
	// SNI443: behind the node's SNI router, clients reach it on 443.
	SNI443         bool `json:"sni443" doc:"Подключение делит порт 443 ноды с другими по имени TLS (SNI-роутер ноды); клиенты подключаются на 443"`
	SNI443Possible bool `json:"sni443_possible" doc:"Можно ли поставить за SNI-роутер: VLESS, Trojan или AnyTLS по TCP с TLS или REALITY"`

	// XHTTP is the tuning the form shows; absent for other transports.
	XHTTP *proto.XHTTPTuning `json:"xhttp,omitempty" doc:"Тонкая настройка XHTTP; у других транспортов поля нет"`

	// Tags are the admin's labels of the inbound, as a user's; clients see none of them.
	Tags []string `json:"tags" doc:"Метки подключения: свои слова админа, по ним фильтруют список; в подписку не попадают"`
}

// ClientEndpoint is where clients connect when a TCP proxy or a CDN stands in front of
// the node. Empty values (port 0) keep the node's address, the inbound's port and SNI.
type ClientEndpoint struct {
	Server string     `json:"server" maxLength:"253" doc:"Адрес для клиентов; пусто — адрес ноды"`
	Port   int        `json:"port" minimum:"0" maximum:"65535" doc:"Порт для клиентов; 0 — порт подключения"`
	SNI    string     `json:"sni" maxLength:"253" doc:"SNI для клиентов; пусто — как обычно"`
	Host   string     `json:"host,omitempty" maxLength:"253" doc:"HTTP Host у клиентов (WebSocket, XHTTP); пусто — как на сервере"`
	CDN    *ClientCDN `json:"cdn,omitempty" doc:"Клиенты ходят через CDN; нет — напрямую или через TCP-прокси"`
}

// ClientCDN is what a CDN in front of the node changes (proto.CDN).
type ClientCDN struct {
	Header  string   `json:"header,omitempty" maxLength:"64" doc:"Заголовок с настоящим адресом клиента: CF-Connecting-IP, X-Forwarded-For, X-Real-IP; пусто — нода видит адреса CDN"`
	Trusted []string `json:"trusted,omitempty" maxItems:"64" doc:"Сети CDN, чьему заголовку нода верит: CIDR, адреса, cloudflare, private, loopback"`
}

// AutoView is what the automatic moves see and last did for an inbound.
type AutoView struct {
	CutOff  bool       `json:"cut_off" doc:"Устройства, которые доходят до других подключений ноды, до этого не доходят"`
	Blocked int        `json:"blocked" doc:"Сколько таких устройств"`
	Reached int        `json:"reached" doc:"Сколько из проверяющих все подключения устройств до него дошли"`
	Since   *time.Time `json:"since,omitempty"`
	Stuck   string     `json:"stuck,omitempty" enum:"off,waiting,no_port,no_target,exhausted" doc:"Почему отрезанное подключение остаётся как есть"`
	// The REALITY target's last check, absent before the first one.
	TargetOK    *bool      `json:"target_ok,omitempty"`
	TargetError string     `json:"target_error,omitempty"`
	Last        *AutoEvent `json:"last,omitempty" doc:"Последняя автоматическая смена"`
}

type AutoEvent struct {
	Kind   string    `json:"kind" enum:"port,sni"`
	Old    string    `json:"old"`
	New    string    `json:"new"`
	Reason string    `json:"reason" enum:"blocked,target_down,still_blocked,busy"`
	At     time.Time `json:"at"`
}

type inboundsOutput struct{ Body []InboundView }
type inboundOutput struct{ Body InboundView }

type createInboundInput struct {
	Body struct {
		Preset string   `json:"preset" enum:"vless_reality_xhttp,hysteria2,hysteria2_gecko,tuic_v5,vless_reality_vision,vless_reality_grpc,trojan_reality,vless_tls_xhttp,vless_tls_vision,vless_tls_ws,vless_cdn_xhttp,vless_cdn_xhttp_get,anytls,vless_reality_xhttp_pq,trusttunnel,shadowquic,mieru,shadowsocks_2022,sudoku,snell,custom"`
		NodeID int64    `json:"node_id,omitempty" minimum:"1" doc:"Нода; по умолчанию — своя нода панели"`
		Port   string   `json:"port,omitempty" pattern:"^[0-9]{1,5}(-[0-9]{1,5})?$"`
		Dest   string   `json:"dest,omitempty" maxLength:"255" doc:"host:port для REALITY"`
		Config string   `json:"config,omitempty" maxLength:"65536" doc:"Шаблон листенера (YAML) для preset=custom"`
		Tags   []string `json:"tags,omitempty" maxItems:"20" doc:"Метки подключения, как у пользователя"`
	}
}

type patchInboundInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Port        *string         `json:"port,omitempty" pattern:"^[0-9]{1,5}(-[0-9]{1,5})?$"`
		Enabled     *bool           `json:"enabled,omitempty"`
		Dest        *string         `json:"dest,omitempty" maxLength:"255"`
		ServerName  *string         `json:"server_name,omitempty" maxLength:"253" doc:"SNI для клиентов, если dest — IP (цель из подбора соседей)"`
		Obfs        *string         `json:"obfs,omitempty" enum:"salamander,gecko" doc:"Обфускация Hysteria2. Gecko понимают только приложения на ядре mihomo 1.19.26+: остальные это подключение не получат"`
		Fingerprint *string         `json:"fingerprint,omitempty" maxLength:"32" doc:"Отпечаток TLS у клиентов: из списка (chrome, firefox, safari, ios, android, edge, 360, qq, random, randomized) или своё — латиница, цифры, _; пусто — общий из настроек"`
		DisplayName *string         `json:"display_name,omitempty" maxLength:"200" doc:"Можно с эмодзи: «🇳🇱 Нидерланды». Пусто — имя по умолчанию"`
		Config      *string         `json:"config,omitempty" maxLength:"65536" doc:"Шаблон листенера (YAML)"`
		Listen      *string         `json:"listen,omitempty" maxLength:"64" doc:"Адрес, на котором нода слушает: пусто — все адреса, иначе один IP (127.0.0.1 — за nginx или HAProxy на том же сервере). Свой адрес выключает перенос порта"`
		Client      *ClientEndpoint `json:"client,omitempty" doc:"Куда подключаются клиенты: адрес, порт, SNI и Host прокси или CDN перед нодой; заменяет всё сразу"`
		AutoPort    *bool           `json:"auto_port,omitempty" doc:"Нельзя включить, пока у подключения свой адрес (listen)"`
		AutoSNI     *bool           `json:"auto_sni,omitempty" doc:"Нельзя включить, пока у подключения свой адрес (listen)"`
		Outbound    *string         `json:"outbound,omitempty" enum:"direct,warp,node" doc:"Выход в интернет: напрямую, через WARP ноды или через другую ноду"`
		ExitNodeID  *int64          `json:"exit_node_id,omitempty" minimum:"1" doc:"Для outbound=node: через какую ноду"`
		PoolID      *int64          `json:"pool_id,omitempty" minimum:"0" doc:"Группа серверов; 0 — без группы, основной трафик"`
		InAuto      *bool           `json:"in_auto,omitempty" doc:"Подключение в «Авто» подписки; false — только в списке и группе страны"`

		XHTTP  *proto.XHTTPTuning `json:"xhttp,omitempty" doc:"Тонкая настройка XHTTP целиком: пустое поле — значение ядра по умолчанию"`
		SNI443 *bool              `json:"sni443,omitempty" doc:"Поставить за SNI-роутер ноды на 443 (true) или вернуть на свой порт (false)"`
		Tags   *[]string          `json:"tags,omitempty" maxItems:"20" doc:"Метки подключения целиком; пустой список снимает все"`
	}
}

type listInboundsInput struct {
	Tag string `query:"tag" maxLength:"100" doc:"Только подключения с этой меткой (без учёта регистра)"`
}

type validateInboundInput struct {
	Body struct {
		NodeID    int64  `json:"node_id,omitempty" minimum:"1"`
		InboundID int64  `json:"inbound_id,omitempty" minimum:"1" doc:"Подключение, чей шаблон проверяется: скрытые от читателя секреты (<hidden>) берутся из него"`
		Config    string `json:"config" maxLength:"65536"`
		Port      string `json:"port,omitempty" pattern:"^[0-9]{1,5}(-[0-9]{1,5})?$"`
	}
}

type validateInboundOutput struct {
	Body struct {
		Type    string `json:"type"`
		Network string `json:"network"`
	}
}

type presetsOutput struct{ Body []presets.Info }

func (h *handlers) registerInbounds() {
	huma.Register(h.api, huma.Operation{OperationID: "list-presets", Method: http.MethodGet, Path: "/api/v1/presets", Summary: "Доступные пресеты подключений", Tags: []string{"inbounds"}}, h.listPresets)
	huma.Register(h.api, huma.Operation{OperationID: "list-inbounds", Method: http.MethodGet, Path: "/api/v1/inbounds", Summary: "Подключения", Tags: []string{"inbounds"}}, h.listInbounds)
	huma.Register(h.api, huma.Operation{OperationID: "create-inbound", Method: http.MethodPost, Path: "/api/v1/inbounds", Summary: "Добавить подключение", Tags: []string{"inbounds"}, DefaultStatus: http.StatusCreated}, h.createInbound)
	huma.Register(h.api, huma.Operation{OperationID: "validate-inbound", Method: http.MethodPost, Path: "/api/v1/inbounds/validate", Summary: "Проверить шаблон листенера без сохранения", Tags: []string{"inbounds"}}, h.validateInbound)
	huma.Register(h.api, huma.Operation{OperationID: "copy-inbound", Method: http.MethodPost, Path: "/api/v1/inbounds/{id}/copy", Summary: "Скопировать подключение: те же настройки с новыми ключами, на ту же или другую ноду",
		Tags: []string{"inbounds"}, DefaultStatus: http.StatusCreated}, h.copyInbound)
	huma.Register(h.api, huma.Operation{OperationID: "set-inbound-exits", Method: http.MethodPost, Path: "/api/v1/inbounds/exit", Summary: "Выход в интернет для нескольких подключений разом: напрямую, через WARP или через ноду",
		Tags: []string{"inbounds"}}, h.setInboundExits)
	huma.Register(h.api, huma.Operation{OperationID: "update-inbound", Method: http.MethodPatch, Path: "/api/v1/inbounds/{id}", Summary: "Изменить подключение", Tags: []string{"inbounds"}}, h.updateInbound)
	huma.Register(h.api, huma.Operation{OperationID: "delete-inbound", Method: http.MethodDelete, Path: "/api/v1/inbounds/{id}", Summary: "Удалить подключение", Tags: []string{"inbounds"}, DefaultStatus: http.StatusNoContent}, h.deleteInbound)

	// Hosts are where clients are sent: as the client endpoint of update-inbound, they change
	// only in the admin's session (requireSession), and are read as the inbound is.
	huma.Register(h.api, huma.Operation{OperationID: "list-inbound-hosts", Method: http.MethodGet, Path: "/api/v1/inbounds/{id}/hosts", Summary: "Адреса подключения для клиентов", Tags: []string{"inbounds"}}, h.listInboundHosts)
	huma.Register(h.api, huma.Operation{OperationID: "add-inbound-host", Method: http.MethodPost, Path: "/api/v1/inbounds/{id}/hosts", Summary: "Добавить адрес подключения",
		Tags: []string{"inbounds"}, Metadata: sessionOnly, Extensions: sessionOnlyExt, DefaultStatus: http.StatusCreated}, h.addInboundHost)
	huma.Register(h.api, huma.Operation{OperationID: "update-inbound-host", Method: http.MethodPut, Path: "/api/v1/inbounds/{id}/hosts/{host_id}", Summary: "Изменить адрес подключения",
		Tags: []string{"inbounds"}, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.updateInboundHost)
	huma.Register(h.api, huma.Operation{OperationID: "delete-inbound-host", Method: http.MethodDelete, Path: "/api/v1/inbounds/{id}/hosts/{host_id}", Summary: "Удалить адрес подключения",
		Tags: []string{"inbounds"}, Metadata: sessionOnly, Extensions: sessionOnlyExt, DefaultStatus: http.StatusNoContent}, h.deleteInboundHost)
}

// HostView is one place clients reach an inbound at. The default host (default: true) is
// the client endpoint of the inbound (InboundView.client and fingerprint): subscriptions
// use it, and changing one changes the other. The other hosts are kept in their order for
// the subscriptions to come and change nothing clients get yet.
type HostView struct {
	ID          int64  `json:"id"`
	Default     bool   `json:"default" doc:"Адрес по умолчанию: тот же, что client и fingerprint подключения; удаляется только с подключением"`
	Address     string `json:"address" doc:"Адрес для клиентов; пусто — адрес ноды"`
	Port        int    `json:"port" doc:"Порт для клиентов; 0 — порт подключения"`
	SNI         string `json:"sni" doc:"SNI для клиентов; пусто — как обычно"`
	Host        string `json:"host" doc:"HTTP Host у клиентов (WebSocket, XHTTP); пусто — как на сервере"`
	Fingerprint string `json:"fingerprint" doc:"Отпечаток TLS (uTLS); пусто — общий из настроек"`
}

// HostInput is a host as it is saved: every field at once.
type HostInput struct {
	Address     string `json:"address" maxLength:"253" doc:"Адрес для клиентов; пусто — адрес ноды"`
	Port        int    `json:"port" minimum:"0" maximum:"65535" doc:"Порт для клиентов; 0 — порт подключения"`
	SNI         string `json:"sni" maxLength:"253" doc:"SNI для клиентов; пусто — как обычно. Не у REALITY: там имя задаёт сайт маскировки"`
	Host        string `json:"host" maxLength:"253" doc:"HTTP Host у клиентов: VLESS и Trojan по WebSocket и XHTTP; пусто — как на сервере"`
	Fingerprint string `json:"fingerprint" maxLength:"32" doc:"Отпечаток TLS у клиентов, как fingerprint подключения; пусто — общий из настроек"`
}

func (b HostInput) host() domain.Host {
	return domain.Host{Address: b.Address, Port: b.Port, SNI: b.SNI, Host: b.Host, Fingerprint: b.Fingerprint}
}

type hostsOutput struct{ Body []HostView }
type hostOutput struct{ Body HostView }

type addHostInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body HostInput
}

type updateHostInput struct {
	ID     int64 `path:"id" minimum:"1"`
	HostID int64 `path:"host_id" minimum:"1"`
	Body   HostInput
}

type hostIDInput struct {
	ID     int64 `path:"id" minimum:"1"`
	HostID int64 `path:"host_id" minimum:"1"`
}

func viewHost(r db.InboundHost) HostView {
	return HostView{ID: r.ID, Default: r.Position == 0, Address: r.Address, Port: int(r.Port), SNI: r.Sni, Host: r.Host, Fingerprint: r.Fingerprint}
}

func (h *handlers) listInboundHosts(ctx context.Context, in *userIDInput) (*hostsOutput, error) {
	rows, err := h.d.Inbounds.Hosts(ctx, in.ID)
	if err != nil {
		return nil, hostError(err)
	}
	out := &hostsOutput{Body: make([]HostView, 0, len(rows))}
	for _, r := range rows {
		out.Body = append(out.Body, viewHost(r))
	}
	return out, nil
}

func (h *handlers) addInboundHost(ctx context.Context, in *addHostInput) (*hostOutput, error) {
	if err := requireSession(ctx, "address"); err != nil {
		return nil, err
	}
	row, err := h.d.Inbounds.AddHost(ctx, in.ID, in.Body.host())
	if err != nil {
		return nil, hostError(err)
	}
	h.auditHost(ctx, "inbound.host_add", in.ID, row)
	return &hostOutput{Body: viewHost(row)}, nil
}

func (h *handlers) updateInboundHost(ctx context.Context, in *updateHostInput) (*hostOutput, error) {
	if err := requireSession(ctx, "address"); err != nil {
		return nil, err
	}
	row, err := h.d.Inbounds.UpdateHost(ctx, in.ID, in.HostID, in.Body.host())
	if err != nil {
		return nil, hostError(err)
	}
	// The default host is the template's: clients get the change with their next update.
	if row.Position == 0 {
		h.d.Changes.SlotsChanged()
	}
	h.auditHost(ctx, "inbound.host_update", in.ID, row)
	return &hostOutput{Body: viewHost(row)}, nil
}

func (h *handlers) deleteInboundHost(ctx context.Context, in *hostIDInput) (*struct{}, error) {
	if err := requireSession(ctx, "host_id"); err != nil {
		return nil, err
	}
	row, err := h.d.Inbounds.DeleteHost(ctx, in.ID, in.HostID)
	if err != nil {
		return nil, hostError(err)
	}
	h.auditHost(ctx, "inbound.host_delete", in.ID, row)
	return nil, nil
}

func (h *handlers) auditHost(ctx context.Context, action string, inboundID int64, r db.InboundHost) {
	name := ""
	if in, err := h.d.Store.Q.GetInbound(ctx, inboundID); err == nil {
		name = in.Name
	}
	h.audit(ctx, sessionOf(ctx).AdminID, action, "inbound", name, map[string]any{"host_id": r.ID, "default": r.Position == 0,
		"address": r.Address, "port": r.Port, "sni": r.Sni, "host": r.Host, "fingerprint": r.Fingerprint})
}

// hostError maps the domain's refusals of a host to the API's codes: a field the inbound's
// template does not take is named as the host's field (body.sni).
func hostError(err error) error {
	var edit *domain.EditError
	switch {
	case errors.Is(err, domain.ErrUnknownInbound), errors.Is(err, domain.ErrUnknownHost):
		return huma.Error404NotFound("not_found")
	case errors.Is(err, domain.ErrDefaultHost):
		return huma.Error409Conflict("default_host")
	case errors.As(err, &edit):
		return huma.Error422UnprocessableEntity("bad_host", &huma.ErrorDetail{Location: "body." + edit.Field, Message: edit.Err.Code})
	}
	return inboundError(err, false)
}

func (h *handlers) listPresets(context.Context, *struct{}) (*presetsOutput, error) {
	return &presetsOutput{Body: presets.All}, nil
}

// lastAuto maps inbound ids to their latest automatic change.
func (h *handlers) lastAuto(ctx context.Context) (map[int64]db.InboundEvent, error) {
	rows, err := h.d.Store.Q.LastInboundEvents(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]db.InboundEvent, len(rows))
	for _, e := range rows {
		out[e.InboundID] = e
	}
	return out, nil
}

// viewInbound is an inbound as the API shows it. A reader that may not see the panel's
// secrets (hidesSecrets) gets the template with them as proto.Hidden: REALITY's and VLESS
// Encryption's server keys would let it stand in for the server, the one key of a shared
// protocol would let it in for free. What it saves back gets them again (updateInbound).
func (h *handlers) viewInbound(ctx context.Context, in db.Inbound, last map[int64]db.InboundEvent) InboundView {
	info, _ := presets.Get(in.Preset)
	config := in.Config
	if hidesSecrets(ctx) {
		config = proto.Redact(config)
	}
	v := InboundView{ID: in.ID, NodeID: in.NodeID, Name: in.Name, Preset: in.Preset, Title: info.Title, Port: in.Port, Enabled: in.Enabled != 0,
		DisplayName: in.DisplayName, SubName: domain.ProxyName(in), Config: config, Status: "unknown", UpdatedAt: store.Time(in.UpdatedAt), Tags: domain.DecodeTags(in.Tags),
		AutoPort: in.AutoPort != 0, AutoSNI: in.AutoSni != 0, Outbound: in.Outbound, Listen: in.Listen, InAuto: in.NoAuto == 0}
	if in.PoolID.Valid {
		id := in.PoolID.Int64
		v.PoolID = &id
	}
	if in.ExitNodeID.Valid {
		id := in.ExitNodeID.Int64
		v.Outbound, v.ExitNodeID = "node", &id
	}
	if e, ok := last[in.ID]; ok {
		v.Auto.Last = &AutoEvent{Kind: e.Kind, Old: e.OldValue, New: e.NewValue, Reason: e.Reason, At: store.Time(e.CreatedAt)}
	}
	if h.d.Tuner != nil {
		if s, ok := h.d.Tuner.Status(in.ID); ok {
			v.Auto.CutOff, v.Auto.Blocked, v.Auto.Reached, v.Auto.Stuck = s.CutOff, s.Blocked, s.Reached, s.Stuck
			if s.CutOff {
				since := s.Since.UTC()
				v.Auto.Since = &since
			}
			if !s.TargetAt.IsZero() {
				ok := s.TargetOK
				v.Auto.TargetOK, v.Auto.TargetError = &ok, s.TargetError
			}
		}
	}
	v.Apps = []string{}
	if t, err := proto.Parse(in.Config); err == nil {
		v.Type, v.Network = t.Type(), t.Network()
		v.Shared = proto.Shared(t)
		v.NodeCert = t.NodeCert()
		for _, f := range apps.For(proto.NeedsOf(t)) {
			v.Apps = append(v.Apps, string(f))
		}
		v.Dest, v.ServerNames = presets.Dest(t)
		c := t.Ext().Client
		v.Client, v.ClientSNI, v.ClientHost = ClientEndpoint{Server: c.Server, Port: c.Port, SNI: c.SNI, Host: c.Host}, proto.ClientSNI(t), proto.ClientHost(t)
		if cdn := proto.CDNOf(t); cdn != nil {
			v.Client.CDN = &ClientCDN{Header: cdn.Header, Trusted: cdn.Trusted}
		}
		v.CDNPossible = proto.CanCDN(t)
		v.SNI443, v.SNI443Possible = proto.SNI443(t), proto.CanSNI443(t)
		if proto.UsesFingerprint(t) {
			fp := c.Fingerprint
			v.Fingerprint = &fp
		}
		if t.Type() == "hysteria2" {
			obfs := proto.Obfs(t)
			v.Obfs = &obfs
		}
		if x, ok := proto.XHTTPOf(t); ok {
			v.XHTTP = &x
		}
		if in.Preset == presets.Custom {
			v.Title = t.Type()
		}
	}
	if h.d.Nodes != nil {
		hv, _ := h.d.Nodes.Health(in.NodeID)
		for _, l := range hv.Listeners {
			if l.Name == in.Name {
				v.Status, v.Error = "ok", ""
				if !l.OK {
					v.Status, v.Error = "error", l.Error
				}
			}
		}
	}
	return v
}

func (h *handlers) listInbounds(ctx context.Context, in *listInboundsInput) (*inboundsOutput, error) {
	rows, err := h.d.Store.Q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	last, err := h.lastAuto(ctx)
	if err != nil {
		return nil, err
	}
	out := &inboundsOutput{Body: make([]InboundView, 0, len(rows))}
	for _, row := range rows {
		if in.Tag != "" && !domain.HasTag(row.Tags, in.Tag) {
			continue
		}
		out.Body = append(out.Body, h.viewInbound(ctx, row, last))
	}
	return out, nil
}

// restoreSecrets is a template that comes back to be saved or checked with the saved
// secrets of the inbound id where its reader got proto.Hidden (viewInbound).
func (h *handlers) restoreSecrets(ctx context.Context, id int64, config string) (string, error) {
	if id == 0 || !strings.Contains(config, proto.Hidden) {
		return config, nil
	}
	cur, err := h.d.Store.Q.GetInbound(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", huma.Error404NotFound("not_found")
	}
	if err != nil {
		return "", err
	}
	return proto.Restore(config, cur.Config), nil
}

func (h *handlers) validateInbound(ctx context.Context, in *validateInboundInput) (*validateInboundOutput, error) {
	port := in.Body.Port
	if port == "" {
		port = "443"
	}
	node, err := h.nodeOf(ctx, in.Body.NodeID)
	if err != nil {
		return nil, err
	}
	config, err := h.restoreSecrets(ctx, in.Body.InboundID, in.Body.Config)
	if err != nil {
		return nil, err
	}
	t, err := h.d.Inbounds.CheckTemplate(ctx, node, config, port)
	if err != nil {
		return nil, configError(err)
	}
	out := &validateInboundOutput{}
	out.Body.Type, out.Body.Network = t.Type(), t.Network()
	return out, nil
}

func (h *handlers) createInbound(ctx context.Context, in *createInboundInput) (*inboundOutput, error) {
	b := in.Body
	row, err := h.d.Inbounds.Create(ctx, domain.NewInbound{NodeID: b.NodeID, Preset: b.Preset, Port: b.Port, Dest: b.Dest, Config: b.Config, Tags: b.Tags})
	if err != nil {
		return nil, inboundError(err, false)
	}
	v := h.viewInbound(ctx, row, nil)
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "inbound.create", "inbound", row.Name, map[string]any{"preset": row.Preset, "type": v.Type, "port": row.Port})
	return &inboundOutput{Body: v}, nil
}

type copyInboundInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		NodeID     int64  `json:"node_id,omitempty" minimum:"1" doc:"Нода копии; по умолчанию — та же"`
		Port       string `json:"port,omitempty" pattern:"^[0-9]{1,5}(-[0-9]{1,5})?$" doc:"Порт копии; пусто — тот же, а если он занят — первый свободный"`
		Dest       string `json:"dest,omitempty" maxLength:"255" doc:"REALITY: другой сайт маскировки, host:port; пусто — тот же"`
		ServerName string `json:"server_name,omitempty" maxLength:"253" doc:"SNI для dest, если это IP"`
	}
}

type setExitsInput struct {
	Body struct {
		IDs        []int64 `json:"ids" minItems:"1" maxItems:"500" uniqueItems:"true" doc:"Подключения"`
		Outbound   string  `json:"outbound" enum:"direct,warp,node" doc:"Выход в интернет: напрямую, через WARP своей ноды или через другую ноду"`
		ExitNodeID int64   `json:"exit_node_id,omitempty" minimum:"1" doc:"Для outbound=node: через какую ноду"`
	}
}

// setInboundExits sends several inbounds out the same way at once; one that cannot (it is
// on the exit node itself, its chain would loop) stops all of them and is named.
func (h *handlers) setInboundExits(ctx context.Context, in *setExitsInput) (*inboundsOutput, error) {
	b := in.Body
	if b.Outbound == "node" && b.ExitNodeID == 0 {
		return nil, cascadeError(domain.ErrNotFound, "exit_node_id")
	}
	rows, err := h.d.Inbounds.SetExits(ctx, b.IDs, b.Outbound, b.ExitNodeID)
	var conflict *domain.ExitConflictError
	switch {
	case errors.As(err, &conflict):
		e := cascadeError(conflict.Err, "exit_node_id")
		var se *huma.ErrorModel
		if errors.As(e, &se) && len(se.Errors) > 0 {
			se.Errors[0].Value = conflict.Inbound
		}
		return nil, e
	case err != nil:
		return nil, inboundError(err, false)
	}
	last, err := h.lastAuto(ctx)
	if err != nil {
		return nil, err
	}
	out := &inboundsOutput{Body: make([]InboundView, 0, len(rows))}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		out.Body = append(out.Body, h.viewInbound(ctx, row, last))
		names = append(names, row.Name)
	}
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "inbound.outbound_bulk", "inbound", "", map[string]any{"inbounds": names, "outbound": b.Outbound, "exit_node_id": b.ExitNodeID})
	return out, nil
}

// copyInbound makes a new inbound from a set-up one: the same settings with keys of its own.
func (h *handlers) copyInbound(ctx context.Context, in *copyInboundInput) (*inboundOutput, error) {
	b := in.Body
	row, err := h.d.Inbounds.Copy(ctx, in.ID, domain.CopyInbound{NodeID: b.NodeID, Port: b.Port, Dest: b.Dest, ServerName: b.ServerName})
	if err != nil {
		return nil, inboundError(err, b.Dest != "")
	}
	v := h.viewInbound(ctx, row, nil)
	h.d.Changes.SlotsChanged()
	if row.PoolID.Valid {
		h.d.Changes.PoliciesChanged()
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "inbound.copy", "inbound", row.Name, map[string]any{"from": in.ID, "node_id": row.NodeID, "port": row.Port})
	return &inboundOutput{Body: v}, nil
}

func (h *handlers) updateInbound(ctx context.Context, in *patchInboundInput) (*inboundOutput, error) {
	b := in.Body
	if b.Config != nil {
		config, err := h.restoreSecrets(ctx, in.ID, *b.Config)
		if err != nil {
			return nil, err
		}
		b.Config = &config
	}
	// How a name may look is the subscription's rule; that it is free, the domain's.
	if b.DisplayName != nil {
		if name := strings.TrimSpace(*b.DisplayName); name != "" {
			if code := h.checkSubName(ctx, name); code != "" {
				return nil, huma.Error422UnprocessableEntity("bad_name", &huma.ErrorDetail{Location: "body.display_name", Message: code})
			}
		}
	}
	p := domain.InboundPatch{Port: b.Port, Enabled: b.Enabled, Config: b.Config, Dest: b.Dest, ServerName: b.ServerName, Fingerprint: b.Fingerprint, Obfs: b.Obfs, XHTTP: b.XHTTP,
		DisplayName: b.DisplayName, Listen: b.Listen, AutoPort: b.AutoPort, AutoSNI: b.AutoSNI, Outbound: b.Outbound, ExitNodeID: b.ExitNodeID, PoolID: b.PoolID,
		SNI443: b.SNI443, InAuto: b.InAuto, Tags: b.Tags}
	if c := b.Client; c != nil {
		// The address clients connect to: like the panel's public host, not for a key.
		if err := requireSession(ctx, "client"); err != nil {
			return nil, err
		}
		p.Client = &domain.ClientEndpoint{Server: c.Server, Port: c.Port, SNI: c.SNI, Host: c.Host}
		if c.CDN != nil {
			p.Client.CDN = &proto.CDN{Header: c.CDN.Header, Trusted: c.CDN.Trusted}
		}
	}
	prev, row, err := h.d.Inbounds.Update(ctx, in.ID, p)
	if err != nil {
		return nil, inboundError(err, b.Dest != nil)
	}
	admin := sessionOf(ctx).AdminID
	if b.PoolID != nil {
		h.d.Changes.PoliciesChanged()
		h.audit(ctx, admin, "inbound.pool", "inbound", row.Name, map[string]any{"pool_id": row.PoolID.Int64})
	}
	if b.Tags != nil {
		h.audit(ctx, admin, "inbound.tags", "inbound", row.Name, map[string]any{"tags": domain.DecodeTags(row.Tags)})
	}
	if b.Outbound != nil {
		h.audit(ctx, admin, "inbound.outbound", "inbound", row.Name, map[string]any{"outbound": *b.Outbound, "exit_node_id": row.ExitNodeID.Int64})
	}
	moved := row.Listen != prev.Listen
	switch {
	case p.ForClients():
		h.audit(ctx, admin, "inbound.update", "inbound", row.Name, map[string]any{"config_changed": p.EditsTemplate(),
			"listen": row.Listen, "auto_port": row.AutoPort != 0, "auto_sni": row.AutoSni != 0, "in_auto": row.NoAuto == 0})
	case moved || row.AutoPort != prev.AutoPort || row.AutoSni != prev.AutoSni:
		h.audit(ctx, admin, "inbound.auto", "inbound", row.Name, map[string]any{"listen": row.Listen, "auto_port": row.AutoPort != 0, "auto_sni": row.AutoSni != 0})
	}
	if p.ForClients() || moved || b.PoolID != nil || b.Outbound != nil {
		h.d.Changes.SlotsChanged()
	}
	last, err := h.lastAuto(ctx)
	if err != nil {
		return nil, err
	}
	return &inboundOutput{Body: h.viewInbound(ctx, row, last)}, nil
}

// inboundError maps the domain's refusals of an inbound change to the API's codes. dest:
// the request set the REALITY target, and the simple form that does shows the
// template's errors on that field.
func inboundError(err error, dest bool) error {
	var edit *domain.EditError
	var name *domain.NameInUseError
	var pe *proto.Error
	var ne *nodeapi.Error
	switch {
	case errors.Is(err, domain.ErrUnknownInbound):
		return huma.Error404NotFound("not_found")
	case errors.Is(err, domain.ErrUnknownPreset):
		return refusal("unknown_preset")
	case errors.Is(err, domain.ErrUnknownNode):
		return fieldErr("body.node_id", "unknown_node")
	case errors.Is(err, domain.ErrBadPort):
		return fieldErr("body.port", "bad_port")
	case errors.Is(err, domain.ErrBadListen):
		return fieldErr("body.listen", "bad_listen")
	case errors.Is(err, domain.ErrAutoPortListen):
		return fieldErr("body.auto_port", "auto_port_listen")
	case errors.Is(err, domain.ErrAutoSNIListen):
		return fieldErr("body.auto_sni", "auto_sni_listen")
	case errors.Is(err, domain.ErrInboundChanged):
		return huma.Error409Conflict("inbound_changed")
	case errors.Is(err, domain.ErrUnknownPool):
		return fieldErr("body.pool_id", "pool_not_found")
	case errors.As(err, &name):
		return huma.Error409Conflict("name_in_use", &huma.ErrorDetail{Location: "body.display_name", Message: "name_in_use", Value: name.Owner})
	case errors.As(err, &edit):
		// The client endpoint names its part: body.client.sni.
		location := "body." + edit.Field
		if part, ok := strings.CutPrefix(edit.Err.Field, "mikan.client."); ok && edit.Field == "client" {
			location += "." + part
		}
		// And the CDN behind it: body.client.cdn.trusted.
		if part, ok := strings.CutPrefix(edit.Err.Field, "mikan.cdn"); ok && edit.Field == "client" {
			location += ".cdn" + part
		}
		// The XHTTP form names the setting: one location holds all of them.
		var value any
		if edit.Field == "xhttp" {
			key := edit.Err.Field
			for _, p := range []string{"xhttp-config.", "mikan.client.xhttp.xmux.", "mikan.client.xhttp."} {
				key = strings.TrimPrefix(key, p)
			}
			value = key
		}
		return huma.Error422UnprocessableEntity("bad_"+edit.Field, &huma.ErrorDetail{Location: location, Message: edit.Err.Code, Value: value})
	case dest && errors.As(err, &pe):
		return huma.Error422UnprocessableEntity("bad_dest", &huma.ErrorDetail{Location: "body.dest", Message: pe.Code})
	case dest && errors.As(err, &ne):
		return huma.Error422UnprocessableEntity("bad_dest", &huma.ErrorDetail{Location: "body.dest", Message: "config_mihomo"})
	}
	return configError(portError(cascadeError(err, "exit_node_id")))
}

// configError maps a template's refusal, mikan's own or the node's mihomo's, to the
// API's code; other errors pass through.
func configError(err error) error {
	var pe *proto.Error
	if errors.As(err, &pe) {
		value := pe.Field
		if pe.Detail != "" {
			value = pe.Detail
		}
		return huma.Error422UnprocessableEntity("invalid_config", &huma.ErrorDetail{Location: "body.config", Message: pe.Code, Value: value})
	}
	var ne *nodeapi.Error
	if errors.As(err, &ne) {
		return huma.Error422UnprocessableEntity("invalid_config", &huma.ErrorDetail{Location: "body.config", Message: "config_mihomo", Value: ne.Message})
	}
	return err
}

// portError is how the API reports a port something else holds; other errors pass
// through.
func portError(err error) error {
	var busy *domain.PortInUseError
	if !errors.As(err, &busy) {
		return err
	}
	switch busy.Kind {
	case domain.PortRelay:
		return huma.Error409Conflict("port_in_use", &huma.ErrorDetail{Location: "body.port", Message: "port_relay"})
	case domain.PortSub:
		return huma.Error409Conflict("port_in_use", &huma.ErrorDetail{Location: "body.port", Message: "port_sub"})
	case domain.PortPanel:
		return huma.Error409Conflict("port_in_use", &huma.ErrorDetail{Location: "body.port", Message: "port_panel"})
	case domain.PortNodeAPI:
		return huma.Error409Conflict("port_in_use", &huma.ErrorDetail{Location: "body.port", Message: "port_node_api"})
	}
	return huma.Error409Conflict("port_in_use", &huma.ErrorDetail{Location: "body.port", Message: "port_in_use", Value: busy.Name})
}

func (h *handlers) deleteInbound(ctx context.Context, in *userIDInput) (*struct{}, error) {
	row, err := h.d.Store.Q.GetInbound(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	if err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		if err := serversLeft(ctx, q, 0, in.ID); err != nil {
			return err
		}
		return q.DeleteInbound(ctx, in.ID)
	}); err != nil {
		return nil, err
	}
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "inbound.delete", "inbound", row.Name, nil)
	return nil, nil
}

// nodeOf loads the node an inbound belongs to; 0 is the panel's own node.
func (h *handlers) nodeOf(ctx context.Context, id int64) (db.Node, error) {
	if id == 0 {
		id = 1
	}
	n, err := h.d.Store.Q.GetNode(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return n, fieldErr("body.node_id", "unknown_node")
	}
	return n, err
}
