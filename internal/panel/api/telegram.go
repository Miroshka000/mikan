package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/infraalerts"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgbot"
)

// TelegramView is the bot as the admin panel shows it. The token itself never leaves
// the panel.
type TelegramView struct {
	Enabled        bool                     `json:"enabled"`
	TokenSet       bool                     `json:"token_set" doc:"Токен сохранён"`
	TokenHint      string                   `json:"token_hint,omitempty" doc:"ID бота из токена"`
	Running        bool                     `json:"running"`
	Error          string                   `json:"error,omitempty" doc:"token_invalid, token_revoked, unreachable или ответ Telegram"`
	Bot            *TelegramBot             `json:"bot,omitempty"`
	Config         tgbot.Config             `json:"config"`
	Defaults       tgbot.Texts              `json:"defaults" doc:"Встроенные тексты на языке бота: пустое поле берёт их"`
	MiniAppURL     string                   `json:"mini_app_url" doc:"Адрес Mini App; пусто — Telegram его не откроет: нет адреса или сертификат самоподписанный"`
	Linked         int64                    `json:"linked" doc:"Подписок, привязанных к Telegram"`
	Accounts       int64                    `json:"accounts" doc:"Аккаунтов Telegram с подписками"`
	Broadcast      *TelegramBroadcast       `json:"broadcast,omitempty" doc:"Последняя рассылка с запуска панели"`
	Route          TelegramRoute            `json:"route" doc:"Как бот ходит в Telegram"`
	Infrastructure infraalerts.AlertsConfig `json:"infrastructure"`
	AdminChatSet   bool                     `json:"admin_chat_set"`
}

type TelegramRoute struct {
	Mode   string `json:"mode" enum:"direct,node,proxy" doc:"Напрямую с сервера панели, через её ноду или через прокси — когда Telegram на сервере заблокирован"`
	NodeID int64  `json:"node_id,omitempty" doc:"Нода, через которую идут запросы"`
	Proxy  string `json:"proxy,omitempty" doc:"Адрес прокси; пароль скрыт"`
}

// TelegramBroadcast is how far the last broadcast went.
type TelegramBroadcast struct {
	Total   int   `json:"total"`
	Sent    int   `json:"sent"`
	Failed  int   `json:"failed" doc:"Не дошло: бот заблокирован, чат удалён"`
	Started int64 `json:"started" doc:"Unix-время начала"`
	Active  bool  `json:"active" doc:"Ещё отправляется"`
}

type TelegramBot struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

type telegramOutput struct{ Body TelegramView }

type patchTelegramInput struct {
	Body struct {
		Enabled        *bool                          `json:"enabled,omitempty"`
		Token          *string                        `json:"token,omitempty" maxLength:"100" doc:"Токен от @BotFather; пустая строка — удалить"`
		Config         *tgbot.Config                  `json:"config,omitempty"`
		Infrastructure *infraalerts.AlertsConfigPatch `json:"infrastructure,omitempty"`
		Route          *struct {
			Mode   string  `json:"mode" enum:"direct,node,proxy"`
			NodeID int64   `json:"node_id,omitempty" minimum:"0" doc:"Удалённая нода панели (mode=node)"`
			Proxy  *string `json:"proxy,omitempty" maxLength:"512" doc:"socks5://user:pass@host:port, http://… или https://…; не передан — прежний (mode=proxy)"`
		} `json:"route,omitempty" doc:"Перед сохранением панель проверяет, что Telegram отвечает этим путём"`
	}
}

type broadcastInput struct {
	Body struct {
		Text string `json:"text" minLength:"1" maxLength:"3500" doc:"Текст с Markdown, как тексты бота; {brand} — название сервиса"`
	}
}

type broadcastOutput struct {
	Body struct {
		Queued int `json:"queued"`
	}
}

type telegramPreviewInput struct {
	Body struct {
		Text string            `json:"text" maxLength:"3500" doc:"Текст бота с Markdown"`
		Vars map[string]string `json:"vars,omitempty" maxProperties:"20" doc:"Значения переменных {name}; без них переменные остаются как написаны"`
	}
}

type telegramPreviewOutput struct {
	Body struct {
		HTML string `json:"html" doc:"Сообщение, как его отправит бот: HTML Telegram"`
	}
}

// previewTelegram shows the admin how the bot sends a text: the same conversion the bot
// runs, without a bot.
func (h *handlers) previewTelegram(_ context.Context, in *telegramPreviewInput) (*telegramPreviewOutput, error) {
	vars := make(map[string]string, len(in.Body.Vars))
	for k, v := range in.Body.Vars {
		if len(k) > 20 || utf8.RuneCountInString(v) > 200 {
			return nil, huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.vars", Message: "tg_text"})
		}
		vars[k] = v
	}
	out := &telegramPreviewOutput{}
	out.Body.HTML = tgbot.Render(in.Body.Text, vars)
	return out, nil
}

type infrastructureConnectOutput struct {
	Body struct {
		URL string `json:"url"`
	}
}

func (h *handlers) connectInfrastructureAdmin(ctx context.Context, _ *struct{}) (*infrastructureConnectOutput, error) {
	if h.d.Telegram == nil {
		return nil, huma.Error503ServiceUnavailable("bot_unavailable")
	}
	url, err := h.d.Telegram.BeginInfrastructureAdminConnect(ctx)
	if errors.Is(err, tgbot.ErrOff) {
		return nil, huma.Error409Conflict("bot_off")
	}
	if err != nil {
		return nil, err
	}
	out := &infrastructureConnectOutput{}
	out.Body.URL = url
	h.audit(ctx, sessionOf(ctx).AdminID, "telegram.infrastructure_connect", "telegram", "", nil)
	return out, nil
}

func (h *handlers) disconnectInfrastructureAdmin(ctx context.Context, _ *struct{}) (*struct{}, error) {
	if h.d.Telegram == nil {
		return nil, huma.Error503ServiceUnavailable("bot_unavailable")
	}
	if err := h.d.Telegram.DisconnectInfrastructureAdmin(ctx); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "telegram.infrastructure_disconnect", "telegram", "", nil)
	return nil, nil
}

func (h *handlers) registerTelegram() {
	tags := []string{"telegram"}
	huma.Register(h.api, huma.Operation{OperationID: "get-telegram", Method: http.MethodGet, Path: "/api/v1/telegram", Summary: "Telegram-бот", Tags: tags}, h.getTelegram)
	huma.Register(h.api, huma.Operation{OperationID: "update-telegram", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPatch, Path: "/api/v1/telegram", Summary: "Настроить Telegram-бота", Tags: tags}, h.updateTelegram)
	huma.Register(h.api, huma.Operation{OperationID: "telegram-preview", Method: http.MethodPost, Path: "/api/v1/telegram/preview", Summary: "Как бот отправит текст с Markdown", Tags: tags}, h.previewTelegram)
	huma.Register(h.api, huma.Operation{OperationID: "telegram-broadcast", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/telegram/broadcast", Summary: "Разослать сообщение всем в боте", Tags: tags, DefaultStatus: http.StatusAccepted}, h.broadcast)
	huma.Register(h.api, huma.Operation{OperationID: "telegram-infrastructure-connect", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/telegram/infrastructure/connect", Summary: "Подключить чат администратора для уведомлений", Tags: tags}, h.connectInfrastructureAdmin)
	huma.Register(h.api, huma.Operation{OperationID: "telegram-infrastructure-disconnect", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodDelete, Path: "/api/v1/telegram/infrastructure/connect", Summary: "Отключить чат администратора для уведомлений", Tags: tags, DefaultStatus: http.StatusNoContent}, h.disconnectInfrastructureAdmin)
	huma.Register(h.api, huma.Operation{OperationID: "unlink-telegram", Method: http.MethodDelete, Path: "/api/v1/users/{id}/telegram", Summary: "Отвязать подписку от Telegram", Tags: tags, DefaultStatus: http.StatusNoContent}, h.unlinkTelegram)
}

func (h *handlers) telegramView(ctx context.Context) (TelegramView, error) {
	var v TelegramView
	var err error
	if v.Enabled, err = h.d.Settings.On(ctx, tgbot.Enabled); err != nil {
		return v, err
	}
	token, err := h.d.Settings.String(ctx, tgbot.KeyToken)
	if err != nil {
		return v, err
	}
	if token != "" {
		v.TokenSet = true
		if id, _, ok := cutToken(token); ok {
			v.TokenHint = id
		}
	}
	if bot, ok, _ := settings.Get[tgbot.User](ctx, h.d.Settings, tgbot.KeyBot); ok && token != "" {
		v.Bot = &TelegramBot{Username: bot.Username, Name: bot.FirstName}
	}
	if h.d.Telegram != nil {
		st := h.d.Telegram.Status()
		v.Running, v.Error = st.Running, st.Error
		v.Config = h.d.Telegram.Config(ctx)
		// The address carries the sub path, which is the panel's secret.
		if !hidesSecrets(ctx) {
			v.MiniAppURL = h.d.Telegram.MiniAppURL(ctx)
		}
		if p := h.d.Telegram.Progress(); p.Total > 0 {
			v.Broadcast = &TelegramBroadcast{Total: p.Total, Sent: p.Sent, Failed: p.Failed, Started: p.Started.Unix(), Active: p.Active()}
		}
	} else {
		lang, err := h.d.Settings.Lang(ctx)
		if err != nil {
			return v, err
		}
		v.Config = tgbot.Default(lang)
	}
	v.Defaults = tgbot.DefaultTexts(v.Config.Lang)
	v.Infrastructure, _, err = settings.GetOver(ctx, h.d.Settings, infraalerts.KeyConfig, infraalerts.Default())
	if err != nil {
		return v, err
	}
	if h.d.Telegram != nil {
		_, v.AdminChatSet, err = h.d.Telegram.InfrastructureAdminChat(ctx)
		if err != nil {
			return v, err
		}
	}
	route := tgbot.Route{Mode: tgbot.RouteDirect}
	if h.d.Telegram != nil {
		if route, err = h.d.Telegram.LoadRoute(ctx); err != nil {
			return v, err
		}
	}
	v.Route = TelegramRoute{Mode: route.Mode, NodeID: route.NodeID, Proxy: tgbot.MaskProxy(route.Proxy)}
	if v.Linked, err = h.d.Store.Q.CountTgLinks(ctx); err != nil {
		return v, err
	}
	if v.Accounts, err = h.d.Store.Q.CountTgChats(ctx); err != nil {
		return v, err
	}
	return v, nil
}

func (h *handlers) getTelegram(ctx context.Context, _ *struct{}) (*telegramOutput, error) {
	v, err := h.telegramView(ctx)
	if err != nil {
		return nil, err
	}
	return &telegramOutput{Body: v}, nil
}

var tokenRe = regexp.MustCompile(`^(\d{5,15}):([A-Za-z0-9_-]{30,50})$`)

// cutToken splits a bot token into the bot id and the secret part.
func cutToken(t string) (id, secret string, ok bool) {
	m := tokenRe.FindStringSubmatch(t)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

func tgFieldErr(field, code string) error {
	return huma.Error422UnprocessableEntity(code, &huma.ErrorDetail{Location: "body." + field, Message: code})
}

func (h *handlers) updateTelegram(ctx context.Context, in *patchTelegramInput) (*telegramOutput, error) {
	if h.d.Telegram == nil {
		return nil, huma.Error503ServiceUnavailable("bot_unavailable")
	}
	b := in.Body
	details := map[string]any{}
	// Everything is checked before anything is written: a refused field leaves the bot as
	// it was, not half changed.
	route, err := h.d.Telegram.LoadRoute(ctx)
	if err != nil {
		return nil, err
	}
	if b.Route != nil {
		if route, err = h.nextRoute(ctx, route, b.Route.Mode, b.Route.NodeID, b.Route.Proxy, details); err != nil {
			return nil, err
		}
	}
	token, err := h.d.Settings.String(ctx, tgbot.KeyToken)
	if err != nil {
		return nil, err
	}
	var bot *tgbot.User
	if b.Token != nil {
		token = *b.Token
		if token == "" {
			details["token"] = "removed"
		} else {
			if _, _, ok := cutToken(token); !ok {
				return nil, tgFieldErr("token", "tg_token_format")
			}
			// Through the route as it will be: a new route is proven by the token's check.
			me, err := h.d.Telegram.CheckTokenVia(ctx, token, route)
			var ae *tgbot.APIError
			switch {
			case errors.As(err, &ae) && (ae.Code == 401 || ae.Code == 404):
				return nil, tgFieldErr("token", "tg_token_invalid")
			case err != nil && b.Route != nil && route.Mode != tgbot.RouteDirect:
				return nil, tgFieldErr("route", "tg_route_unreachable")
			case err != nil:
				return nil, huma.Error502BadGateway("tg_unreachable")
			}
			bot = &me
			details["token"], details["bot"] = "set", me.Username
		}
	}
	if b.Route != nil && route.Mode != tgbot.RouteDirect && bot == nil {
		if err := h.d.Telegram.CheckRoute(ctx, route, token); err != nil {
			return nil, tgFieldErr("route", "tg_route_unreachable")
		}
	}
	if b.Config != nil {
		if err := b.Config.Validate(); err != nil {
			return nil, tgFieldErr("config", err.Error())
		}
		details["config"] = true
	}
	if b.Infrastructure != nil {
		details["infrastructure"] = true
	}
	if b.Enabled != nil && *b.Enabled && token == "" {
		return nil, tgFieldErr("enabled", "tg_no_token")
	}

	// One transaction: a token saved without the route that reaches it, or a route without
	// the switch that turns the bot on, is a bot that does not start. The alerts' settings
	// are merged into what the transaction reads, so two PATCHes at once do not undo each
	// other's fields (a serialization conflict merges again).
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		var infrastructure *infraalerts.AlertsConfig
		if b.Infrastructure != nil {
			current, _, err := settings.GetOver(ctx, set, infraalerts.KeyConfig, infraalerts.Default())
			if err != nil {
				return err
			}
			merged := b.Infrastructure.Merge(current)
			if err := merged.Validate(); err != nil {
				return tgFieldErr("infrastructure", err.Error())
			}
			infrastructure = &merged
		}
		if b.Enabled != nil && *b.Enabled && b.Token == nil {
			// The token checked above may have been removed meanwhile.
			if cur, err := set.String(ctx, tgbot.KeyToken); err != nil {
				return err
			} else if cur == "" {
				return tgFieldErr("enabled", "tg_no_token")
			}
		}
		if b.Route != nil {
			if err := settings.Set(ctx, set, tgbot.KeyRoute, route); err != nil {
				return err
			}
		}
		if b.Token != nil {
			if err := settings.Set(ctx, set, tgbot.KeyToken, token); err != nil {
				return err
			}
			if bot != nil {
				if err := settings.Set(ctx, set, tgbot.KeyBot, *bot); err != nil {
					return err
				}
			} else if err := settings.Set(ctx, set, tgbot.KeyEnabled, false); err != nil {
				return err
			}
		}
		if b.Config != nil {
			if err := settings.Set(ctx, set, tgbot.KeyConfig, *b.Config); err != nil {
				return err
			}
		}
		if infrastructure != nil {
			if err := settings.Set(ctx, set, infraalerts.KeyConfig, *infrastructure); err != nil {
				return err
			}
		}
		if b.Enabled != nil {
			return settings.Set(ctx, set, tgbot.KeyEnabled, *b.Enabled)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if b.Enabled != nil {
		details["enabled"] = *b.Enabled
	}
	h.d.Telegram.Reload()
	h.audit(ctx, sessionOf(ctx).AdminID, "telegram.update", "telegram", "", details)
	v, err := h.telegramView(ctx)
	if err != nil {
		return nil, err
	}
	return &telegramOutput{Body: v}, nil
}

// proxyHostOK: where the bot may connect as its proxy. A proxy next to the panel (an
// address on this host or the LAN) is the admin's explicit choice, so a literal address is
// fine; a name that leads to this host itself or to the metadata address is not: it is how
// an internal service is reached through a harmless-looking name. The name is looked up
// here, and a name that cannot be is let through, the check made on route is the real one.
func (h *handlers) proxyHostOK(ctx context.Context, host string) bool {
	inside := func(a netip.Addr) bool {
		a = a.Unmap()
		return a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified() || a.IsMulticast()
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return !(a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified() || a.IsMulticast())
	}
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	resolve := h.d.Resolve
	if resolve == nil {
		resolve = domain.SystemResolve
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := resolve(ctx, host)
	if err != nil {
		return true
	}
	return !slices.ContainsFunc(addrs, inside)
}

// nextRoute checks a route change; nothing is saved here.
func (h *handlers) nextRoute(ctx context.Context, r tgbot.Route, mode string, nodeID int64, proxy *string, details map[string]any) (tgbot.Route, error) {
	r.Mode = mode
	switch mode {
	case tgbot.RouteNode:
		n, err := h.d.Store.Q.GetNode(ctx, nodeID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return r, tgFieldErr("route", "tg_route_node_missing")
		case err != nil:
			h.d.Log.Warn("telegram: the route's node not read", "node", nodeID, "err", err)
			return r, err
		case n.Address == "":
			// The panel's own node shares its server, and with it the block.
			return r, tgFieldErr("route", "tg_route_node")
		}
		r.NodeID = n.ID
		details["route"], details["node"] = mode, n.Name
	case tgbot.RouteProxy:
		if proxy != nil {
			r.Proxy = strings.TrimSpace(*proxy)
		}
		u, err := tgbot.ParseProxy(r.Proxy)
		if err != nil {
			return r, tgFieldErr("route", "tg_proxy_invalid")
		}
		if !h.proxyHostOK(ctx, u.Hostname()) {
			return r, tgFieldErr("route", "tg_proxy_private")
		}
		details["route"], details["proxy"] = mode, tgbot.ProxyHost(r.Proxy)
	default:
		details["route"] = mode
	}
	return r, nil
}

func (h *handlers) broadcast(ctx context.Context, in *broadcastInput) (*broadcastOutput, error) {
	if h.d.Telegram == nil {
		return nil, huma.Error503ServiceUnavailable("bot_unavailable")
	}
	n, err := h.d.Telegram.Broadcast(ctx, in.Body.Text)
	switch {
	case errors.Is(err, tgbot.ErrOff):
		return nil, huma.Error409Conflict("bot_off")
	case errors.Is(err, tgbot.ErrBusy):
		return nil, huma.Error409Conflict("broadcast_busy")
	case err != nil:
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "telegram.broadcast", "telegram", "", map[string]any{"recipients": n})
	out := &broadcastOutput{}
	out.Body.Queued = n
	return out, nil
}

func (h *handlers) unlinkTelegram(ctx context.Context, in *userIDInput) (*struct{}, error) {
	if _, err := h.d.Users.Get(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := h.d.Store.Q.UnlinkTg(ctx, in.ID); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user.unlink_telegram", "user", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}
