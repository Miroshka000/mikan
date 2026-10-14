// VK TURN on a node: the way in for networks where a whitelist lets only calls through.
// The panel runs the vk-turn-proxy server on the node; the server hands what it unwraps
// to one inbound of the same node, and clients reach it over the TURN servers of VK
// calls, through the client program running next to their app. The subscription lists
// that inbound a second time, at the client's own address, so the app shows one more
// server next to the usual one.
package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/turn"
)

type TurnView struct {
	Configured bool       `json:"configured"`
	Enabled    bool       `json:"enabled"`
	InboundID  int64      `json:"inbound_id,omitempty"`
	Inbound    string     `json:"inbound,omitempty" doc:"Подключение ноды, которому сервер отдаёт трафик"`
	ListenPort string     `json:"listen_port,omitempty" doc:"Порт сервера на ноде (UDP)"`
	ClientHost string     `json:"client_host,omitempty" doc:"Адрес, который набирает приложение клиента: там слушает программа-клиент"`
	ClientPort string     `json:"client_port,omitempty"`
	Streams    int        `json:"streams,omitempty" doc:"Потоков у клиента: VK ограничивает один, клиент разводит их несколько"`
	UDP        bool       `json:"udp" doc:"Клиент говорит с TURN-серверами VK по UDP"`
	Title      string     `json:"title,omitempty" doc:"Имя записи в приложении; пусто — имя подключения плюс « WL»"`
	Links      []string   `json:"links" doc:"Ссылки на звонки VK: по одной в строке, пользователи расходятся по ним"`
	BinaryPath string     `json:"binary_path,omitempty" doc:"Путь к серверу на ноде; пусто — тот, что в образе"`
	Entry      string     `json:"entry,omitempty" doc:"Как запись выглядит в подписке"`
	Mode       string     `json:"mode,omitempty" enum:"vless,udp," doc:"Что несёт клиент: целые соединения (VLESS, XHTTP) или датаграммы (Hysteria2)"`
	Peer       string     `json:"peer,omitempty" doc:"Адрес сервера для клиента: хост ноды и порт сервера"`
	Status     *TurnCheck `json:"status,omitempty" doc:"Сервер на ноде, как его видит панель; нет — нода старая или не ответила"`
}

// TurnCheck is one look at the node's TURN server.
type TurnCheck struct {
	OK        bool      `json:"ok"`
	Running   bool      `json:"running"`
	PID       int       `json:"pid,omitempty"`
	Since     time.Time `json:"since,omitempty" doc:"Когда запущен нынешний процесс"`
	Restarts  int       `json:"restarts,omitempty" doc:"Сколько раз сервер поднимался заново"`
	Error     string    `json:"error,omitempty" doc:"no_binary | bad_config | exited | failed — с ноды; node_too_old — нода старше этой возможности; node_unreachable — нода не ответила"`
	Detail    string    `json:"detail,omitempty" doc:"Своими словами, без секретов; на английском"`
	Log       []string  `json:"log,omitempty" doc:"Последние строки вывода сервера"`
	CheckedAt time.Time `json:"checked_at"`
}

type turnOutput struct{ Body TurnView }

type turnGetInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type turnPatchInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Enabled    *bool     `json:"enabled,omitempty"`
		InboundID  *int64    `json:"inbound_id,omitempty" doc:"Подключение этой ноды, которому сервер отдаёт трафик"`
		ListenPort *string   `json:"listen_port,omitempty" pattern:"^[0-9]{1,5}$"`
		ClientHost *string   `json:"client_host,omitempty" maxLength:"64"`
		ClientPort *string   `json:"client_port,omitempty" pattern:"^[0-9]{1,5}$"`
		Streams    *int      `json:"streams,omitempty" minimum:"1" maximum:"64"`
		UDP        *bool     `json:"udp,omitempty"`
		Title      *string   `json:"title,omitempty" maxLength:"64"`
		BinaryPath *string   `json:"binary_path,omitempty" maxLength:"200"`
		Links      *[]string `json:"links,omitempty" maxItems:"100" doc:"Ссылки на звонки VK (https://vk.com/call/join/…); пустой список — у записи нет ссылки"`
	}
}

func (h *handlers) registerTurn() {
	tags := []string{"node"}
	huma.Register(h.api, huma.Operation{OperationID: "get-node-turn", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/turn", Summary: "VK TURN ноды", Tags: tags}, h.getTurn)
	huma.Register(h.api, huma.Operation{OperationID: "save-node-turn", Method: http.MethodPatch, Path: "/api/v1/nodes/{id}/turn", Summary: "Настроить VK TURN на ноде", Tags: tags}, h.patchTurn)
	huma.Register(h.api, huma.Operation{OperationID: "delete-node-turn", Method: http.MethodDelete, Path: "/api/v1/nodes/{id}/turn", Summary: "Убрать VK TURN с ноды", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteTurn)
}

// turnError turns the package's refusal into the field the form shows.
func turnError(err error) error {
	var te *turn.Error
	if !errors.As(err, &te) {
		return err
	}
	location := "body"
	if te.Field != "" {
		location += "." + te.Field
	}
	return huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: location, Message: te.Code, Value: te.Detail})
}

// turnInbound is the refusal of an inbound that is not this node's, is off, or is gone.
func turnInbound() error {
	return huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.inbound_id", Message: "turn_inbound"})
}

// turnView is the node's setup as the drawer shows it. withStatus asks the node itself,
// which costs a round trip to it.
func (h *handlers) turnView(ctx context.Context, n db.Node, withStatus bool) (TurnView, error) {
	row, err := h.d.Store.Q.GetNodeTurn(ctx, n.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnView{Links: []string{}}, nil
	}
	if err != nil {
		return TurnView{}, err
	}
	cfg := turn.FromRow(row)
	v := TurnView{
		Configured: true, Enabled: cfg.Enabled, InboundID: cfg.InboundID, ListenPort: cfg.ListenPort,
		ClientHost: cfg.ClientHost, ClientPort: cfg.ClientPort, Streams: cfg.Streams, UDP: cfg.UDP,
		Title: cfg.Title, Links: cfg.Links, BinaryPath: cfg.BinaryPath,
	}
	// A node without an address of its own (a fresh install) has no peer to show yet.
	if host := domain.NodeHost(n); host != "" {
		v.Peer = cfg.Peer(host)
	}
	if in, err := h.d.Store.Q.GetInbound(ctx, cfg.InboundID); err == nil {
		v.Inbound = in.Name
		v.Entry = cfg.Name(domain.ProxyName(in))
		if mode, err := cfg.Mode(in); err == nil {
			v.Mode = mode
		}
	}
	if withStatus && h.d.Nodes != nil {
		s, err := h.d.Nodes.Turn(ctx, n.ID)
		switch {
		case err != nil:
			// A node that predates the server answers 404; one that is silent says nothing.
			code, detail := "node_unreachable", clipText(err.Error(), 200)
			var se *nodeapi.StatusError
			if errors.As(err, &se) && se.Status == http.StatusNotFound {
				code, detail = "node_too_old", ""
			}
			v.Status = &TurnCheck{Error: code, Detail: detail, CheckedAt: h.d.Now().UTC()}
		case s.Configured:
			v.Status = &TurnCheck{OK: s.Running, Running: s.Running, PID: s.PID, Since: s.Since,
				Restarts: s.Restarts, Error: s.Error, Detail: s.Detail, Log: s.Log, CheckedAt: s.CheckedAt}
		}
	}
	return v, nil
}

func (h *handlers) getTurn(ctx context.Context, in *turnGetInput) (*turnOutput, error) {
	n, err := h.getNode(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	v, err := h.turnView(ctx, n, true)
	if err != nil {
		return nil, err
	}
	return &turnOutput{Body: v}, nil
}

// patchTurn saves the node's turn setup; the first save creates it, so inbound_id is
// required then.
func (h *handlers) patchTurn(ctx context.Context, in *turnPatchInput) (*turnOutput, error) {
	n, err := h.getNode(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	row, err := h.d.Store.Q.GetNodeTurn(ctx, in.ID)
	fresh := errors.Is(err, sql.ErrNoRows)
	if err != nil && !fresh {
		return nil, err
	}
	if fresh {
		row = db.NodeTurn{NodeID: in.ID, ListenPort: turn.DefaultListenPort, ClientHost: turn.DefaultClientHost,
			ClientPort: turn.DefaultClientPort, Streams: turn.DefaultStreams, Udp: 1}
	}
	cfg := turn.FromRow(row)
	b := in.Body
	switch {
	case b.Enabled != nil:
		cfg.Enabled = *b.Enabled
	default:
		cfg.Enabled = cfg.Enabled || fresh
	}
	if b.InboundID != nil {
		cfg.InboundID = *b.InboundID
	}
	if b.ListenPort != nil {
		cfg.ListenPort = strings.TrimSpace(*b.ListenPort)
	}
	if b.ClientHost != nil {
		cfg.ClientHost = strings.TrimSpace(*b.ClientHost)
	}
	if b.ClientPort != nil {
		cfg.ClientPort = strings.TrimSpace(*b.ClientPort)
	}
	if b.Streams != nil {
		cfg.Streams = *b.Streams
	}
	if b.UDP != nil {
		cfg.UDP = *b.UDP
	}
	if b.Title != nil {
		cfg.Title = strings.TrimSpace(*b.Title)
	}
	if b.BinaryPath != nil {
		cfg.BinaryPath = strings.TrimSpace(*b.BinaryPath)
	}
	if b.Links != nil {
		cfg.Links = *b.Links
	}
	if err := cfg.Validate(); err != nil {
		return nil, turnError(err)
	}
	bound, err := h.d.Store.Q.GetInbound(ctx, cfg.InboundID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, turnInbound()
	}
	if err != nil {
		return nil, err
	}
	if bound.NodeID != n.ID || bound.Enabled == 0 {
		return nil, turnInbound()
	}
	// The inbound must be one the server can actually serve: its port and its kind.
	if _, err := cfg.Server(bound); err != nil {
		return nil, turnError(err)
	}
	if fresh || row.ListenPort != cfg.ListenPort {
		if err := domain.CheckPort(ctx, h.d.Store.Q, n, cfg.ListenPort, "udp", domain.PortHolder{Kind: domain.PortTurn}); err != nil {
			return nil, portErrorAt(err, "body.listen_port")
		}
	}
	now := h.d.Now().Unix()
	err = h.d.Store.Q.SaveNodeTurn(ctx, db.SaveNodeTurnParams{
		NodeID: in.ID, Enabled: domain.Flag(cfg.Enabled), InboundID: cfg.InboundID, ListenPort: cfg.ListenPort,
		ClientHost: cfg.ClientHost, ClientPort: cfg.ClientPort, Streams: int64(cfg.Streams), Udp: domain.Flag(cfg.UDP),
		Title: cfg.Title, VkLinks: turn.JoinLinks(cfg.Links), BinaryPath: cfg.BinaryPath, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil, err
	}
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.turn.save", "node", strconv.FormatInt(in.ID, 10), map[string]any{
		"enabled": cfg.Enabled, "inbound_id": cfg.InboundID, "port": cfg.ListenPort, "links": len(cfg.Links),
	})
	v, err := h.turnView(ctx, n, true)
	if err != nil {
		return nil, err
	}
	return &turnOutput{Body: v}, nil
}

func (h *handlers) deleteTurn(ctx context.Context, in *nodeIDInput) (*struct{}, error) {
	n, err := h.d.Store.Q.DeleteNodeTurn(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.turn.delete", "node", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}
