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
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subs"
	"mikan/internal/proto"
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
	PoolID      *int64         `json:"pool_id,omitempty" doc:"Пул трафика, в который считается подключение; нет — основной трафик"`
	Client      ClientEndpoint `json:"client" doc:"Куда подключаются клиенты, если не к ноде напрямую (mikan.client в шаблоне)"`
	ClientSNI   bool           `json:"client_sni" doc:"Можно ли задать клиентам свой SNI: у REALITY имя задаёт сайт маскировки"`

	// XHTTP is the tuning the form shows; absent for other transports.
	XHTTP *proto.XHTTPTuning `json:"xhttp,omitempty" doc:"Тонкая настройка XHTTP; у других транспортов поля нет"`
}

// ClientEndpoint is where clients connect when a TCP proxy or a CDN stands in front of
// the node. Empty values (port 0) keep the node's address, the inbound's port and SNI.
type ClientEndpoint struct {
	Server string `json:"server" maxLength:"253" doc:"Адрес для клиентов; пусто — адрес ноды"`
	Port   int    `json:"port" minimum:"0" maximum:"65535" doc:"Порт для клиентов; 0 — порт подключения"`
	SNI    string `json:"sni" maxLength:"253" doc:"SNI для клиентов; пусто — как обычно"`
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
		Preset string `json:"preset" enum:"vless_reality_xhttp,hysteria2,hysteria2_gecko,tuic_v5,vless_reality_vision,vless_reality_grpc,trojan_reality,vless_tls_xhttp,vless_tls_vision,anytls,vless_reality_xhttp_pq,trusttunnel,shadowquic,mieru,shadowsocks_2022,sudoku,snell,custom"`
		NodeID int64  `json:"node_id,omitempty" minimum:"1" doc:"Нода; по умолчанию — своя нода панели"`
		Port   string `json:"port,omitempty" pattern:"^[0-9]{1,5}(-[0-9]{1,5})?$"`
		Dest   string `json:"dest,omitempty" maxLength:"255" doc:"host:port для REALITY"`
		Config string `json:"config,omitempty" maxLength:"65536" doc:"Шаблон листенера (YAML) для preset=custom"`
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
		Client      *ClientEndpoint `json:"client,omitempty" doc:"Куда подключаются клиенты: адрес, порт и SNI прокси перед нодой; заменяет все три"`
		AutoPort    *bool           `json:"auto_port,omitempty" doc:"Нельзя включить, пока у подключения свой адрес (listen)"`
		AutoSNI     *bool           `json:"auto_sni,omitempty" doc:"Нельзя включить, пока у подключения свой адрес (listen)"`
		Outbound    *string         `json:"outbound,omitempty" enum:"direct,warp,node" doc:"Выход в интернет: напрямую, через WARP ноды или через другую ноду"`
		ExitNodeID  *int64          `json:"exit_node_id,omitempty" minimum:"1" doc:"Для outbound=node: через какую ноду"`
		PoolID      *int64          `json:"pool_id,omitempty" minimum:"0" doc:"Пул трафика; 0 — основной трафик"`

		XHTTP *proto.XHTTPTuning `json:"xhttp,omitempty" doc:"Тонкая настройка XHTTP целиком: пустое поле — значение ядра по умолчанию"`
	}
}

type validateInboundInput struct {
	Body struct {
		NodeID int64  `json:"node_id,omitempty" minimum:"1"`
		Config string `json:"config" maxLength:"65536"`
		Port   string `json:"port,omitempty" pattern:"^[0-9]{1,5}(-[0-9]{1,5})?$"`
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
	huma.Register(h.api, huma.Operation{OperationID: "update-inbound", Method: http.MethodPatch, Path: "/api/v1/inbounds/{id}", Summary: "Изменить подключение", Tags: []string{"inbounds"}}, h.updateInbound)
	huma.Register(h.api, huma.Operation{OperationID: "delete-inbound", Method: http.MethodDelete, Path: "/api/v1/inbounds/{id}", Summary: "Удалить подключение", Tags: []string{"inbounds"}, DefaultStatus: http.StatusNoContent}, h.deleteInbound)
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

func (h *handlers) viewInbound(in db.Inbound, last map[int64]db.InboundEvent) InboundView {
	info, _ := presets.Get(in.Preset)
	v := InboundView{ID: in.ID, NodeID: in.NodeID, Name: in.Name, Preset: in.Preset, Title: info.Title, Port: in.Port, Enabled: in.Enabled != 0,
		DisplayName: in.DisplayName, SubName: domain.ProxyName(in), Config: in.Config, Status: "unknown", UpdatedAt: time.Unix(in.UpdatedAt, 0).UTC(),
		AutoPort: in.AutoPort != 0, AutoSNI: in.AutoSni != 0, Outbound: in.Outbound, Listen: in.Listen}
	if in.PoolID.Valid {
		id := in.PoolID.Int64
		v.PoolID = &id
	}
	if in.ExitNodeID.Valid {
		id := in.ExitNodeID.Int64
		v.Outbound, v.ExitNodeID = "node", &id
	}
	if e, ok := last[in.ID]; ok {
		v.Auto.Last = &AutoEvent{Kind: e.Kind, Old: e.OldValue, New: e.NewValue, Reason: e.Reason, At: time.Unix(e.CreatedAt, 0).UTC()}
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
		v.Shared = proto.Shared(t.Type())
		v.NodeCert = t.NodeCert()
		for _, f := range subs.AppsFor(proto.NeedsOf(t)) {
			v.Apps = append(v.Apps, string(f))
		}
		v.Dest, v.ServerNames = presets.Dest(t)
		c := t.Ext().Client
		v.Client, v.ClientSNI = ClientEndpoint{Server: c.Server, Port: c.Port, SNI: c.SNI}, proto.ClientSNI(t)
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

func (h *handlers) listInbounds(ctx context.Context, _ *struct{}) (*inboundsOutput, error) {
	rows, err := h.d.Store.Q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	last, err := h.lastAuto(ctx)
	if err != nil {
		return nil, err
	}
	out := &inboundsOutput{Body: make([]InboundView, 0, len(rows))}
	for _, in := range rows {
		out.Body = append(out.Body, h.viewInbound(in, last))
	}
	return out, nil
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
	t, err := h.d.Inbounds.CheckTemplate(ctx, node, in.Body.Config, port)
	if err != nil {
		return nil, configError(err)
	}
	out := &validateInboundOutput{}
	out.Body.Type, out.Body.Network = t.Type(), t.Network()
	return out, nil
}

func (h *handlers) createInbound(ctx context.Context, in *createInboundInput) (*inboundOutput, error) {
	b := in.Body
	row, err := h.d.Inbounds.Create(ctx, domain.NewInbound{NodeID: b.NodeID, Preset: b.Preset, Port: b.Port, Dest: b.Dest, Config: b.Config})
	if err != nil {
		return nil, inboundError(err, false)
	}
	v := h.viewInbound(row, nil)
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "inbound.create", "inbound", row.Name, map[string]any{"preset": row.Preset, "type": v.Type, "port": row.Port})
	return &inboundOutput{Body: v}, nil
}

func (h *handlers) updateInbound(ctx context.Context, in *patchInboundInput) (*inboundOutput, error) {
	b := in.Body
	// How a name may look is the subscription's rule; that it is free, the domain's.
	if b.DisplayName != nil {
		if name := strings.TrimSpace(*b.DisplayName); name != "" {
			if code := h.checkSubName(ctx, name); code != "" {
				return nil, huma.Error422UnprocessableEntity("bad_name", &huma.ErrorDetail{Location: "body.display_name", Message: code})
			}
		}
	}
	p := domain.InboundPatch{Port: b.Port, Enabled: b.Enabled, Config: b.Config, Dest: b.Dest, ServerName: b.ServerName, Fingerprint: b.Fingerprint, Obfs: b.Obfs, XHTTP: b.XHTTP,
		DisplayName: b.DisplayName, Listen: b.Listen, AutoPort: b.AutoPort, AutoSNI: b.AutoSNI, Outbound: b.Outbound, ExitNodeID: b.ExitNodeID, PoolID: b.PoolID}
	if c := b.Client; c != nil {
		// The address clients connect to: like the panel's public host, not for a key.
		if err := requireSession(ctx, "client"); err != nil {
			return nil, err
		}
		p.Client = &domain.ClientEndpoint{Server: c.Server, Port: c.Port, SNI: c.SNI}
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
	if b.Outbound != nil {
		h.audit(ctx, admin, "inbound.outbound", "inbound", row.Name, map[string]any{"outbound": *b.Outbound, "exit_node_id": row.ExitNodeID.Int64})
	}
	moved := row.Listen != prev.Listen
	switch {
	case p.ForClients():
		h.audit(ctx, admin, "inbound.update", "inbound", row.Name, map[string]any{"config_changed": p.EditsTemplate(),
			"listen": row.Listen, "auto_port": row.AutoPort != 0, "auto_sni": row.AutoSni != 0})
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
	return &inboundOutput{Body: h.viewInbound(row, last)}, nil
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
		return huma.Error422UnprocessableEntity("unknown_preset")
	case errors.Is(err, domain.ErrUnknownNode):
		return huma.Error422UnprocessableEntity("unknown_node", &huma.ErrorDetail{Location: "body.node_id", Message: "unknown_node"})
	case errors.Is(err, domain.ErrBadPort):
		return huma.Error422UnprocessableEntity("bad_port", &huma.ErrorDetail{Location: "body.port", Message: "bad_port"})
	case errors.Is(err, domain.ErrBadListen):
		return huma.Error422UnprocessableEntity("bad_listen", &huma.ErrorDetail{Location: "body.listen", Message: "bad_listen"})
	case errors.Is(err, domain.ErrAutoPortListen):
		return huma.Error422UnprocessableEntity("auto_port_listen", &huma.ErrorDetail{Location: "body.auto_port", Message: "auto_port_listen"})
	case errors.Is(err, domain.ErrAutoSNIListen):
		return huma.Error422UnprocessableEntity("auto_sni_listen", &huma.ErrorDetail{Location: "body.auto_sni", Message: "auto_sni_listen"})
	case errors.Is(err, domain.ErrInboundChanged):
		return huma.Error409Conflict("inbound_changed")
	case errors.Is(err, domain.ErrUnknownPool):
		return huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.pool_id", Message: "pool_not_found"})
	case errors.As(err, &name):
		return huma.Error409Conflict("name_in_use", &huma.ErrorDetail{Location: "body.display_name", Message: "name_in_use", Value: name.Owner})
	case errors.As(err, &edit):
		// The client endpoint names its part: body.client.sni.
		location := "body." + edit.Field
		if part, ok := strings.CutPrefix(edit.Err.Field, "mikan.client."); ok && edit.Field == "client" {
			location += "." + part
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
	if err := h.d.Store.Q.DeleteInbound(ctx, in.ID); err != nil {
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
		return n, huma.Error422UnprocessableEntity("unknown_node", &huma.ErrorDetail{Location: "body.node_id", Message: "unknown_node"})
	}
	return n, err
}
