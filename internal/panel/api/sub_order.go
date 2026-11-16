package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// SubOrderItem is one server of the subscriptions: an address for clients of a protocol.
type SubOrderItem struct {
	HostID    int64  `json:"host_id" doc:"Адрес подключения для клиентов: его id в порядке, который сохраняют"`
	InboundID int64  `json:"inbound_id" doc:"Подключение (протокол), чей это адрес"`
	NodeID    int64  `json:"node_id"`
	Default   bool   `json:"default" doc:"Адрес по умолчанию: тот, что задаёт само подключение"`
	Address   string `json:"address" doc:"Адрес для клиентов; пусто — адрес ноды"`
	Port      int    `json:"port" doc:"Порт для клиентов; 0 — порт подключения"`
	CDN       bool   `json:"cdn" doc:"Клиенты ходят через CDN"`
	Name      string `json:"name" doc:"Имя сервера в приложении клиента, которому доступно всё; переменные — как написаны. У сервера, который клиенты сейчас не получают, — его собственное имя или имя подключения, без флага ноды"`
	Served    bool   `json:"served" doc:"Клиенты получают этот сервер сейчас: подключение и нода включены"`
	ForSome   bool   `json:"for_some" doc:"Адрес выдают не всем: только части статусов подписки, только пробным или всем, кроме них, или по группам клиентов"`
}

type subOrderOutput struct{ Body []SubOrderItem }

type orderServersInput struct {
	Body struct {
		HostIDs []int64 `json:"host_ids" minItems:"1" maxItems:"10000" doc:"Все адреса подключений панели (host_id), каждый один раз, в том порядке, в каком серверы идут в подписках. Нода встаёт туда, где её первый сервер"`
	}
}

func (h *handlers) registerSubOrder() {
	huma.Register(h.api, huma.Operation{OperationID: "list-server-order", Method: http.MethodGet, Path: "/api/v1/inbounds/order", Summary: "Порядок серверов в подписке",
		Description: "Все серверы подписок по порядку, у всех нод: каждый адрес для клиентов каждого подключения. Каждый клиент получает те из них, что ему доступны, в этом порядке.",
		Tags:        []string{"inbounds"}}, h.listServerOrder)
	huma.Register(h.api, huma.Operation{OperationID: "order-servers", Method: http.MethodPut, Path: "/api/v1/inbounds/order", Summary: "Задать порядок серверов в подписке",
		Tags: []string{"inbounds"}, DefaultStatus: http.StatusNoContent}, h.orderServers)
}

// listServerOrder lists every server of the subscriptions in their order: the hosts of every
// inbound, named as a client given all of them sees them.
func (h *handlers) listServerOrder(ctx context.Context, _ *struct{}) (*subOrderOutput, error) {
	rows, err := h.d.Store.Q.ListHostsInOrder(ctx)
	if err != nil {
		return nil, err
	}
	inbounds, err := h.d.Store.Q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]db.Inbound, len(inbounds))
	for _, in := range inbounds {
		byID[in.ID] = in
	}
	names := map[int64]string{}
	if h.d.ServerNames != nil {
		if names, err = h.d.ServerNames(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]SubOrderItem, 0, len(rows))
	for _, r := range rows {
		host := r.InboundHost
		name, served := names[host.ID]
		if !served {
			// Off for everyone now: its own name, or its protocol's, as the admin knows it.
			if name = host.Name; name == "" {
				name = domain.ProxyName(byID[host.InboundID])
			}
		}
		out = append(out, SubOrderItem{HostID: host.ID, InboundID: host.InboundID, NodeID: r.NodeID, Default: host.Position == 0,
			Address: host.Address, Port: int(host.Port), CDN: host.Cdn, Name: name, Served: served, ForSome: domain.HostForSome(host.States, host.Trial, host.GroupMode)})
	}
	return &subOrderOutput{Body: out}, nil
}

// orderServers sets the order of the servers in the subscriptions: the list must be every
// host once, so a stale page of the admin cannot silently drop or duplicate one. The nodes
// follow their first servers.
func (h *handlers) orderServers(ctx context.Context, in *orderServersInput) (*struct{}, error) {
	ids := in.Body.HostIDs
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.LockSubOrder(ctx); err != nil {
			return err
		}
		rows, err := q.ListHostsInOrder(ctx)
		if err != nil {
			return err
		}
		nodes, err := q.ListNodes(ctx)
		if err != nil {
			return err
		}
		return placeServers(ctx, q, ids, rows, nodes)
	})
	if err != nil {
		return nil, err
	}
	// The nodes' order may have moved, and with it the subscriptions' groups of countries.
	h.nodesChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "inbound.order", "inbound", "", map[string]any{"host_ids": ids})
	return nil, nil
}
