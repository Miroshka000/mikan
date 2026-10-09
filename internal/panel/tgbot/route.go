package tgbot

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/settings"
)

// How the bot reaches Telegram: straight from the panel's server, through one of the
// panel's nodes, or through a proxy. A server where Telegram is blocked needs one of the
// last two; everything the bot sends goes the chosen way (updates, messages, invoices).

const KeyRoute = "telegram.route"

const (
	RouteDirect = "direct"
	RouteNode   = "node"
	RouteProxy  = "proxy"
)

type Route struct {
	Mode   string `json:"mode"`
	NodeID int64  `json:"node_id,omitempty"`
	// Proxy is a secret: it may carry a password. It is kept while another mode is on, so
	// switching back needs no retyping.
	Proxy string `json:"proxy,omitempty"`
}

var (
	ErrProxyInvalid = errors.New("tg_proxy_invalid")
	ErrRouteNode    = errors.New("tg_route_node")
)

// ParseProxy checks a proxy address: http, https or socks5 with a host and a port, and
// nothing else (no path, query or fragment).
func ParseProxy(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 {
		return nil, ErrProxyInvalid
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, ErrProxyInvalid
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, ErrProxyInvalid
	}
	port, err := strconv.Atoi(u.Port())
	if u.Hostname() == "" || err != nil || port < 1 || port > 65535 || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrProxyInvalid
	}
	return u, nil
}

// MaskProxy is the address as the panel shows it: the password hidden.
func MaskProxy(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	out := u.Scheme + "://"
	if u.User != nil {
		out += u.User.Username()
		if _, ok := u.User.Password(); ok {
			out += ":•••"
		}
		out += "@"
	}
	return out + u.Host
}

// ProxyHost is host:port of a proxy address, for the audit log.
func ProxyHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return ""
}

// LoadRoute is the saved route. A read error is returned, not hidden: a change built
// on a fallback and saved would lose the admin's proxy.
func (b *Bot) LoadRoute(ctx context.Context) (Route, error) {
	r, _, err := settings.Get[Route](ctx, b.d.Settings, KeyRoute)
	if err != nil {
		return Route{}, err
	}
	if r.Mode == "" {
		r.Mode = RouteDirect
	}
	return r, nil
}

// Route is how the bot reaches Telegram now; straight when the setting cannot be read.
func (b *Bot) Route(ctx context.Context) Route {
	r, err := b.LoadRoute(ctx)
	if err != nil {
		return Route{Mode: RouteDirect}
	}
	return r
}

// transport for a route; nil is Go's default, straight to Telegram.
func (b *Bot) transport(r Route) (http.RoundTripper, error) {
	switch r.Mode {
	case RouteProxy:
		u, err := ParseProxy(r.Proxy)
		if err != nil {
			return nil, err
		}
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = http.ProxyURL(u)
		return t, nil
	case RouteNode:
		if b.d.Tunnel == nil || r.NodeID == 0 {
			return nil, ErrRouteNode
		}
		id := r.NodeID
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		t.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) { return b.d.Tunnel(ctx, id, addr) }
		return t, nil
	}
	return nil, nil
}

// CheckRoute tries a route before it is saved. With a token Telegram has to answer
// getMe (any answer: the token is checked on its own); without one, the Bot API has to
// answer at all.
func (b *Bot) CheckRoute(ctx context.Context, r Route, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	rt, err := b.transport(r)
	if err != nil {
		return err
	}
	if token != "" {
		_, err := NewClient(b.d.API, token, rt).Me(ctx)
		var ae *APIError
		if err == nil || errors.As(err, &ae) {
			return nil
		}
		b.routeFailed(r, err)
		return ErrUnreachable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.d.API+"/", nil)
	if err != nil {
		return err
	}
	// Any answer will do. Telegram's is a redirect to core.telegram.org, which a node does
	// not tunnel to and which may be blocked where api.telegram.org is not.
	hc := &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		b.routeFailed(r, err)
		return ErrUnreachable
	}
	resp.Body.Close()
	return nil
}

// routeFailed logs why a route did not reach Telegram: the admin sees only that it did not.
// The error carries no token: Client.do keeps the cause, not the URL.
func (b *Bot) routeFailed(r Route, err error) {
	b.d.Log.Warn("telegram: route check failed", "mode", r.Mode, "node", r.NodeID, "err", err)
}
