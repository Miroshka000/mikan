package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/warp"
	"mikan/internal/textutil"
)

type WarpView struct {
	Configured bool       `json:"configured"`
	Enabled    bool       `json:"enabled"`
	Source     string     `json:"source,omitempty" enum:"register,import,"`
	Plus       bool       `json:"plus" doc:"Аккаунт WARP+"`
	Endpoint   string     `json:"endpoint,omitempty"`
	IPv4       string     `json:"ipv4,omitempty"`
	IPv6       string     `json:"ipv6,omitempty"`
	Routes     []string   `json:"routes" doc:"Домены и сети, которые идут через WARP у всех подключений ноды"`
	Inbounds   []string   `json:"inbounds" doc:"Подключения ноды, у которых весь трафик идёт через WARP"`
	NoReserved bool       `json:"no_reserved,omitempty" doc:"Свой конфиг без Reserved: на части endpoint'ов Cloudflare молча не отвечает на handshake"`
	Status     *WarpCheck `json:"status,omitempty" doc:"Последняя проверка выхода через WARP с ноды; нет — у ноды ещё нет настроенного WARP"`
}

// WarpCheck is one look at the internet through WARP.
type WarpCheck struct {
	OK        bool      `json:"ok"`
	IP        string    `json:"ip,omitempty" doc:"Адрес, который видят сайты"`
	Warp      string    `json:"warp,omitempty" doc:"on | plus | off — как отвечает Cloudflare"`
	Colo      string    `json:"colo,omitempty"`
	Error     string    `json:"error,omitempty" doc:"Причина: timeout | dns | tls | refused | not_loaded | bad_answer | https_timeout | failed; node_unreachable — нода не ответила панели; unreachable — старая нода"`
	Detail    string    `json:"detail,omitempty" doc:"Причина словами, без секретов; на английском"`
	CheckedAt time.Time `json:"checked_at"`
}

type warpOutput struct{ Body WarpView }

type warpGetInput struct {
	ID    int64 `path:"id" minimum:"1"`
	Force bool  `query:"force" doc:"Проверить заново, не беря ответ ноды из кеша (не чаще раза в 5 секунд)"`
}

type warpRegisterInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		License string `json:"license,omitempty" maxLength:"40" doc:"Ключ WARP+ (необязательно)"`
	}
}

type warpImportInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Config string `json:"config" minLength:"1" maxLength:"8192" doc:"WireGuard-конфиг WARP: wgcf, warp-plus или экспорт приложения"`
	}
}

type warpPatchInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Enabled *bool     `json:"enabled,omitempty"`
		Routes  *[]string `json:"routes,omitempty" maxItems:"1000" doc:"Домены (example.com — вместе с поддоменами) и сети (104.16.0.0/13)"`
		License *string   `json:"license,omitempty" maxLength:"40" doc:"Ключ WARP+ для зарегистрированного аккаунта"`
	}
}

func (h *handlers) registerWarp() {
	tags := []string{"node"}
	huma.Register(h.api, huma.Operation{OperationID: "get-node-warp", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/warp", Summary: "WARP ноды", Tags: tags}, h.getWarp)
	huma.Register(h.api, huma.Operation{OperationID: "register-node-warp", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/nodes/{id}/warp/register", Summary: "Зарегистрировать WARP для ноды в Cloudflare", Tags: tags}, h.registerNodeWarp)
	huma.Register(h.api, huma.Operation{OperationID: "import-node-warp", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/nodes/{id}/warp/import", Summary: "Загрузить свой WireGuard-конфиг WARP", Tags: tags}, h.importNodeWarp)
	huma.Register(h.api, huma.Operation{OperationID: "update-node-warp", Method: http.MethodPatch, Path: "/api/v1/nodes/{id}/warp", Summary: "Включить WARP, списки доменов, ключ WARP+", Tags: tags}, h.patchWarp)
	huma.Register(h.api, huma.Operation{OperationID: "delete-node-warp", Method: http.MethodDelete, Path: "/api/v1/nodes/{id}/warp", Summary: "Удалить WARP ноды", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteWarp)
}

// warpDetail is Cloudflare's or a config's refusal at location, with Cloudflare's HTTP
// status when it refused.
func warpDetail(location string, err error) *huma.ErrorDetail {
	var we *warp.Error
	if !errors.As(err, &we) {
		return &huma.ErrorDetail{Location: location, Message: "warp_failed"}
	}
	d := &huma.ErrorDetail{Location: location, Message: we.Code}
	if we.Status != 0 {
		d.Value = we.Status
	}
	return d
}

// warpUsers are the names of the node's inbounds that send all their traffic through WARP.
func (h *handlers) warpUsers(ctx context.Context, nodeID int64) ([]string, error) {
	ins, err := h.d.Store.Q.ListNodeInbounds(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, in := range ins {
		if in.Outbound == "warp" {
			names = append(names, in.Name)
		}
	}
	return names, nil
}

// warpUnused refuses to take WARP away from a node while inbounds go out through it:
// without it they would leave directly, from the node's own address, which is what the
// admin put them behind WARP to avoid. The same rule keeps a cascade from going direct
// when its exit is gone (node_in_use).
func (h *handlers) warpUnused(ctx context.Context, nodeID int64) error {
	names, err := h.warpUsers(ctx, nodeID)
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return huma.Error409Conflict("warp_in_use", &huma.ErrorDetail{Location: "path.id", Message: "warp_in_use_inbounds", Value: strings.Join(names, ", ")})
	}
	return nil
}

// warpView is the node's WARP as the drawer shows it. check asks the node for the state of
// the tunnel; force makes it look again instead of answering from its minute-old check.
func (h *handlers) warpView(ctx context.Context, nodeID int64, check, force bool) (WarpView, error) {
	v := WarpView{Routes: []string{}, Inbounds: []string{}}
	w, err := h.d.Store.Q.GetNodeWarp(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Configured, v.Enabled, v.Source, v.Plus = true, w.Enabled != 0, w.Source, w.Plus != 0
	v.Endpoint, v.IPv4, v.IPv6 = w.Endpoint, w.Ipv4, w.Ipv6
	v.NoReserved = w.Source == "import" && w.Reserved == ""
	_ = json.Unmarshal([]byte(w.Routes), &v.Routes)
	if v.Inbounds, err = h.warpUsers(ctx, nodeID); err != nil {
		return v, err
	}
	if check && v.Enabled && h.d.Nodes != nil {
		s, err := h.d.Nodes.Warp(ctx, nodeID, force)
		switch {
		case err != nil:
			// Not "no status": the admin must see that the node did not answer, and why.
			h.d.Log.Warn("warp: the node did not answer a check", "node", nodeID, "err", err)
			v.Status = &WarpCheck{Error: "node_unreachable", Detail: textutil.Clip(err.Error(), 200), CheckedAt: h.d.Now().UTC()}
		case s.Configured:
			v.Status = &WarpCheck{s.OK, s.IP, s.Warp, s.Colo, s.Error, s.Detail, s.CheckedAt}
		}
	}
	return v, nil
}

func (h *handlers) getWarp(ctx context.Context, in *warpGetInput) (*warpOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	v, err := h.warpView(ctx, in.ID, true, in.Force)
	if err != nil {
		return nil, err
	}
	return &warpOutput{Body: v}, nil
}

// saveWarp stores an account for the node and pushes it.
func (h *handlers) saveWarp(ctx context.Context, nodeID int64, source string, a warp.Account) (*warpOutput, error) {
	now := h.d.Now().Unix()
	mtu := int64(a.MTU)
	if mtu == 0 {
		mtu = 1280
	}
	routes := "[]"
	if old, err := h.d.Store.Q.GetNodeWarp(ctx, nodeID); err == nil {
		routes = old.Routes // a new account keeps the admin's lists
	}
	err := h.d.Store.Q.SaveNodeWarp(ctx, db.SaveNodeWarpParams{NodeID: nodeID, Source: source, PrivateKey: a.PrivateKey, PeerPublicKey: a.PeerPublicKey,
		Endpoint: a.Endpoint, Ipv4: a.IPv4, Ipv6: a.IPv6, Reserved: base64.StdEncoding.EncodeToString(a.Reserved), Mtu: mtu,
		AccountID: a.ID, AccountToken: a.Token, Plus: domain.Flag(a.Plus), Routes: routes, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return nil, err
	}
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.warp."+source, "node", strconv.FormatInt(nodeID, 10), map[string]any{"plus": a.Plus})
	v, err := h.warpView(ctx, nodeID, false, false)
	if err != nil {
		return nil, err
	}
	return &warpOutput{Body: v}, nil
}

func (h *handlers) registerNodeWarp(ctx context.Context, in *warpRegisterInput) (*warpOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	a, err := h.d.Warp.Register(ctx, strings.TrimSpace(in.Body.License))
	if err != nil {
		d := warpDetail("body", err)
		if strings.HasPrefix(d.Message, "warp_license") {
			d.Location = "body.license"
		}
		return nil, huma.Error422UnprocessableEntity("warp", d)
	}
	return h.saveWarp(ctx, in.ID, "register", a)
}

func (h *handlers) importNodeWarp(ctx context.Context, in *warpImportInput) (*warpOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	a, err := warp.ParseConf(in.Body.Config)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("warp", warpDetail("body.config", err))
	}
	return h.saveWarp(ctx, in.ID, "import", a)
}

func (h *handlers) patchWarp(ctx context.Context, in *warpPatchInput) (*warpOutput, error) {
	w, err := h.d.Store.Q.GetNodeWarp(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("warp_not_configured")
	}
	if err != nil {
		return nil, err
	}
	b := in.Body
	enabled, routes := w.Enabled, w.Routes
	if b.Enabled != nil {
		enabled = domain.Flag(*b.Enabled)
		if enabled == 0 && w.Enabled != 0 {
			if err := h.warpUnused(ctx, in.ID); err != nil {
				return nil, err
			}
		}
	}
	if b.Routes != nil {
		rs, bad := warp.ParseRoutes(*b.Routes)
		if len(bad) > 0 {
			return nil, fieldErrValue("body.routes", "warp_bad_route", bad[0])
		}
		if err := rs.Check(); err != nil {
			return nil, fieldErr("body.routes", err.Error())
		}
		raw, _ := json.Marshal(rs.All())
		routes = string(raw)
	}
	if b.License != nil {
		plus, err := h.d.Warp.SetLicense(ctx, w.AccountID, w.AccountToken, strings.TrimSpace(*b.License))
		if err != nil {
			return nil, fieldErrs(warpDetail("body.license", err))
		}
		if err := h.d.Store.Q.SetNodeWarpPlus(ctx, db.SetNodeWarpPlusParams{Plus: domain.Flag(plus), UpdatedAt: h.d.Now().Unix(), NodeID: in.ID}); err != nil {
			return nil, err
		}
	}
	if err := h.d.Store.Q.SetNodeWarpOptions(ctx, db.SetNodeWarpOptionsParams{Enabled: enabled, Routes: routes, UpdatedAt: h.d.Now().Unix(), NodeID: in.ID}); err != nil {
		return nil, err
	}
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.warp.update", "node", strconv.FormatInt(in.ID, 10), map[string]any{"enabled": enabled != 0, "routes_changed": b.Routes != nil})
	v, err := h.warpView(ctx, in.ID, false, false)
	if err != nil {
		return nil, err
	}
	return &warpOutput{Body: v}, nil
}

func (h *handlers) deleteWarp(ctx context.Context, in *nodeIDInput) (*struct{}, error) {
	if err := h.warpUnused(ctx, in.ID); err != nil {
		return nil, err
	}
	n, err := h.d.Store.Q.DeleteNodeWarp(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.warp.delete", "node", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}
