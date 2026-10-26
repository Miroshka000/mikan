package api

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodeupdate"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subsrc"
	"mikan/internal/panel/tgate"
	"mikan/internal/release"
)

type NodeInfo struct {
	ID         int64  `json:"id"`
	Name       string `json:"name" doc:"Группа в подписке, например «🇳🇱 Нидерланды»; её флаг — префикс имён подключений"`
	Local      bool   `json:"local" doc:"Своя нода панели"`
	Address    string `json:"address" doc:"host:port API ноды; пусто у своей ноды"`
	Host       string `json:"host" doc:"Адрес для клиентов: домен ноды, без него server_host"`
	ServerHost string `json:"server_host" doc:"IP или имя сервера, как его задали: по нему панель ходит в API ноды; пусто у своей ноды"`
	Domain     string `json:"domain"`
	PublicName string `json:"public_name" doc:"Публичное имя для канала состояния; пустое — нода скрыта из списка"`
	Enabled    bool   `json:"enabled"`
	Inbounds   int    `json:"inbounds"`
	Status     string `json:"status" enum:"ok,error,unknown"`
	Error      string `json:"error,omitempty" doc:"Слова ошибки связи как есть, для «подробнее»"`
	// Why the panel cannot reach the node, since when, and when it last could; the node's
	// clock against the panel's; its last hello.
	ErrorCode   string            `json:"error_code,omitempty" enum:"timeout,refused,unreachable,dns,pin_mismatch,tls,http_status,unknown" doc:"Почему панель не достучалась до ноды"`
	ErrorParams map[string]string `json:"error_params,omitempty" doc:"host и port адреса API ноды, status ответа"`
	ErrorSince  *time.Time        `json:"error_since,omitempty" doc:"С какого момента нет связи"`
	LastOKAt    *time.Time        `json:"last_ok_at,omitempty" doc:"Когда нода последний раз отвечала (с запуска панели)"`
	ClockSkew   *int64            `json:"clock_skew,omitempty" doc:"Часы ноды минус часы панели, секунды; нет у старых нод"`
	Hello       *NodeHello        `json:"hello,omitempty" doc:"Последний hello ноды после запуска: достучалась ли панель в ответ"`
	Version     string            `json:"version,omitempty"`
	Listeners   int               `json:"listeners"`
	ListenersOK int               `json:"listeners_ok"`
	Conns       int               `json:"conns"`
	CPUPercent  float64           `json:"cpu_percent" doc:"Загрузка процессора всего сервера, не только ноды"`
	ProcCPU     *float64          `json:"proc_cpu_percent,omitempty" doc:"Доля процессора самого процесса ноды (вся машина = 100); нет у нод до 0.5.0.4"`
	MemUsed     uint64            `json:"mem_used"`
	MemTotal    uint64            `json:"mem_total"`
	CheckedAt   *time.Time        `json:"checked_at,omitempty"`
	// Traffic24h is what the node carried in the last day, both ways.
	Traffic24h int64 `json:"traffic_24h" doc:"Сколько унесла нода за последние сутки (вверх и вниз вместе), байты; у только что добавленной ноды 0"`
	// Cascade is the other nodes' cascades through this one: the relay's open connections
	// by source node, and what it carried for them in the last day.
	Cascade    []CascadeConns `json:"cascade" doc:"Открытые сейчас подключения каскада других нод через эту, по ноде-источнику; пусто без каскада и у нод до 0.5.1.1-rc.14"`
	Cascade24h int64          `json:"cascade_24h" doc:"Сколько нода унесла за сутки для каскада других нод (вверх и вниз вместе), байты; в traffic_24h не входит: этих пользователей считает нода-источник"`
	// Behind: the node runs an older version than the panel.
	Behind bool `json:"behind" doc:"Нода старее панели"`
	// CanUpdate: the panel can update the node itself (a remote node that answers and has
	// the update endpoint, version 0.5.0.2 or later). An older node is updated once by hand.
	CanUpdate bool `json:"can_update" doc:"Панель может обновить ноду сама: удалённая, отвечает, версия 0.5.0.2 или новее; старую обновляют один раз вручную командой mikan update на её сервере"`
	// Update is how the last update of the node goes or went.
	Update *nodeapi.UpdateStatus `json:"update,omitempty" doc:"Как идёт или прошло обновление ноды"`
	// Certificate is the node's own one for its protocols on the node's TLS; nil: the
	// node uses its self-signed certificate.
	Certificate *NodeCertView `json:"certificate,omitempty"`
	// TLS is the certificate its protocols on TLS use now; nil when not known.
	TLS *NodeTLSView `json:"tls,omitempty"`
	// FairShare splits ChannelMbps evenly between the users moving traffic through the node.
	FairShare bool `json:"fair_share" doc:"Канал ноды делится поровну между теми, кто сейчас качает"`
	// BalanceGroup and Capacity: the node shares its group's clients out (domain.Balancer).
	BalanceGroup string `json:"balance_group" doc:"Группа балансировки; пусто — вне групп"`
	// HTTPRedirect: plain HTTP on port 80 gets 301 to https; RedirectError says why the
	// node could not (another program holds the port).
	HTTPRedirect  bool   `json:"http_redirect" doc:"HTTP на порту 80 получает 301 на https"`
	RedirectError string `json:"redirect_error,omitempty" doc:"Почему нода не смогла занять порт 80"`
	// SNIRouter: the node shares its TCP 443 between the inbounds marked for it;
	// RouterError says why it could not take the port.
	SNIRouter   bool   `json:"sni_router" doc:"Порт 443 ноды делят подключения по имени TLS (SNI-роутер)"`
	RouterError string `json:"router_error,omitempty" doc:"Почему нода не смогла занять порт 443"`
	// FrontPage is the built-in page strangers get on the node's own domain through the
	// shared 443.
	FrontPage string `json:"front_page" enum:",maintenance,cats" doc:"Заглушка для посторонних: её отдают протоколы VLESS и Trojan с XHTTP, WebSocket или gRPC на чужие запросы вместо пустого 404, а с общим портом 443 и домен ноды; пусто — нет, maintenance — технические работы, cats — котики-кликер"`
	// InAuto: the node's servers are in the subscriptions' automatic choice.
	InAuto bool `json:"in_auto" doc:"Серверы ноды входят в «Авто» подписки; выключено — только в списке и группе страны, например сервер для белых списков"`
	// APILockdown: the node's API answers only the panel's addresses and APIAllow;
	// APILockError says why the node's server has no firewall rule for it (the node refuses
	// strangers in its own listener all the same).
	APILockdown  bool     `json:"api_lockdown" doc:"API ноды отвечает только адресам панели"`
	APIAllow     []string `json:"api_allow" doc:"Сети и адреса, которые админ добавил к адресам панели"`
	APILockError string   `json:"api_lock_error,omitempty" doc:"Почему на сервере ноды нет правила файрвола для API"`
	Capacity     *int64   `json:"capacity" doc:"Мест для клиентов в группе; null — без ограничения"`
	ChannelMbps  *int64   `json:"channel_mbps" doc:"Ширина канала ноды, Мбит/с в каждую сторону; null — не задана"`
}

type nodesOutput struct{ Body []NodeInfo }
type nodeInfoOutput struct{ Body NodeInfo }

type nodeKeyOutput struct {
	Body struct {
		Node    NodeInfo `json:"node"`
		Key     string   `json:"key" doc:"Ключ подключения ноды: показывается один раз"`
		Command string   `json:"command" doc:"Команда установки на сервере ноды"`
	}
}

type createNodeInput struct {
	Body struct {
		Name    string `json:"name" maxLength:"200"`
		Host    string `json:"host" maxLength:"253" doc:"IP или имя сервера ноды"`
		Domain  string `json:"domain,omitempty" maxLength:"253" doc:"Домен для Hysteria2/TUIC и ссылок (необязательно)"`
		APIPort int    `json:"api_port,omitempty" minimum:"1" maximum:"65535" doc:"Порт API ноды; по умолчанию случайный"`
	}
}

type patchNodeInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Name       *string `json:"name,omitempty" maxLength:"200"`
		PublicName *string `json:"public_name,omitempty" maxLength:"80" doc:"Публичное имя ноды; пустое — не публиковать её в канале"`
		Host       *string `json:"host,omitempty" maxLength:"253"`
		Domain     *string `json:"domain,omitempty" maxLength:"253"`
		Enabled    *bool   `json:"enabled,omitempty"`
		FairShare  *bool   `json:"fair_share,omitempty" doc:"Делить канал ноды поровну между теми, кто сейчас качает; нужен channel_mbps"`
		// The node's channel each way, which the fair share splits.
		ChannelMbps  *int64  `json:"channel_mbps,omitempty" minimum:"1" maximum:"100000" doc:"Ширина канала ноды, Мбит/с в каждую сторону"`
		HTTPRedirect *bool   `json:"http_redirect,omitempty" doc:"Отвечать на HTTP (порт 80) редиректом 301 на https; не для ноды панели"`
		SNIRouter    *bool   `json:"sni_router,omitempty" doc:"SNI-роутер на 443: подключения с отметкой «через общий 443» слушают на loopback за ним"`
		FrontPage    *string `json:"front_page,omitempty" enum:",maintenance,cats" doc:"Заглушка для посторонних: протоколы с XHTTP, WebSocket или gRPC отдают её на чужие запросы вместо пустого 404, общий порт 443 — на домене ноды и на запросах без имени (сертификат ноды, TLS 1.2+, HSTS); пусто — нет"`
		InAuto       *bool   `json:"in_auto,omitempty" doc:"Серверы ноды в «Авто» подписки; false — только в списке и группе страны"`
		// The node's API for the panel alone, and the networks added to the panel's own.
		APILockdown *bool     `json:"api_lockdown,omitempty" doc:"API ноды только с адресов панели; не для ноды панели"`
		APIAllow    *[]string `json:"api_allow,omitempty" maxItems:"16" doc:"Ещё сети или адреса (203.0.113.5, 198.51.100.0/24), с которых панель ходит к ноде"`
	}
}

type nodeIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type orderNodesInput struct {
	Body struct {
		IDs []int64 `json:"ids" minItems:"1" maxItems:"1000" doc:"Все ноды панели, каждая один раз, в том порядке, в каком их серверы идут в подписках"`
	}
}

func (h *handlers) registerNodes() {
	huma.Register(h.api, huma.Operation{OperationID: "list-nodes", Method: http.MethodGet, Path: "/api/v1/nodes", Summary: "Ноды", Tags: []string{"node"}}, h.listNodes)
	huma.Register(h.api, huma.Operation{OperationID: "create-node", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/nodes", Summary: "Добавить ноду", Tags: []string{"node"}, DefaultStatus: http.StatusCreated}, h.createNode)
	huma.Register(h.api, huma.Operation{OperationID: "update-node", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPatch, Path: "/api/v1/nodes/{id}", Summary: "Изменить ноду", Tags: []string{"node"}}, h.updateNode)
	huma.Register(h.api, huma.Operation{OperationID: "rekey-node", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/nodes/{id}/key", Summary: "Выпустить новый ключ ноды (старый перестаёт работать)", Tags: []string{"node"}}, h.rekeyNode)
	huma.Register(h.api, huma.Operation{OperationID: "order-nodes", Method: http.MethodPut, Path: "/api/v1/nodes/order", Summary: "Порядок серверов в подписке", Tags: []string{"node"}, DefaultStatus: http.StatusNoContent}, h.orderNodes)
	huma.Register(h.api, huma.Operation{OperationID: "delete-node", Method: http.MethodDelete, Path: "/api/v1/nodes/{id}", Summary: "Удалить ноду", Tags: []string{"node"}, DefaultStatus: http.StatusNoContent}, h.deleteNode)
}

// orderNodes sets the order of the servers in the subscriptions: the list must be every
// node once, so a stale page of the admin cannot silently drop or duplicate one.
func (h *handlers) orderNodes(ctx context.Context, in *orderNodesInput) (*struct{}, error) {
	ids := in.Body.IDs
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		nodes, err := q.ListNodes(ctx)
		if err != nil {
			return err
		}
		have := make([]int64, len(nodes))
		for i, n := range nodes {
			have[i] = n.ID
		}
		return setOrder(ids, have, "node_order_mismatch", func(id, place int64) error {
			return q.SetNodeSort(ctx, db.SetNodeSortParams{Sort: place, ID: id})
		})
	})
	if err != nil {
		return nil, err
	}
	// The subscription's server list is built again from the new order.
	h.nodesChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.order", "node", "", map[string]any{"ids": ids})
	return nil, nil
}

func (h *handlers) viewNode(ctx context.Context, n db.Node, inbounds []db.Inbound) NodeInfo {
	v := NodeInfo{ID: n.ID, Name: n.Name, PublicName: n.PublicName, Local: n.Address == "", Address: n.Address, Host: domain.NodeHost(n), ServerHost: n.PublicHost, Domain: n.Domain,
		Enabled: n.Enabled != 0, Inbounds: len(domain.NodeInbounds(inbounds, n.ID)), Status: "unknown",
		FairShare: n.FairShare != 0, ChannelMbps: store.Int64Ptr(n.ChannelMbps), BalanceGroup: n.BalanceGroup, Capacity: store.Int64Ptr(n.Capacity),
		HTTPRedirect: n.HttpRedirect != 0, SNIRouter: n.SniRouter != 0, FrontPage: n.FrontPage, InAuto: n.NoAuto == 0, APILockdown: n.ApiLockdown != 0, APIAllow: n.ApiAllow}
	if v.APIAllow == nil {
		v.APIAllow = []string{}
	}
	v.Cascade = []CascadeConns{}
	if v.Local {
		// The panel's own node is reached at the panel's address.
		ep, _ := h.d.Settings.Endpoint(ctx)
		v.Host = ep.Host
		v.Domain, _ = h.d.Settings.String(ctx, settings.KeyDomain)
	}
	v.Certificate = h.nodeCertView(n.ID, v.Host)
	v.TLS = h.nodeTLSView(ctx, n)
	if h.d.Nodes == nil {
		return v
	}
	hv, ok := h.d.Nodes.Health(n.ID)
	if !ok || hv.CheckedAt.IsZero() {
		return v
	}
	t := hv.CheckedAt
	v.CheckedAt = &t
	if !hv.LastOK.IsZero() {
		last := hv.LastOK
		v.LastOKAt = &last
	}
	if hello, ok := h.d.Nodes.Hello(n.ID); ok {
		v.Hello = helloView(hello)
	}
	if !hv.OK {
		v.Status, v.Error, v.ErrorCode, v.ErrorParams = "error", hv.Error, hv.Code, hv.Params
		if !hv.Since.IsZero() {
			since := hv.Since
			v.ErrorSince = &since
		}
		return v
	}
	if hv.Skew != nil {
		s := int64(math.Round(hv.Skew.Seconds()))
		v.ClockSkew = &s
	}
	v.Status, v.Version, v.Conns = "ok", hv.Health.Version, hv.Health.Conns
	v.Cascade = cascadeConns(hv.Relay)
	v.CPUPercent, v.MemUsed, v.MemTotal = hv.Health.System.CPUPercent, hv.Health.System.MemUsed, hv.Health.System.MemTotal
	if p := hv.Health.System.ProcCPUPercent; p > 0 {
		v.ProcCPU = &p
	}
	for _, l := range hv.Listeners {
		v.Listeners++
		if l.OK {
			v.ListenersOK++
		}
	}
	if l := hv.Health.APILock; v.APILockdown && !v.Local && l != nil && !l.OK {
		v.APILockError = l.Error
	}
	if r := hv.Health.Redirect; v.HTTPRedirect && r != nil && !r.OK {
		v.RedirectError = r.Error
	}
	if r := hv.Health.Router; v.SNIRouter && r != nil && !r.OK {
		v.RouterError = r.Error
	}
	h.nodeUpdateView(ctx, &v, nodeupdate.Reported(hv.Health.Update))
	return v
}

func (h *handlers) listNodes(ctx context.Context, _ *struct{}) (*nodesOutput, error) {
	nodes, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	inbounds, err := h.d.Store.Q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	day, err := h.nodeBytes(ctx, "24h")
	if err != nil {
		return nil, err
	}
	out := &nodesOutput{Body: make([]NodeInfo, 0, len(nodes))}
	for _, n := range nodes {
		v := h.viewNode(ctx, n, inbounds)
		v.Traffic24h, v.Cascade24h = day[n.ID].users.Up+day[n.ID].users.Down, day[n.ID].relay.Up+day[n.ID].relay.Down
		out.Body = append(out.Body, v)
	}
	return out, nil
}

// CascadeConns is how many connections one source node's cascade holds open through the node.
type CascadeConns struct {
	NodeID int64 `json:"node_id"`
	Conns  int   `json:"conns"`
}

// cascadeConns are the relay's open connections by source node (HealthView.Relay), the
// smallest id first; a name that is no relay user's is left out.
func cascadeConns(relay map[string]int) []CascadeConns {
	out := []CascadeConns{}
	for name, n := range relay {
		if id, ok := domain.RelaySource(name); ok && n > 0 {
			out = append(out, CascadeConns{NodeID: id, Conns: n})
		}
	}
	slices.SortFunc(out, func(a, b CascadeConns) int { return cmp.Compare(a.NodeID, b.NodeID) })
	return out
}

func (h *handlers) createNode(ctx context.Context, in *createNodeInput) (*nodeKeyOutput, error) {
	if h.d.PanelCert == nil {
		return nil, huma.Error409Conflict("nodes_disabled")
	}
	b := in.Body
	panel, err := h.d.PanelCert()
	if err != nil {
		return nil, err
	}
	rules, err := h.nodeRules(ctx)
	if err != nil {
		return nil, err
	}
	n, key, err := domain.AddNode(ctx, h.d.Store, panel, domain.NodeInput{Name: b.Name, Host: b.Host, Domain: b.Domain, APIPort: b.APIPort, PanelURL: h.panelURL(ctx), Rules: rules}, h.d.Now())
	if err != nil {
		return nil, h.nodeErr(err)
	}
	h.forgetNode(n.ID)
	h.nodesChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.create", "node", strconv.FormatInt(n.ID, 10), map[string]any{"name": n.Name, "address": n.Address})
	inbounds, err := h.d.Store.Q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	out := &nodeKeyOutput{}
	out.Body.Node, out.Body.Key, out.Body.Command = h.viewNode(ctx, n, inbounds), key, release.JoinCommand(key)
	return out, nil
}

// panelURL is where a node's hello goes: the panel's own address and port ("" while the
// panel has no address).
func (h *handlers) panelURL(ctx context.Context) string {
	ep, err := h.d.Settings.Endpoint(ctx)
	if err != nil {
		return ""
	}
	return ep.URL()
}

func (h *handlers) getNode(ctx context.Context, id int64) (db.Node, error) {
	n, err := h.d.Store.Q.GetNode(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return n, huma.Error404NotFound("not_found")
	}
	return n, err
}

func (h *handlers) updateNode(ctx context.Context, in *patchNodeInput) (*nodeInfoOutput, error) {
	b := in.Body
	// A node's address and domain are where its clients go: the admin's alone, or an
	// operator's in the extended mode. The form sends both as they are, so only a change is
	// refused.
	if _, ok := supportGrantOf(ctx); ok && (b.Host != nil || b.Domain != nil) {
		cur, err := h.d.Store.Q.GetNode(ctx, in.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, huma.Error404NotFound("not_found")
		}
		if err != nil {
			return nil, err
		}
		if b.Host != nil && strings.TrimSpace(*b.Host) != cur.PublicHost {
			if err := requireSession(ctx, "host"); err != nil {
				return nil, err
			}
		}
		if b.Domain != nil && strings.TrimSpace(*b.Domain) != cur.Domain {
			if err := requireSession(ctx, "domain"); err != nil {
				return nil, err
			}
		}
	}
	rules, err := h.nodeRules(ctx)
	if err != nil {
		return nil, err
	}
	n, err := domain.UpdateNode(ctx, h.d.Store, in.ID, domain.NodePatch{Name: b.Name, PublicName: b.PublicName, Host: b.Host, Domain: b.Domain,
		Enabled: b.Enabled, FairShare: b.FairShare, ChannelMbps: b.ChannelMbps, HTTPRedirect: b.HTTPRedirect, SNIRouter: b.SNIRouter, FrontPage: b.FrontPage, InAuto: b.InAuto, APILockdown: b.APILockdown, APIAllow: b.APIAllow, Rules: rules}, h.d.Now())
	if err != nil {
		return nil, h.nodeErr(err)
	}
	h.nodesChanged()
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.update", "node", strconv.FormatInt(n.ID, 10), map[string]any{"name": n.Name, "public_name": n.PublicName, "enabled": n.Enabled != 0,
		"fair_share": n.FairShare != 0, "channel_mbps": n.ChannelMbps.Int64, "http_redirect": n.HttpRedirect != 0, "sni_router": n.SniRouter != 0, "front_page": n.FrontPage, "in_auto": n.NoAuto == 0, "api_lockdown": n.ApiLockdown != 0, "api_allow": n.ApiAllow})
	return h.nodeInfo(ctx, n.ID)
}

// nodeRules are what a node's name and domain are checked against: the subscription's
// groups, and public DNS.
func (h *handlers) nodeRules(ctx context.Context) (domain.NodeRules, error) {
	g, err := subsrc.LoadGroups(ctx, h.d.Settings)
	if err != nil {
		return domain.NodeRules{}, err
	}
	return domain.NodeRules{Name: g.CheckName, DNS: h.d.DNS}, nil
}

// nodeErr says what the rules of a node refused the way the UI reads it; an error that is
// none of theirs comes back as it is.
func (h *handlers) nodeErr(err error) error {
	var name *domain.NameError
	var dom *domain.DomainError
	switch {
	case errors.As(err, &name):
		return fieldErr("body.name", name.Code)
	case errors.Is(err, domain.ErrUnknownNode):
		return huma.Error404NotFound("not_found")
	case errors.Is(err, domain.ErrLocalNode):
		// The panel's own node follows the panel's address in the settings.
		return refusal("local_node")
	case errors.Is(err, domain.ErrChannelRequired):
		return fieldErr("body.channel_mbps", "channel_required")
	case errors.Is(err, domain.ErrLocalLockdown):
		// The panel reaches its own node over the socket: there is no API port to close.
		return refusal("local_node_lockdown")
	case errors.Is(err, domain.ErrManyAPIAllow):
		return fieldErr("body.api_allow", "api_allow_many")
	}
	if fe, ok := fieldFromDomain(err); ok {
		return fe
	}
	var bad *domain.APIAllowError
	if errors.As(err, &bad) {
		return fieldErrValue("body.api_allow", "api_allow_invalid", bad.Value)
	}
	var details []error
	if errors.Is(err, domain.ErrBadHost) {
		details = append(details, fieldDetail("body.host", "public_host_invalid", nil))
	}
	if errors.Is(err, domain.ErrBadDomain) {
		details = append(details, fieldDetail("body.domain", "domain_invalid", nil))
	}
	if errors.As(err, &dom) {
		details = append(details, h.domainDetail("body.domain", dom))
	}
	if len(details) > 0 {
		return fieldErrs(details...)
	}
	return err
}

// nodeInfo is one node as the Nodes page shows it.
func (h *handlers) nodeInfo(ctx context.Context, id int64) (*nodeInfoOutput, error) {
	n, err := h.getNode(ctx, id)
	if err != nil {
		return nil, err
	}
	inbounds, err := h.d.Store.Q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	day, err := h.nodeBytes(ctx, "24h")
	if err != nil {
		return nil, err
	}
	v := h.viewNode(ctx, n, inbounds)
	v.Traffic24h, v.Cascade24h = day[n.ID].users.Up+day[n.ID].users.Down, day[n.ID].relay.Up+day[n.ID].relay.Down
	return &nodeInfoOutput{Body: v}, nil
}

func (h *handlers) rekeyNode(ctx context.Context, in *nodeIDInput) (*nodeKeyOutput, error) {
	if h.d.PanelCert == nil {
		return nil, huma.Error409Conflict("nodes_disabled")
	}
	panel, err := h.d.PanelCert()
	if err != nil {
		return nil, err
	}
	key, err := domain.RekeyNode(ctx, h.d.Store, panel, in.ID, h.panelURL(ctx), h.d.Now())
	switch {
	case errors.Is(err, domain.ErrUnknownNode):
		return nil, huma.Error404NotFound("not_found")
	case errors.Is(err, domain.ErrLocalNode):
		return nil, huma.Error409Conflict("local_node")
	case err != nil:
		return nil, err
	}
	h.nodesChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.rekey", "node", strconv.FormatInt(in.ID, 10), nil)
	n, err := h.getNode(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	inbounds, err := h.d.Store.Q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	out := &nodeKeyOutput{}
	out.Body.Node, out.Body.Key, out.Body.Command = h.viewNode(ctx, n, inbounds), key, release.JoinCommand(key)
	return out, nil
}

// nodeUnused refuses to delete the node the bot goes to Telegram through: it would lose
// its way there. What else leaves through the node moves on first (domain.MoveExits).
func (h *handlers) nodeUnused(ctx context.Context, q *db.Queries, id int64) error {
	route, _, err := settings.Get[tgate.Route](ctx, settings.New(q), tgate.KeyRoute)
	if err != nil {
		return err
	}
	if route.Mode == tgate.RouteNode && route.NodeID == id {
		return huma.Error409Conflict("node_in_use", &huma.ErrorDetail{Location: "path.id", Message: "node_in_use_telegram"})
	}
	return serversLeft(ctx, q, id, 0)
}

// serversLeft refuses a deletion (of node nodeID, or of inbound inboundID) that would leave a
// tariff of chosen servers with none: its users would have nothing to connect to. The answer
// names the tariffs, to choose other servers for first.
func serversLeft(ctx context.Context, q *db.Queries, nodeID, inboundID int64) error {
	rows, err := q.TariffsLeftWithoutServers(ctx, db.TariffsLeftWithoutServersParams{NodeID: nodeID, InboundID: inboundID})
	if err != nil || len(rows) == 0 {
		return err
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	return huma.Error409Conflict("tariff_servers", &huma.ErrorDetail{Location: "path.id", Message: "tariff_servers", Value: strings.Join(names, ", ")})
}

func (h *handlers) deleteNode(ctx context.Context, in *nodeIDInput) (*struct{}, error) {
	n, err := h.getNode(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n.Address == "" {
		return nil, huma.Error409Conflict("local_node")
	}
	var moved []domain.MovedExit
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		// What went out through the node goes through another one, or straight out: the
		// cascade does not keep the node from going.
		var err error
		if moved, err = domain.MoveExits(ctx, q, n.ID, h.d.Now(), h.hostOf); err != nil {
			return err
		}
		if err := h.nodeUnused(ctx, q, n.ID); err != nil {
			return err
		}
		if err := q.DeleteNode(ctx, n.ID); err != nil {
			return err
		}
		// The bot's route may still name it from when it went through it: the admin
		// panel would offer a node that is gone.
		set := settings.New(q)
		if route, ok, err := settings.Get[tgate.Route](ctx, set, tgate.KeyRoute); err != nil {
			return err
		} else if ok && route.NodeID == n.ID {
			route.NodeID = 0
			if err := settings.Set(ctx, set, tgate.KeyRoute, route); err != nil {
				return err
			}
		}
		return q.DeleteNodeStateOf(ctx, strconv.FormatInt(n.ID, 10))
	})
	if err != nil {
		return nil, err
	}
	if h.d.Nodes != nil {
		// Best effort: an unreachable node keeps serving until it is reinstalled.
		if err := h.d.Nodes.Retire(ctx, n.ID); err != nil {
			h.d.Log.Warn("retire node", "node", n.ID, "err", err)
		}
		h.d.Nodes.NodesChanged()
	}
	// The row is gone: its certificates and private keys go from disk too, and no node
	// that gets this id later (see forgetNode) inherits them.
	h.forgetNode(n.ID)
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.delete", "node", strconv.FormatInt(n.ID, 10), map[string]any{"name": n.Name, "exits_moved": moved})
	return nil, nil
}

// nodesChanged tells the node syncer to look at the nodes again; the panel may run
// without nodes (tests, development).
func (h *handlers) nodesChanged() {
	if h.d.Nodes != nil {
		h.d.Nodes.NodesChanged()
	}
	// A new node, address or domain gets its public certificate now, not at the next round.
	if h.d.WakeNodeCerts != nil {
		h.d.WakeNodeCerts()
	}
}

// forgetNode drops the certificates and keys the panel keeps for a node id: the admin's
// own certificate and the node's self-signed pair. PostgreSQL does not hand an id out
// twice, but files may outlive their row (a restored older backup winds the ids back, a
// SQLite panel reused them), so a new node also clears what its id left behind.
func (h *handlers) forgetNode(id int64) {
	if h.d.ForgetNode == nil {
		return
	}
	if err := h.d.ForgetNode(id); err != nil {
		h.d.Log.Warn("remove node certificates", "node", id, "err", err)
	}
}
